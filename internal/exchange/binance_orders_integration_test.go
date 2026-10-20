//go:build integration

package exchange

import (
	"context"
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
