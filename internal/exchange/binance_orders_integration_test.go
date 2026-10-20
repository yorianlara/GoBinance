//go:build integration

package exchange

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"mi-bot/internal/config"
	"mi-bot/internal/domain"
)

func TestIntegration_PlaceAndQueryOrder(t *testing.T) {
	if os.Getenv("BINANCE_API_KEY") == "" || os.Getenv("BINANCE_API_SECRET") == "" {
		t.Skip("credenciales no configuradas")
	}

	cfg := &config.Config{}
	cfg.Exchange.Name = "binance"
	cfg.Exchange.Market = "spot"
	cfg.Exchange.Testnet = true
	cfg.Exchange.APIKeyEnv = "BINANCE_API_KEY"
	cfg.Exchange.APISecretEnv = "BINANCE_API_SECRET"

	client, err := NewBinanceClient(cfg)
	if err != nil {
		t.Fatalf("NewBinanceClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Compra market con cantidad pequeña (0.0001 BTC) para no gastar mucho.
	req := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     domain.OrderSideBuy,
		Type:     domain.OrderTypeMarket,
		Quantity: 0.0001,
	}
	resp, err := client.PlaceOrder(ctx, req)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if resp.OrderID == 0 {
		t.Fatalf("OrderID vacío: %+v", resp)
	}

	// Consultar estado.
	ord, err := client.GetOrder(ctx, "BTCUSDT", resp.OrderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if ord.Symbol != "BTCUSDT" {
		t.Fatalf("símbolo inesperado: %q", ord.Symbol)
	}

	// Consultar trades.
	fills, err := client.GetTrades(ctx, "BTCUSDT", resp.OrderID)
	if err != nil {
		t.Fatalf("GetTrades: %v", err)
	}
	t.Logf("fills: %d", len(fills))
}

// Verifica el ciclo crear orden → llenar → vender: market buy seguido de
// market sell con la cantidad comprada, comprobando que ambas quedan FILLED.
func TestIntegration_MarketBuyThenMarketSellRoundtrip(t *testing.T) {
	if os.Getenv("BINANCE_API_KEY") == "" || os.Getenv("BINANCE_API_SECRET") == "" {
		t.Skip("credenciales no configuradas")
	}

	cfg := &config.Config{}
	cfg.Exchange.Name = "binance"
	cfg.Exchange.Market = "spot"
	cfg.Exchange.Testnet = true
	cfg.Exchange.APIKeyEnv = "BINANCE_API_KEY"
	cfg.Exchange.APISecretEnv = "BINANCE_API_SECRET"

	client, err := NewBinanceClient(cfg)
	if err != nil {
		t.Fatalf("NewBinanceClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1) Crear la orden de compra (market, cantidad mínima).
	buy, err := client.PlaceOrder(ctx, OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     domain.OrderSideBuy,
		Type:     domain.OrderTypeMarket,
		Quantity: 0.0001,
	})
	if err != nil {
		t.Fatalf("PlaceOrder buy: %v", err)
	}
	if buy.OrderID == 0 {
		t.Fatalf("OrderID vacío: %+v", buy)
	}

	buyState, err := client.GetOrder(ctx, "BTCUSDT", buy.OrderID)
	if err != nil {
		t.Fatalf("GetOrder buy: %v", err)
	}
	if buyState.Status != "FILLED" {
		t.Fatalf("la compra debe haber llenado, estado %q", buyState.Status)
	}
	fills, err := client.GetTrades(ctx, "BTCUSDT", buy.OrderID)
	if err != nil {
		t.Fatalf("GetTrades buy: %v", err)
	}
	if len(fills) == 0 {
		t.Fatal("la compra llena debe tener ejecuciones")
	}
	t.Logf("buy filled: exec_qty=%v quote=%v fills=%d", buyState.ExecutedQty, buyState.CummulativeQuoteQty, len(fills))

	// 2) Vender exactamente lo comprado para no dejar posición.
	freeBTC, err := client.GetFreeBalance(ctx, "BTC")
	if err != nil {
		t.Fatalf("GetFreeBalance BTC: %v", err)
	}
	sellQty := math.Min(freeBTC, buyState.ExecutedQty)
	if sellQty <= 0 {
		t.Fatalf("saldo BTC libre insuficiente para vender: %v", freeBTC)
	}

	sell, err := client.PlaceOrder(ctx, OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     domain.OrderSideSell,
		Type:     domain.OrderTypeMarket,
		Quantity: sellQty,
	})
	if err != nil {
		t.Fatalf("PlaceOrder sell: %v", err)
	}

	sellState, err := client.GetOrder(ctx, "BTCUSDT", sell.OrderID)
	if err != nil {
		t.Fatalf("GetOrder sell: %v", err)
	}
	if sellState.Status != "FILLED" {
		t.Fatalf("la venta debe haber llenado, estado %q", sellState.Status)
	}
	t.Logf("sell filled: exec_qty=%v quote=%v", sellState.ExecutedQty, sellState.CummulativeQuoteQty)
	if sellState.ExecutedQty <= 0 {
		t.Fatal("la venta no ejecutó cantidad")
	}
}
