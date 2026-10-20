package manager

import (
	"context"

	"mi-bot/internal/exchange"
)

// OrderExecutor abstrae el envío, consulta y cancelación de órdenes en el exchange.
type OrderExecutor interface {
	PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.OrderResponse, error)
	GetOrder(ctx context.Context, symbol string, orderID int64) (exchange.OrderResponse, error)
	GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (exchange.OrderResponse, error)
	CancelOrder(ctx context.Context, symbol string, orderID int64) error
	GetTrades(ctx context.Context, symbol string, orderID int64) ([]exchange.Fill, error)
	GetFreeBalance(ctx context.Context, asset string) (float64, error)
	GetSymbolBaseAsset(ctx context.Context, symbol string) (string, error)
}
