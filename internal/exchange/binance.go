package exchange

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"mi-bot/internal/config"

	"github.com/binance/binance-connector-go/clients/spot"
	"github.com/binance/binance-connector-go/common/v2/common"
)

type BinanceClient struct {
	client    *spot.BinanceSpotClient
	filtersMu sync.RWMutex
	filters   map[string]SymbolFilters
}

func NewBinanceClient(cfg *config.Config) (*BinanceClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("la configuración no puede ser nil")
	}

	if !cfg.Exchange.Testnet {
		return nil, fmt.Errorf("por seguridad, solo se permite inicializar Binance Spot Testnet")
	}

	apiKey := os.Getenv(cfg.Exchange.APIKeyEnv)
	apiSecret := os.Getenv(cfg.Exchange.APISecretEnv)

	restConfig := common.NewConfigurationRestAPI(
		common.WithBasePath("https://testnet.binance.vision"),
		common.WithTimeout(10*time.Second),
		common.WithRetries(2),
		common.WithApiKey(apiKey),
		common.WithApiSecret(apiSecret),
	)

	client := spot.NewBinanceSpotClient(
		spot.WithRestAPI(restConfig),
	)

	return &BinanceClient{
		client:  client,
		filters: make(map[string]SymbolFilters),
	}, nil
}

func (b *BinanceClient) Ping(ctx context.Context) error {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return fmt.Errorf("el cliente de Binance no está inicializado")
	}

	_, err := b.client.RestApi.GeneralAPI.Ping(ctx).Execute()
	if err != nil {
		return fmt.Errorf("falló la conexión con Binance Spot Testnet: %w", err)
	}

	return nil
}

// GetSpotBalances devuelve un mapa asset -> cantidad total (free + locked).
// Los assets con cantidad 0 se omiten.
func (b *BinanceClient) GetSpotBalances(ctx context.Context) (map[string]float64, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return nil, fmt.Errorf("el cliente de Binance no está inicializado")
	}

	resp, err := b.client.RestApi.AccountAPI.GetAccount(ctx).Execute()
	if err != nil {
		return nil, fmt.Errorf("obtener cuenta de Binance: %w", err)
	}

	if resp == nil {
		return nil, fmt.Errorf("respuesta de cuenta vacía")
	}

	balances := make(map[string]float64)
	for _, entry := range resp.Data.Balances {
		if entry.Asset == nil || entry.Free == nil || entry.Locked == nil {
			continue
		}
		free, err := strconv.ParseFloat(*entry.Free, 64)
		if err != nil {
			return nil, fmt.Errorf("asset %q: free inválido %q: %w", *entry.Asset, *entry.Free, err)
		}
		locked, err := strconv.ParseFloat(*entry.Locked, 64)
		if err != nil {
			return nil, fmt.Errorf("asset %q: locked inválido %q: %w", *entry.Asset, *entry.Locked, err)
		}
		total := free + locked
		if total > 0 {
			balances[*entry.Asset] = total
		}
	}

	return balances, nil
}

func (b *BinanceClient) GetFreeBalance(ctx context.Context, asset string) (float64, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return 0, fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if asset == "" {
		return 0, fmt.Errorf("el asset es obligatorio")
	}
	resp, err := b.client.RestApi.AccountAPI.GetAccount(ctx).Execute()
	if err != nil {
		return 0, fmt.Errorf("obtener balance libre de %s: %w", asset, err)
	}
	if resp == nil {
		return 0, fmt.Errorf("respuesta de cuenta vacía")
	}
	for _, balance := range resp.Data.Balances {
		if balance.GetAsset() != asset {
			continue
		}
		free, err := strconv.ParseFloat(balance.GetFree(), 64)
		if err != nil {
			return 0, fmt.Errorf("balance libre inválido para %s: %w", asset, err)
		}
		return free, nil
	}
	return 0, nil
}

func (b *BinanceClient) GetSymbolBaseAsset(ctx context.Context, symbol string) (string, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return "", fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if symbol == "" {
		return "", fmt.Errorf("el símbolo es obligatorio")
	}
	filters, err := b.filtersFor(ctx, symbol)
	if err != nil {
		return "", fmt.Errorf("obtener asset base de %s: %w", symbol, err)
	}
	if filters.BaseAsset == "" {
		return "", fmt.Errorf("Binance no devolvió el asset base de %s", symbol)
	}
	return filters.BaseAsset, nil
}
