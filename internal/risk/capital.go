package risk

import "context"

// CapitalProvider abstrae la obtención del capital total disponible en USDT.
// El RiskManager no conoce Binance directamente: pide el capital a esta
// interfaz. En producción la implementa BinanceCapitalProvider; en tests,
// FakeCapitalProvider.
type CapitalProvider interface {
	CapitalUSDT(ctx context.Context) (float64, error)
}
