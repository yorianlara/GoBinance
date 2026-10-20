package bots

import (
	"context"
	"testing"

	"mi-bot/internal/domain"
)

func newGrid(t *testing.T, levels, spacing, size float64) *GridStrategy {
	t.Helper()
	params := map[string]float64{
		"grid_levels":          levels,
		"grid_spacing_percent": spacing,
		"order_size_usdt":      size,
	}
	g, err := NewGridStrategy(domain.StrategyConfig{Type: "grid", Parameters: params})
	if err != nil {
		t.Fatalf("NewGridStrategy: %v", err)
	}
	return g
}

func TestGridInvalidParams(t *testing.T) {
	cases := []domain.StrategyConfig{
		{Type: "grid"},
		{Type: "grid", Parameters: map[string]float64{"grid_levels": 0, "grid_spacing_percent": 1, "order_size_usdt": 10}},
		{Type: "grid", Parameters: map[string]float64{"grid_levels": 3, "grid_spacing_percent": 0, "order_size_usdt": 10}},
		{Type: "grid", Parameters: map[string]float64{"grid_levels": 3, "grid_spacing_percent": 1, "order_size_usdt": 0}},
		{Type: "grid", Parameters: map[string]float64{"grid_levels": 3, "grid_spacing_percent": 1, "order_size_usdt": 10, "max_orders": -1}},
	}
	for i, c := range cases {
		if _, err := NewGridStrategy(c); err == nil {
			t.Fatalf("caso %d: se esperaba error", i)
		}
	}
}

func TestGridFirstEvaluateSetsCenterNoProposal(t *testing.T) {
	g := newGrid(t, 5, 1, 10)
	props, err := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("primer evaluate no debe proponer, hay %d", len(props))
	}
}

func TestGridPriceBelowCenterProposesBuy(t *testing.T) {
	g := newGrid(t, 5, 1, 10)
	// Centro en 100
	g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	// Precio baja a 97 → -3% → -3 niveles (spacing 1%)
	props, err := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 97})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("se esperaba 1, hay %d", len(props))
	}
	p := props[0]
	if p.Side != domain.OrderSideBuy {
		t.Fatalf("side: %v", p.Side)
	}
	if p.Type != domain.OrderTypeLimit {
		t.Fatalf("type: %v", p.Type)
	}
	if p.LimitPrice != 97 {
		t.Fatalf("limit_price: %v (esperado 97)", p.LimitPrice)
	}
}

func TestGridPriceAboveCenterProposesSell(t *testing.T) {
	g := newGrid(t, 5, 1, 10)
	g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	props, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 102})
	if len(props) != 1 {
		t.Fatalf("se esperaba 1, hay %d", len(props))
	}
	if props[0].Side != domain.OrderSideSell {
		t.Fatalf("side: %v", props[0].Side)
	}
}

func TestGridOnlyOneActiveOrder(t *testing.T) {
	g := newGrid(t, 5, 1, 10)
	g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	props1, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 97})
	if len(props1) != 1 {
		t.Fatalf("primera: %d", len(props1))
	}
	props2, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 96})
	if len(props2) != 0 {
		t.Fatalf("segunda: %d (no debe haber)", len(props2))
	}
	g.OnOrderClosed()
	props3, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 96})
	if len(props3) != 1 {
		t.Fatalf("tras OnOrderClosed: %d", len(props3))
	}
}

func TestGridRecentersOnLargeDrift(t *testing.T) {
	g := newGrid(t, 3, 1, 10)
	g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	// maxDrift = 2*3*1 = 6%; precio 80 → -20% → recentra
	props, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 80})
	if len(props) != 0 {
		t.Fatalf("tras recentrar no debe proponer, hay %d", len(props))
	}
	// Siguiente tick cerca del nuevo centro (80) → puede proponer
	props, _ = g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 79})
	if len(props) != 1 {
		t.Fatalf("se esperaba 1 tras recentrar, hay %d", len(props))
	}
}

func TestGridPriceAtCenterNoProposal(t *testing.T) {
	g := newGrid(t, 5, 1, 10)
	g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	props, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	if len(props) != 0 {
		t.Fatalf("en el centro no debe proponer, hay %d", len(props))
	}
}

func TestGridPriceOutOfRange(t *testing.T) {
	g := newGrid(t, 2, 1, 10)
	g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	// maxDrift = 4%; precio 97 (3% abajo) → dentro, pero level=-3 < -2 → fuera de rejilla
	props, _ := g.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 97})
	if len(props) != 0 {
		t.Fatalf("fuera de rejilla no debe proponer, hay %d", len(props))
	}
}
