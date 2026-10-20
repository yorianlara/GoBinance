//go:build !integration

package exchange

import (
	"context"
	"testing"

	"mi-bot/internal/domain"
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
