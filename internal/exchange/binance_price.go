package exchange

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/binance/binance-connector-go/clients/spot/src/restapi/models"
)

// GetSymbolPrice devuelve el precio actual del símbolo contra su divisa de
// cotización (p. ej. BTCUSDT → precio de 1 BTC en USDT).
// Endpoint público: no requiere credenciales.
func (b *BinanceClient) GetSymbolPrice(ctx context.Context, symbol string) (float64, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return 0, fmt.Errorf("el cliente de Binance no está inicializado")
	}

	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "" {
		return 0, fmt.Errorf("el símbolo es obligatorio")
	}

	resp, err := b.client.RestApi.MarketAPI.TickerPrice(ctx).Symbol(sym).Execute()
	if err != nil {
		return 0, fmt.Errorf("obtener precio de %q: %w", sym, err)
	}
	if resp == nil {
		return 0, fmt.Errorf("respuesta de precio vacía para %q", sym)
	}

	instance := resp.Data.GetActualInstance()
	resp1, ok := instance.(*models.TickerPriceResponse1)
	if !ok || resp1 == nil {
		return 0, fmt.Errorf("respuesta de precio no es un objeto simple para %q", sym)
	}

	raw, ok := resp1.GetPriceOk()
	if !ok || raw == nil {
		return 0, fmt.Errorf("precio ausente para %q", sym)
	}

	price, err := strconv.ParseFloat(*raw, 64)
	if err != nil {
		return 0, fmt.Errorf("precio inválido para %q: %q: %w", sym, *raw, err)
	}
	if price <= 0 {
		return 0, fmt.Errorf("precio no positivo para %q: %v", sym, price)
	}

	return price, nil
}
