package exchange

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/binance/binance-connector-go/clients/spot/src/restapi/models"
)

// SymbolFilters contiene las restricciones de un par.
type SymbolFilters struct {
	StepSize       float64 // LOT_SIZE (para limit)
	MarketStepSize float64 // MARKET_LOT_SIZE (para market)
	MinQty         float64
	MinNotional    float64
	TickSize       float64 // PRICE_FILTER (para el precio de órdenes limit)
	BaseAsset      string
}

// stepFor devuelve el stepSize adecuado según el tipo de orden.
func (f SymbolFilters) stepFor(isMarket bool) float64 {
	if isMarket && f.MarketStepSize > 0 {
		return f.MarketStepSize
	}
	return f.StepSize
}

func (b *BinanceClient) filtersFor(ctx context.Context, symbol string) (SymbolFilters, error) {
	b.filtersMu.RLock()
	if f, ok := b.filters[symbol]; ok {
		b.filtersMu.RUnlock()
		return f, nil
	}
	b.filtersMu.RUnlock()

	resp, err := b.client.RestApi.GeneralAPI.ExchangeInfo(ctx).Symbol(symbol).Execute()
	if err != nil {
		return SymbolFilters{}, fmt.Errorf("exchangeInfo %s: %w", symbol, err)
	}
	if resp == nil || len(resp.Data.Symbols) == 0 {
		return SymbolFilters{}, fmt.Errorf("sin exchangeInfo para %s", symbol)
	}

	var f SymbolFilters
	f.BaseAsset = resp.Data.Symbols[0].GetBaseAsset()
	for _, filter := range resp.Data.Symbols[0].Filters {
		switch v := filter.GetActualInstance().(type) {
		case *models.LotSizeFilter:
			f.StepSize = parseFloatSafe(v.GetStepSize())
			f.MinQty = parseFloatSafe(v.GetMinQty())
		case *models.MarketLotSizeFilter:
			f.MarketStepSize = parseFloatSafe(v.GetStepSize())
			if f.MinQty == 0 {
				f.MinQty = parseFloatSafe(v.GetMinQty())
			}
		case *models.PriceFilter:
			f.TickSize = parseFloatSafe(v.GetTickSize())
		case *models.MinNotionalFilter:
			f.MinNotional = parseFloatSafe(v.GetMinNotional())
		case *models.NotionalFilter:
			f.MinNotional = parseFloatSafe(v.GetMinNotional())
		}
	}

	if f.StepSize <= 0 && f.MarketStepSize <= 0 {
		return SymbolFilters{}, fmt.Errorf("stepSize inválido para %s", symbol)
	}

	// Lazy init: clientes construidos a mano (tests o Paths nuevos) pueden
	// llegar aquí sin el mapa inicializado; el panic de escribir en un mapa
	// nil sería un fallo en plena ejecución de órdenes.
	b.filtersMu.Lock()
	if b.filters == nil {
		b.filters = make(map[string]SymbolFilters)
	}
	b.filters[symbol] = f
	b.filtersMu.Unlock()
	return f, nil
}

// truncateToStep trunca value al múltiplo de step más cercano por debajo.
func truncateToStep(value, step float64) float64 {
	if step <= 0 {
		return value
	}
	// Truncar hacia abajo con tolerancia al ruido de float64 en la división
	// (p. ej. 0.34567/0.00001 ≈ 34566.999999999996): un casi-entero desde
	// abajo debe caer en el entero correcto, no en un paso inferior.
	q := value / step
	eps := math.Max(1e-9, math.Abs(q)*1e-12)
	truncated := math.Floor(q+eps) * step

	// Calcular cuántos decimales tiene el stepSize para redondear el ruido
	// Convertimos el step a string para contar los decimales de forma segura.
	stepStr := strconv.FormatFloat(step, 'f', -1, 64)
	decimals := 0
	if idx := strings.Index(stepStr, "."); idx != -1 {
		decimals = len(stepStr) - idx - 1
	}

	// Redondear el resultado truncado a esos decimales para limpiar el ruido
	factor := math.Pow(10, float64(decimals))
	return math.Round(truncated*factor) / factor
}
