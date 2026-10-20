package bots

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mi-bot/internal/domain"
)

// DCAStrategy implementa Dollar Cost Averaging: compra una cantidad fija
// en USDT cada cierto intervalo, hasta un máximo de compras opcional.
//
// Parámetros (StrategyConfig.Parameters, todos float64):
//   - amount_per_buy_usdt: importe a gastar por compra (obligatorio, > 0)
//   - interval_minutes:    minutos entre compras (obligatorio, > 0)
//   - max_buys:            número máximo de compras (0 = ilimitado)
type DCAStrategy struct {
	amountPerBuy float64
	interval     time.Duration
	maxBuys      int

	mu      sync.Mutex
	lastBuy time.Time
	buys    int
}

func NewDCAStrategy(cfg domain.StrategyConfig) (*DCAStrategy, error) {
	if cfg.Type != "dca" {
		return nil, fmt.Errorf("tipo de estrategia inesperado: %q", cfg.Type)
	}

	amount, ok := cfg.Parameters["amount_per_buy_usdt"]
	if !ok || amount <= 0 {
		return nil, fmt.Errorf("dca: amount_per_buy_usdt debe ser > 0")
	}

	intervalMin, ok := cfg.Parameters["interval_minutes"]
	if !ok || intervalMin <= 0 {
		return nil, fmt.Errorf("dca: interval_minutes debe ser > 0")
	}

	maxBuys := 0
	if v, ok := cfg.Parameters["max_buys"]; ok {
		if v < 0 {
			return nil, fmt.Errorf("dca: max_buys no puede ser negativo")
		}
		maxBuys = int(v)
	}

	return &DCAStrategy{
		amountPerBuy: amount,
		interval:     time.Duration(intervalMin) * time.Minute,
		maxBuys:      maxBuys,
	}, nil
}

func (d *DCAStrategy) Type() string { return "dca" }

func (d *DCAStrategy) Evaluate(ctx context.Context, data MarketData) ([]domain.OrderProposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if data.Price <= 0 {
		return nil, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.maxBuys > 0 && d.buys >= d.maxBuys {
		return nil, nil
	}
	if !d.lastBuy.IsZero() && time.Since(d.lastBuy) < d.interval {
		return nil, nil
	}

	qty := d.amountPerBuy / data.Price
	if qty <= 0 {
		return nil, nil
	}

	// Solución A: actualizamos estado incondicionalmente al proponer.
	d.lastBuy = time.Now()
	d.buys++

	return []domain.OrderProposal{
		{
			// BotID vacío a propósito: lo rellena el BotRuntime.
			Symbol:     data.Symbol,
			Side:       domain.OrderSideBuy,
			Type:       domain.OrderTypeMarket,
			Quantity:   qty,
			Reason:     "dca",
			ProposedAt: time.Now(),
		},
	}, nil
}
