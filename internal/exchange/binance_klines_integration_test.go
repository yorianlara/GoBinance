//go:build integration

package exchange

import (
	"context"
	"testing"
	"time"

	"mi-bot/internal/config"
)

func TestIntegration_GetKlines(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	candles, err := client.GetKlines(ctx, "BTCUSDT", "1m", 5)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(candles) == 0 {
		t.Fatal("se esperaba al menos una vela")
	}
	for i, c := range candles {
		if c.Open <= 0 || c.High <= 0 || c.Low <= 0 || c.Close <= 0 {
			t.Fatalf("vela %d inválida: %+v", i, c)
		}
		if c.High < c.Low {
			t.Fatalf("vela %d high<low: %+v", i, c)
		}
	}
}
