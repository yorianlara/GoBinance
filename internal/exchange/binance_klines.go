package exchange

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/binance/binance-connector-go/clients/spot/src/restapi/models"
)

// Candle es una vela OHLCV de Binance.
type Candle struct {
	OpenTime  time.Time
	CloseTime time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// GetKlines devuelve hasta `limit` velas del símbolo e intervalo indicados.
// El intervalo debe ser uno de los admitidos por Binance ("1m", "5m", "1h",
// "1d", etc.) y `limit` debe estar entre 1 y 1000.
// Endpoint público: no requiere credenciales.
func (b *BinanceClient) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	if b == nil || b.client == nil || b.client.RestApi == nil {
		return nil, fmt.Errorf("el cliente de Binance no está inicializado")
	}
	if symbol == "" {
		return nil, fmt.Errorf("el símbolo es obligatorio")
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("limit debe estar entre 1 y 1000 (recibido %d)", limit)
	}

	intervalParam, err := models.NewKlinesIntervalParameterFromValue(interval)
	if err != nil || intervalParam == nil || !intervalParam.IsValid() {
		return nil, fmt.Errorf("intervalo no válido: %q", interval)
	}

	resp, err := b.client.RestApi.MarketAPI.
		Klines(ctx).
		Symbol(symbol).
		Interval(*intervalParam).
		Limit(int32(limit)).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("obtener klines de %q: %w", symbol, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("respuesta de klines vacía para %q", symbol)
	}

	candles := make([]Candle, 0, len(resp.Data.Items))
	for i, item := range resp.Data.Items {
		c, err := parseCandle(item)
		if err != nil {
			return nil, fmt.Errorf("vela %d de %q: %w", i, symbol, err)
		}
		candles = append(candles, c)
	}

	return candles, nil
}

// parseCandle convierte un KlinesItem (array heterogéneo) en Candle.
func parseCandle(item models.KlinesItem) (Candle, error) {
	if len(item.Items) < 7 {
		return Candle{}, fmt.Errorf("se esperaban al menos 7 campos, hay %d", len(item.Items))
	}

	openTimeMs, err := innerInt64(item.Items[0])
	if err != nil {
		return Candle{}, fmt.Errorf("open_time: %w", err)
	}
	open, err := innerFloat(item.Items[1])
	if err != nil {
		return Candle{}, fmt.Errorf("open: %w", err)
	}
	high, err := innerFloat(item.Items[2])
	if err != nil {
		return Candle{}, fmt.Errorf("high: %w", err)
	}
	low, err := innerFloat(item.Items[3])
	if err != nil {
		return Candle{}, fmt.Errorf("low: %w", err)
	}
	closePrice, err := innerFloat(item.Items[4])
	if err != nil {
		return Candle{}, fmt.Errorf("close: %w", err)
	}
	volume, err := innerFloat(item.Items[5])
	if err != nil {
		return Candle{}, fmt.Errorf("volume: %w", err)
	}
	closeTimeMs, err := innerInt64(item.Items[6])
	if err != nil {
		return Candle{}, fmt.Errorf("close_time: %w", err)
	}

	return Candle{
		OpenTime:  time.UnixMilli(openTimeMs).UTC(),
		CloseTime: time.UnixMilli(closeTimeMs).UTC(),
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
	}, nil
}

func innerInt64(v models.KlinesItemInner) (int64, error) {
	inst := v.GetActualInstance()
	switch x := inst.(type) {
	case *int64:
		if x == nil {
			return 0, fmt.Errorf("valor nil")
		}
		return *x, nil
	default:
		return 0, fmt.Errorf("tipo inesperado: %T", inst)
	}
}

func innerFloat(v models.KlinesItemInner) (float64, error) {
	inst := v.GetActualInstance()
	switch x := inst.(type) {
	case *string:
		if x == nil {
			return 0, fmt.Errorf("valor nil")
		}
		f, err := strconv.ParseFloat(*x, 64)
		if err != nil {
			return 0, fmt.Errorf("parsear %q: %w", *x, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("tipo inesperado: %T", inst)
	}
}
