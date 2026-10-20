//go:build integration

package exchange

import (
	"context"
	"os"
	"testing"
	"time"

	"mi-bot/internal/config"
)

func TestIntegration_GetSpotBalances(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	balances, err := client.GetSpotBalances(ctx)
	if err != nil {
		t.Fatalf("GetSpotBalances: %v", err)
	}
	if len(balances) == 0 {
		t.Fatal("se esperaba al menos un asset con saldo en Testnet")
	}
	if _, ok := balances["USDT"]; !ok {
		t.Logf("aviso: no hay USDT en la cuenta de Testnet (balances: %v)", balances)
	}
}
