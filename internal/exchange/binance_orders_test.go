//go:build !integration

package exchange

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"mi-bot/internal/domain"

	"github.com/binance/binance-connector-go/clients/spot"
	"github.com/binance/binance-connector-go/common/v2/common"
)

func TestPlaceOrderNilClient(t *testing.T) {
	var c *BinanceClient
	_, err := c.PlaceOrder(context.Background(), OrderRequest{Symbol: "BTCUSDT", Quantity: 1})
	if err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestPlaceOrderInvalidArgs(t *testing.T) {
	c := &BinanceClient{}
	if _, err := c.PlaceOrder(context.Background(), OrderRequest{Quantity: 1}); err == nil {
		t.Fatal("se esperaba error por símbolo vacío")
	}
	if _, err := c.PlaceOrder(context.Background(), OrderRequest{Symbol: "BTCUSDT", Quantity: 0}); err == nil {
		t.Fatal("se esperaba error por cantidad 0")
	}
	if _, err := c.PlaceOrder(context.Background(), OrderRequest{
		Symbol: "BTCUSDT", Quantity: 1, Type: domain.OrderTypeLimit,
	}); err == nil {
		t.Fatal("se esperaba error por limit_price 0")
	}
}

func TestGetOrderNilClient(t *testing.T) {
	var c *BinanceClient
	_, err := c.GetOrder(context.Background(), "BTCUSDT", 1)
	if err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestGetOrderInvalidArgs(t *testing.T) {
	c := &BinanceClient{}
	if _, err := c.GetOrder(context.Background(), "", 1); err == nil {
		t.Fatal("se esperaba error por símbolo vacío")
	}
	if _, err := c.GetOrder(context.Background(), "BTCUSDT", 0); err == nil {
		t.Fatal("se esperaba error por orderID 0")
	}
}

func TestCancelOrderNilClient(t *testing.T) {
	var c *BinanceClient
	if err := c.CancelOrder(context.Background(), "BTCUSDT", 1); err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestCancelOrderInvalidArgs(t *testing.T) {
	c := &BinanceClient{}
	if err := c.CancelOrder(context.Background(), "", 1); err == nil {
		t.Fatal("se esperaba error por símbolo vacío")
	}
	if err := c.CancelOrder(context.Background(), "BTCUSDT", 0); err == nil {
		t.Fatal("se esperaba error por orderID 0")
	}
}

func TestCancelAllOpenOrdersCancelsEveryDiscoveredSymbol(t *testing.T) {
	var deletedSymbols []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/openOrders":
			_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","orderId":21},{"symbol":"BTCUSDT","orderId":11},{"symbol":"ETHUSDT","orderId":22}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/openOrders":
			symbol := r.URL.Query().Get("symbol")
			deletedSymbols = append(deletedSymbols, symbol)
			switch symbol {
			case "BTCUSDT":
				_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"client-btc","status":"CANCELED"}]`))
			case "ETHUSDT":
				_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","orderId":21,"clientOrderId":"client-eth-1","status":"CANCELED"},{"symbol":"ETHUSDT","orderId":22,"clientOrderId":"client-eth-2","status":"CANCELED"}]`))
			default:
				http.Error(w, "unexpected symbol", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config := common.NewConfigurationRestAPI(
		common.WithBasePath(server.URL),
		common.WithTimeout(time.Second),
		common.WithRetries(0),
		common.WithApiKey("test-api-key"),
		common.WithApiSecret("test-api-secret"),
	)
	client := &BinanceClient{
		client: spot.NewBinanceSpotClient(spot.WithRestAPI(config)),
	}

	result, err := client.CancelAllOpenOrders(context.Background())
	if err != nil {
		t.Fatalf("CancelAllOpenOrders: %v", err)
	}
	if !reflect.DeepEqual(deletedSymbols, []string{"BTCUSDT", "ETHUSDT"}) {
		t.Fatalf("símbolos cancelados en orden inesperado: %v", deletedSymbols)
	}
	if !reflect.DeepEqual(result.Symbols, deletedSymbols) {
		t.Fatalf("símbolos informados inesperados: %v", result.Symbols)
	}
	if len(result.Orders) != 3 {
		t.Fatalf("se esperaban 3 órdenes canceladas, hay %d", len(result.Orders))
	}
	if result.Orders[0].Symbol != "BTCUSDT" || result.Orders[0].OrderID != 11 || result.Orders[0].ClientOrderID != "client-btc" {
		t.Fatalf("orden cancelada inesperada: %+v", result.Orders[0])
	}
}

func TestGetTradesNilClient(t *testing.T) {
	var c *BinanceClient
	_, err := c.GetTrades(context.Background(), "BTCUSDT", 1)
	if err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestGetTradesInvalidArgs(t *testing.T) {
	c := &BinanceClient{}
	if _, err := c.GetTrades(context.Background(), "", 1); err == nil {
		t.Fatal("se esperaba error por símbolo vacío")
	}
	if _, err := c.GetTrades(context.Background(), "BTCUSDT", 0); err == nil {
		t.Fatal("se esperaba error por orderID 0")
	}
}

func TestParseFloatSafe(t *testing.T) {
	if got := parseFloatSafe("123.45"); got != 123.45 {
		t.Fatalf("se esperaba 123.45, hay %v", got)
	}
	if got := parseFloatSafe("abc"); got != 0 {
		t.Fatalf("se esperaba 0, hay %v", got)
	}
	if got := parseFloatSafe(""); got != 0 {
		t.Fatalf("se esperaba 0, hay %v", got)
	}
}

func TestErrorIsAmbiguousClassification(t *testing.T) {
	ambiguos := []error{
		common.NewNetworkError("read: connection reset by peer"),
		common.NewServerError("request failed after 2 retries: received status 502", 502),
		fmt.Errorf("algo inesperado"),
	}
	definitivos := []error{
		common.NewBadRequestError(`HTTP error 400: Bad Request, {"code":-1100,"msg":"Illegal characters found in parameter."}`),
		common.NewUnauthorizedError("HTTP error"),
		common.NewForbiddenError("HTTP error"),
		common.NewNotFoundError("HTTP error"),
		common.NewRateLimitBanError("HTTP error"),
		common.NewTooManyRequestsError("HTTP error"),
	}
	for _, err := range ambiguos {
		if !errorIsAmbiguous(err) {
			t.Fatalf("se esperaba ambiguo para: %v", err)
		}
	}
	for _, err := range definitivos {
		if errorIsAmbiguous(err) {
			t.Fatalf("se esperaba definitivo para: %v", err)
		}
	}
}

func TestOrderDoesNotExistDetection(t *testing.T) {
	if orderDoesNotExist(nil) {
		t.Fatal("nil no debía detectarse como orden inexistente")
	}
	binanceMiss := common.NewBadRequestError(`HTTP error 400: Bad Request, {"code":-2013,"msg":"Order does not exist."}`)
	if !orderDoesNotExist(binanceMiss) {
		t.Fatalf("la respuesta -2013 debía detectarse como inexistente: %v", binanceMiss)
	}
	otro := common.NewBadRequestError(`HTTP error 400: Bad Request, {"code":-1100,"msg":"Illegal characters found in parameter."}`)
	if orderDoesNotExist(otro) {
		t.Fatalf("un rechazo por parámetros no es 'orden inexistente': %v", otro)
	}
}

func TestGetOrderByClientIDInvalidArgs(t *testing.T) {
	c := &BinanceClient{}
	if _, err := c.GetOrderByClientID(context.Background(), "", "id"); err == nil {
		t.Fatal("se esperaba error por símbolo vacío")
	}
	if _, err := c.GetOrderByClientID(context.Background(), "BTCUSDT", ""); err == nil {
		t.Fatal("se esperaba error por clientOrderID vacío")
	}
}

func TestGetOrderByClientIDReturnsErrOrderNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Binance responde HTTP 400 con código -2013 para órdenes inexistentes.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-2013,"msg":"Order does not exist."}`))
	}))
	defer server.Close()

	config := common.NewConfigurationRestAPI(
		common.WithBasePath(server.URL),
		common.WithTimeout(time.Second),
		common.WithRetries(0),
		common.WithApiKey("test-api-key"),
		common.WithApiSecret("test-api-secret"),
	)
	client := &BinanceClient{
		client: spot.NewBinanceSpotClient(spot.WithRestAPI(config)),
	}

	_, err := client.GetOrderByClientID(context.Background(), "BTCUSDT", "no-existo")
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("se esperaba ErrOrderNotFound, hay %v", err)
	}
}

func TestGetOrderByClientIDReturnsOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("origClientOrderId") != "mi-orden-1" {
			t.Errorf("se esperaba origClientOrderId=mi-orden-1, hay %q", r.URL.Query().Get("origClientOrderId"))
		}
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":4242,"clientOrderId":"mi-orden-1","status":"NEW","executedQty":"0","cummulativeQuoteQty":"0","price":"0"}`))
	}))
	defer server.Close()

	config := common.NewConfigurationRestAPI(
		common.WithBasePath(server.URL),
		common.WithTimeout(time.Second),
		common.WithRetries(0),
		common.WithApiKey("test-api-key"),
		common.WithApiSecret("test-api-secret"),
	)
	client := &BinanceClient{
		client: spot.NewBinanceSpotClient(spot.WithRestAPI(config)),
	}

	resp, err := client.GetOrderByClientID(context.Background(), "BTCUSDT", "mi-orden-1")
	if err != nil {
		t.Fatalf("GetOrderByClientID: %v", err)
	}
	if resp.OrderID != 4242 || resp.Status != "NEW" || resp.ClientOrderID != "mi-orden-1" {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
}
