//go:build !integration

package exchange

import (
	"context"
	"testing"
	"time"

	"github.com/binance/binance-connector-go/clients/spot/src/restapi/models"
)

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }

func makeItem(values ...interface{}) models.KlinesItem {
	items := make([]models.KlinesItemInner, 0, len(values))
	for _, v := range values {
		switch x := v.(type) {
		case string:
			items = append(items, models.StringAsKlinesItemInner(strPtr(x)))
		case int64:
			items = append(items, models.Int64AsKlinesItemInner(i64Ptr(x)))
		}
	}
	return models.KlinesItem{Items: items}
}

func TestParseCandleValid(t *testing.T) {
	item := makeItem(
		int64(1729382400000), // 2024-10-20T00:00:00Z
		"60000.00",
		"61000.00",
		"59000.00",
		"60500.00",
		"123.45",
		int64(1729382459999), // +1m -1ms
	)

	c, err := parseCandle(item)
	if err != nil {
		t.Fatalf("parseCandle: %v", err)
	}
	if c.Open != 60000 || c.High != 61000 || c.Low != 59000 || c.Close != 60500 || c.Volume != 123.45 {
		t.Fatalf("valores inesperados: %+v", c)
	}
	if c.OpenTime.UTC().Year() < 2024 {
		t.Fatalf("OpenTime inesperado: %v", c.OpenTime)
	}
}

func TestParseCandleTooFewFields(t *testing.T) {
	item := makeItem(int64(1), "1", "2")
	if _, err := parseCandle(item); err == nil {
		t.Fatal("se esperaba error con pocos campos")
	}
}

func TestParseCandleWrongType(t *testing.T) {
	// open_time como string → error
	item := makeItem("not-int", "1", "2", "3", "4", "5", int64(6))
	if _, err := parseCandle(item); err == nil {
		t.Fatal("se esperaba error de tipo")
	}
}

func TestParseCandleBadFloat(t *testing.T) {
	item := makeItem(int64(1), "abc", "2", "3", "4", "5", int64(6))
	if _, err := parseCandle(item); err == nil {
		t.Fatal("se esperaba error de parseo")
	}
}

func TestGetKlinesNilClient(t *testing.T) {
	var c *BinanceClient
	if _, err := c.GetKlines(context.Background(), "BTCUSDT", "1m", 10); err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestGetKlinesInvalidArgs(t *testing.T) {
	c := &BinanceClient{}
	if _, err := c.GetKlines(context.Background(), "", "1m", 10); err == nil {
		t.Fatal("se esperaba error por símbolo vacío")
	}
	if _, err := c.GetKlines(context.Background(), "BTCUSDT", "1m", 0); err == nil {
		t.Fatal("se esperaba error por limit 0")
	}
	if _, err := c.GetKlines(context.Background(), "BTCUSDT", "1m", 1001); err == nil {
		t.Fatal("se esperaba error por limit 1001")
	}
}

func TestGetKlinesInvalidInterval(t *testing.T) {
	c := &BinanceClient{}
	if _, err := c.GetKlines(context.Background(), "BTCUSDT", "noexiste", 10); err == nil {
		t.Fatal("se esperaba error por intervalo inválido")
	}
}

// Sanity check sobre el wrapper de intervalos.
func TestKlinesIntervalValid(t *testing.T) {
	p, err := models.NewKlinesIntervalParameterFromValue("1m")
	if err != nil || p == nil {
		t.Fatalf("1m debía ser válido: %v", err)
	}
	if !p.IsValid() {
		t.Fatal("1m debía pasar IsValid")
	}
}

// Silencia el uso no importado de time si en el futuro no se usa.
var _ = time.Time{}
