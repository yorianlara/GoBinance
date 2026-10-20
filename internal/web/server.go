package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"strconv"
	"time"

	"mi-bot/internal/config"
	"mi-bot/internal/domain"
	"mi-bot/internal/manager"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/session"
)

//go:embed all:templates
var templatesFS embed.FS

type Deps struct {
	Bots     *manager.BotManager
	Risk     *risk.StatefulManager
	Registry registry.OrderRegistry
	Capital  risk.CapitalProvider
	Config   *config.Store
}

type Server struct {
	app          *fiber.App
	deps         Deps
	pages        *template.Template
	sessionStore *session.Store
	auth         authConfig
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
	app.Use(logger.New(logger.Config{
		Format:     "[web] ${time} ${status} ${method} ${path} (${latency})\n",
		TimeFormat: "15:04:05",
	}))

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
	s.app.Post("/login", s.handleLoginSubmit)

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
	s.app.Delete("/bots/:id", auth, s.handleDelete)

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
	Metrics metricsData
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
		orders, _ := s.deps.Registry.List()
		if len(orders) > 10 {
			orders = orders[len(orders)-10:]
		}
		for i := len(orders) - 1; i >= 0; i-- {
			o := orders[i]
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
	data := pageData{Title: "Dashboard", Bots: s.deps.Bots.List(), Metrics: s.buildMetrics()}
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "layout", data)
}

func (s *Server) renderBots(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "bots-lista", s.deps.Bots.List())
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
		return s.renderBots(c)
	}
	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 {
		return s.renderBots(c)
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
	}
	return s.renderBots(c)
}

func (s *Server) handleEnable(c *fiber.Ctx) error {
	if err := s.deps.Bots.Enable(c.Params("id")); err != nil {
		slog.Warn("web: enable", "err", err)
	}
	return s.renderBots(c)
}
func (s *Server) handlePause(c *fiber.Ctx) error {
	if err := s.deps.Bots.Pause(c.Params("id")); err != nil {
		slog.Warn("web: pause", "err", err)
	}
	return s.renderBots(c)
}
func (s *Server) handleStop(c *fiber.Ctx) error {
	if err := s.deps.Bots.StopBot(c.Params("id")); err != nil {
		slog.Warn("web: stop", "err", err)
	}
	return s.renderBots(c)
}
func (s *Server) handleDelete(c *fiber.Ctx) error {
	if err := s.deps.Bots.Delete(c.Params("id")); err != nil {
		slog.Warn("web: delete", "err", err)
	}
	return s.renderBots(c)
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
	slog.Info("web: configuración de riesgo actualizada")
	return s.renderConfigForm(c, r, true, "")
}

func (s *Server) renderConfigForm(c *fiber.Ctx, r config.RiskConfig, success bool, errMsg string) error {
	data := configFormData{Risk: r, Success: success, Error: errMsg}
	c.Set("Content-Type", "text/html; charset=utf-8")
	return s.pages.ExecuteTemplate(c, "config-form", data)
}
