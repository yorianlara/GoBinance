package exchange

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"mi-bot/internal/domain"

	"github.com/binance/binance-connector-go/clients/spot/src/restapi/models"
	"github.com/binance/binance-connector-go/common/v2/common"
)

var ErrOrderSubmissionUncertain = errors.New("resultado del envío de la orden incierto")

// ErrOrderNotFound informa que el exchange confirmó que NO existe una orden
// para el clientOrderID consultado. Solo se devuelve tras una respuesta
// explícita de Binance (HTTP 400, código -2013).
var ErrOrderNotFound = errors.New("la orden no existe en el exchange")

// errorIsAmbiguous clasifica un error de PlaceOrder: devuelve true cuando el
// fallo no permite afirmar que la orden no exista en el exchange. Solo los
// rechazos HTTP explícitos del exchange (400/401/403/404/418/429, etc.) son
// definitivos; los errores de red o de servidor (que además disparan
// reintentos del SDK) pueden haber dejado la orden creada.
func errorIsAmbiguous(err error) bool {
	var networkErr *common.NetworkError
	var serverErr *common.ServerError
	if errors.As(err, &networkErr) || errors.As(err, &serverErr) {
		return true
	}

	var badRequest *common.BadRequestError
	var unauthorized *common.UnauthorizedError
	var forbidden *common.ForbiddenError
	var notFound *common.NotFoundError
	var rateLimitBan *common.RateLimitBanError
	var tooManyRequests *common.TooManyRequestsError
	if errors.As(err, &badRequest) || errors.As(err, &unauthorized) || errors.As(err, &forbidden) ||
		errors.As(err, &notFound) || errors.As(err, &rateLimitBan) || errors.As(err, &tooManyRequests) {
		// El exchange procesó la petición y respondió un rechazo definitivo:
		// la orden no fue creada.
		return false
	}

	// Errores del conector o cualquier otro (decodificación, gzip, peticiones
	// mal construidas...): sin certezas, se tratan como ambiguos.
	return true
}

// orderDoesNotExist detecta la respuesta canónica de Binance para una orden
// inexistente: HTTP 400 con el código -2013 ("Order does not exist.").
func orderDoesNotExist(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Order does not exist") || strings.Contains(msg, `"code":-2013`)
}

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

	// Precio de referencia para el precio a enviar y para validar minNotional:
	// el límite redondeado en órdenes limit, el actual en market.
	priceRef := req.LimitPrice
	switch req.Type {
	case domain.OrderTypeLimit:
		if filters.TickSize > 0 {
			// Binance valida el precio contra PRICE_FILTER (tickSize): sin
			// redondear, una rejilla con precios de decimales raros sería
			// rechazada en cada tick y la orden nunca llegaría al book.
			priceRef = truncateToStep(req.LimitPrice, filters.TickSize)
		}
		if priceRef <= 0 {
			return OrderResponse{}, fmt.Errorf("precio límite inválido tras aplicar tickSize de %s", req.Symbol)
		}
	case domain.OrderTypeMarket:
		if priceRef <= 0 {
			// Para market, usamos el precio actual para validar minNotional.
			if p, err := b.GetSymbolPrice(ctx, req.Symbol); err == nil {
				priceRef = p
			}
		}
	}
	if filters.MinNotional > 0 && priceRef > 0 && qty*priceRef < filters.MinNotional {
		return OrderResponse{}, fmt.Errorf("valor %v < minNotional %v para %s", qty*priceRef, filters.MinNotional, req.Symbol)
	}

	builder := b.client.RestApi.TradeAPI.NewOrder(ctx).
		Symbol(req.Symbol).
		Side(sideParam(req.Side)).
		Type(typeParam(req.Type)).
		Quantity(qty)

	if req.Type == domain.OrderTypeLimit {
		builder = builder.
			Price(priceRef).
			TimeInForce(models.NewOrderTimeInForceParameterGtc)
	}
	if req.ClientOrderID != "" {
		builder = builder.NewClientOrderId(req.ClientOrderID)
	}

	resp, err := builder.Execute()
	if err != nil {
		if errorIsAmbiguous(err) {
			// No se puede afirmar que la orden no exista: la petición pudo
			// llegar a crearse (red que cayó antes de la respuesta, error de
			// servidor tras reintentos, respuesta corrupta...). El llamador
			// debe consultar al exchange antes de liberar la reserva.
			return OrderResponse{}, fmt.Errorf("enviar orden: %w: %w", ErrOrderSubmissionUncertain, err)
		}
		// El exchange respondió con un rechazo definitivo: la orden no existe.
		return OrderResponse{}, fmt.Errorf("enviar orden: %w", err)
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

// GetOrderByClientID consulta una orden por su clientOrderID
// (origClientOrderId en Binance). Si el exchange confirma que la orden no
// existe devuelve ErrOrderNotFound; cualquier otro fallo se reporta tal cual
// porque no permite afirmar nada sobre la orden.
func (b *BinanceClient) GetOrderByClientID(ctx context.Context, symbol, clientOrderID string) (OrderResponse, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return OrderResponse{}, fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if symbol == "" || clientOrderID == "" {
		return OrderResponse{}, fmt.Errorf("symbol y clientOrderID son obligatorios")
	}

	resp, err := b.client.RestApi.AccountAPI.GetOrder(ctx).
		Symbol(symbol).
		OrigClientOrderId(clientOrderID).
		Execute()
	if err != nil {
		if orderDoesNotExist(err) {
			return OrderResponse{}, fmt.Errorf("consultar orden %q en %s: %w", clientOrderID, symbol, ErrOrderNotFound)
		}
		return OrderResponse{}, fmt.Errorf("consultar orden %q en %s: %w", clientOrderID, symbol, err)
	}
	if resp == nil {
		return OrderResponse{}, fmt.Errorf("respuesta de orden vacía para %q", clientOrderID)
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
