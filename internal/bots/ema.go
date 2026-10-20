package bots

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mi-bot/internal/domain"
)

// EMAStrategy detecta cruces de EMA rápida sobre lenta.
//
// Parámetros (StrategyConfig.Parameters, todos float64):
//   - fast_period: periodo de la EMA rápida (obligatorio, >= 1)
//   - slow_period: periodo de la EMA lenta (obligatorio, > fast_period)
//   - order_size_usdt: importe a comprar cuando hay señal (obligatorio, > 0)
//
// Hasta Fase F solo se emite BUY; las señales SELL se descartan.
type EMAStrategy struct {
	fastPeriod int
	slowPeriod int
	sizeUSDT   float64

	mu   sync.Mutex
	last time.Time // última señal emitida (evita repetir en el mismo tick)
}

func NewEMAStrategy(cfg domain.StrategyConfig) (*EMAStrategy, error) {
	if cfg.Type != "ema" {
		return nil, fmt.Errorf("tipo de estrategia inesperado: %q", cfg.Type)
	}

	fast, ok := cfg.Parameters["fast_period"]
	if !ok || fast < 1 {
		return nil, fmt.Errorf("ema: fast_period debe ser >= 1")
	}
	slow, ok := cfg.Parameters["slow_period"]
	if !ok || slow <= fast {
		return nil, fmt.Errorf("ema: slow_period debe ser > fast_period")
	}
	size, ok := cfg.Parameters["order_size_usdt"]
	if !ok || size <= 0 {
		return nil, fmt.Errorf("ema: order_size_usdt debe ser > 0")
	}

	return &EMAStrategy{
		fastPeriod: int(fast),
		slowPeriod: int(slow),
		sizeUSDT:   size,
	}, nil
}

func (e *EMAStrategy) Type() string { return "ema" }

func (e *EMAStrategy) Evaluate(ctx context.Context, data MarketData) ([]domain.OrderProposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if data.Price <= 0 {
		return nil, nil
	}
	if len(data.Candles) < e.slowPeriod+1 {
		return nil, nil
	}

	closes := make([]float64, len(data.Candles))
	for i, c := range data.Candles {
		if c.Close <= 0 {
			return nil, fmt.Errorf("ema: cierre no positivo en vela %d", i)
		}
		closes[i] = c.Close
	}

	fast := ema(closes, e.fastPeriod)
	slow := ema(closes, e.slowPeriod)

	prev := fast[len(fast)-2] - slow[len(slow)-2]
	curr := fast[len(fast)-1] - slow[len(slow)-1]

	// Cruce al alza → BUY. Cruce a la baja → SELL (ignorado en esta fase).
	if !(prev <= 0 && curr > 0) {
		return nil, nil
	}

	qty := e.sizeUSDT / data.Price
	if qty <= 0 {
		return nil, nil
	}

	return []domain.OrderProposal{
		{
			Symbol:     data.Symbol,
			Side:       domain.OrderSideBuy,
			Type:       domain.OrderTypeMarket,
			Quantity:   qty,
			Reason:     fmt.Sprintf("ema cross %d/%d", e.fastPeriod, e.slowPeriod),
			ProposedAt: time.Now(),
		},
	}, nil
}

// ema calcula la EMA de `values` con el periodo dado (mismo algoritmo que
// el Python: se inicializa con el primer valor).
func ema(values []float64, period int) []float64 {
	if len(values) == 0 {
		return nil
	}
	mult := 2.0 / float64(period+1)
	out := make([]float64, len(values))
	out[0] = values[0]
	for i := 1; i < len(values); i++ {
		out[i] = (values[i]-out[i-1])*mult + out[i-1]
	}
	return out
}
