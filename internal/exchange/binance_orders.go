package exchange

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"mi-bot/internal/domain"

	"github.com/binance/binance-connector-go/clients/spot/src/restapi/models"
)

var ErrOrderSubmissionUncertain = errors.New("resultado del envío de la orden incierto")

// OrderRequest es la petición de nueva orden.
type OrderRequest struct {
	Symbol        string
	Side          domain.OrderSide
	Type          domain.OrderType
	Quantity      float64
	LimitPrice    float64 // solo si Type == limit
	ClientOrderID string  // opcional, para idempotencia
}

// OrderResponse es la respuesta normalizada de una orden.
type OrderResponse struct {
	OrderID             int64
	ClientOrderID       string
	Symbol              string
	Status              string
	ExecutedQty         float64
	CummulativeQuoteQty float64
	Price               float64
}

// Fill representa una ejecución individual de una orden.
type Fill struct {
	TradeID         int64
	OrderID         int64
	Symbol          string
	Quantity        float64
	Price           float64
	Commission      float64
	CommissionAsset string
	ExecutedAt      time.Time
}

type CancelledOrder struct {
	Symbol        string
	OrderID       int64
	ClientOrderID string
	Status        string
}

type CancelAllResult struct {
	Symbols []string
	Orders  []CancelledOrder
}

// PlaceOrder envía una orden al exchange. Requiere credenciales.
func (b *BinanceClient) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResponse, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return OrderResponse{}, fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if req.Symbol == "" {
		return OrderResponse{}, fmt.Errorf("el símbolo es obligatorio")
	}
	if req.Quantity <= 0 {
		return OrderResponse{}, fmt.Errorf("la cantidad debe ser > 0")
	}
	if req.Type == domain.OrderTypeLimit && req.LimitPrice <= 0 {
		return OrderResponse{}, fmt.Errorf("limit requiere limit_price > 0")
	}

	// Normalizar cantidad según stepSize del símbolo.
	filters, err := b.filtersFor(ctx, req.Symbol)
	if err != nil {
		return OrderResponse{}, fmt.Errorf("obtener filtros: %w", err)
	}
	qty := truncateToStep(req.Quantity, filters.stepFor(req.Type == domain.OrderTypeMarket))
	if qty < filters.MinQty {
		return OrderResponse{}, fmt.Errorf("cantidad %v < mínimo %v para %s", qty, filters.MinQty, req.Symbol)
	}
	if req.Type == domain.OrderTypeMarket {
		price := req.LimitPrice
		if price <= 0 {
			// Para market, usamos el precio actual para validar minNotional.
			p, err := b.GetSymbolPrice(ctx, req.Symbol)
			if err == nil {
				price = p
			}
		}
		if price > 0 && qty*price < filters.MinNotional {
			return OrderResponse{}, fmt.Errorf("valor %v < minNotional %v para %s", qty*price, filters.MinNotional, req.Symbol)
		}
	}

	builder := b.client.RestApi.TradeAPI.NewOrder(ctx).
		Symbol(req.Symbol).
		Side(sideParam(req.Side)).
		Type(typeParam(req.Type)).
		Quantity(qty)

	if req.Type == domain.OrderTypeLimit {
		builder = builder.
			Price(req.LimitPrice).
			TimeInForce(models.NewOrderTimeInForceParameterGtc)
	}
	if req.ClientOrderID != "" {
		builder = builder.NewClientOrderId(req.ClientOrderID)
	}

	resp, err := builder.Execute()
	if err != nil {
		return OrderResponse{}, fmt.Errorf("enviar orden: %w: %w", ErrOrderSubmissionUncertain, err)
	}
	if resp == nil {
		return OrderResponse{}, fmt.Errorf("%w: respuesta de orden vacía", ErrOrderSubmissionUncertain)
	}

	return OrderResponse{
		OrderID:             resp.Data.GetOrderId(),
		ClientOrderID:       resp.Data.GetClientOrderId(),
		Symbol:              resp.Data.GetSymbol(),
		Status:              resp.Data.GetStatus(),
		ExecutedQty:         parseFloatSafe(resp.Data.GetExecutedQty()),
		CummulativeQuoteQty: parseFloatSafe(resp.Data.GetCummulativeQuoteQty()),
		Price:               parseFloatSafe(resp.Data.GetPrice()),
	}, nil
}

// GetOrder consulta el estado de una orden.
func (b *BinanceClient) GetOrder(ctx context.Context, symbol string, orderID int64) (OrderResponse, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return OrderResponse{}, fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if symbol == "" || orderID <= 0 {
		return OrderResponse{}, fmt.Errorf("symbol y orderID son obligatorios")
	}

	resp, err := b.client.RestApi.AccountAPI.GetOrder(ctx).
		Symbol(symbol).
		OrderId(orderID).
		Execute()
	if err != nil {
		return OrderResponse{}, fmt.Errorf("consultar orden %d: %w", orderID, err)
	}
	if resp == nil {
		return OrderResponse{}, fmt.Errorf("respuesta de orden vacía")
	}

	return OrderResponse{
		OrderID:             resp.Data.GetOrderId(),
		ClientOrderID:       resp.Data.GetClientOrderId(),
		Symbol:              resp.Data.GetSymbol(),
		Status:              resp.Data.GetStatus(),
		ExecutedQty:         parseFloatSafe(resp.Data.GetExecutedQty()),
		CummulativeQuoteQty: parseFloatSafe(resp.Data.GetCummulativeQuoteQty()),
		Price:               parseFloatSafe(resp.Data.GetPrice()),
	}, nil
}

// CancelOrder cancela una orden abierta en el exchange.
func (b *BinanceClient) CancelOrder(ctx context.Context, symbol string, orderID int64) error {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if symbol == "" || orderID <= 0 {
		return fmt.Errorf("symbol y orderID son obligatorios")
	}

	_, err := b.client.RestApi.TradeAPI.DeleteOrder(ctx).
		Symbol(symbol).
		OrderId(orderID).
		Execute()
	if err != nil {
		return fmt.Errorf("cancelar orden %d: %w", orderID, err)
	}
	return nil
}

// CancelAllOpenOrders cancela todas las órdenes abiertas, consultando primero
// los símbolos activos y usando la cancelación masiva por símbolo de Binance.
func (b *BinanceClient) CancelAllOpenOrders(ctx context.Context) (CancelAllResult, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return CancelAllResult{}, fmt.Errorf("el cliente de Binance no está inicializado")
	}

	openOrders, err := b.client.RestApi.AccountAPI.GetOpenOrders(ctx).Execute()
	if err != nil {
		return CancelAllResult{}, fmt.Errorf("consultar órdenes abiertas: %w", err)
	}
	if openOrders == nil {
		return CancelAllResult{}, fmt.Errorf("respuesta de órdenes abiertas vacía")
	}

	symbolSet := make(map[string]struct{})
	var cancelErrors []error
	for _, order := range openOrders.Data.Items {
		symbol := order.GetSymbol()
		if symbol == "" {
			cancelErrors = append(cancelErrors, fmt.Errorf("orden abierta %d sin símbolo en la respuesta de Binance", order.GetOrderId()))
			continue
		}
		symbolSet[symbol] = struct{}{}
	}
	symbols := make([]string, 0, len(symbolSet))
	for symbol := range symbolSet {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	result := CancelAllResult{Symbols: symbols}

	for _, symbol := range symbols {
		response, err := b.client.RestApi.TradeAPI.DeleteOpenOrders(ctx).
			Symbol(symbol).
			Execute()
		if err != nil {
			cancelErrors = append(cancelErrors, fmt.Errorf("cancelar órdenes de %s: %w", symbol, err))
			continue
		}
		if response == nil {
			cancelErrors = append(cancelErrors, fmt.Errorf("cancelar órdenes de %s: respuesta vacía", symbol))
			continue
		}
		for _, order := range response.Data.Items {
			if order.GetStatus() != "CANCELED" {
				cancelErrors = append(cancelErrors, fmt.Errorf("Binance no confirmó cancelación de la orden %d de %s; estado %q", order.GetOrderId(), symbol, order.GetStatus()))
				continue
			}
			result.Orders = append(result.Orders, CancelledOrder{
				Symbol:        order.GetSymbol(),
				OrderID:       order.GetOrderId(),
				ClientOrderID: order.GetClientOrderId(),
				Status:        order.GetStatus(),
			})
		}
	}
	return result, errors.Join(cancelErrors...)
}

// GetTrades devuelve las ejecuciones de una orden.
func (b *BinanceClient) GetTrades(ctx context.Context, symbol string, orderID int64) ([]Fill, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return nil, fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if symbol == "" || orderID <= 0 {
		return nil, fmt.Errorf("symbol y orderID son obligatorios")
	}

	resp, err := b.client.RestApi.AccountAPI.MyTrades(ctx).
		Symbol(symbol).
		OrderId(orderID).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("consultar trades de orden %d: %w", orderID, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("respuesta de trades vacía")
	}

	fills := make([]Fill, 0, len(resp.Data.Items))
	for _, t := range resp.Data.Items {
		fills = append(fills, Fill{
			TradeID:         t.GetId(),
			OrderID:         t.GetOrderId(),
			Symbol:          t.GetSymbol(),
			Quantity:        parseFloatSafe(t.GetQty()),
			Price:           parseFloatSafe(t.GetPrice()),
			Commission:      parseFloatSafe(t.GetCommission()),
			CommissionAsset: t.GetCommissionAsset(),
			ExecutedAt:      time.UnixMilli(t.GetTime()).UTC(),
		})
	}
	return fills, nil
}

func sideParam(s domain.OrderSide) models.NewOrderSideParameter {
	if s == domain.OrderSideSell {
		return models.NewOrderSideParameterSell
	}
	return models.NewOrderSideParameterBuy
}

func typeParam(t domain.OrderType) models.NewOrderTypeParameter {
	if t == domain.OrderTypeLimit {
		return models.NewOrderTypeParameterLimit
	}
	return models.NewOrderTypeParameterMarket
}

func parseFloatSafe(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
