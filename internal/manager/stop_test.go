package manager

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"
)

// Simula el flujo completo que pide el panel: crear una orden (compra) y
// detener el bot cuando Binance ya la llena. El detener debe: cancelar las
// pendientes, registrar el fill descubierto al consultar y vender la
// posición, dejando el bot sin inventario y sin órdenes activas.
func TestStopCancelsPendingLearnsFilledBuyAndSellsPosition(t *testing.T) {
	exec := &fakeExecutor{
		// 1ª PlaceOrder: la compra que el tick envía (queda viva en el fake
		// hasta que CancelOrder falle, como ocurre con una orden market).
		placeQueue: []exchange.OrderResponse{
			{OrderID: 101, Status: "NEW"},
			// 2ª PlaceOrder: la venta automática del detener, que ya llena.
			{OrderID: 202, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1.05},
		},
		// CancelOrder falla (la orden ya terminó en Binance) y la consulta
		// revela una compra llena a precio de mercado.
		orderResp:      exchange.OrderResponse{Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1.0},
		cancelOrderErr: errors.New("orden inexistente o ya terminada"),
		trades:         []exchange.Fill{{Quantity: 0.01, Price: 100}},
		freeBalances:   map[string]float64{"BTC": 0.01},
		baseAssets:     map[string]string{"BTCUSDT": "BTC"},
	}
	deps := newDepsWithExecutor(t, exec)
	proposal := domain.OrderProposal{
		Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01,
		ProposedAt: time.Now(),
	}
	strategy := &fakeStrategy{props: []domain.OrderProposal{proposal}}
	deps.StrategyFactory = fixedFactory(strategy)

	rt := NewBotRuntime(validBot("b1"), strategy, deps)

	// 1) El bot crea la orden de compra.
	rt.tick(context.Background())
	orders, err := deps.Registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(orders) != 1 || orders[0].State != registry.StateSubmitted {
		t.Fatalf("tras el tick se esperaba 1 orden submitted, hay %+v", orders)
	}

	// 2) Se detiene el bot: se descubre el fill y debe venderse la posición.
	result, err := rt.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// 3) La compra quedó registrada como fill...
	orders, err = deps.Registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	stateBySide := map[domain.OrderSide]registry.TrackedOrder{}
	for _, order := range orders {
		if order.Proposal.BotID != "b1" || order.Proposal.Symbol != "BTCUSDT" {
			continue
		}
		switch order.Proposal.Side {
		case domain.OrderSideBuy:
			if order.State != registry.StateFilled || order.FilledQty != 0.01 || order.FilledValue != 1.0 {
				t.Fatalf("la compra descubierta llena debe registrarse: %+v", order)
			}
		case domain.OrderSideSell:
			stateBySide[order.Proposal.Side] = order
		default:
			t.Fatalf("lado inesperado: %+v", order)
		}
	}
	// ...y la venta se envió y registró con su coste base.
	sell, ok := stateBySide[domain.OrderSideSell]
	if !ok {
		t.Fatal("no se registró la orden de venta del detener")
	}
	if sell.State != registry.StateFilled ||
		sell.Proposal.PositionCostBasisUSDT != 1.0 || sell.Proposal.PositionsClosed != 1 {
		t.Fatalf("venta no persistida correctamente: %+v", sell)
	}

	// 4) Resultado de la liquidación.
	if result.SoldQuantity != 0.01 || result.ProceedsUSDT != 1.05 ||
		result.CostBasisUSDT != 1.0 || math.Abs(result.RealizedPnLUSDT-0.05) > 1e-9 {
		t.Fatalf("resultado de liquidación inesperado: %+v", result)
	}

	// 5) En Binance se envió el market sell con la cantidad completa.
	if len(exec.orderRequests) != 2 {
		t.Fatalf("se esperaban 2 PlaceOrder (compra y venta), hay %+v", exec.orderRequests)
	}
	sellReq := exec.orderRequests[1]
	if sellReq.Side != domain.OrderSideSell || sellReq.Quantity != 0.01 {
		t.Fatalf("la orden de venta es incorrecta: %+v", sellReq)
	}

	// 6) El bot queda detenido, sin posición ni órdenes activas.
	if status := rt.Status(); status != domain.BotStatusStopped {
		t.Fatalf("el bot debe quedar detenido, está %q", status)
	}
	inventory, err := inventoryFor(orders, "b1", "BTCUSDT")
	if err != nil {
		t.Fatalf("inventoryFor: %v", err)
	}
	if inventory.quantity() > positionEpsilon {
		t.Fatalf("no debe quedar inventario sin liquidar: %v", inventory.quantity())
	}
	active, err := deps.Registry.ListActiveByBot("b1")
	if err != nil {
		t.Fatalf("ListActiveByBot: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("no deben quedar órdenes activas (reserved/submitted): %+v", active)
	}
	// 7) El riesgo vuelve a cero y registra el PnL realizado.
	state := deps.RiskState.State()
	if state.OpenPositions != 0 || state.ExposureTotalUSDT != 0 {
		t.Fatalf("la exposición debía quedar en cero: %+v", state)
	}
	if math.Abs(state.TotalPnL-0.05) > 1e-9 || math.Abs(state.DailyPnL-0.05) > 1e-9 {
		t.Fatalf("PnL realizado incorrecto: %+v", state)
	}
}

// Un bot recién detenido (sin órdenes ni posición) informa NoPosition y no
// envía nada al exchange.
func TestStopWithoutActivityReportsNoPosition(t *testing.T) {
	exec := &fakeExecutor{}
	deps := newDepsWithExecutor(t, exec)
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	result, err := rt.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !result.NoPosition {
		t.Fatalf("se esperaba NoPosition, hay %+v", result)
	}
	if exec.placed != 0 || len(exec.cancelled) != 0 {
		t.Fatalf("no debía haber llamadas al exchange: placed=%d cancelled=%+v", exec.placed, exec.cancelled)
	}
	if status := rt.Status(); status != domain.BotStatusStopped {
		t.Fatalf("el bot debe quedar detenido, está %q", status)
	}
}

// Si el exchange no confirma (ni cancelación ni estado terminal), la orden
// conserva submitted para reconciliarla más tarde; el error se informa y la
// liquidación no debe vender nada sin inventario confirmado.
func TestStopKeepsSubmittedWhenExchangeCannotBeConfirmed(t *testing.T) {
	exec := &fakeExecutor{
		cancelOrderErr: errors.New("exchange caído"),
		orderErr:       errors.New("exchange caído"),
	}
	deps := newDepsWithExecutor(t, exec)
	clientOrderID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01,
		ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(clientOrderID, 4321); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}

	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)
	if _, err := rt.Stop(); err == nil {
		t.Fatal("se esperaba error al no poder confirmar la cancelación")
	}

	order, ok := deps.Registry.Get(clientOrderID)
	if !ok {
		t.Fatal("orden no encontrada")
	}
	if order.State != registry.StateSubmitted {
		t.Fatalf("la orden debe permanecer submitted para reconciliar; estado %q", order.State)
	}
	if exec.placed != 0 {
		t.Fatalf("sin inventario confirmado no debía enviarse venta, hay %d", exec.placed)
	}
	active, err := deps.Registry.ListActiveByBot("b1")
	if err != nil {
		t.Fatalf("ListActiveByBot: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("la orden submitted debe seguir activa: %+v", active)
	}
}

// Detener sin ejecutor (modo solo-reservar) detiene sin liquidar y sin error.
func TestStopWithoutExecutorOnlyStopsRuntime(t *testing.T) {
	deps := makeDeps(t, fixedFactory(bots.NoopStrategy{}), &risk.FakeCapitalProvider{Value: 1000}, &fakeMarket{price: 100})
	rt := NewBotRuntime(validBot("b1"), bots.NoopStrategy{}, deps)

	result, err := rt.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !result.NoPosition {
		t.Fatalf("se esperaba NoPosition, hay %+v", result)
	}
	if status := rt.Status(); status != domain.BotStatusStopped {
		t.Fatalf("el bot debe quedar detenido, está %q", status)
	}
}

// Inventories reconstruye posiciones desde el registro: FIFO, ignora
// terminal y devuelve solo pares con lotes sin vender.
func TestInventoriesRecomputesFromRegistry(t *testing.T) {
	deps := newDepsWithExecutor(t, &fakeExecutor{})

	// Compra 1: 1 @ 100 (coste 100).
	b1, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 1, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve b1: %v", err)
	}
	_ = deps.Registry.MarkSubmitted(b1, 101)
	_ = deps.Registry.MarkFilled(b1, 1, 100)

	// Compra 2 llena, más orden cancelada de eskiva (no debe contar).
	b2, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 2, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve b2: %v", err)
	}
	_ = deps.Registry.MarkSubmitted(b2, 102)
	_ = deps.Registry.MarkFilled(b2, 2, 300)
	bC, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 3, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve cancelada: %v", err)
	}
	_ = deps.Registry.MarkSubmitted(bC, 103)
	_ = deps.Registry.MarkCancelled(bC)

	// Venta parcial: 0.5 del primer lote (coste 50).
	s1, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideSell,
		Type: domain.OrderTypeMarket, Quantity: 0.5, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve sell: %v", err)
	}
	_ = deps.Registry.MarkSubmitted(s1, 201)
	if err := deps.Registry.SetPositionCloseDetails(s1, 50, 0); err != nil {
		t.Fatalf("SetPositionCloseDetails: %v", err)
	}
	_ = deps.Registry.MarkFilled(s1, 0.5, 60)

	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	snapshots, err := m.Inventories()
	if err != nil {
		t.Fatalf("Inventories: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("se esperaba 1 snapshot, hay %+v", snapshots)
	}
	got := snapshots[0]
	if got.BotID != "b1" || got.Symbol != "BTCUSDT" ||
		math.Abs(got.Quantity-2.5) > 1e-9 || math.Abs(got.CostBasisUSDT-350) > 1e-9 {
		t.Fatalf("inventario reconstruido inesperado: %+v", got)
	}
}

// Con contabilidad inconsistente (venta sin compras que la respalden)
// Inventories falla en lugar de ofrecer una reconstrucción parcial.
func TestInventoriesRejectsInconsistentLedger(t *testing.T) {
	deps := newDepsWithExecutor(t, &fakeExecutor{})
	s1, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideSell,
		Type: domain.OrderTypeMarket, Quantity: 1, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_ = deps.Registry.MarkSubmitted(s1, 201)
	if err := deps.Registry.SetPositionCloseDetails(s1, 100, 0); err != nil {
		t.Fatalf("SetPositionCloseDetails: %v", err)
	}
	_ = deps.Registry.MarkFilled(s1, 1, 110)

	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)

	if _, err := m.Inventories(); err == nil {
		t.Fatal("se esperaba error con ventas sin compras que las respalden")
	}
}

// CancelAllPendingOrders neutraliza las órdenes pendientes de una ejecución
// anterior sin alterar el estado de los bots ni tocar los fills.
func TestCancelAllPendingOrdersNeutralizesRegistryWithoutChangingBotStatus(t *testing.T) {
	exec := &fakeExecutor{
		clientOrderResp: exchange.OrderResponse{OrderID: 555, Status: "NEW"},
		orderResp:       exchange.OrderResponse{OrderID: 555, Status: "CANCELED"},
	}
	deps := newDepsWithExecutor(t, exec)

	// Bot b1: reserva huérfana + un fill histórico que no debe tocarse.
	reservedID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve reserved: %v", err)
	}
	filledID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.02, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve filled: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(filledID, 123); err != nil {
		t.Fatalf("MarkSubmitted filled: %v", err)
	}
	if err := deps.Registry.MarkFilled(filledID, 0.02, 2.0); err != nil {
		t.Fatalf("MarkFilled: %v", err)
	}

	// Bot b2: orden submitted viva de una ejecución anterior.
	submittedID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "b2", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.015, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve submitted: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(submittedID, 321); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}

	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Create(validBot("b1")); err != nil {
		t.Fatalf("Create b1: %v", err)
	}
	if err := m.Create(validBot("b2")); err != nil {
		t.Fatalf("Create b2: %v", err)
	}

	swept, err := m.CancelAllPendingOrders(context.Background())
	if err != nil {
		t.Fatalf("CancelAllPendingOrders: %v", err)
	}
	if swept != 2 {
		t.Fatalf("se esperaban 2 bots con pendientes, hay %d", swept)
	}

	reserved, _ := deps.Registry.Get(reservedID)
	if reserved.State != registry.StateCancelled || reserved.ExchangeID != 555 {
		t.Fatalf("la reserva huérfana debía reclamarse y cancelarse: %+v", reserved)
	}
	submitted, _ := deps.Registry.Get(submittedID)
	if submitted.State != registry.StateCancelled {
		t.Fatalf("la orden submitted debía cancelarse: %+v", submitted)
	}
	filled, _ := deps.Registry.Get(filledID)
	if filled.State != registry.StateFilled || filled.FilledQty != 0.02 {
		t.Fatalf("el fill histórico no debe alterarse: %+v", filled)
	}
	for _, id := range []string{"b1", "b2"} {
		active, err := deps.Registry.ListActiveByBot(id)
		if err != nil {
			t.Fatalf("ListActiveByBot %s: %v", id, err)
		}
		if len(active) != 0 {
			t.Fatalf("no deben quedar órdenes activas para %s: %+v", id, active)
		}
		if status, _ := m.Status(id); status != domain.BotStatusCreated {
			t.Fatalf("el estado del bot %s no debe cambiar, está %q", id, status)
		}
	}
	if len(exec.cancelled) != 2 {
		t.Fatalf("se esperaban 2 cancelaciones en el exchange, hay %+v", exec.cancelled)
	}
}

// El barrido también cubre bots eliminados que dejaron pendencias en el
// registro, creando un runtime efímero solo para cancelar.
func TestCancelAllPendingOrderCoversDeletedBot(t *testing.T) {
	exec := &fakeExecutor{orderResp: exchange.OrderResponse{OrderID: 700, Status: "CANCELED"}}
	deps := newDepsWithExecutor(t, exec)

	ghostID, err := deps.Registry.Reserve(domain.OrderProposal{
		BotID: "ghost", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		Type: domain.OrderTypeMarket, Quantity: 0.01, ProposedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := deps.Registry.MarkSubmitted(ghostID, 699); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}

	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)
	// "ghost" nunca se registra como bot.

	swept, err := m.CancelAllPendingOrders(context.Background())
	if err != nil {
		t.Fatalf("CancelAllPendingOrders: %v", err)
	}
	if swept != 1 {
		t.Fatalf("se esperaba 1 bot fantasma, hay %d", swept)
	}
	ghost, _ := deps.Registry.Get(ghostID)
	if ghost.State != registry.StateCancelled {
		t.Fatalf("la orden del bot eliminado debía cancelarse: %+v", ghost)
	}
	if len(exec.cancelled) != 1 || exec.cancelled[0].orderID != 699 {
		t.Fatalf("cancelación inesperada: %+v", exec.cancelled)
	}
}
