package bots

import (
	"context"
	"testing"
	"time"

	"mi-bot/internal/domain"
)

func newEMA(t *testing.T, fast, slow, size float64) *EMAStrategy {
	t.Helper()
	e, err := NewEMAStrategy(domain.StrategyConfig{Type: "ema", Parameters: map[string]float64{
		"fast_period": fast, "slow_period": slow, "order_size_usdt": size,
	}})
	if err != nil {
		t.Fatalf("NewEMAStrategy: %v", err)
	}
	return e
}

func makeCandles(closes ...float64) []Candle {
	out := make([]Candle, len(closes))
	for i, c := range closes {
		out[i] = Candle{OpenTime: time.Unix(int64(i*60), 0), Close: c}
	}
	return out
}

func TestEMAInvalidParams(t *testing.T) {
	cases := []domain.StrategyConfig{
		{Type: "ema"},
		{Type: "ema", Parameters: map[string]float64{"fast_period": 0, "slow_period": 5, "order_size_usdt": 10}},
		{Type: "ema", Parameters: map[string]float64{"fast_period": 5, "slow_period": 5, "order_size_usdt": 10}},
		{Type: "ema", Parameters: map[string]float64{"fast_period": 5, "slow_period": 3, "order_size_usdt": 10}},
		{Type: "ema", Parameters: map[string]float64{"fast_period": 3, "slow_period": 5}},
		{Type: "ema", Parameters: map[string]float64{"fast_period": 3, "slow_period": 5, "order_size_usdt": 0}},
	}
	for i, c := range cases {
		if _, err := NewEMAStrategy(c); err == nil {
			t.Fatalf("caso %d: se esperaba error", i)
		}
	}
}

func TestEMANotEnoughCandles(t *testing.T) {
	e := newEMA(t, 3, 5, 10)
	props, err := e.Evaluate(context.Background(), MarketData{Symbol: "BTCUSDT", Price: 100, Candles: makeCandles(1, 2, 3)})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("sin velas suficientes no debe proponer, hay %d", len(props))
	}
}

func TestEMACrossUpBuy(t *testing.T) {
	e := newEMA(t, 2, 4, 10)
	// Serie plana que rompe al alza en la última vela: EMA2 cruza sobre EMA4.
	closes := []float64{100, 100, 100, 100, 100, 100, 200}
	props, err := e.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 200, Candles: makeCandles(closes...),
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

func TestEMANoCrossNoProposal(t *testing.T) {
	e := newEMA(t, 3, 5, 10)
	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107}
	props, err := e.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 107, Candles: makeCandles(closes...),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("no debía proponer, hay %d", len(props))
	}
}

func TestEMACrossDownIsIgnored(t *testing.T) {
	e := newEMA(t, 2, 4, 10)
	// Serie plana que rompe a la baja en la última vela.
	closes := []float64{100, 100, 100, 100, 100, 100, 0.01}
	props, err := e.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 0.01, Candles: makeCandles(closes...),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("SELL ignorado, hay %d", len(props))
	}
}

func TestEMACandleWithBadClose(t *testing.T) {
	e := newEMA(t, 2, 4, 10)
	props, err := e.Evaluate(context.Background(), MarketData{
		Symbol: "BTCUSDT", Price: 100,
		Candles: makeCandles(100, 101, 0, 102, 103, 104, 105),
	})
	if err == nil {
		t.Fatalf("se esperaba error por cierre 0, props=%d", len(props))
	}
}

func TestEMAOneShotPerCandle(t *testing.T) {
	// Un cruce implica UNA compra: repetir Evaluate con las mismas velas
	// (los ticks de 5 s lo hacen) no debe ametrallar propuestas, ni mientras
	// la orden esté en vuelo ni tras su cierre (fill).
	e := newEMA(t, 2, 4, 10)
	data := MarketData{Symbol: "BTCUSDT", Price: 200, Candles: makeCandles(100, 100, 100, 100, 100, 100, 200)}

	props, err := e.Evaluate(context.Background(), data)
	if err != nil {
		t.Fatalf("Evaluate 1: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("se esperaba 1 propuesta inicial, hay %d", len(props))
	}

	// Misma vela, orden en vuelo: no repetir.
	props, err = e.Evaluate(context.Background(), data)
	if err != nil {
		t.Fatalf("Evaluate 2: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("la señal no debe repetirse con la orden en vuelo, hay %d", len(props))
	}

	// Misma vela incluso tras cerrar la orden (fill): el cruce ya disparó.
	e.OnOrderClosed(true)
	props, err = e.Evaluate(context.Background(), data)
	if err != nil {
		t.Fatalf("Evaluate 3: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("tras el fill el mismo cruce no debe volver a comprar, hay %d", len(props))
	}

	// Vela siguiente sin nuevo cruce: nada.
	data2 := MarketData{Symbol: "BTCUSDT", Price: 210, Candles: makeCandles(100, 100, 100, 100, 100, 100, 200, 210)}
	props, err = e.Evaluate(context.Background(), data2)
	if err != nil {
		t.Fatalf("Evaluate 4: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("una vela nueva sin cruce no debe proponer, hay %d", len(props))
	}
}
