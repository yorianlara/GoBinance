//go:build integration

package manager

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"mi-bot/internal/db"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"
)

// Verifica el flujo completo contra PostgreSQL real: crear la orden (tick con
// reserva + envío a Binance fake), descubrir que ya llenó al detener, y
// comprobar que el registro persistido queda sin órdenes activas y la
// posición vendida.
func TestIntegration_StopFlowWithPostgresRegistry(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no configurada")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	database, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := db.Migrate(ctx, database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := database.ExecContext(ctx, "TRUNCATE orders"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	reg, err := registry.NewPostgres(database)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}

	exec := &fakeExecutor{
		// Compra del tick (queda viva) y venta automática del detener.
		placeQueue: []exchange.OrderResponse{
			{OrderID: 101, Status: "NEW"},
			{OrderID: 202, Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1.05},
		},
		orderResp:      exchange.OrderResponse{Status: "FILLED", ExecutedQty: 0.01, CummulativeQuoteQty: 1.0},
		cancelOrderErr: errors.New("orden ya terminada"),
		trades:         []exchange.Fill{{Quantity: 0.01, Price: 100}},
		freeBalances:   map[string]float64{"BTC": 0.01},
		baseAssets:     map[string]string{"BTCUSDT": "BTC"},
	}

	stateStore, err := risk.NewStateStore(t.TempDir() + "/risk-state.json")
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	riskManager, err := risk.NewStatefulManager(risk.New(), stateStore)
	if err != nil {
		t.Fatalf("NewStatefulManager: %v", err)
	}

	deps := Deps{
		StrategyFactory: fixedFactory(&fakeStrategy{props: []domain.OrderProposal{{
			Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
			Type: domain.OrderTypeMarket, Quantity: 0.01,
			ProposedAt: time.Now(),
		}}}),
		Capital:     &risk.FakeCapitalProvider{Value: 1000},
		RiskState:   riskManager,
		Registry:    reg,
		ConfigStore: mustConfigStore(t),
		Market:      &fakeMarket{price: 100},
		Executor:    exec,
	}
	m, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Shutdown)

	now := time.Now()
	if err := m.Create(domain.Bot{
		ID: "pg-stop", Name: "pg-stop", Strategy: domain.StrategyConfig{Type: "fake"},
		Symbol: "BTCUSDT", AllocationPercent: 10, Status: domain.BotStatusCreated,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rt := runtimeOf(m, "pg-stop")
	rt.tick(context.Background())

	// La orden de compra está persistida como submitted.
	active, err := reg.ListActiveByBot("pg-stop")
	if err != nil {
		t.Fatalf("ListActiveByBot: %v", err)
	}
	if len(active) != 1 || active[0].State != registry.StateSubmitted {
		t.Fatalf("tras el tick se esperaba 1 orden submitted, hay %+v", active)
	}

	// Detener: consulta Binance fake, descubre la compra llena, la registra
	// y vende la posición; el resultado queda persistido en PostgreSQL.
	result, err := m.StopBot("pg-stop")
	if err != nil {
		t.Fatalf("StopBot: %v", err)
	}
	if result.SoldQuantity != 0.01 || result.ProceedsUSDT != 1.05 {
		t.Fatalf("resultado de liquidación inesperado: %+v", result)
	}

	orders, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("se esperaban 2 órdenes persistidas (compra y venta), hay %d", len(orders))
	}
	filledBySide := map[domain.OrderSide]int{}
	var sellCostBasis float64
	for _, order := range orders {
		if order.State != registry.StateFilled {
			t.Fatalf("toda orden debe quedar en estado terminal filled, hay %+v", order)
		}
		filledBySide[order.Proposal.Side]++
		if order.Proposal.Side == domain.OrderSideSell {
			sellCostBasis = order.Proposal.PositionCostBasisUSDT
		}
	}
	if filledBySide[domain.OrderSideBuy] != 1 || filledBySide[domain.OrderSideSell] != 1 {
		t.Fatalf("se esperaba 1 compra y 1 venta en estado filled: %+v", filledBySide)
	}
	if math.Abs(sellCostBasis-1.0) > 1e-9 {
		t.Fatalf("coste base de la venta incorrecto: %v", sellCostBasis)
	}

	active, err = reg.ListActiveByBot("pg-stop")
	if err != nil {
		t.Fatalf("ListActiveByBot: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("no deben quedar órdenes activas en PostgreSQL: %+v", active)
	}

	// Inventario residencial en cero (reconstruido desde la base de datos).
	inventory, err := inventoryFor(orders, "pg-stop", "BTCUSDT")
	if err != nil {
		t.Fatalf("inventoryFor: %v", err)
	}
	if inventory.quantity() > positionEpsilon {
		t.Fatalf("no debe quedar posición registrada: %v", inventory.quantity())
	}

	// El bot queda detenido y el riesgo a cero.
	if st, _ := m.Status("pg-stop"); st != domain.BotStatusStopped {
		t.Fatalf("el bot debe quedar detenido, está %q", st)
	}
	state := riskManager.State()
	if state.OpenPositions != 0 || state.ExposureTotalUSDT != 0 {
		t.Fatalf("la exposición debía quedar en cero: %+v", state)
	}
}

func runtimeOf(m *BotManager, id string) *BotRuntime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runtimes[id]
}
