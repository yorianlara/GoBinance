package bots

import (
	"context"
	"testing"
)

// Comprobación en tiempo de compilación de que NoopStrategy satisface Strategy.
var _ Strategy = NoopStrategy{}

func TestNoopStrategyType(t *testing.T) {
	if got := (NoopStrategy{}).Type(); got != "noop" {
		t.Fatalf("Type() = %q, se esperaba %q", got, "noop")
	}
}

func TestNoopStrategyEvaluate(t *testing.T) {
	props, err := NoopStrategy{}.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT",
		Price:  100,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if props != nil {
		t.Fatalf("se esperaba nil, hay %v", props)
	}
}
