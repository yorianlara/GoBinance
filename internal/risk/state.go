package risk

import "time"

// State es el estado acumulado del RiskManager, persistible entre reinicios.
// Refleja la exposición actual, el PnL acumulado y el pico de capital
// (para calcular drawdown). No incluye la configuración de límites
// (eso está en config.RiskConfig).
type State struct {
	ExposureBySymbol  map[string]float64 `json:"exposure_by_symbol"`
	ExposureTotalUSDT float64            `json:"exposure_total_usdt"`
	DailyPnL          float64            `json:"daily_pnl"`
	TotalPnL          float64            `json:"total_pnl"`
	PeakCapitalUSDT   float64            `json:"peak_capital_usdt"`
	OpenPositions     int                `json:"open_positions"`
	LastDailyReset    time.Time          `json:"last_daily_reset"`
}

// NewState devuelve un State vacío con el mapa inicializado.
func NewState() State {
	return State{
		ExposureBySymbol: make(map[string]float64),
	}
}

// clone devuelve una copia profunda del State.
func (s State) clone() State {
	out := State{
		ExposureTotalUSDT: s.ExposureTotalUSDT,
		DailyPnL:          s.DailyPnL,
		TotalPnL:          s.TotalPnL,
		PeakCapitalUSDT:   s.PeakCapitalUSDT,
		OpenPositions:     s.OpenPositions,
		LastDailyReset:    s.LastDailyReset,
		ExposureBySymbol:  make(map[string]float64, len(s.ExposureBySymbol)),
	}
	for k, v := range s.ExposureBySymbol {
		out.ExposureBySymbol[k] = v
	}
	return out
}
