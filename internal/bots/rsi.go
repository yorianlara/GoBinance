package bots

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mi-bot/internal/domain"
)

// RSIStrategy detecta cruces del RSI sobre sus umbrales.
//
// Parámetros (StrategyConfig.Parameters, todos float64):
//   - period: periodo del RSI (obligatorio, >= 2)
//   - oversold: umbral inferior (obligatorio, 0 < oversold < overbought < 100)
//   - overbought: umbral superior (obligatorio)
//   - order_size_usdt: importe a comprar cuando hay señal (obligatorio, > 0)
//
// Hasta Fase F solo se emite BUY; las señales SELL se descartan.
type RSIStrategy struct {
	period     int
	oversold   float64
	overbought float64
	sizeUSDT   float64

	mu               sync.Mutex
	pending          bool      // hay una propuesta en curso
	lastSignalCandle time.Time // vela cuya señal ya se disparó: 1 compra por cruce
}

func NewRSIStrategy(cfg domain.StrategyConfig) (*RSIStrategy, error) {
	if cfg.Type != "rsi" {
		return nil, fmt.Errorf("tipo de estrategia inesperado: %q", cfg.Type)
	}

	period, ok := cfg.Parameters["period"]
	if !ok || period < 2 {
		return nil, fmt.Errorf("rsi: period debe ser >= 2")
	}
	over, ok := cfg.Parameters["oversold"]
	if !ok {
		return nil, fmt.Errorf("rsi: oversold es obligatorio")
	}
	ob, ok := cfg.Parameters["overbought"]
	if !ok {
		return nil, fmt.Errorf("rsi: overbought es obligatorio")
	}
	if !(0 < over && over < ob && ob < 100) {
		return nil, fmt.Errorf("rsi: se requiere 0 < oversold < overbought < 100")
	}
	size, ok := cfg.Parameters["order_size_usdt"]
	if !ok || size <= 0 {
		return nil, fmt.Errorf("rsi: order_size_usdt debe ser > 0")
	}

	return &RSIStrategy{
		period:     int(period),
		oversold:   over,
		overbought: ob,
		sizeUSDT:   size,
	}, nil
}

func (r *RSIStrategy) Type() string { return "rsi" }

// OnOrderRejected libera una propuesta que no llegó a convertirse en orden.
func (r *RSIStrategy) OnOrderRejected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = false
}

// OnOrderClosed marca el fin de una orden (rellenada o cancelada); la señal
// concreta del cruce no se repetirá (la guarma lastSignalCandle).
func (r *RSIStrategy) OnOrderClosed(_ bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = false
}

func (r *RSIStrategy) Evaluate(ctx context.Context, data MarketData) ([]domain.OrderProposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if data.Price <= 0 {
		return nil, nil
	}
	if len(data.Candles) < r.period+2 {
		return nil, nil
	}

	closes := make([]float64, len(data.Candles))
	for i, c := range data.Candles {
		if c.Close <= 0 {
			return nil, fmt.Errorf("rsi: cierre no positivo en vela %d", i)
		}
		closes[i] = c.Close
	}

	rsi := rsiValues(closes, r.period)
	if len(rsi) < 2 {
		return nil, nil
	}

	prev := rsi[len(rsi)-2]
	curr := rsi[len(rsi)-1]

	// Cruce al alza del oversold → BUY. Cruce a la baja del overbought → SELL (ignorado).
	if !(prev <= r.oversold && curr > r.oversold) {
		return nil, nil
	}

	// Un disparo por cruce: mientras el cruce siga "fresco" en la misma vela
	// no repetir, y el gate `pending` evita solapar dos propuestas mientras
	// la primera espera su envío/cierre.
	signalCandle := data.Candles[len(data.Candles)-1].OpenTime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending || signalCandle.Equal(r.lastSignalCandle) {
		return nil, nil
	}
	r.pending = true
	r.lastSignalCandle = signalCandle

	qty := r.sizeUSDT / data.Price
	if qty <= 0 {
		r.pending = false
		return nil, nil
	}

	return []domain.OrderProposal{
		{
			Symbol:     data.Symbol,
			Side:       domain.OrderSideBuy,
			Type:       domain.OrderTypeMarket,
			Quantity:   qty,
			Reason:     fmt.Sprintf("rsi cross %.2f (%.2f->%.2f)", r.oversold, prev, curr),
			ProposedAt: time.Now(),
		},
	}, nil
}

// rsiValues calcula la serie RSI con el método de Wilder (mismo que el Python).
func rsiValues(closes []float64, period int) []float64 {
	if len(closes) < period+1 {
		return nil
	}

	changes := make([]float64, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		changes[i-1] = closes[i] - closes[i-1]
	}

	gains := make([]float64, len(changes))
	losses := make([]float64, len(changes))
	for i, c := range changes {
		if c > 0 {
			gains[i] = c
		} else {
			losses[i] = -c
		}
	}

	sumGain, sumLoss := 0.0, 0.0
	for i := 0; i < period; i++ {
		sumGain += gains[i]
		sumLoss += losses[i]
	}
	avgGain := sumGain / float64(period)
	avgLoss := sumLoss / float64(period)

	values := make([]float64, 0, len(changes)-period+1)
	values = append(values, calcRSI(avgGain, avgLoss))

	for i := period; i < len(changes); i++ {
		avgGain = (avgGain*float64(period-1) + gains[i]) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + losses[i]) / float64(period)
		values = append(values, calcRSI(avgGain, avgLoss))
	}
	return values
}

func calcRSI(gain, loss float64) float64 {
	if gain == 0 && loss == 0 {
		return 50
	}
	if loss == 0 {
		return 100
	}
	if gain == 0 {
		return 0
	}
	return 100 - 100/(1+gain/loss)
}
