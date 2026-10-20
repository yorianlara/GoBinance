package bots

import (
	"context"
	"testing"
	"time"

	"mi-bot/internal/domain"
)

func newDCA(t *testing.T, amount float64, intervalMin float64, maxBuys int) *DCAStrategy {
	t.Helper()
	params := map[string]float64{
		"amount_per_buy_usdt": amount,
		"interval_minutes":    intervalMin,
	}
	if maxBuys > 0 {
		params["max_buys"] = float64(maxBuys)
	}
	s, err := NewDCAStrategy(domain.StrategyConfig{Type: "dca", Parameters: params})
	if err != nil {
		t.Fatalf("NewDCAStrategy: %v", err)
	}
	return s
}

func TestDCAInvalidParams(t *testing.T) {
	cases := []domain.StrategyConfig{
		{Type: "dca"},
		{Type: "dca", Parameters: map[string]float64{"amount_per_buy_usdt": 0, "interval_minutes": 10}},
		{Type: "dca", Parameters: map[string]float64{"amount_per_buy_usdt": 10}},
		{Type: "dca", Parameters: map[string]float64{"amount_per_buy_usdt": 10, "interval_minutes": -1}},
		{Type: "dca", Parameters: map[string]float64{"amount_per_buy_usdt": 10, "interval_minutes": 1, "max_buys": -1}},
	}
	for i, c := range cases {
		if _, err := NewDCAStrategy(c); err == nil {
			t.Fatalf("caso %d: se esperaba error", i)
		}
	}
}

func TestDCAFirstEvaluateProposes(t *testing.T) {
	d := newDCA(t, 10, 60, 0)
	props, err := d.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("se esperaba 1 propuesta, hay %d", len(props))
	}
	p := props[0]
	if p.Quantity != 0.1 {
		t.Fatalf("qty: %v", p.Quantity)
	}
	if p.Side != domain.OrderSideBuy {
		t.Fatalf("side: %v", p.Side)
	}
	if p.BotID != "" {
		t.Fatalf("BotID debía ir vacío, hay %q", p.BotID)
	}
}

func TestDCASecondEvaluateWithinIntervalDoesNothing(t *testing.T) {
	d := newDCA(t, 10, 60, 0)
	_, _ = d.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	props, err := d.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("se esperaba 0 propuestas (intervalo no pasado), hay %d", len(props))
	}
}

func TestDCARespectsMaxBuys(t *testing.T) {
	d := newDCA(t, 10, 0.0001, 2) // intervalo muy corto
	for i := 0; i < 2; i++ {
		props, err := d.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
		if err != nil {
			t.Fatalf("Evaluate %d: %v", i, err)
		}
		if len(props) != 1 {
			t.Fatalf("compra %d: se esperaba 1, hay %d", i, len(props))
		}
		time.Sleep(10 * time.Millisecond)
	}
	props, _ := d.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100})
	if len(props) != 0 {
		t.Fatalf("se esperaba 0 tras max_buys, hay %d", len(props))
	}
}

func TestDCAPriceZeroDoesNothing(t *testing.T) {
	d := newDCA(t, 10, 60, 0)
	props, err := d.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 0})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("se esperaba 0, hay %d", len(props))
	}
}

func TestDCAContextCancelled(t *testing.T) {
	d := newDCA(t, 10, 60, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Evaluate(ctx, MarketData{Symbol: "BTCUSDT", Price: 100}); err == nil {
		t.Fatal("se esperaba error de contexto cancelado")
	}
}
