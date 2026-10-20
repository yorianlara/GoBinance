//go:build !integration

package exchange

import (
	"context"
	"fmt"
	"testing"
)

type fakeProvider struct {
	balances map[string]float64
	prices   map[string]float64
	errOn    map[string]error // symbol → error a devolver
}

func (f *fakeProvider) GetSpotBalances(ctx context.Context) (map[string]float64, error) {
	return f.balances, nil
}

func (f *fakeProvider) GetSymbolPrice(ctx context.Context, symbol string) (float64, error) {
	if err, ok := f.errOn[symbol]; ok {
		return 0, err
	}
	p, ok := f.prices[symbol]
	if !ok {
		return 0, fmt.Errorf("sin precio para %q", symbol)
	}
	return p, nil
}

func TestCalculateCapitalUSDT_SoloUSDT(t *testing.T) {
	p := &fakeProvider{balances: map[string]float64{"USDT": 100}}

	total, err := calculateCapitalUSDT(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("calculateCapitalUSDT: %v", err)
	}
	if total != 100 {
		t.Fatalf("se esperaba 100, hay %v", total)
	}
}

func TestCalculateCapitalUSDT_Mezcla(t *testing.T) {
	p := &fakeProvider{
		balances: map[string]float64{"USDT": 50, "BTC": 0.001, "ETH": 0.1},
		prices:   map[string]float64{"BTCUSDT": 60000, "ETHUSDT": 3000},
	}

	total, err := calculateCapitalUSDT(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("calculateCapitalUSDT: %v", err)
	}

	// 50 + 0.001*60000 + 0.1*3000 = 50 + 60 + 300 = 410
	if total != 410 {
		t.Fatalf("se esperaba 410, hay %v", total)
	}
}

func TestCalculateCapitalUSDT_IgnoraAssetSinPar(t *testing.T) {
	p := &fakeProvider{
		balances: map[string]float64{"USDT": 10, "RARO": 5},
		prices:   map[string]float64{},
		errOn:    map[string]error{"RAROUSDT": fmt.Errorf("no existe")},
	}

	total, err := calculateCapitalUSDT(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("calculateCapitalUSDT: %v", err)
	}
	if total != 10 {
		t.Fatalf("se esperaba 10 (solo USDT), hay %v", total)
	}
}

func TestCalculateCapitalUSDT_FiltroSymbols(t *testing.T) {
	p := &fakeProvider{
		balances: map[string]float64{"USDT": 10, "BTC": 0.001, "ETH": 1},
		prices:   map[string]float64{"BTCUSDT": 60000, "ETHUSDT": 3000},
	}

	// Solo BTC: USDT se incluye siempre, ETH se ignora.
	total, err := calculateCapitalUSDT(context.Background(), p, []string{"BTCUSDT"})
	if err != nil {
		t.Fatalf("calculateCapitalUSDT: %v", err)
	}
	// 10 + 0.001*60000 = 70
	if total != 70 {
		t.Fatalf("se esperaba 70, hay %v", total)
	}
}

func TestCalculateCapitalUSDT_IgnoraCantidadCero(t *testing.T) {
	p := &fakeProvider{
		balances: map[string]float64{"USDT": 10, "BTC": 0},
		prices:   map[string]float64{"BTCUSDT": 60000},
	}
	total, err := calculateCapitalUSDT(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("calculateCapitalUSDT: %v", err)
	}
	if total != 10 {
		t.Fatalf("se esperaba 10, hay %v", total)
	}
}
