//go:build !integration

package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mi-bot/internal/domain"

	"github.com/binance/binance-connector-go/clients/spot"
	"github.com/binance/binance-connector-go/common/v2/common"
)

const filtersJSON = `{"timezone":"UTC","serverTime":1,"symbols":[{
	"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT",
	"filters":[
		{"filterType":"PRICE_FILTER","minPrice":"0.01","maxPrice":"1000000","tickSize":"0.01"},
		{"filterType":"LOT_SIZE","minQty":"0.00001","maxQty":"1000","stepSize":"0.00001"},
		{"filterType":"MIN_NOTIONAL","minNotional":"5","applyToMarket":true,"avgPriceMins":5}
	]}]}`

func newFilteredTestClient(serverURL string) *BinanceClient {
	config := common.NewConfigurationRestAPI(
		common.WithBasePath(serverURL),
		common.WithTimeout(time.Second),
		common.WithRetries(0),
		common.WithApiKey("test-api-key"),
		common.WithApiSecret("test-api-secret"),
	)
	return &BinanceClient{client: spot.NewBinanceSpotClient(spot.WithRestAPI(config))}
}

func TestPlaceOrderLimitRoundsPriceToTickSize(t *testing.T) {
	var gotPrice, gotQty string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/exchangeInfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(filtersJSON))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/order":
			q := r.URL.Query()
			gotPrice = q.Get("price")
			gotQty = q.Get("quantity")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":7,"clientOrderId":"c1","status":"NEW","executedQty":"0","cummulativeQuoteQty":"0","price":"0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFilteredTestClient(server.URL)
	resp, err := client.PlaceOrder(context.Background(), OrderRequest{
		Symbol:     "BTCUSDT",
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeLimit,
		Quantity:   0.34567,
		LimitPrice: 123.4597,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if resp.OrderID != 7 || resp.Status != "NEW" {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
	// tickSize 0.01: 123.4597 debe enviarse como 123.45 (truncado al tick).
	if gotPrice != "123.45" {
		t.Fatalf("precio enviado: %q (esperado 123.45)", gotPrice)
	}
	// stepSize 0.00001: la cantidad no debe alterarse.
	if gotQty != "0.34567" {
		t.Fatalf("cantidad enviada: %q", gotQty)
	}
}

func TestPlaceOrderLimitRejectsNotionalBelowMinNotional(t *testing.T) {
	orderHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/exchangeInfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(filtersJSON))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/order":
			orderHits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":8,"clientOrderId":"c2","status":"NEW","executedQty":"0","cummulativeQuoteQty":"0","price":"0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFilteredTestClient(server.URL)
	_, err := client.PlaceOrder(context.Background(), OrderRequest{
		Symbol:     "BTCUSDT",
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeLimit,
		Quantity:   0.0001,
		LimitPrice: 10,
	})
	if err == nil {
		t.Fatal("se esperaba error por minNotional")
	}
	if orderHits != 0 {
		t.Fatalf("la orden no debía enviarse al exchange, envíos: %d", orderHits)
	}
}
