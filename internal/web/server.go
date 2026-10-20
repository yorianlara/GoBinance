package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"mi-bot/internal/config"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/manager"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/session"
)

//go:embed all:templates
var templatesFS embed.FS

type OpenOrdersCanceller interface {
	CancelAllOpenOrders(context.Context) (exchange.CancelAllResult, error)
}

type PositionCloser interface {
	ClosePosition(context.Context, string) (manager.PositionCloseResult, error)
}

type AllPositionsCloser interface {
	CloseAllPositions(context.Context) ([]manager.PositionCloseAttempt, error)
}

type Deps struct {
	Bots               *manager.BotManager
	Risk               *risk.StatefulManager
	Registry           registry.OrderRegistry
	Capital            risk.CapitalProvider
	Config             *config.Store
	OrderCanceller     OpenOrdersCanceller
	PositionCloser     PositionCloser
	AllPositionsCloser AllPositionsCloser
}

type Server struct {
	app          *fiber.App
	deps         Deps
	pages        *template.Template
	sessionStore *session.Store
	auth         authConfig
	cancelAllMu  sync.Mutex
	closeAllMu   sync.Mutex
}

func New(deps Deps) (*Server, error) {
	if deps.Bots == nil {
		return nil, fmt.Errorf("BotManager es obligatorio")
	}
	if deps.Risk == nil {
		return nil, fmt.Errorf("StatefulManager es obligatorio")
	}
	if deps.Config == nil {
		return nil, fmt.Errorf("ConfigStore es obligatorio")
	}

	pages, err := template.ParseFS(templatesFS,
		"templates/layout.html",
		"templates/index.html",
		"templates/config.html",
		"templates/login.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates: %w", err)
	}

	store, err := sessionStoreFromDB()
	if err != nil {
		return nil, fmt.Errorf("crear session store: %w", err)
	}

	app := fiber.New(fiber.Config{AppName: "mi-bot", DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		started := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		args := []any{
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"latency", time.Since(started),
		}
		if err != nil {
			args = append(args, "err", err)
		}
		if err != nil || status >= fiber.StatusInternalServerError {
			slog.Error("web: request failed", args...)
		} else if status >= fiber.StatusBadRequest {
			slog.Warn("web: request rejected", args...)
		} else {
			slog.Info("web: request", args...)
		}
		return err
	})

	s := &Server{
		app:          app,
		deps:         deps,
		pages:        pages,
		sessionStore: store,
		auth:         loadAuthConfig(),
	}
	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	// Públicas
	s.app.Get("/health", func(c *fiber.Ctx) error { return c.SendString("ok") })
	s.app.Get("/login", s.handleLoginPage)

	// Rate limit anti fuerza bruta en el login: máx. 10 intentos por IP y
	// minuto; el middleware responde 429 al superarlo.
	loginLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: time.Minute,
	})
	s.app.Post("/login", loginLimiter, s.handleLoginSubmit)

	// Protegidas: middleware requireAuth
	auth := requireAuth(s.auth, s.sessionStore)
	s.app.Get("/logout", s.handleLogout)

	s.app.Get("/", auth, s.handleIndex)
	s.app.Get("/config", auth, s.handleConfigPage)
	s.app.Post("/config/risk", auth, s.handleUpdateRisk)
	s.app.Get("/partials/bots", auth, s.handlePartialBots)
	s.app.Get("/partials/metrics", auth, s.handlePartialMetrics)
	s.app.Post("/bots", auth, s.handleCreateBot)
	s.app.Post("/bots/:id/enable", auth, s.handleEnable)
	s.app.Post("/bots/:id/pause", auth, s.handlePause)
	s.app.Post("/bots/:id/stop", auth, s.handleStop)
	s.app.Post("/bots/:id/close-position", auth, s.handleClosePosition)
	s.app.Delete("/bots/:id", auth, s.handleDelete)
	s.app.Post("/account/orders/cancel-all", auth, s.handleCancelAllOrders)
	s.app.Post("/account/positions/close-all", auth, s.handleCloseAllPositions)

	if s.auth.Enabled {
		slog.Info("web: autenticación activada")
	} else {
		slog.Warn("web: DASHBOARD_USER/DASHBOARD_PASSWORD no configurados; dashboard SIN autenticación")
	}
}

func (s *Server) Start(addr string) error {
	slog.Info("web: servidor escuchando", "addr", addr)
	return s.app.Listen(addr)
}

func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.app.ShutdownWithContext(ctx)
}

// ---------- index / métricas ----------
type metricsData struct {
	CapitalUSDT   float64
	CapitalErr    string
	ExposureUSDT  float64
	DrawdownPct   float64
	DailyPnL      float64
	TotalPnL      float64
	OpenPositions int
	RecentOrders  []recentOrder
}

type recentOrder struct {
	ClientOrderID string
	Symbol        string
	State         string
	Quantity      float64
	FilledValue   float64
	Age           string
}

type pageData struct {
	Title   string
	Bots    []domain.Bot
	BotList botsResultData
	Metrics metricsData
}

type botsResultData struct {
	Bots    []domain.Bot
	Message string
	IsError bool
}

type botFeedbackData struct {
	Message string
	IsError bool
}

func (s *Server) buildMetrics() metricsData {
	m := metricsData{}
	state := s.deps.Risk.State()
	m.ExposureUSDT = state.ExposureTotalUSDT
	m.DailyPnL = state.DailyPnL
	m.TotalPnL = state.TotalPnL
	m.OpenPositions = state.OpenPositions

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if capital, err := s.deps.Capital.CapitalUSDT(ctx); err != nil {
		m.CapitalErr = err.Error()
	} else {
		m.CapitalUSDT = capital
		if state.PeakCapitalUSDT > 0 {
			m.DrawdownPct = (state.PeakCapitalUSDT - capital) / state.PeakCapitalUSDT * 100
			if m.DrawdownPct < 0 {
				m.DrawdownPct = 0
			}
		}
	}

	if s.deps.Registry != nil {
		orders, err := s.deps.Registry.Recent(10)
		if err != nil {
			slog.Warn("web: consultar órdenes recientes", "err", err)
		} else {
			for _, o := range orders {
				m.RecentOrders = append(m.RecentOrders, recentOrder{
					ClientOrderID: o.ClientOrderID,
					Symbol:        o.Proposal.Symbol,
					State:         string(o.State),
					Quantity:      o.Proposal.Quantity,
					FilledValue:   o.FilledValue,
					Age:           humanAge(time.Since(o.CreatedAt)),
				})
			}
		}
	}
	return m
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func (s *Server) handleIndex(c *fiber.Ctx) error {
	bots := s.deps.Bots.List()
	data := pageData{
		Title:   "Dashboard",
		Bots:    bots,
		BotList: botsResultData{Bots: bots},
		Metrics: s.buildMetrics(),
	}
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "layout", data)
}

func (s *Server) renderBots(c *fiber.Ctx) error {
	return s.renderBotResults(c, "", false)
}

func (s *Server) renderBotResults(c *fiber.Ctx, message string, isError bool) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "bots-results", botsResultData{
		Bots: s.deps.Bots.List(), Message: message, IsError: isError,
	})
}

func (s *Server) renderBotFeedback(c *fiber.Ctx, message string, isError bool) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "bot-form-feedback", botFeedbackData{
		Message: message, IsError: isError,
	})
}

func (s *Server) handlePartialBots(c *fiber.Ctx) error { return s.renderBots(c) }

func (s *Server) handlePartialMetrics(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "metrics", s.buildMetrics())
}

// ---------- bots ----------
func (s *Server) handleCreateBot(c *fiber.Ctx) error {
	id := c.FormValue("id")
	name := c.FormValue("name")
	strategyType := c.FormValue("strategy_type")
	symbol := c.FormValue("symbol")
	allocStr := c.FormValue("allocation")
	amountStr := c.FormValue("amount_per_buy")

	alloc, err := strconv.ParseFloat(allocStr, 64)
	if err != nil || alloc <= 0 || alloc > 100 {
		slog.Warn("web: crear bot rechazado", "id", id, "reason", "asignación inválida")
		return s.renderBotFeedback(c, "La asignación debe ser un número entre 0 y 100.", true)
	}
	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 {
		slog.Warn("web: crear bot rechazado", "id", id, "reason", "importe por compra inválido")
		return s.renderBotFeedback(c, "El importe por compra debe ser mayor que cero.", true)
	}

	params := map[string]float64{}
	switch strategyType {
	case "dca":
		params["amount_per_buy_usdt"] = amount
		params["interval_minutes"] = 1
		params["max_buys"] = 100
	case "grid":
		params["grid_levels"] = 5
		params["grid_spacing_percent"] = 1
		params["order_size_usdt"] = amount
	case "ema":
		params["fast_period"] = 9
		params["slow_period"] = 21
		params["order_size_usdt"] = amount
	case "rsi":
		params["period"] = 14
		params["oversold"] = 30
		params["overbought"] = 70
		params["order_size_usdt"] = amount
	}

	now := time.Now()
	bot := domain.Bot{
		ID: id, Name: name,
		Strategy: domain.StrategyConfig{Type: strategyType, Parameters: params},
		Symbol:   symbol, AllocationPercent: alloc,
		Status: domain.BotStatusCreated, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.deps.Bots.Create(bot); err != nil {
		slog.Warn("web: crear bot", "id", id, "err", err)
		return s.renderBotFeedback(c, err.Error(), true)
	}
	slog.Info("web: bot creado",
		"bot", bot.ID, "strategy", bot.Strategy.Type,
		"symbol", bot.Symbol, "allocation_percent", bot.AllocationPercent,
	)
	c.Set("HX-Trigger", "botsChanged")
	return s.renderBotFeedback(c, "Bot creado correctamente.", false)
}

func (s *Server) handleEnable(c *fiber.Ctx) error {
	if err := s.deps.Bots.Enable(c.Params("id")); err != nil {
		slog.Warn("web: enable", "err", err)
		return s.renderBotResults(c, err.Error(), true)
	}
	slog.Info("web: bot encendido", "bot", c.Params("id"))
	return s.renderBotResults(c, "Bot encendido.", false)
}
func (s *Server) handlePause(c *fiber.Ctx) error {
	if err := s.deps.Bots.Pause(c.Params("id")); err != nil {
		slog.Warn("web: pause", "err", err)
		return s.renderBotResults(c, err.Error(), true)
	}
	slog.Info("web: bot pausado", "bot", c.Params("id"))
	return s.renderBotResults(c, "Bot pausado.", false)
}
func (s *Server) handleStop(c *fiber.Ctx) error {
	result, err := s.deps.Bots.StopBot(c.Params("id"))
	if err != nil {
		slog.Warn("web: stop", "bot", c.Params("id"), "sold_qty", result.SoldQuantity, "err", err)
		if result.SoldQuantity > 0 {
			message := fmt.Sprintf("Venta parcial al detener: %.8f %s por %.2f USDT; PnL realizado %.2f USDT. %v",
				result.SoldQuantity, result.Symbol, result.ProceedsUSDT, result.RealizedPnLUSDT, err)
			return s.renderBotResults(c, message, true)
		}
		return s.renderBotResults(c, err.Error(), true)
	}
	slog.Info("web: bot detenido",
		"bot", c.Params("id"),
		"sold_qty", result.SoldQuantity,
		"proceeds_usdt", result.ProceedsUSDT,
		"realized_pnl_usdt", result.RealizedPnLUSDT,
	)
	if result.NoPosition {
		return s.renderBotResults(c, fmt.Sprintf("Bot %q detenido con todas sus órdenes terminadas; no quedaba posición abierta por vender.", result.BotID), false)
	}
	message := fmt.Sprintf("Bot detenido y posición vendida: %.8f %s por %.2f USDT; coste %.2f USDT; PnL realizado %+.2f USDT.",
		result.SoldQuantity, result.Symbol, result.ProceedsUSDT, result.CostBasisUSDT, result.RealizedPnLUSDT)
	return s.renderBotResults(c, message, false)
}

func (s *Server) handleClosePosition(c *fiber.Ctx) error {
	if s.deps.PositionCloser == nil {
		return s.renderBotResults(c, "No está configurado el cierre de posiciones.", true)
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 60*time.Second)
	result, err := s.deps.PositionCloser.ClosePosition(ctx, c.Params("id"))
	cancel()
	c.Set("HX-Trigger", "botsChanged")
	if err != nil {
		slog.Error("web: cerrar posición del bot", "bot", c.Params("id"), "sold_qty", result.SoldQuantity, "err", err)
		if result.SoldQuantity > 0 {
			message := fmt.Sprintf("Venta parcial: %.8f %s; PnL realizado %.2f USDT. %v", result.SoldQuantity, result.Symbol, result.RealizedPnLUSDT, err)
			return s.renderBotResults(c, message, true)
		}
		return s.renderBotResults(c, err.Error(), true)
	}
	if result.NoPosition {
		return s.renderBotResults(c, fmt.Sprintf("Bot %q detenido. No tiene posición registrada para cerrar.", result.BotID), false)
	}
	slog.Info("web: posición del bot cerrada",
		"bot", result.BotID,
		"symbol", result.Symbol,
		"sold_qty", result.SoldQuantity,
		"proceeds_usdt", result.ProceedsUSDT,
		"cost_basis_usdt", result.CostBasisUSDT,
		"realized_pnl_usdt", result.RealizedPnLUSDT,
	)
	message := fmt.Sprintf("Posición cerrada: vendidas %.8f %s por %.2f USDT; coste %.2f USDT; PnL realizado %+.2f USDT. El bot quedó detenido.",
		result.SoldQuantity, result.Symbol, result.ProceedsUSDT, result.CostBasisUSDT, result.RealizedPnLUSDT)
	return s.renderBotResults(c, message, false)
}

func (s *Server) handleCloseAllPositions(c *fiber.Ctx) error {
	s.closeAllMu.Lock()
	defer s.closeAllMu.Unlock()

	if s.deps.AllPositionsCloser == nil {
		return s.renderBotFeedback(c, "No está configurado el cierre de posiciones.", true)
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Minute)
	defer cancel()

	attempts, err := s.deps.AllPositionsCloser.CloseAllPositions(ctx)
	if err != nil {
		slog.Error("web: iniciar cierre global de posiciones", "err", err)
		return s.renderBotFeedback(c, "No se pudo iniciar el cierre global: "+err.Error(), true)
	}

	closed, partial, withoutPosition, failed := 0, 0, 0, 0
	var details []string
	for _, attempt := range attempts {
		if attempt.Err != nil {
			failed++
			details = append(details, fmt.Sprintf("%s/%s: %v", attempt.BotID, attempt.Symbol, attempt.Err))
			slog.Error("web: cerrar posición en operación global",
				"bot", attempt.BotID, "symbol", attempt.Symbol,
				"sold_qty", attempt.Result.SoldQuantity, "err", attempt.Err)
			if attempt.Result.SoldQuantity > 0 {
				partial++
			}
			continue
		}
		result := attempt.Result
		if result.NoPosition {
			withoutPosition++
			continue
		}
		if result.SoldQuantity > 0 {
			if result.Partial {
				partial++
			} else {
				closed++
			}
			slog.Info("web: posición cerrada en operación global",
				"bot", result.BotID,
				"symbol", result.Symbol,
				"sold_qty", result.SoldQuantity,
				"proceeds_usdt", result.ProceedsUSDT,
				"cost_basis_usdt", result.CostBasisUSDT,
				"realized_pnl_usdt", result.RealizedPnLUSDT,
			)
		}
	}

	message := fmt.Sprintf(
		"Cierre global terminado: %d posición(es) cerrada(s), %d venta(s) parcial(es), %d bot/símbolo(s) sin posición registrada y %d error(es). Se revisaron %d posición(es) registrada(s).",
		closed, partial, withoutPosition, failed, len(attempts),
	)
	if len(details) > 0 {
		message += " Detalles: " + strings.Join(details, " | ")
	}
	slog.Info("web: cierre global de posiciones completado",
		"position_attempts", len(attempts),
		"positions_closed", closed,
		"bots_without_position", withoutPosition,
		"errors", failed,
	)
	c.Set("HX-Trigger", "botsChanged, metricsChanged")
	return s.renderBotFeedback(c, message, failed > 0)
}

func (s *Server) handleDelete(c *fiber.Ctx) error {
	if err := s.deps.Bots.Delete(c.Params("id")); err != nil {
		slog.Warn("web: delete", "err", err)
		return s.renderBotResults(c, err.Error(), true)
	}
	slog.Info("web: bot eliminado", "bot", c.Params("id"))
	return s.renderBotResults(c, "Bot eliminado.", false)
}

func (s *Server) handleCancelAllOrders(c *fiber.Ctx) error {
	s.cancelAllMu.Lock()
	defer s.cancelAllMu.Unlock()

	if s.deps.OrderCanceller == nil {
		return s.renderBotFeedback(c, "No está configurada la conexión de cancelación de órdenes.", true)
	}

	var operationErrors []error
	pausedBots := 0
	for _, bot := range s.deps.Bots.List() {
		if bot.Status != domain.BotStatusRunning {
			continue
		}
		if err := s.deps.Bots.Pause(bot.ID); err != nil {
			slog.Warn("web: pausar bot antes de cancelar órdenes", "bot", bot.ID, "err", err)
			operationErrors = append(operationErrors, fmt.Errorf("pausar bot %q: %w", bot.ID, err))
			continue
		}
		pausedBots++
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 60*time.Second)
	result, err := s.deps.OrderCanceller.CancelAllOpenOrders(ctx)
	cancel()
	if err != nil {
		slog.Error("web: cancelar todas las órdenes abiertas", "err", err)
		operationErrors = append(operationErrors, err)
	}

	updated, err := s.markCancelledRegistryOrders(result.Orders)
	if err != nil {
		slog.Error("web: actualizar registro tras cancelación global", "err", err)
		operationErrors = append(operationErrors, err)
	}

	slog.Info("web: cancelación global de órdenes completada",
		"symbols", result.Symbols,
		"cancelled_orders", len(result.Orders),
		"registry_orders_updated", updated,
		"bots_paused", pausedBots,
		"has_errors", len(operationErrors) > 0,
	)

	orderLabel := "órdenes canceladas"
	if len(result.Orders) == 1 {
		orderLabel = "orden cancelada"
	}
	symbolLabel := "símbolos revisados"
	if len(result.Symbols) == 1 {
		symbolLabel = "símbolo revisado"
	}
	message := fmt.Sprintf("Cancelación global: Binance confirmó %d %s en %d %s. Bots pausados: %d.", len(result.Orders), orderLabel, len(result.Symbols), symbolLabel, pausedBots)
	if len(operationErrors) > 0 {
		message += " Algunas operaciones no se completaron; revisa los logs y las órdenes abiertas en Binance."
	}
	c.Set("HX-Trigger", "botsChanged")
	return s.renderBotFeedback(c, message, len(operationErrors) > 0)
}

func (s *Server) markCancelledRegistryOrders(cancelled []exchange.CancelledOrder) (int, error) {
	if s.deps.Registry == nil || len(cancelled) == 0 {
		return 0, nil
	}
	orders, err := s.deps.Registry.List()
	if err != nil {
		return 0, fmt.Errorf("listar órdenes registradas: %w", err)
	}
	type exchangeOrderKey struct {
		symbol string
		id     int64
	}
	cancelledByExchangeID := make(map[exchangeOrderKey]struct{}, len(cancelled))
	for _, order := range cancelled {
		cancelledByExchangeID[exchangeOrderKey{symbol: order.Symbol, id: order.OrderID}] = struct{}{}
	}

	updated := 0
	var updateErrors []error
	for _, order := range orders {
		if order.State != registry.StateSubmitted {
			continue
		}
		key := exchangeOrderKey{symbol: order.Proposal.Symbol, id: order.ExchangeID}
		if _, ok := cancelledByExchangeID[key]; !ok {
			continue
		}
		if err := s.deps.Registry.MarkCancelled(order.ClientOrderID); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("marcar orden %q como cancelada: %w", order.ClientOrderID, err))
			continue
		}
		updated++
	}
	return updated, errors.Join(updateErrors...)
}

// ---------- configuración ----------
type configFormData struct {
	Risk    config.RiskConfig
	Success bool
	Error   string
}

func (s *Server) handleConfigPage(c *fiber.Ctx) error {
	data := struct {
		Title   string
		Risk    config.RiskConfig
		Success bool
		Error   string
	}{Title: "Configuración", Risk: s.deps.Config.Risk()}
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "config-page", data)
}

func (s *Server) handleUpdateRisk(c *fiber.Ctx) error {
	parseFloat := func(name string) (float64, error) {
		v := c.FormValue(name)
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return f, nil
	}
	parseInt := func(name string) (int, error) {
		v := c.FormValue(name)
		i, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return i, nil
	}

	r := config.RiskConfig{}
	var err error
	if r.MaxTotalAllocationPercent, err = parseFloat("max_total_allocation_percent"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxDrawdownPercent, err = parseFloat("max_drawdown_percent"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxOrderPercent, err = parseFloat("max_order_percent"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxAssetExposurePercent, err = parseFloat("max_asset_exposure_percent"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxTotalExposurePercent, err = parseFloat("max_total_exposure_percent"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxOpenPositions, err = parseInt("max_open_positions"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxDailyLossEUR, err = parseFloat("max_daily_loss_eur"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxTotalLossEUR, err = parseFloat("max_total_loss_eur"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxAllocationPerStrategyPercent, err = parseFloat("max_allocation_per_strategy_percent"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	if r.MaxOrderAgeMinutes, err = parseInt("max_order_age_minutes"); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}

	if err := s.deps.Config.UpdateRisk(r); err != nil {
		return s.renderConfigForm(c, r, false, err.Error())
	}
	slog.Info("web: configuración de riesgo actualizada", "risk", r)
	return s.renderConfigForm(c, r, true, "")
}

func (s *Server) renderConfigForm(c *fiber.Ctx, r config.RiskConfig, success bool, errMsg string) error {
	if errMsg != "" {
		slog.Warn("web: actualización de riesgo rechazada", "reason", errMsg)
	}
	data := configFormData{Risk: r, Success: success, Error: errMsg}
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "config-form", data)
}
