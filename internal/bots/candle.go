package bots

import "time"

// Candle es una vela OHLCV que las estrategias consumen.
// Es independiente de exchange.Candle para no acoplar bots a exchange.
type Candle struct {
	OpenTime  time.Time
	CloseTime time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}
