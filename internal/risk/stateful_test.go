package risk

import (
	"path/filepath"
	"testing"
	"time"

	"mi-bot/internal/config"
	"mi-bot/internal/domain"
)

func defaultCfg() config.RiskConfig {
	return config.RiskConfig{
		MaxTotalAllocationPercent:       100,
		MaxDrawdownPercent:              10,
		MaxOrderPercent:                 10,
		MaxAssetExposurePercent:         25,
		MaxTotalExposurePercent:         80,
		MaxOpenPositions:                5,
		MaxDailyLossEUR:                 5,
		MaxTotalLossEUR:                 20,
		MaxAllocationPerStrategyPercent: 40,
	}
}

func newStatefulMgr(t *testing.T, initial State) (*StatefulManager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "risk-state.json")
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	if err := store.Save(initial); err != nil {
		t.Fatalf("Save initial: %v", err)
	}
	sm, err := NewStatefulManager(New(), store)
	if err != nil {
		t.Fatalf("NewStatefulManager: %v", err)
	}
	return sm, path
}

func validProposalQty(qty float64) domain.OrderProposal {
	return domain.OrderProposal{
		BotID:      "bot-001",
		Symbol:     "BTCUSDT",
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeMarket,
		Quantity:   qty,
		ProposedAt: time.Now(),
	}
}

func TestStatefulManagerNewNilArgs(t *testing.T) {
	if _, err := NewStatefulManager(nil, &StateStore{}); err == nil {
		t.Fatal("se esperaba error con rm nil")
	}
	if _, err := NewStatefulManager(New(), nil); err == nil {
		t.Fatal("se esperaba error con store nil")
	}
}

func TestStatefulManagerEvaluateEmpty(t *testing.T) {
	sm, _ := newStatefulMgr(t, NewState())
	d := sm.Evaluate(validProposalQty(0.01), &staticConfigProvider{defaultCfg()}, 1000, 100, 0)
	if !d.Approved {
		t.Fatalf("se esperaba aprobada, motivo: %s", d.Reason)
	}
}

func TestStatefulManagerPeakUpdated(t *testing.T) {
	sm, _ := newStatefulMgr(t, NewState())
	sm.Evaluate(validProposalQty(0.01), &staticConfigProvider{defaultCfg()}, 1000, 100, 0)
	sm.Evaluate(validProposalQty(0.01), &staticConfigProvider{defaultCfg()}, 900, 100, 0)
	if sm.State().PeakCapitalUSDT != 1000 {
		t.Fatalf("Peak esperado 1000, hay %v", sm.State().PeakCapitalUSDT)
	}
}

func TestStatefulManagerDrawdown(t *testing.T) {
	sm, _ := newStatefulMgr(t, NewState())
	// Establece pico
	sm.Evaluate(validProposalQty(0.01), &staticConfigProvider{defaultCfg()}, 1000, 100, 0)
	// Capital cae al 90% → drawdown 10% → debe rechazar
	state := sm.State()
	state.PeakCapitalUSDT = 1000
	// Forzamos por segunda llamada con capital 900
	d := sm.Evaluate(validProposalQty(0.01), &staticConfigProvider{defaultCfg()}, 900, 100, 0)
	if d.Approved {
		t.Fatalf("se esperaba rechazo por drawdown 10%%, motivo: %s", d.Reason)
	}
	_ = state
}

func TestStatefulManagerOnOrderFilled(t *testing.T) {
	sm, _ := newStatefulMgr(t, NewState())
	sm.OnOrderFilled("BTCUSDT", 100)

	st := sm.State()
	if st.ExposureBySymbol["BTCUSDT"] != 100 {
		t.Fatalf("exposición símbolo: %v", st.ExposureBySymbol["BTCUSDT"])
	}
	if st.ExposureTotalUSDT != 100 {
		t.Fatalf("exposición total: %v", st.ExposureTotalUSDT)
	}
	if st.OpenPositions != 1 {
		t.Fatalf("posiciones: %v", st.OpenPositions)
	}
}

func TestStatefulManagerOnPositionClosed(t *testing.T) {
	sm, _ := newStatefulMgr(t, NewState())
	sm.OnOrderFilled("BTCUSDT", 100)
	sm.OnPositionClosed("BTCUSDT", 100, -2.5)

	st := sm.State()
	if st.ExposureBySymbol["BTCUSDT"] != 0 {
		t.Fatalf("exposición símbolo: %v", st.ExposureBySymbol["BTCUSDT"])
	}
	if st.ExposureTotalUSDT != 0 {
		t.Fatalf("exposición total: %v", st.ExposureTotalUSDT)
	}
	if st.OpenPositions != 0 {
		t.Fatalf("posiciones: %v", st.OpenPositions)
	}
	if st.DailyPnL != -2.5 {
		t.Fatalf("DailyPnL: %v", st.DailyPnL)
	}
	if st.TotalPnL != -2.5 {
		t.Fatalf("TotalPnL: %v", st.TotalPnL)
	}
}

func TestStatefulManagerResetDaily(t *testing.T) {
	initial := NewState()
	initial.DailyPnL = -1.5
	initial.LastDailyReset = time.Now().Add(-48 * time.Hour)
	sm, _ := newStatefulMgr(t, initial)

	sm.Evaluate(validProposalQty(0.01), &staticConfigProvider{defaultCfg()}, 1000, 100, 0)

	if sm.State().DailyPnL != 0 {
		t.Fatalf("DailyPnL debía resetearse, hay %v", sm.State().DailyPnL)
	}
}

func TestStatefulManagerPersist(t *testing.T) {
	sm, path := newStatefulMgr(t, NewState())
	sm.OnOrderFilled("BTCUSDT", 100)
	if err := sm.Persist(); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	store, _ := NewStateStore(path)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.ExposureTotalUSDT != 100 {
		t.Fatalf("exposición persistida: %v", st.ExposureTotalUSDT)
	}
}

func TestStatefulManagerOnOrderCancelledNoOp(t *testing.T) {
	sm, _ := newStatefulMgr(t, NewState())
	sm.OnOrderFilled("BTCUSDT", 100)
	before := sm.State()
	sm.OnOrderCancelled("BTCUSDT", 50)
	after := sm.State()
	if before.ExposureTotalUSDT != after.ExposureTotalUSDT {
		t.Fatalf("OnOrderCancelled no debe tocar exposición: %v -> %v",
			before.ExposureTotalUSDT, after.ExposureTotalUSDT)
	}
	if before.OpenPositions != after.OpenPositions {
		t.Fatalf("OnOrderCancelled no debe tocar posiciones: %v -> %v",
			before.OpenPositions, after.OpenPositions)
	}
}
