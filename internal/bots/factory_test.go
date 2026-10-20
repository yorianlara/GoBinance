package bots

import (
	"testing"

	"mi-bot/internal/domain"
)

func TestDefaultFactoryNoop(t *testing.T) {
	s, err := DefaultFactory(domain.StrategyConfig{Type: "noop"})
	if err != nil {
		t.Fatalf("DefaultFactory: %v", err)
	}
	if _, ok := s.(NoopStrategy); !ok {
		t.Fatalf("se esperaba NoopStrategy, hay %T", s)
	}
}

func TestDefaultFactoryDCA(t *testing.T) {
	s, err := DefaultFactory(domain.StrategyConfig{
		Type:       "dca",
		Parameters: map[string]float64{"amount_per_buy_usdt": 10, "interval_minutes": 60},
	})
	if err != nil {
		t.Fatalf("DefaultFactory: %v", err)
	}
	if _, ok := s.(*DCAStrategy); !ok {
		t.Fatalf("se esperaba *DCAStrategy, hay %T", s)
	}
}

func TestDefaultFactoryGrid(t *testing.T) {
	s, err := DefaultFactory(domain.StrategyConfig{
		Type: "grid",
		Parameters: map[string]float64{
			"grid_levels":          5,
			"grid_spacing_percent": 1,
			"order_size_usdt":      10,
		},
	})
	if err != nil {
		t.Fatalf("DefaultFactory: %v", err)
	}
	if _, ok := s.(*GridStrategy); !ok {
		t.Fatalf("se esperaba *GridStrategy, hay %T", s)
	}
}

func TestDefaultFactoryEMA(t *testing.T) {
	s, err := DefaultFactory(domain.StrategyConfig{
		Type:       "ema",
		Parameters: map[string]float64{"fast_period": 9, "slow_period": 21, "order_size_usdt": 10},
	})
	if err != nil {
		t.Fatalf("DefaultFactory: %v", err)
	}
	if _, ok := s.(*EMAStrategy); !ok {
		t.Fatalf("se esperaba *EMAStrategy, hay %T", s)
	}
}

func TestDefaultFactoryRSI(t *testing.T) {
	s, err := DefaultFactory(domain.StrategyConfig{
		Type:       "rsi",
		Parameters: map[string]float64{"period": 14, "oversold": 30, "overbought": 70, "order_size_usdt": 10},
	})
	if err != nil {
		t.Fatalf("DefaultFactory: %v", err)
	}
	if _, ok := s.(*RSIStrategy); !ok {
		t.Fatalf("se esperaba *RSIStrategy, hay %T", s)
	}
}

func TestDefaultFactoryUnknown(t *testing.T) {
	if _, err := DefaultFactory(domain.StrategyConfig{Type: "mystery"}); err == nil {
		t.Fatal("se esperaba error con estrategia desconocida")
	}
}
