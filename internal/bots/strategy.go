package bots

import (
	"context"

	"mi-bot/internal/domain"
)

// MarketData es el snapshot que recibe una estrategia en cada tick.
// Price siempre viene; Candles puede venir vacío si el kline provider
// no está disponible o falla.
type MarketData struct {
	Symbol  string
	Price   float64
	Candles []Candle
}

// Strategy genera propuestas de orden a partir de datos de mercado.
type Strategy interface {
	Type() string
	Evaluate(ctx context.Context, data MarketData) ([]domain.OrderProposal, error)
}
