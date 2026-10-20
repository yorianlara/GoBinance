package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/config"
	"mi-bot/internal/domain"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"
)

type fakeMarket struct {
	price float64
	err   error
}

func (f *fakeMarket) Price(ctx context.Context, symbol string) (float64, error) {
	return f.price, f.err
}

type fakeKlines struct {
	candles []bots.Candle
	err     error
}

func (f *fakeKlines) Klines(ctx context.Context, symbol, interval string, limit int) ([]bots.Candle, error) {
	return f.candles, f.err
}

type fakeStrategy struct {
	props []domain.OrderProposal
	err   error
	mu    sync.Mutex
	calls int
	last  bots.MarketData
}

func (f *fakeStrategy) Type() string { return "fake" }
func (f *fakeStrategy) Evaluate(ctx context.Context, data bots.MarketData) ([]domain.OrderProposal, error) {
	f.mu.Lock()
	f.calls++
	f.last = data
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.OrderProposal, len(f.props))
	copy(out, f.props)
	return out, nil
}
func (f *fakeStrategy) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
func (f *fakeStrategy) LastData() bots.MarketData {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func fixedFactory(s bots.Strategy) bots.Factory {
	return func(cfg domain.StrategyConfig) (bots.Strategy, error) {
		return s, nil
	}
}

func baseRiskCfg() config.RiskConfig {
	return config.RiskConfig{
		MaxTotalAllocationPercent:       100,
		MaxDrawdownPercent:              10,
		MaxOrderPercent:                 10,
		MaxAssetExposurePercent:         25,
		MaxTotalExposurePercent:         80,
		MaxOpenPositions:                5,
		MaxDailyLossEUR:                 5,
		MaxTotalLossEUR:                 20,
		MaxAllocationPerStrategyPercent: 40,
	}
}

func makeDeps(t *testing.T, factory bots.Factory, capital risk.CapitalProvider, market MarketProvider) Deps {
	t.Helper()
	return makeDepsWithKlines(t, factory, capital, market, nil)
}

func makeDepsWithKlines(t *testing.T, factory bots.Factory, capital risk.CapitalProvider, market MarketProvider, klines KlineProvider) Deps {
	t.Helper()
	store, err := risk.NewStateStore(filepath.Join(t.TempDir(), "risk-state.json"))
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	sm, err := risk.NewStatefulManager(risk.New(), store)
	if err != nil {
		t.Fatalf("NewStatefulManager: %v", err)
	}
	return Deps{
		StrategyFactory: factory,
		Capital:         capital,
		RiskState:       sm,
		Registry:        registry.NewMemory(),
		ConfigStore:     mustConfigStore(t),
		Market:          market,
		Klines:          klines,
	}
}

func validBot(id string) domain.Bot {
	now := time.Now()
	return domain.Bot{
		ID:                id,
		Name:              "Bot " + id,
		Strategy:          domain.StrategyConfig{Type: "fake"},
		Symbol:            "BTCUSDT",
		AllocationPercent: 10,
		Status:            domain.BotStatusCreated,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func newManager(t *testing.T, strat bots.Strategy, capital risk.CapitalProvider, market MarketProvider) *BotManager {
	t.Helper()
	m, err := New(makeDeps(t, fixedFactory(strat), capital, market))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func mustConfigStore(t *testing.T) *config.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := `app:
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
  max_total_allocation_percent: 100.0
  max_drawdown_percent: 10.0
  max_order_percent: 10.0
  max_asset_exposure_percent: 25.0
  max_total_exposure_percent: 80.0
  max_open_positions: 5
  max_daily_loss_eur: 5.0
  max_total_loss_eur: 20.0
  max_allocation_per_strategy_percent: 40.0
  max_order_age_minutes: 60
`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := config.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func withMaxOrderAge(t *testing.T, r config.RiskConfig, minutes int) config.RiskConfig {
	t.Helper()
	r.MaxOrderAgeMinutes = minutes
	return r
}
func TestNewManagerMissingDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("se esperaba error con Deps vacías")
	}
}

func TestManagerCreateDuplicate(t *testing.T) {
	m := newManager(t, bots.NoopStrategy{}, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	defer m.Shutdown()
	_ = m.Create(validBot("b1"))
	if err := m.Create(validBot("b1")); err == nil {
		t.Fatal("se esperaba error con duplicado")
	}
}

func TestManagerLifecycle(t *testing.T) {
	m := newManager(t, bots.NoopStrategy{}, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	defer m.Shutdown()
	_ = m.Create(validBot("b1"))
	if st, _ := m.Status("b1"); st != domain.BotStatusCreated {
		t.Fatalf("estado: %q", st)
	}
	_ = m.Enable("b1")
	if st, _ := m.Status("b1"); st != domain.BotStatusRunning {
		t.Fatalf("estado: %q", st)
	}
	_ = m.Pause("b1")
	if st, _ := m.Status("b1"); st != domain.BotStatusPaused {
		t.Fatalf("estado: %q", st)
	}
	_, _ = m.StopBot("b1")
	if st, _ := m.Status("b1"); st != domain.BotStatusStopped {
		t.Fatalf("estado: %q", st)
	}
}

func TestManagerDeleteRunningForbidden(t *testing.T) {
	m := newManager(t, bots.NoopStrategy{}, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	defer m.Shutdown()
	_ = m.Create(validBot("b1"))
	_ = m.Enable("b1")
	if err := m.Delete("b1"); err == nil {
		t.Fatal("se esperaba error")
	}
	_ = m.Pause("b1")
	if err := m.Delete("b1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestManagerNoGoroutineLeak(t *testing.T) {
	base := runtime.NumGoroutine()
	m := newManager(t, bots.NoopStrategy{}, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		_ = m.Create(validBot(id))
		_ = m.Enable(id)
	}
	time.Sleep(50 * time.Millisecond)
	m.Shutdown()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	n := runtime.Stack(buf, true)
	if strings.Contains(string(buf[:n]), "manager.(*BotRuntime).run") {
		t.Fatalf("fuga detectada.\n%s", string(buf[:n]))
	}
}

func TestManagerUpdate(t *testing.T) {
	m := newManager(t, bots.NoopStrategy{}, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	defer m.Shutdown()
	_ = m.Create(validBot("b1"))
	updated := validBot("b1")
	updated.AllocationPercent = 25
	_ = m.Update(updated)
	got, _ := m.Get("b1")
	if got.AllocationPercent != 25 {
		t.Fatalf("update: %v", got.AllocationPercent)
	}
	_ = m.Enable("b1")
	if err := m.Update(updated); err == nil {
		t.Fatal("se esperaba error al editar running")
	}
}

func TestManagerAllocationPercentages(t *testing.T) {
	m := newManager(t, bots.NoopStrategy{}, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	defer m.Shutdown()

	bot1 := validBot("b1")
	bot1.AllocationPercent = 20
	if err := m.Create(bot1); err != nil {
		t.Fatal(err)
	}

	bot2 := validBot("b2")
	bot2.AllocationPercent = 30
	if err := m.Create(bot2); err != nil {
		t.Fatal(err)
	}

	bot3 := validBot("b3")
	bot3.Strategy.Type = "other"
	bot3.AllocationPercent = 15
	if err := m.Create(bot3); err != nil {
		t.Fatal(err)
	}

	total, sameStrategy := m.AllocationPercentages("fake")
	if total != 65 {
		t.Fatalf("asignación global esperada 65, hay %v", total)
	}
	if sameStrategy != 50 {
		t.Fatalf("asignación de estrategia esperada 50, hay %v", sameStrategy)
	}
}

func TestTickRejectsConfiguredAllocationOverLimit(t *testing.T) {
	tests := []struct {
		name          string
		maxTotal      float64
		maxStrategy   float64
		expectedTotal float64
	}{
		{name: "global", maxTotal: 50, maxStrategy: 100, expectedTotal: 60},
		{name: "estrategia", maxTotal: 100, maxStrategy: 40, expectedTotal: 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal := domain.OrderProposal{
				Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
				Type: domain.OrderTypeMarket, Quantity: 0.01,
				ProposedAt: time.Now(),
			}
			strategy := &fakeStrategy{props: []domain.OrderProposal{proposal}}
			m := newManager(t, strategy, &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
			defer m.Shutdown()

			cfg := m.deps.ConfigStore.Risk()
			cfg.MaxTotalAllocationPercent = tt.maxTotal
			cfg.MaxAllocationPerStrategyPercent = tt.maxStrategy
			if err := m.deps.ConfigStore.UpdateRisk(cfg); err != nil {
				t.Fatal(err)
			}

			bot1 := validBot("b1")
			bot1.AllocationPercent = tt.expectedTotal / 2
			if err := m.Create(bot1); err != nil {
				t.Fatal(err)
			}
			bot2 := validBot("b2")
			bot2.AllocationPercent = tt.expectedTotal / 2
			if err := m.Create(bot2); err != nil {
				t.Fatal(err)
			}

			rt := NewBotRuntime(bot1, strategy, m.deps)
			rt.tick(context.Background())
			if n, _ := m.deps.Registry.Len(); n != 0 {
				t.Fatalf("la asignación excedida debía impedir la reserva, hay %d órdenes", n)
			}
		})
	}
}

func TestTickReleasesGridProposalWhenRiskRejects(t *testing.T) {
	market := &fakeMarket{price: 100}
	grid, err := bots.NewGridStrategy(domain.StrategyConfig{
		Type: "grid",
		Parameters: map[string]float64{
			"grid_levels": 5, "grid_spacing_percent": 1, "order_size_usdt": 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := makeDeps(t, fixedFactory(grid), &risk.FakeCapitalProvider{Value: 1000}, market)
	cfg := deps.ConfigStore.Risk()
	cfg.MaxOrderPercent = 1
	if err := deps.ConfigStore.UpdateRisk(cfg); err != nil {
		t.Fatal(err)
	}
	rt := NewBotRuntime(validBot("grid"), grid, deps)

	rt.tick(context.Background())
	market.price = 97
	rt.tick(context.Background())

	props, err := grid.Evaluate(context.Background(), bots.MarketData{Symbol: "BTCUSDT", Price: 97})
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 {
		t.Fatalf("tras rechazar la propuesta, Grid debe quedar listo para reintentar; hay %d propuestas", len(props))
	}
}

func TestTickRejectsSellProposal(t *testing.T) {
	proposal := domain.OrderProposal{
		Symbol: "BTCUSDT", Side: domain.OrderSideSell,
		Type: domain.OrderTypeMarket, Quantity: 0.01,
		ProposedAt: time.Now(),
	}
	strategy := &fakeStrategy{props: []domain.OrderProposal{proposal}}
	deps := makeDeps(t, fixedFactory(strategy), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), strategy, deps)

	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 0 {
		t.Fatalf("las ventas deben permanecer deshabilitadas, hay %d reservas", n)
	}
}

// --- flujo de tick ---

func TestTickReservesApprovedProposal(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 1 {
		t.Fatalf("se esperaba 1 reserva, hay %d", n)
	}
}

func TestTickNoopDoesNothing(t *testing.T) {
	strat := bots.NoopStrategy{}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 0 {
		t.Fatalf("Noop no debe reservar, hay %d", n)
	}
}

func TestTickNoCapitalDoesNotReserve(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 0}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 0 {
		t.Fatalf("capital 0 no debe reservar, hay %d", n)
	}
}

func TestTickNoPriceDoesNotCallStrategy(t *testing.T) {
	strat := &fakeStrategy{}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{err: errors.New("sin precio")})
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if strat.Calls() != 0 {
		t.Fatalf("estrategia no debía llamarse, calls=%d", strat.Calls())
	}
}

func TestTickKlinesPopulated(t *testing.T) {
	strat := &fakeStrategy{}
	kl := &fakeKlines{candles: []bots.Candle{{Close: 100}}}
	deps := makeDepsWithKlines(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100}, kl)
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if got := len(strat.LastData().Candles); got != 1 {
		t.Fatalf("se esperaba 1 vela, hay %d", got)
	}
}

func TestTickKlinesErrorContinues(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	kl := &fakeKlines{err: errors.New("klines fallan")}
	deps := makeDepsWithKlines(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100}, kl)
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if strat.Calls() != 1 {
		t.Fatalf("estrategia debía llamarse aunque klines falle, calls=%d", strat.Calls())
	}
	if len(strat.LastData().Candles) != 0 {
		t.Fatalf("Candles debía estar vacío, hay %d", len(strat.LastData().Candles))
	}
	if n, _ := deps.Registry.Len(); n != 1 {
		t.Fatalf("debía reservar, hay %d", n)
	}
}

func TestTickDuplicateProposalOnlyOneReserved(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p, p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 1 {
		t.Fatalf("se esperaba 1 reserva, hay %d", n)
	}
}

func TestTickRiskRejects(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 100, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 0 {
		t.Fatalf("risk debía rechazar, hay %d", n)
	}
}

func TestTickAfterOnOrderFilledRejectsByExposure(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 2, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.RiskState.OnOrderFilled("BTCUSDT", 700)
	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 0 {
		t.Fatalf("risk debía rechazar por exposición, hay %d", n)
	}
}

func TestTickWithDCAStrategy(t *testing.T) {
	dca, err := bots.NewDCAStrategy(domain.StrategyConfig{
		Type:       "dca",
		Parameters: map[string]float64{"amount_per_buy_usdt": 10, "interval_minutes": 60},
	})
	if err != nil {
		t.Fatalf("NewDCAStrategy: %v", err)
	}
	deps := makeDeps(t, fixedFactory(dca), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), dca, deps)
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 1 {
		t.Fatalf("DCA debía reservar 1, hay %d", n)
	}
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 1 {
		t.Fatalf("segundo tick no debía reservar, hay %d", n)
	}
}

func TestTickDCARejectedByRiskCanRetry(t *testing.T) {
	dca, err := bots.NewDCAStrategy(domain.StrategyConfig{
		Type: "dca",
		Parameters: map[string]float64{
			"amount_per_buy_usdt": 20, "interval_minutes": 60, "max_buys": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := makeDeps(t, fixedFactory(dca), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	cfg := deps.ConfigStore.Risk()
	cfg.MaxOrderPercent = 1
	if err := deps.ConfigStore.UpdateRisk(cfg); err != nil {
		t.Fatal(err)
	}
	rt := NewBotRuntime(validBot("dca"), dca, deps)

	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 0 {
		t.Fatalf("la propuesta excedida por riesgo no debía reservarse; hay %d", n)
	}

	cfg.MaxOrderPercent = 10
	if err := deps.ConfigStore.UpdateRisk(cfg); err != nil {
		t.Fatal(err)
	}
	rt.tick(context.Background())
	if n, _ := deps.Registry.Len(); n != 1 {
		t.Fatalf("DCA debe reintentar tras el rechazo de riesgo; hay %d reservas", n)
	}
}
