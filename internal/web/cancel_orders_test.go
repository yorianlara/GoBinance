package web

import (
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/config"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/manager"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"

	"github.com/gofiber/fiber/v2"
)

type fakeOrderCanceller struct {
	result exchange.CancelAllResult
	err    error
	called bool
}

type fakePositionCloser struct {
	attempts []manager.PositionCloseAttempt
	called   []string
}

func (f *fakePositionCloser) ClosePosition(_ context.Context, botID string) (manager.PositionCloseResult, error) {
	f.called = append(f.called, botID)
	return manager.PositionCloseResult{}, nil
}

func (f *fakePositionCloser) CloseAllPositions(context.Context) ([]manager.PositionCloseAttempt, error) {
	for _, attempt := range f.attempts {
		f.called = append(f.called, attempt.BotID)
	}
	return f.attempts, nil
}

type staticMarket struct{}

func (staticMarket) Price(context.Context, string) (float64, error) { return 100, nil }

func (f *fakeOrderCanceller) CancelAllOpenOrders(context.Context) (exchange.CancelAllResult, error) {
	f.called = true
	return f.result, f.err
}

func TestCloseAllPositionsAttemptsEveryBotAndReportsPartialFailures(t *testing.T) {
	botManager := newTestBotManager(t)
	now := time.Now()
	for _, id := range []string{"bot-a", "bot-b", "bot-c"} {
		if err := botManager.Create(domain.Bot{
			ID: id, Name: id, Strategy: domain.StrategyConfig{Type: "noop"},
			Symbol: "BTCUSDT", AllocationPercent: 10, Status: domain.BotStatusCreated,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	t.Cleanup(botManager.Shutdown)

	closer := &fakePositionCloser{
		attempts: []manager.PositionCloseAttempt{
			{BotID: "bot-a", Symbol: "BTCUSDT", Result: manager.PositionCloseResult{BotID: "bot-a", Symbol: "BTCUSDT", SoldQuantity: 0.1}},
			{BotID: "bot-b", Symbol: "ETHUSDT", Result: manager.PositionCloseResult{BotID: "bot-b", Symbol: "ETHUSDT", NoPosition: true}},
			{BotID: "bot-c", Symbol: "SOLUSDT", Err: errors.New("balance insuficiente")},
		},
	}
	pages, err := template.ParseFS(templatesFS, "templates/partials/bot-form-feedback.html")
	if err != nil {
		t.Fatalf("ParseFS: %v", err)
	}
	server := &Server{
		deps:  Deps{Bots: botManager, AllPositionsCloser: closer},
		pages: pages,
	}
	app := fiber.New()
	app.Post("/account/positions/close-all", server.handleCloseAllPositions)
	req, err := http.NewRequest(http.MethodPost, "/account/positions/close-all", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: esperaba %d, hay %d; body=%s", http.StatusOK, resp.StatusCode, body)
	}
	if strings.Join(closer.called, ",") != "bot-a,bot-b,bot-c" {
		t.Fatalf("se esperaba intentar todos los bots en orden estable; llamados=%v", closer.called)
	}
	if !strings.Contains(string(body), "1 posición(es) cerrada(s), 0 venta(s) parcial(es), 1 bot/símbolo(s) sin posición registrada y 1 error(es)") ||
		!strings.Contains(string(body), "bot-c/SOLUSDT: balance insuficiente") {
		t.Fatalf("respuesta no incluye resumen y error parcial: %s", body)
	}
	if resp.Header.Get("HX-Trigger") != "botsChanged, metricsChanged" {
		t.Fatalf("no se disparó la actualización del dashboard: %q", resp.Header.Get("HX-Trigger"))
	}
}

func newTestBotManager(t *testing.T) *manager.BotManager {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `app:
  name: "Test"
  version: "0.0.1"
  environment: "test"
server:
  host: "127.0.0.1"
  port: 8080
exchange:
  name: "binance"
  market: "spot"
  testnet: true
  api_key_env: "BINANCE_API_KEY"
  api_secret_env: "BINANCE_API_SECRET"
database:
  url_env: "DATABASE_URL"
risk:
  max_total_allocation_percent: 100
  max_drawdown_percent: 10
  max_order_percent: 10
  max_asset_exposure_percent: 25
  max_total_exposure_percent: 80
  max_open_positions: 5
  max_daily_loss_eur: 5
  max_total_loss_eur: 20
  max_allocation_per_strategy_percent: 40
  max_order_age_minutes: 60
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configStore, err := config.NewStore(configPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	stateStore, err := risk.NewStateStore(filepath.Join(t.TempDir(), "risk-state.json"))
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	riskManager, err := risk.NewStatefulManager(risk.New(), stateStore)
	if err != nil {
		t.Fatalf("NewStatefulManager: %v", err)
	}
	botManager, err := manager.New(manager.Deps{
		StrategyFactory: bots.DefaultFactory,
		Capital:         &risk.FakeCapitalProvider{Value: 1000},
		RiskState:       riskManager,
		Registry:        registry.NewMemory(),
		ConfigStore:     configStore,
		Market:          staticMarket{},
	})
	if err != nil {
		t.Fatalf("manager.New: %v", err)
	}
	return botManager
}

func TestCancelAllOrdersUpdatesRegistryAndReportsSummary(t *testing.T) {
	orderRegistry := registry.NewMemory()
	clientOrderID, err := orderRegistry.Reserve(domain.OrderProposal{
		BotID:      "bot-1",
		Symbol:     "BTCUSDT",
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeLimit,
		Quantity:   0.01,
		LimitPrice: 100,
		ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := orderRegistry.MarkSubmitted(clientOrderID, 456); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}

	canceller := &fakeOrderCanceller{result: exchange.CancelAllResult{
		Symbols: []string{"BTCUSDT"},
		Orders:  []exchange.CancelledOrder{{Symbol: "BTCUSDT", OrderID: 456, Status: "CANCELED"}},
	}}
	pages, err := template.ParseFS(templatesFS, "templates/partials/bot-form-feedback.html")
	if err != nil {
		t.Fatalf("ParseFS: %v", err)
	}
	server := &Server{
		deps: Deps{
			Bots:           &manager.BotManager{},
			Registry:       orderRegistry,
			OrderCanceller: canceller,
		},
		pages: pages,
	}
	app := fiber.New()
	app.Post("/account/orders/cancel-all", server.handleCancelAllOrders)
	req, err := http.NewRequest(http.MethodPost, "/account/orders/cancel-all", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: esperaba %d, hay %d; body=%s", http.StatusOK, resp.StatusCode, body)
	}
	if !canceller.called {
		t.Fatal("no se llamó CancelAllOpenOrders")
	}
	if !strings.Contains(string(body), "Binance confirmó 1 orden cancelada en 1 símbolo revisado") {
		t.Fatalf("respuesta no incluye resumen de cancelación: %s", body)
	}
	order, ok := orderRegistry.Get(clientOrderID)
	if !ok {
		t.Fatal("orden no encontrada en registry")
	}
	if order.State != registry.StateCancelled {
		t.Fatalf("estado esperado %q, hay %q", registry.StateCancelled, order.State)
	}
}

func TestCancelAllOrdersWithoutCancellerReturnsVisibleError(t *testing.T) {
	pages, err := template.ParseFS(templatesFS, "templates/partials/bot-form-feedback.html")
	if err != nil {
		t.Fatalf("ParseFS: %v", err)
	}
	server := &Server{
		deps:  Deps{Bots: &manager.BotManager{}},
		pages: pages,
	}
	app := fiber.New()
	app.Post("/account/orders/cancel-all", server.handleCancelAllOrders)
	req, err := http.NewRequest(http.MethodPost, "/account/orders/cancel-all", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), `role="alert"`) {
		t.Fatalf("se esperaba un error visible: %s", body)
	}
}

func TestCancelAllOrdersPausesRunningBots(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `app:
  name: "Test"
  version: "0.0.1"
  environment: "test"
server:
  host: "127.0.0.1"
  port: 8080
exchange:
  name: "binance"
  market: "spot"
  testnet: true
  api_key_env: "BINANCE_API_KEY"
  api_secret_env: "BINANCE_API_SECRET"
database:
  url_env: "DATABASE_URL"
risk:
  max_total_allocation_percent: 100
  max_drawdown_percent: 10
  max_order_percent: 10
  max_asset_exposure_percent: 25
  max_total_exposure_percent: 80
  max_open_positions: 5
  max_daily_loss_eur: 5
  max_total_loss_eur: 20
  max_allocation_per_strategy_percent: 40
  max_order_age_minutes: 60
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configStore, err := config.NewStore(configPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	stateStore, err := risk.NewStateStore(filepath.Join(t.TempDir(), "risk-state.json"))
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	riskManager, err := risk.NewStatefulManager(risk.New(), stateStore)
	if err != nil {
		t.Fatalf("NewStatefulManager: %v", err)
	}
	orderRegistry := registry.NewMemory()
	botManager, err := manager.New(manager.Deps{
		StrategyFactory: bots.DefaultFactory,
		Capital:         &risk.FakeCapitalProvider{Value: 1000},
		RiskState:       riskManager,
		Registry:        orderRegistry,
		ConfigStore:     configStore,
		Market:          staticMarket{},
	})
	if err != nil {
		t.Fatalf("manager.New: %v", err)
	}
	t.Cleanup(botManager.Shutdown)
	now := time.Now()
	if err := botManager.Create(domain.Bot{
		ID:                "running-bot",
		Name:              "Running bot",
		Strategy:          domain.StrategyConfig{Type: "noop"},
		Symbol:            "BTCUSDT",
		AllocationPercent: 10,
		Status:            domain.BotStatusCreated,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := botManager.Enable("running-bot"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	pages, err := template.ParseFS(templatesFS, "templates/partials/bot-form-feedback.html")
	if err != nil {
		t.Fatalf("ParseFS: %v", err)
	}
	server := &Server{
		deps: Deps{
			Bots:           botManager,
			Registry:       orderRegistry,
			OrderCanceller: &fakeOrderCanceller{},
		},
		pages: pages,
	}
	app := fiber.New()
	app.Post("/account/orders/cancel-all", server.handleCancelAllOrders)
	req, err := http.NewRequest(http.MethodPost, "/account/orders/cancel-all", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if status := resp.StatusCode; status != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: esperaba %d, hay %d; body=%s", http.StatusOK, status, body)
	}
	if status, ok := botManager.Status("running-bot"); !ok || status != domain.BotStatusPaused {
		t.Fatalf("el bot debe quedar pausado; status=%q, existe=%v", status, ok)
	}
}
