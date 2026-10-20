//go:build !integration

package exchange

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
)

type accountFixture struct {
	Balances []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	} `json:"balances"`
}

func parseBalancesFromJSON(t *testing.T, raw string) map[string]float64 {
	t.Helper()

	var fix accountFixture
	if err := json.Unmarshal([]byte(raw), &fix); err != nil {
		t.Fatalf("fixture inválido: %v", err)
	}

	out := make(map[string]float64)
	for _, entry := range fix.Balances {
		free, err := strconv.ParseFloat(entry.Free, 64)
		if err != nil {
			t.Fatalf("free inválido en %q: %v", entry.Asset, err)
		}
		locked, err := strconv.ParseFloat(entry.Locked, 64)
		if err != nil {
			t.Fatalf("locked inválido en %q: %v", entry.Asset, err)
		}
		total := free + locked
		if total > 0 {
			out[entry.Asset] = total
		}
	}
	return out
}

func TestParseBalancesFixture(t *testing.T) {
	raw := `{
		"balances": [
			{"asset": "USDT", "free": "100.00000000", "locked": "0.00000000"},
			{"asset": "BTC",  "free": "0.00100000",  "locked": "0.00050000"},
			{"asset": "ETH",  "free": "0.00000000",  "locked": "0.00000000"},
			{"asset": "BNB",  "free": "0.00000000",  "locked": "0.00000000"}
		]
	}`

	got := parseBalancesFromJSON(t, raw)

	if got["USDT"] != 100.0 {
		t.Errorf("USDT: se esperaba 100, hay %v", got["USDT"])
	}
	if got["BTC"] != 0.0015 {
		t.Errorf("BTC: se esperaba 0.0015, hay %v", got["BTC"])
	}
	if _, ok := got["ETH"]; ok {
		t.Error("ETH con cantidad 0 no debía aparecer")
	}
	if _, ok := got["BNB"]; ok {
		t.Error("BNB con cantidad 0 no debía aparecer")
	}
}

func TestGetSpotBalancesNilClient(t *testing.T) {
	var c *BinanceClient
	if _, err := c.GetSpotBalances(context.Background()); err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}
