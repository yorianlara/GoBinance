//go:build integration

package exchange

import (
	"context"
	"os"
	"testing"
	"time"

	"mi-bot/internal/config"
)

func TestIntegration_GetCapitalUSDT(t *testing.T) {
	if os.Getenv("BINANCE_API_KEY") == "" || os.Getenv("BINANCE_API_SECRET") == "" {
		t.Skip("BINANCE_API_KEY/BINANCE_API_SECRET no configuradas")
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

	total, err := client.GetCapitalUSDT(ctx, nil)
	if err != nil {
		t.Fatalf("GetCapitalUSDT: %v", err)
	}

	t.Logf("capital total estimado: %.4f USDT", total)
	if total < 0 {
		t.Fatalf("capital negativo: %v", total)
	}
}
