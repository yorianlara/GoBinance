package exchange

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// DefaultSymbols es la lista de pares populares que se valoran cuando
// GetCapitalUSDT recibe symbols vacío/nil.
var DefaultSymbols = []string{
	"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
	"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "LINKUSDT",
}

type priceProvider interface {
	GetSymbolPrice(ctx context.Context, symbol string) (float64, error)
	GetSpotBalances(ctx context.Context) (map[string]float64, error)
}

// GetCapitalUSDT calcula el capital total valorado en USDT.
//
// symbols es opcional:
//   - si viene vacío/nil, se usan los pares populares de DefaultSymbols.
//   - si viene con valores, solo se valoran los assets base de esos símbolos.
//
// USDT se cuenta siempre directamente, sin conversión.
func (b *BinanceClient) GetCapitalUSDT(ctx context.Context, symbols []string) (float64, error) {
	return calculateCapitalUSDT(ctx, b, symbols)
}

func calculateCapitalUSDT(ctx context.Context, p priceProvider, symbols []string) (float64, error) {
	balances, err := p.GetSpotBalances(ctx)
	if err != nil {
		return 0, fmt.Errorf("obtener balances: %w", err)
	}

	if len(symbols) == 0 {
		symbols = DefaultSymbols
	}
	allowed := buildAssetFilter(symbols)

	total := 0.0

	assets := make([]string, 0, len(balances))
	for asset := range balances {
		assets = append(assets, asset)
	}
	sort.Strings(assets)

	for _, asset := range assets {
		qty := balances[asset]
		if qty <= 0 {
			continue
		}
		if _, ok := allowed[asset]; !ok {
			continue
		}

		if asset == "USDT" {
			total += qty
			continue
		}

		pair := asset + "USDT"
		price, err := p.GetSymbolPrice(ctx, pair)
		if err != nil {
			slog.Warn("asset sin par USDT, se omite del capital",
				"asset", asset, "pair", pair, "error", err)
			continue
		}

		total += qty * price
	}

	return total, nil
}

// buildAssetFilter devuelve un conjunto de assets base derivados de los
// símbolos (BTCUSDT → BTC), incluyendo siempre USDT.
func buildAssetFilter(symbols []string) map[string]struct{} {
	filter := make(map[string]struct{}, len(symbols)+1)
	filter["USDT"] = struct{}{}

	for _, s := range symbols {
		sym := strings.ToUpper(strings.TrimSpace(s))
		if sym == "" {
			continue
		}
		if strings.HasSuffix(sym, "USDT") && len(sym) > len("USDT") {
			base := strings.TrimSuffix(sym, "USDT")
			filter[base] = struct{}{}
		}
	}

	return filter
}
