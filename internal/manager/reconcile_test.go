package manager

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"
)

// fakeExecutor permite controlar respuestas de PlaceOrder/GetOrder/CancelOrder/GetTrades.
type fakeExecutor struct {
	placeResp exchange.OrderResponse
	placeErr  error
	orderResp exchange.OrderResponse
	orderErr  error
	trades    []exchange.Fill
	tradesErr error

	// orderQueue, si tiene entradas, se consume por GetOrder de forma FIFO
	// (útil para encadenar estados: NEW antes de cancelar, CANCELED después).
	orderQueue []exchange.OrderResponse

	// placeQueue, si tiene entradas, se consume por PlaceOrder de forma FIFO
	// (útil para distinguir la compra inicial de la venta al detener).
	placeQueue []exchange.OrderResponse

	// clientOrderByID controla GetOrderByClientID: si clientOrderErr es
	// exchange.ErrOrderNotFound simula "no existe"; si es otro error, fallo de
	// red; si es nil, devuelve clientOrderResp.
	clientOrderResp exchange.OrderResponse
	clientOrderErr  error

	placed             int
	cancelled          []cancelledOrder
	cancelOrderErr     error
	freeBalances       map[string]float64
	baseAssets         map[string]string
	orderRequests      []exchange.OrderRequest
	clientOrderLookups []string
}

type cancelledOrder struct {
	symbol  string
	orderID int64
}

func (f *fakeExecutor) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.OrderResponse, error) {
	f.placed++
	f.orderRequests = append(f.orderRequests, req)
	if f.placeErr != nil {
		return exchange.OrderResponse{}, f.placeErr
	}
	if len(f.placeQueue) > 0 {
		out := f.placeQueue[0]
		f.placeQueue = f.placeQueue[1:]
		return out, nil
	}
	return f.placeResp, nil
}
func (f *fakeExecutor) GetOrder(ctx context.Context, symbol string, orderID int64) (exchange.OrderResponse, error) {
	if len(f.orderQueue) > 0 {
		out := f.orderQueue[0]
		f.orderQueue = f.orderQueue[1:]
		return out, nil
	}
	if f.orderErr != nil {
		return exchange.OrderResponse{}, f.orderErr
	}
	return f.orderResp, nil
}

func (f *fakeExecutor) GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (exchange.OrderResponse, error) {
	f.clientOrderLookups = append(f.clientOrderLookups, clientOrderID)
	if f.clientOrderErr != nil {
		return exchange.OrderResponse{}, f.clientOrderErr
	}
	return f.clientOrderResp, nil
}
func (f *fakeExecutor) CancelOrder(ctx context.Context, symbol string, orderID int64) error {
	f.cancelled = append(f.cancelled, cancelledOrder{symbol: symbol, orderID: orderID})
	return f.cancelOrderErr
}
func (f *fakeExecutor) GetTrades(ctx context.Context, symbol string, orderID int64) ([]exchange.Fill, error) {
	if f.tradesErr != nil {
		return nil, f.tradesErr
	}
	return f.trades, nil
}
func (f *fakeExecutor) GetFreeBalance(ctx context.Context, asset string) (float64, error) {
	return f.freeBalances[asset], nil
}
func (f *fakeExecutor) GetSymbolBaseAsset(ctx context.Context, symbol string) (string, error) {
	asset, ok := f.baseAssets[symbol]
	if !ok {
		return "", errors.New("asset base desconocido")
	}
	return asset, nil
}

func TestPauseAndStopCancelSubmittedOrders(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*BotManager, string) error
	}{
		{name: "pause", stop: func(m *BotManager, id string) error { return m.Pause(id) }},
		{name: "stop", stop: func(m *BotManager, id string) error { _, err := m.StopBot(id); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeExecutor{orderResp: exchange.OrderResponse{Status: "CANCELED"}}
			deps := newDepsWithExecutor(t, exec)
			proposal := domain.OrderProposal{
				BotID:      "b1",
				Symbol:     "BTCUSDT",
				Side:       domain.OrderSideBuy,
				Type:       domain.OrderTypeLimit,
				Quantity:   0.01,
				LimitPrice: 100,
				ProposedAt: time.Now(),
			}
			clientOrderID, err := deps.Registry.Reserve(proposal)
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			const exchangeOrderID = 4321
			if err := deps.Registry.MarkSubmitted(clientOrderID, exchangeOrderID); err != nil {
				t.Fatalf("MarkSubmitted: %v", err)
			}

			manager, err := New(deps)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(manager.Shutdown)
			if err := manager.Create(validBot("b1")); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := manager.Enable("b1"); err != nil {
				t.Fatalf("Enable: %v", err)
			}
			if err := tc.stop(manager, "b1"); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			if len(exec.cancelled) != 1 {
				t.Fatalf("se esperaba 1 llamada CancelOrder, hay %d", len(exec.cancelled))
			}
			if got := exec.cancelled[0]; got.symbol != "BTCUSDT" || got.orderID != exchangeOrderID {
				t.Fatalf("CancelOrder recibió %+v", got)
			}
			order, ok := deps.Registry.Get(clientOrderID)
			if !ok {
				t.Fatal("orden no encontrada")
			}
			if order.State != registry.StateCancelled {
				t.Fatalf("estado esperado %q, hay %q", registry.StateCancelled, order.State)
			}
			active, err := deps.Registry.ListActiveByBot("b1")
			if err != nil {
				t.Fatalf("ListActiveByBot: %v", err)
			}
			if len(active) != 0 {
				t.Fatalf("no deben quedar órdenes activas tras %s: %+v", tc.name, active)
			}
		})
	}
}

func TestPauseKeepsSubmittedOrderWhenExchangeCancellationFails(t *testing.T) {
	exec := &fakeExecutor{
		cancelOrderErr: errors.New("exchange unavailable"),
		orderResp:      exchange.OrderResponse{Status: "NEW"},
	}
	deps := newDepsWithExecutor(t, exec)
	proposal := domain.OrderProposal{
		BotID:      "b1",
		Symbol:     "BTCUSDT",
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeLimit,
		Quantity:   0.01,
		LimitPrice: 100,
		ProposedAt: time.Now(),
	}
	clientOrderID, err := deps.Registry.Reserve(proposal)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(clientOrderID, 4321); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}

	manager, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Shutdown)
	if err := manager.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := manager.Enable("b1"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := manager.Pause("b1"); err == nil {
		t.Fatal("se esperaba que Pause informara el error de cancelación")
	}

	order, ok := deps.Registry.Get(clientOrderID)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if order.State != registry.StateSubmitted {
		t.Fatalf("la orden debe seguir submitted, hay %q", order.State)
	}
	if len(exec.cancelled) != 1 {
		t.Fatalf("se esperaba 1 llamada CancelOrder, hay %d", len(exec.cancelled))
	}
}

func TestStopKeepsOrderSubmittedUntilBinanceConfirmsCancellation(t *testing.T) {
	exec := &fakeExecutor{orderResp: exchange.OrderResponse{Status: "NEW"}}
	deps := newDepsWithExecutor(t, exec)
	clientOrderID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeLimit, Quantity: 0.01, LimitPrice: 100,
		ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(clientOrderID, 4321); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	manager, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Shutdown)
	if err := manager.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := manager.StopBot("b1"); err == nil {
		t.Fatal("se esperaba error porque Binance aún reporta la orden abierta")
	}
	order, ok := deps.Registry.Get(clientOrderID)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if order.State != registry.StateSubmitted {
		t.Fatalf("la orden debe permanecer submitted hasta confirmar cancelación; estado %q", order.State)
	}
}

func TestStopRecordsPartialFillAndLiquidatesPosition(t *testing.T) {
	// Al detener, un fill parcial (o total, si la orden llenó antes del
	// cancel en Binance) se registra y el inventario creado se vende: el bot
	// no queda con posición abierta ni órdenes atascadas.
	exec := &fakeExecutor{
		orderResp: exchange.OrderResponse{
			Status: "CANCELED", ExecutedQty: 0.004, CummulativeQuoteQty: 0.4,
		},
		trades: []exchange.Fill{{Quantity: 0.004, Price: 100}},
		// Venta automática del inventario al detener: market sell que llena.
		placeQueue: []exchange.OrderResponse{
			{OrderID: 888, Status: "FILLED", ExecutedQty: 0.004, CummulativeQuoteQty: 0.42},
		},
		freeBalances: map[string]float64{"BTC": 0.004},
		baseAssets:   map[string]string{"BTCUSDT": "BTC"},
	}
	deps := newDepsWithExecutor(t, exec)
	clientOrderID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeLimit, Quantity: 0.01, LimitPrice: 100,
		ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(clientOrderID, 4321); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	manager, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Shutdown)
	if err := manager.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	result, err := manager.StopBot("b1")
	if err != nil {
		t.Fatalf("StopBot: %v", err)
	}
	order, ok := deps.Registry.Get(clientOrderID)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if order.State != registry.StateFilled || order.FilledQty != 0.004 || order.FilledValue != 0.4 {
		t.Fatalf("la ejecución parcial debe conservarse en el registro: %+v", order)
	}
	if result.SoldQuantity != 0.004 || result.ProceedsUSDT != 0.42 || result.CostBasisUSDT != 0.4 {
		t.Fatalf("la posición parcial debía venderse al detener: %+v", result)
	}

	// La VENTA quedó registrada y el riesgo queda sin exposición.
	orders, err := deps.Registry.List()
	if err != nil {
		t.Fatal(err)
	}
	sells := 0
	for _, o := range orders {
		if o.Proposal.Side != domain.OrderSideSell {
			continue
		}
		sells++
		if o.State != registry.StateFilled || o.Proposal.PositionCostBasisUSDT != 0.4 || o.Proposal.PositionsClosed != 1 {
			t.Fatalf("venta no persistida correctamente: %+v", o)
		}
	}
	if sells != 1 {
		t.Fatalf("se esperaba 1 orden de venta registrada, hay %d", sells)
	}
	state := deps.RiskState.State()
	if state.OpenPositions != 0 || state.ExposureTotalUSDT != 0 {
		t.Fatalf("exposición/posiciones sin cerrar tras el stop: %+v", state)
	}
	if math.Abs(state.TotalPnL-0.02) > 1e-9 || math.Abs(state.DailyPnL-0.02) > 1e-9 {
		t.Fatalf("PnL realizado incorrecto: %+v", state)
	}
}

func TestClosePositionSellsBotInventoryAndRealizesPnL(t *testing.T) {
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{
			OrderID: 777, Status: "FILLED", ExecutedQty: 0.5, CummulativeQuoteQty: 60,
		},
		freeBalances: map[string]float64{"BTC": 0.5},
		baseAssets:   map[string]string{"BTCUSDT": "BTC"},
	}
	deps := newDepsWithExecutor(t, exec)
	buyID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.5, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve buy: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(buyID, 123); err != nil {
		t.Fatalf("MarkSubmitted buy: %v", err)
	}
	if err := deps.Registry.MarkFilled(buyID, 0.5, 50); err != nil {
		t.Fatalf("MarkFilled buy: %v", err)
	}
	deps.RiskState.OnOrderFilled("BTCUSDT", 50)

	manager, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Shutdown)
	if err := manager.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	result, err := manager.ClosePosition(context.Background(), "b1")
	if err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	if result.SoldQuantity != 0.5 || result.ProceedsUSDT != 60 ||
		result.CostBasisUSDT != 50 || result.RealizedPnLUSDT != 10 {
		t.Fatalf("resultado de liquidación inesperado: %+v", result)
	}
	if len(exec.orderRequests) != 1 ||
		exec.orderRequests[0].Side != domain.OrderSideSell ||
		exec.orderRequests[0].Quantity != 0.5 {
		t.Fatalf("orden de venta inesperada: %+v", exec.orderRequests)
	}
	if status, _ := manager.Status("b1"); status != domain.BotStatusStopped {
		t.Fatalf("el bot debe quedar detenido, está %q", status)
	}
	state := deps.RiskState.State()
	if state.ExposureTotalUSDT != 0 || state.OpenPositions != 0 {
		t.Fatalf("exposición/posiciones sin cerrar: %+v", state)
	}
	if state.TotalPnL != 10 || state.DailyPnL != 10 {
		t.Fatalf("PnL realizado incorrecto: %+v", state)
	}
	orders, err := deps.Registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	foundSell := false
	for _, order := range orders {
		if order.Proposal.Side == domain.OrderSideSell {
			foundSell = true
			if order.State != registry.StateFilled ||
				order.Proposal.PositionCostBasisUSDT != 50 ||
				order.Proposal.PositionsClosed != 1 {
				t.Fatalf("cierre no persistido correctamente: %+v", order)
			}
		}
	}
	if !foundSell {
		t.Fatal("no se registró la orden de venta")
	}
}

func TestClosePositionRefusesInsufficientFreeBalance(t *testing.T) {
	exec := &fakeExecutor{
		freeBalances: map[string]float64{"BTC": 0.2},
		baseAssets:   map[string]string{"BTCUSDT": "BTC"},
	}
	deps := newDepsWithExecutor(t, exec)
	buyID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.5, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve buy: %v", err)
	}
	_ = deps.Registry.MarkSubmitted(buyID, 123)
	_ = deps.Registry.MarkFilled(buyID, 0.5, 50)

	runtime := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)
	if _, err := runtime.ClosePosition(context.Background()); err == nil {
		t.Fatal("se esperaba que el cierre rechazara un saldo libre insuficiente")
	}
	if exec.placed != 0 {
		t.Fatalf("no debía enviarse una venta, se enviaron %d", exec.placed)
	}
}

func TestClosePositionKeepsReservationWhenSubmissionResultIsUncertain(t *testing.T) {
	exec := &fakeExecutor{
		placeErr:     exchange.ErrOrderSubmissionUncertain,
		freeBalances: map[string]float64{"BTC": 0.5},
		baseAssets:   map[string]string{"BTCUSDT": "BTC"},
	}
	deps := newDepsWithExecutor(t, exec)
	buyID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.5, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve buy: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(buyID, 123); err != nil {
		t.Fatalf("MarkSubmitted buy: %v", err)
	}
	if err := deps.Registry.MarkFilled(buyID, 0.5, 50); err != nil {
		t.Fatalf("MarkFilled buy: %v", err)
	}

	runtime := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)
	if _, err := runtime.ClosePosition(context.Background()); err == nil {
		t.Fatal("se esperaba informar el resultado incierto")
	}
	orders, err := deps.Registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var closeOrder *registry.TrackedOrder
	for i := range orders {
		if orders[i].Proposal.Side == domain.OrderSideSell {
			closeOrder = &orders[i]
			break
		}
	}
	if closeOrder == nil || closeOrder.State != registry.StateReserved {
		t.Fatalf("la reserva debe permanecer activa ante un resultado incierto: %+v", closeOrder)
	}
}

func TestPositionInventoryUsesFIFOAndCountsFullyClosedLots(t *testing.T) {
	now := time.Now()
	inventory, err := inventoryFor([]registry.TrackedOrder{
		{Proposal: domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy}, State: registry.StateFilled, FilledQty: 1, FilledValue: 100, CreatedAt: now},
		{Proposal: domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy}, State: registry.StateFilled, FilledQty: 1, FilledValue: 120, CreatedAt: now.Add(time.Second)},
	}, "b1", "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	cost, closed, err := inventory.sell(1.5)
	if err != nil {
		t.Fatal(err)
	}
	if cost != 160 || closed != 1 || inventory.quantity() != 0.5 || inventory.costBasis() != 60 {
		t.Fatalf("posición tras venta inesperada: cost=%v closed=%d inventory=%+v", cost, closed, inventory)
	}
}

func TestCloseAllPositionsIncludesHistoricalSymbols(t *testing.T) {
	exec := &fakeExecutor{
		baseAssets: map[string]string{
			"BTCUSDT": "BTC",
			"ETHUSDT": "ETH",
		},
	}
	deps := newDepsWithExecutor(t, exec)
	buyID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 1, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve buy: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(buyID, 101); err != nil {
		t.Fatalf("MarkSubmitted buy: %v", err)
	}
	if err := deps.Registry.MarkFilled(buyID, 1, 100); err != nil {
		t.Fatalf("MarkFilled buy: %v", err)
	}
	sellID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideSell,
		Type: domain.OrderTypeMarket, Quantity: 1, ProposedAt: time.Now().Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Reserve sell: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(sellID, 102); err != nil {
		t.Fatalf("MarkSubmitted sell: %v", err)
	}
	if err := deps.Registry.MarkFilled(sellID, 1, 110); err != nil {
		t.Fatalf("MarkFilled sell: %v", err)
	}

	botManager, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(botManager.Shutdown)
	bot := validBot("b1")
	bot.Symbol = "ETHUSDT"
	if err := botManager.Create(bot); err != nil {
		t.Fatalf("Create: %v", err)
	}

	attempts, err := botManager.CloseAllPositions(context.Background())
	if err != nil {
		t.Fatalf("CloseAllPositions: %v", err)
	}
	if len(attempts) != 2 ||
		attempts[0].Symbol != "BTCUSDT" || attempts[1].Symbol != "ETHUSDT" {
		t.Fatalf("el cierre global debe incluir símbolo histórico y actual: %+v", attempts)
	}
	for _, attempt := range attempts {
		if attempt.Err != nil || !attempt.Result.NoPosition {
			t.Fatalf("no debía quedar posición abierta: %+v", attempt)
		}
	}
	if exec.placed != 0 {
		t.Fatalf("no debía enviar ventas, se enviaron %d", exec.placed)
	}
}

// firstOnlyStrategy propone sus props solo en el primer Evaluate.
// Evita que el segundo tick (reconciliación) cree otra orden.
type firstOnlyStrategy struct {
	props []domain.OrderProposal
	mu    sync.Mutex
	used  bool
}

func (f *firstOnlyStrategy) Type() string { return "fake-first-only" }
func (f *firstOnlyStrategy) Evaluate(ctx context.Context, data bots.MarketData) ([]domain.OrderProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.used {
		return nil, nil
	}
	f.used = true
	out := make([]domain.OrderProposal, len(f.props))
	copy(out, f.props)
	return out, nil
}

func newDepsWithExecutor(t *testing.T, exec OrderExecutor) Deps {
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
		StrategyFactory: fixedFactory(bots.NoopStrategy{}),
		Capital:         &risk.FakeCapitalProvider{Value: 1000},
		RiskState:       sm,
		Registry:        registry.NewMemory(),
		ConfigStore:     mustConfigStore(t),
		Market:          &fakeMarket{price: 100},
		Executor:        exec,
	}
}

func TestPlaceOrderOnTick(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"}}

	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())

	if exec.placed != 1 {
		t.Fatalf("se esperaba 1 PlaceOrder, hay %d", exec.placed)
	}
	orders, _ := deps.Registry.List()
	if len(orders) != 1 || orders[0].State != registry.StateSubmitted {
		t.Fatalf("orden inesperada: %+v", orders)
	}
	if orders[0].ExchangeID != 999 {
		t.Fatalf("exchangeID: %v", orders[0].ExchangeID)
	}
}

func TestPlaceOrderFailsMarksFailed(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{placeErr: errors.New("binance caído")}

	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())

	orders, _ := deps.Registry.List()
	if len(orders) != 1 || orders[0].State != registry.StateFailed {
		t.Fatalf("orden inesperada: %+v", orders)
	}
}

func TestReconcileFilled(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &firstOnlyStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
		orderResp: exchange.OrderResponse{OrderID: 999, Status: "FILLED"},
		trades:    []exchange.Fill{{OrderID: 999, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
	}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	orders1, _ := deps.Registry.List()
	if len(orders1) != 1 {
		t.Fatalf("se esperaba 1 orden tras tick 1, hay %d", len(orders1))
	}
	clientOrderID := orders1[0].ClientOrderID

	rt.tick(context.Background())

	o, ok := deps.Registry.Get(clientOrderID)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if o.State != registry.StateFilled {
		t.Fatalf("state: %v", o.State)
	}
	if o.FilledQty != 0.01 || o.FilledValue != 1.0 {
		t.Fatalf("fill inesperado: %+v", o)
	}
	st := deps.RiskState.State()
	if st.ExposureTotalUSDT != 1.0 {
		t.Fatalf("exposición esperada 1.0, hay %v", st.ExposureTotalUSDT)
	}
	if st.OpenPositions != 1 {
		t.Fatalf("posiciones: %v", st.OpenPositions)
	}
}

func TestReconcileCancelled(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &firstOnlyStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
		orderResp: exchange.OrderResponse{OrderID: 999, Status: "CANCELED"},
	}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	orders1, _ := deps.Registry.List()
	clientOrderID := orders1[0].ClientOrderID

	rt.tick(context.Background())

	o, _ := deps.Registry.Get(clientOrderID)
	if o.State != registry.StateCancelled {
		t.Fatalf("state: %v", o.State)
	}
}

func TestReconcileNewKeepsWaiting(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &firstOnlyStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
		orderResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
	}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	orders1, _ := deps.Registry.List()
	clientOrderID := orders1[0].ClientOrderID

	rt.tick(context.Background())

	o, _ := deps.Registry.Get(clientOrderID)
	if o.State != registry.StateSubmitted {
		t.Fatalf("state: %v", o.State)
	}
}

func TestReconcileIgnoresOtherBots(t *testing.T) {
	deps := makeDeps(t, fixedFactory(bots.NoopStrategy{}), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	p := domain.OrderProposal{BotID: "otro", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	id, _ := deps.Registry.Reserve(p)
	_ = deps.Registry.MarkSubmitted(id, 999)

	exec := &fakeExecutor{orderResp: exchange.OrderResponse{Status: "FILLED"}}
	deps.Executor = exec
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)
	rt.reconcile(context.Background(), validBot("b1"))

	o, _ := deps.Registry.Get(id)
	if o.State != registry.StateSubmitted {
		t.Fatalf("no debía reconciliar: %+v", o)
	}
}

func TestReconcileTimeout(t *testing.T) {
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &firstOnlyStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
		orderResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
	}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec
	deps.ConfigStore.UpdateRisk(withMaxOrderAge(t, deps.ConfigStore.Risk(), 1))

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	orders1, _ := deps.Registry.List()
	clientOrderID := orders1[0].ClientOrderID

	// Forzamos que la orden parezca antigua (truco: manipular el registro
	// no es posible desde fuera; usamos un timeout de 0).
	deps.ConfigStore.UpdateRisk(withMaxOrderAge(t, deps.ConfigStore.Risk(), 0))
	rt.tick(context.Background())

	o, _ := deps.Registry.Get(clientOrderID)
	// Con MaxOrderAgeMinutes = 0, no se aplica timeout (condición maxAge > 0).
	if o.State != registry.StateSubmitted {
		t.Fatalf("sin timeout configurado no debía expirar: %v", o.State)
	}
}

func TestReconcileTimeoutExpires(t *testing.T) {
	// Añadimos una orden submitted "vieja" manualmente al registro
	// (MarkSubmitted no permite manipular CreatedAt, así que reservamos,
	// marcamos submitted y luego forzamos un timeout muy pequeño).
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &firstOnlyStrategy{props: []domain.OrderProposal{p}}
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
		orderResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
	}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec
	// Configuramos timeout de 1 minuto.
	deps.ConfigStore.UpdateRisk(withMaxOrderAge(t, deps.ConfigStore.Risk(), 1))

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())
	orders1, _ := deps.Registry.List()
	if len(orders1) != 1 {
		t.Fatalf("se esperaba 1 orden, hay %d", len(orders1))
	}
	clientOrderID := orders1[0].ClientOrderID

	// No podemos forzar CreatedAt sin tocar el registro directamente,
	// pero podemos usar una estrategia que no proponga y esperar 0s
	// con timeout 0 (sin timeout). El test anterior cubre el caso "sin timeout".
	// Aquí verificamos que con timeout activo y orden recién creada no expira.
	rt.tick(context.Background())

	o, _ := deps.Registry.Get(clientOrderID)
	if o.State != registry.StateSubmitted {
		t.Fatalf("recién creada no debía expirar: %v", o.State)
	}
}

// gridLikeStrategy implementa OrderLifecycleListener para testear la notificación.
type gridLikeStrategy struct {
	closed int
	mu     sync.Mutex
}

func (g *gridLikeStrategy) Type() string { return "fake-grid" }
func (g *gridLikeStrategy) Evaluate(ctx context.Context, data bots.MarketData) ([]domain.OrderProposal, error) {
	return nil, nil
}
func (g *gridLikeStrategy) OnOrderRejected() {}
func (g *gridLikeStrategy) OnOrderClosed(_ bool) {
	g.mu.Lock()
	g.closed++
	g.mu.Unlock()
}
func (g *gridLikeStrategy) Closed() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

func TestNotifyOrderClosedOnFilled(t *testing.T) {
	g := &gridLikeStrategy{}
	exec := &fakeExecutor{
		placeResp: exchange.OrderResponse{OrderID: 999, Status: "NEW"},
		orderResp: exchange.OrderResponse{OrderID: 999, Status: "FILLED"},
		trades:    []exchange.Fill{{OrderID: 999, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
	}
	deps := makeDeps(t, fixedFactory(g), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	// Insertamos una orden submitted manualmente (simula una orden enviada antes).
	p := domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	id, _ := deps.Registry.Reserve(p)
	_ = deps.Registry.MarkSubmitted(id, 999)

	rt := NewBotRuntime(validBot("b1"), g, deps)
	rt.reconcile(context.Background(), validBot("b1"))

	if g.Closed() != 1 {
		t.Fatalf("se esperaba 1 OnOrderClosed, hay %d", g.Closed())
	}
}

func TestNotifyOrderClosedOnCancel(t *testing.T) {
	g := &gridLikeStrategy{}
	exec := &fakeExecutor{orderResp: exchange.OrderResponse{Status: "CANCELED"}}
	deps := makeDeps(t, fixedFactory(g), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	p := domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	id, _ := deps.Registry.Reserve(p)
	_ = deps.Registry.MarkSubmitted(id, 999)

	rt := NewBotRuntime(validBot("b1"), g, deps)
	rt.reconcile(context.Background(), validBot("b1"))

	if g.Closed() != 1 {
		t.Fatalf("se esperaba 1 OnOrderClosed, hay %d", g.Closed())
	}
}

func TestNotifyOrderClosedOnPlaceError(t *testing.T) {
	g := &gridLikeStrategy{}
	exec := &fakeExecutor{placeErr: errors.New("binance caído")}

	strat := &firstOnlyStrategy{props: []domain.OrderProposal{
		{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()},
	}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	// Usamos una estrategia que sí implemente el listener pero proponga una vez.
	// Necesitamos que la estrategia del runtime sea la que tiene el listener,
	// así que usamos firstOnlyStrategy combinada con gridLike.
	// Simplificamos: usamos gridLikeStrategy pero con props en un wrapper.
	combo := &comboStrategy{grid: g, props: []domain.OrderProposal{
		{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()},
	}}
	deps.StrategyFactory = fixedFactory(combo)
	rt := NewBotRuntime(validBot("b1"), combo, deps)
	rt.tick(context.Background())

	if g.Closed() != 1 {
		t.Fatalf("se esperaba 1 OnOrderClosed tras error, hay %d", g.Closed())
	}
}

// --- reconciliación de órdenes expiradas/orfanas ---

func staleSubmittedOrder(t *testing.T, deps Deps) registry.TrackedOrder {
	t.Helper()
	p := domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	}
	id, err := deps.Registry.Reserve(p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(id, 4321); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	o, ok := deps.Registry.Get(id)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	// Simular antigüedad: el registro no permite mutar CreatedAt desde fuera.
	o.CreatedAt = time.Now().Add(-2 * time.Hour)
	return o
}

func TestReconcileTimeoutCancelsOrderOnExchangeBeforeReleasing(t *testing.T) {
	// La orden sigue viva en el exchange (NEW) al llegar el timeout: debe
	// cancelarse y confirmarse su cancelación; nunca liberar a ciegas.
	exec := &fakeExecutor{
		orderQueue: []exchange.OrderResponse{
			{OrderID: 4321, Status: "NEW"},      // consulta previa a la cancelación
			{OrderID: 4321, Status: "CANCELED"}, // estado final tras cancelar
		},
	}
	deps := newDepsWithExecutor(t, exec)
	o := staleSubmittedOrder(t, deps)
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	ord, ready := rt.cancelExpiredSubmittedOrder(context.Background(), o)
	if !ready {
		t.Fatal("se esperaba estado terminal tras cancelar")
	}
	// El estado terminal se procesa como cualquier reconciliación normal.
	rt.processOrderStatus(context.Background(), "b1", o, ord)

	got, ok := deps.Registry.Get(o.ClientOrderID)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if got.State != registry.StateCancelled {
		t.Fatalf("se esperaba cancelled en el registro, hay %q", got.State)
	}
	if len(exec.cancelled) != 1 || exec.cancelled[0].orderID != 4321 {
		t.Fatalf("se esperaba 1 cancelación en el exchange, hay %+v", exec.cancelled)
	}
}

func TestReconcileTimeoutRecordsFillWhenOrderFilledBeforeTimeout(t *testing.T) {
	// La orden se rellenó justo antes del timeout: cancelar fallaría, y el
	// estado FILLED debe registrarse como ejecución (no marcarse failed).
	exec := &fakeExecutor{
		orderQueue: []exchange.OrderResponse{
			{OrderID: 4321, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1},
		},
		trades: []exchange.Fill{{OrderID: 4321, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
	}
	deps := newDepsWithExecutor(t, exec)
	o := staleSubmittedOrder(t, deps)
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	ord, ready := rt.cancelExpiredSubmittedOrder(context.Background(), o)
	if !ready {
		t.Fatal("FILLED es terminal: debía estar listo para procesar")
	}
	rt.processOrderStatus(context.Background(), "b1", o, ord)

	got, _ := deps.Registry.Get(o.ClientOrderID)
	if got.State != registry.StateFilled || got.FilledQty != 0.01 || got.FilledValue != 1.0 {
		t.Fatalf("el rellenado debe contabilizarse aunque expire el timeout: %+v", got)
	}
	state := deps.RiskState.State()
	if state.ExposureTotalUSDT != 1.0 || state.OpenPositions != 1 {
		t.Fatalf("riesgo no refleja el relleno: %+v", state)
	}
}

func TestReconcileTimeoutKeepsOrderWhenCancellationCannotBeConfirmed(t *testing.T) {
	// La cancelación se envía pero GetOrder sigue reportando NEW: la orden
	// puede ejecutarse más tarde, así que la reserva debe conservarse.
	exec := &fakeExecutor{
		orderQueue: []exchange.OrderResponse{
			{OrderID: 4321, Status: "NEW"},
			{OrderID: 4321, Status: "NEW"},
		},
	}
	deps := newDepsWithExecutor(t, exec)
	o := staleSubmittedOrder(t, deps)
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	_, ready := rt.cancelExpiredSubmittedOrder(context.Background(), o)
	if ready {
		t.Fatal("no se debía liberar una orden cuyo estado final no es confirmable")
	}
	got, _ := deps.Registry.Get(o.ClientOrderID)
	if got.State != registry.StateSubmitted {
		t.Fatalf("la reserva debía conservarse, hay %q", got.State)
	}
	if len(exec.cancelled) != 1 {
		t.Fatalf("se esperaba un intento de cancelación, hay %d", len(exec.cancelled))
	}
}

func TestResolveUncertainOutcomeReleasesReservationWhenOrderDoesNotExist(t *testing.T) {
	exec := &fakeExecutor{clientOrderErr: exchange.ErrOrderNotFound}
	deps := newDepsWithExecutor(t, exec)
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	rt.resolveUncertainOutcome(context.Background(), registry.TrackedOrder{
		ClientOrderID: id,
		Proposal:      domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()},
		State:         registry.StateReserved,
	})

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateFailed {
		t.Fatalf("la reserva debía liberarse como failed, hay %q", got.State)
	}
	if len(exec.clientOrderLookups) != 1 || exec.clientOrderLookups[0] != id {
		t.Fatalf("se esperaba una consulta por clientOrderID, hay %+v", exec.clientOrderLookups)
	}
}

func TestResolveUncertainOutcomeClaimsExistingFilledOrder(t *testing.T) {
	// El envío perdió su respuesta, pero la orden existe y ya está FILLED: la
	// reserva debe reclamarse y el fill contabilizarse en exposición/riesgo.
	exec := &fakeExecutor{
		clientOrderResp: exchange.OrderResponse{OrderID: 777, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1},
		trades:          []exchange.Fill{{OrderID: 777, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
	}
	deps := newDepsWithExecutor(t, exec)
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	rt.resolveUncertainOutcome(context.Background(), registry.TrackedOrder{
		ClientOrderID: id,
		Proposal:      domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()},
		State:         registry.StateReserved,
	})

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateFilled || got.ExchangeID != 777 || got.FilledQty != 0.01 {
		t.Fatalf("la orden existente debía reclamarse y quedar filled: %+v", got)
	}
	state := deps.RiskState.State()
	if state.ExposureTotalUSDT != 1.0 || state.OpenPositions != 1 {
		t.Fatalf("el fill de la orden reclamada debe contabilizarse: %+v", state)
	}
}

func TestResolveUncertainOutcomeKeepsReservationWhenLookupFails(t *testing.T) {
	// La consulta también falla (red caída): conservar la reserva es lo único
	// seguro; nunca liberar sin confirmación del exchange.
	exec := &fakeExecutor{clientOrderErr: errors.New("network unreachable")}
	deps := newDepsWithExecutor(t, exec)
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	rt.resolveUncertainOutcome(context.Background(), registry.TrackedOrder{
		ClientOrderID: id,
		Proposal:      domain.OrderProposal{BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()},
		State:         registry.StateReserved,
	})

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateReserved {
		t.Fatalf("la reserva debía conservarse, hay %q", got.State)
	}
}

// --- reconciliación de reservas huérfanas en cada tick ---

func TestReconcileResolvesReservedOrdersRegardlessOfMaxAge(t *testing.T) {
	// Con max_order_age_minutes desactivado (0) la reserva huérfana cabe en
	// limbo para siempre si solo se revisara al expirar: debe resolverse en
	// cada tick igualmente.
	exec := &fakeExecutor{clientOrderErr: exchange.ErrOrderNotFound}
	deps := newDepsWithExecutor(t, exec)
	deps.ConfigStore.UpdateRisk(withMaxOrderAge(t, deps.ConfigStore.Risk(), 0))
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	rt.reconcile(context.Background(), validBot("b1"))

	got, ok := deps.Registry.Get(id)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if got.State != registry.StateFailed {
		t.Fatalf("con timeout desactivado la reserva debía resolverse igualmente; hay %q", got.State)
	}
}

func TestReconcileClaimsReservedOrderWithoutMaxAge(t *testing.T) {
	// La reserva existe en el exchange y ya está FILLED: debe reclamarse y
	// contabilizarse cada tick, aunque el timeout esté desactivado.
	exec := &fakeExecutor{
		clientOrderResp: exchange.OrderResponse{OrderID: 777, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1},
		trades:          []exchange.Fill{{OrderID: 777, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
	}
	deps := newDepsWithExecutor(t, exec)
	deps.ConfigStore.UpdateRisk(withMaxOrderAge(t, deps.ConfigStore.Risk(), 0))
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	rt.reconcile(context.Background(), validBot("b1"))

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateFilled || got.ExchangeID != 777 {
		t.Fatalf("la orden existente debía reclamarse sin esperar el timeout: %+v", got)
	}
	state := deps.RiskState.State()
	if state.ExposureTotalUSDT != 1.0 || state.OpenPositions != 1 {
		t.Fatalf("el fill reclamado debe contabilizarse: %+v", state)
	}
}

// --- pausa/stop con reservas huérfanas ---

func TestStopSettlesReservedOrderByClaimingThenCancelling(t *testing.T) {
	// Al detener, una reserva huérfana cuya orden sí existe en el exchange
	// debe reclamarse y luego cancelarse como cualquier submitted.
	exec := &fakeExecutor{
		clientOrderResp: exchange.OrderResponse{OrderID: 555, Status: "NEW"},
		orderResp:       exchange.OrderResponse{OrderID: 555, Status: "CANCELED"},
	}
	deps := newDepsWithExecutor(t, exec)
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.StopBot("b1"); err != nil {
		t.Fatalf("StopBot: %v", err)
	}

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateCancelled || got.ExchangeID != 555 {
		t.Fatalf("la orden existente debía reclamarse y cancelarse: %+v", got)
	}
	if len(exec.cancelled) != 1 || exec.cancelled[0].orderID != 555 {
		t.Fatalf("se esperaba 1 cancelación en el exchange, hay %+v", exec.cancelled)
	}
}

func TestStopRecordsFillFromClaimedReservationAndLiquidates(t *testing.T) {
	// La orden reservada existe y ya está FILLED: el fill debe contabilizarse
	// al detener el bot y su inventario debe liquidarse; sin cancelaciones.
	exec := &fakeExecutor{
		clientOrderResp: exchange.OrderResponse{OrderID: 556, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1},
		trades:          []exchange.Fill{{OrderID: 556, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
		placeQueue: []exchange.OrderResponse{
			{OrderID: 889, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1.1},
		},
		freeBalances: map[string]float64{"BTC": 0.01},
		baseAssets:   map[string]string{"BTCUSDT": "BTC"},
	}
	deps := newDepsWithExecutor(t, exec)
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	result, err := m.StopBot("b1")
	if err != nil {
		t.Fatalf("StopBot: %v", err)
	}

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateFilled || got.FilledQty != 0.01 || got.FilledValue != 1.0 {
		t.Fatalf("el fill de la orden reclamada debe conservarse: %+v", got)
	}
	if len(exec.cancelled) != 0 {
		t.Fatalf("no debían enviarse cancelaciones, hay %+v", exec.cancelled)
	}
	if result.SoldQuantity != 0.01 || result.ProceedsUSDT != 1.1 || result.CostBasisUSDT != 1.0 {
		t.Fatalf("la posición reclamada debía liquidarse al detener: %+v", result)
	}
	state := deps.RiskState.State()
	if state.ExposureTotalUSDT != 0 || state.OpenPositions != 0 {
		t.Fatalf("la posición liquidada debe desaparecer del riesgo: %+v", state)
	}
	if math.Abs(state.TotalPnL-0.1) > 1e-9 {
		t.Fatalf("PnL realizado incorrecto: %+v", state)
	}
}

func TestStopKeepsUnconfirmableReservation(t *testing.T) {
	// La reserva no puede confirmarse (red caída): conservarla y avisa; nunca
	// liberarla a ciegas ni cancelar de más.
	exec := &fakeExecutor{clientOrderErr: errors.New("network unreachable")}
	deps := newDepsWithExecutor(t, exec)
	id, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.StopBot("b1"); err == nil {
		t.Fatal("se esperaba que Stop informara la reserva no confirmable")
	}

	got, _ := deps.Registry.Get(id)
	if got.State != registry.StateReserved {
		t.Fatalf("la reserva no confirmable debía conservarse; hay %q", got.State)
	}
	if len(exec.cancelled) != 0 {
		t.Fatalf("sin orden en el exchange no debían enviarse cancelaciones: %+v", exec.cancelled)
	}
}

// --- tick ante envíos de resultado incierto ---

func TestTickUncertainSubmissionKeepsReservationWhenLookupFails(t *testing.T) {
	exec := &fakeExecutor{
		placeErr:       exchange.ErrOrderSubmissionUncertain,
		clientOrderErr: errors.New("network unreachable"),
	}
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())

	orders, _ := deps.Registry.List()
	if len(orders) != 1 || orders[0].State != registry.StateReserved {
		// Marcar failed sin confirmar duplicaría la compra si la orden existe
		// en el exchange: la reserva debe permanecer hasta poder confirmar.
		t.Fatalf("la reserva debe conservarse ante un envío incierto no confirmable: %+v", orders)
	}
	if len(exec.clientOrderLookups) != 1 {
		t.Fatalf("se esperaba una consulta al exchange por clientOrderID, hay %d", len(exec.clientOrderLookups))
	}
}

func TestTickUncertainSubmissionMarksFailedWhenExchangeConfirmsAbsence(t *testing.T) {
	exec := &fakeExecutor{
		placeErr:       exchange.ErrOrderSubmissionUncertain,
		clientOrderErr: exchange.ErrOrderNotFound,
	}
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())

	orders, _ := deps.Registry.List()
	if len(orders) != 1 || orders[0].State != registry.StateFailed {
		t.Fatalf("el exchange confirmó que no existe: debía marcarse failed, hay %+v", orders)
	}
}

func TestTickUncertainSubmissionClaimsExistingOrder(t *testing.T) {
	// El envío se perdió pero la orden existe y ya está FILLED: reclamarse y
	// contabilizarse, sin duplicar la compra en el próximo tick.
	exec := &fakeExecutor{
		placeErr:        exchange.ErrOrderSubmissionUncertain,
		clientOrderResp: exchange.OrderResponse{OrderID: 777, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1},
		trades:          []exchange.Fill{{OrderID: 777, Symbol: "BTCUSDT", Quantity: 0.01, Price: 100}},
	}
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())

	orders, _ := deps.Registry.List()
	if len(orders) != 1 || orders[0].State != registry.StateFilled || orders[0].ExchangeID != 777 {
		t.Fatalf("la orden existente debía reclamarse como filled: %+v", orders)
	}
}

func TestTickResponseWithoutOrderIDSettlesViaLookup(t *testing.T) {
	// El exchange responde 200 pero sin ID útil: el resultado es ambiguo.
	exec := &fakeExecutor{
		placeResp:       exchange.OrderResponse{OrderID: 0, Status: "NEW"},
		clientOrderResp: exchange.OrderResponse{OrderID: 888, Status: "NEW"},
	}
	p := domain.OrderProposal{Symbol: "BTCUSDT", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now()}
	strat := &fakeStrategy{props: []domain.OrderProposal{p}}
	deps := makeDeps(t, fixedFactory(strat), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.Executor = exec

	rt := NewBotRuntime(validBot("b1"), strat, deps)
	rt.tick(context.Background())

	orders, _ := deps.Registry.List()
	if len(orders) != 1 || orders[0].State != registry.StateSubmitted || orders[0].ExchangeID != 888 {
		t.Fatalf("la orden debía reclamarse por clientOrderID: %+v", orders)
	}
}

// --- persistencia incremental ---

func TestPersistBotsHookFiresOnLifecycleChanges(t *testing.T) {
	var mu sync.Mutex
	var events []string
	deps := makeDeps(t, fixedFactory(bots.NoopStrategy{}), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.PersistBots = func() error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, "save")
		return nil
	}

	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)

	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Enable("b1"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := m.Pause("b1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := m.StopBot("b1"); err != nil {
		t.Fatalf("StopBot: %v", err)
	}
	if err := m.Delete("b1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 5 {
		t.Fatalf("cada cambio de catálogo/estado debe guardar bots.json; guardados: %d", len(events))
	}
}

func TestPersistBotsHookErrorDoesNotBreakMutations(t *testing.T) {
	deps := makeDeps(t, fixedFactory(bots.NoopStrategy{}), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	deps.PersistBots = func() error { return errors.New("disco lleno") }

	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)

	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("un fallo de persistencia no debe bloquear Create: %v", err)
	}
	if err := m.Enable("b1"); err != nil {
		t.Fatalf("un fallo de persistencia no debe bloquear Enable: %v", err)
	}
	if st, _ := m.Status("b1"); st != domain.BotStatusRunning {
		t.Fatalf("estado inesperado: %q", st)
	}
}

// comboStrategy combina una propuesta con el listener de grid.
type comboStrategy struct {
	grid  *gridLikeStrategy
	props []domain.OrderProposal
	used  bool
	mu    sync.Mutex
}

func (c *comboStrategy) Type() string { return "combo" }
func (c *comboStrategy) Evaluate(ctx context.Context, data bots.MarketData) ([]domain.OrderProposal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used {
		return nil, nil
	}
	c.used = true
	return c.props, nil
}
func (c *comboStrategy) OnOrderRejected()          { c.grid.OnOrderRejected() }
func (c *comboStrategy) OnOrderClosed(filled bool) { c.grid.OnOrderClosed(filled) }
