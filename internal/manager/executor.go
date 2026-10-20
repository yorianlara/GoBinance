package manager

import (
	"context"

	"mi-bot/internal/exchange"
)

// OrderExecutor abstrae el envío y consulta de órdenes en el exchange.
type OrderExecutor interface {
	PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.OrderResponse, error)
	GetOrder(ctx context.Context, symbol string, orderID int64) (exchange.OrderResponse, error)
	GetTrades(ctx context.Context, symbol string, orderID int64) ([]exchange.Fill, error)
}
