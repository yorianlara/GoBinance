package bots

import (
	"context"
	"testing"

	"mi-bot/internal/domain"
)

func newRSI(t *testing.T, period, oversold, overbought, size float64) *RSIStrategy {
	t.Helper()
	r, err := NewRSIStrategy(domain.StrategyConfig{Type: "rsi", Parameters: map[string]float64{
		"period": period, "oversold": oversold, "overbought": overbought, "order_size_usdt": size,
	}})
	if err != nil {
		t.Fatalf("NewRSIStrategy: %v", err)
	}
	return r
}

func TestRSIInvalidParams(t *testing.T) {
	cases := []domain.StrategyConfig{
		{Type: "rsi"},
		{Type: "rsi", Parameters: map[string]float64{"period": 1, "oversold": 30, "overbought": 70, "order_size_usdt": 10}},
		{Type: "rsi", Parameters: map[string]float64{"period": 14, "oversold": 30, "overbought": 70}},
		{Type: "rsi", Parameters: map[string]float64{"period": 14, "oversold": 80, "overbought": 70, "order_size_usdt": 10}},
		{Type: "rsi", Parameters: map[string]float64{"period": 14, "oversold": 0, "overbought": 70, "order_size_usdt": 10}},
		{Type: "rsi", Parameters: map[string]float64{"period": 14, "oversold": 30, "overbought": 100, "order_size_usdt": 10}},
		{Type: "rsi", Parameters: map[string]float64{"period": 14, "oversold": 30, "overbought": 70, "order_size_usdt": 0}},
	}
	for i, c := range cases {
		if _, err := NewRSIStrategy(c); err == nil {
			t.Fatalf("caso %d: se esperaba error", i)
		}
	}
}

func TestRSINotEnoughCandles(t *testing.T) {
	r := newRSI(t, 14, 30, 70, 10)
	props, err := r.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100, Candles: makeCandles(1, 2, 3)})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("sin velas suficientes no debe proponer, hay %d", len(props))
	}
}

func TestRSICrossUpBuy(t *testing.T) {
	r := newRSI(t, 5, 30, 70, 10)
	// Serie verificada con el probe: RSI[-2]=0, RSI[-1]=45.45 → cruce al alza.
	closes := []float64{100, 100, 100, 100, 100, 100, 95, 90, 96}
	props, err := r.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 96, Candles: makeCandles(closes...),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("se esperaba 1, hay %d", len(props))
	}
	if props[0].Side != domain.OrderSideBuy {
		t.Fatalf("side: %v", props[0].Side)
	}
}

func TestRSINoCrossNoProposal(t *testing.T) {
	r := newRSI(t, 5, 30, 70, 10)
	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107}
	props, err := r.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 107, Candles: makeCandles(closes...),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("no debía proponer, hay %d", len(props))
	}
}

func TestRSICrossDownIsIgnored(t *testing.T) {
	r := newRSI(t, 5, 30, 70, 10)
	// Subida fuerte y caída final: RSI cruza a la baja el overbought → SELL (ignorado).
	closes := []float64{100, 100, 100, 100, 100, 100, 105, 110, 100}
	props, err := r.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 100, Candles: makeCandles(closes...),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("SELL ignorado, hay %d", len(props))
	}
}
