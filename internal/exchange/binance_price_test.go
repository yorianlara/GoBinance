//go:build !integration

package exchange

import (
	"context"
	"testing"
)

func TestGetSymbolPriceNilClient(t *testing.T) {
	var c *BinanceClient
	if _, err := c.GetSymbolPrice(context.Background(), "BTCUSDT"); err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestGetSymbolPriceEmptySymbol(t *testing.T) {
	c := &BinanceClient{}
	// No llegamos a la red: el cliente es nil internamente y falla antes.
	if _, err := c.GetSymbolPrice(context.Background(), "   "); err == nil {
		t.Fatal("se esperaba error con símbolo vacío")
	}
}
