package web

import (
	"context"
	"html/template"
	"io"
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

// fakeStopExecutor simula el exchange para el stop: aquí solo importa que la
// venta market llene con la cantidad pedida.
type fakeStopExecutor struct {
	placed       []exchange.OrderRequest
	freeBalances map[string]float64
	baseAssets   map[string]string
}

func (f *fakeStopExecutor) PlaceOrder(_ context.Context, req exchange.OrderRequest) (exchange.OrderResponse, error) {
	f.placed = append(f.placed, req)
	return exchange.OrderResponse{
		OrderID: 900, Symbol: req.Symbol, Status: "FILLED",
		ExecutedQty: req.Quantity, CummulativeQuoteQty: req.Quantity * 10,
	}, nil
}

func (f *fakeStopExecutor) GetOrder(_ context.Context, symbol string, orderID int64) (exchange.OrderResponse, error) {
	return exchange.OrderResponse{OrderID: orderID, Symbol: symbol, Status: "FILLED"}, nil
}

func (f *fakeStopExecutor) GetOrderByClientID(_ context.Context, symbol, clientOrderID string) (exchange.OrderResponse, error) {
	return exchange.OrderResponse{}, exchange.ErrOrderNotFound
}

func (f *fakeStopExecutor) CancelOrder(_ context.Context, symbol string, orderID int64) error {
	return nil
}

func (f *fakeStopExecutor) GetTrades(_ context.Context, symbol string, orderID int64) ([]exchange.Fill, error) {
	return nil, nil
}

func (f *fakeStopExecutor) GetFreeBalance(_ context.Context, asset string) (float64, error) {
	return f.freeBalances[asset], nil
}

func (f *fakeStopExecutor) GetSymbolBaseAsset(_ context.Context, symbol string) (string, error) {
	return f.baseAssets[symbol], nil
}

func newStopTestBotManager(t *testing.T, exec manager.OrderExecutor, reg registry.OrderRegistry) *manager.BotManager {
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
		Registry:        reg,
		ConfigStore:     configStore,
		Market:          staticMarket{},
		Executor:        exec,
	})
	if err != nil {
		t.Fatalf("manager.New: %v", err)
	}
	return botManager
}

func newStopTestApp(t *testing.T, botManager *manager.BotManager) *fiber.App {
	t.Helper()
	pages, err := template.ParseFS(templatesFS,
		"templates/partials/bots-results.html",
		"templates/partials/bots-lista.html",
	)
	if err != nil {
		t.Fatalf("ParseFS: %v", err)
	}
	server := &Server{deps: Deps{Bots: botManager}, pages: pages}
	app := fiber.New()
	app.Post("/bots/:id/stop", server.handleStop)
	return app
}

func postStop(t *testing.T, app *fiber.App, id string) (string, int) {
	t.Helper()
	resp, err := app.Test(mustReq(t, "POST", "/bots/"+id+"/stop", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(body), resp.StatusCode
}

func TestHandleStopReportsAutomaticSell(t *testing.T) {
	reg := registry.NewMemory()
	buyID, err := reg.Reserve(domain.OrderProposal{
		BotID: "stop-me", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.5, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve buy: %v", err)
	}
	if err := reg.MarkSubmitted(buyID, 123); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	if err := reg.MarkFilled(buyID, 0.5, 40); err != nil {
		t.Fatalf("MarkFilled: %v", err)
	}

	botManager := newStopTestBotManager(t, &fakeStopExecutor{
		freeBalances: map[string]float64{"BTC": 0.5},
		baseAssets:   map[string]string{"BTCUSDT": "BTC"},
	}, reg)
	t.Cleanup(botManager.Shutdown)
	if err := botManager.Create(domain.Bot{
		ID: "stop-me", Name: "stop-me", Strategy: domain.StrategyConfig{Type: "noop"},
		Symbol: "BTCUSDT", AllocationPercent: 10, Status: domain.BotStatusCreated,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	app := newStopTestApp(t, botManager)

	body, status := postStop(t, app, "stop-me")
	if status != fiber.StatusOK {
		t.Fatalf("status %d; body=%s", status, body)
	}
	if !strings.Contains(body, "Bot detenido y posición vendida") {
		t.Fatalf("la respuesta debe anunciar la venta automática: %s", body)
	}
	if !strings.Contains(body, "0.50000000") || !strings.Contains(body, "5.00") || !strings.Contains(body, "40.00") || !strings.Contains(body, "-35.00") {
		t.Fatalf("la respuesta debe contener cantidad, ingreso y PnL: %s", body)
	}
}

func TestHandleStopReportsNoPosition(t *testing.T) {
	reg := registry.NewMemory()
	botManager := newStopTestBotManager(t, &fakeStopExecutor{}, reg)
	t.Cleanup(botManager.Shutdown)
	if err := botManager.Create(domain.Bot{
		ID: "stop-empty", Name: "stop-empty", Strategy: domain.StrategyConfig{Type: "noop"},
		Symbol: "BTCUSDT", AllocationPercent: 10, Status: domain.BotStatusCreated,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	app := newStopTestApp(t, botManager)
	body, status := postStop(t, app, "stop-empty")
	if status != fiber.StatusOK {
		t.Fatalf("status %d; body=%s", status, body)
	}
	if !strings.Contains(body, "no quedaba posición abierta por vender") {
		t.Fatalf("la respuesta debe informar de que no había posición: %s", body)
	}
}
