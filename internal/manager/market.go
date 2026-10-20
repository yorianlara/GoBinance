package manager

import (
	"context"

	"mi-bot/internal/bots"
)

// MarketProvider abstrae la obtención de precios de mercado.
type MarketProvider interface {
	Price(ctx context.Context, symbol string) (float64, error)
}

// KlineProvider abstrae la obtención de velas.
type KlineProvider interface {
	Klines(ctx context.Context, symbol, interval string, limit int) ([]bots.Candle, error)
}
