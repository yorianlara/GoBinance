package risk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewState(t *testing.T) {
	s := NewState()
	if s.ExposureBySymbol == nil {
		t.Fatal("ExposureBySymbol debe estar inicializado")
	}
	if s.ExposureTotalUSDT != 0 || s.DailyPnL != 0 || s.TotalPnL != 0 {
		t.Fatal("NewState debe devolver todo a cero")
	}
}

func TestStateClone(t *testing.T) {
	s := NewState()
	s.ExposureBySymbol["BTCUSDT"] = 100
	s.ExposureTotalUSDT = 100

	c := s.clone()
	c.ExposureBySymbol["BTCUSDT"] = 999

	if s.ExposureBySymbol["BTCUSDT"] != 100 {
		t.Fatal("clone no debe compartir el mapa")
	}
}

func newStateStore(t *testing.T) (*StateStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "risk-state.json")
	s, err := NewStateStore(path)
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	return s, path
}

func TestStateStoreNewInvalidPath(t *testing.T) {
	if _, err := NewStateStore(""); err == nil {
		t.Fatal("se esperaba error con ruta vacía")
	}
	if _, err := NewStateStore("/ruta/que/no/existe/state.json"); err == nil {
		t.Fatal("se esperaba error con directorio inexistente")
	}
}

func TestStateStoreLoadMissing(t *testing.T) {
	s, _ := newStateStore(t)
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.ExposureBySymbol == nil {
		t.Fatal("ExposureBySymbol debe estar inicializado")
	}
	if len(st.ExposureBySymbol) != 0 {
		t.Fatal("se esperaba vacío")
	}
}

func TestStateStoreRoundTrip(t *testing.T) {
	s, path := newStateStore(t)

	in := State{
		ExposureBySymbol:  map[string]float64{"BTCUSDT": 100, "ETHUSDT": 50},
		ExposureTotalUSDT: 150,
		DailyPnL:          -2.5,
		TotalPnL:          -5.0,
		PeakCapitalUSDT:   1000,
		OpenPositions:     2,
		LastDailyReset:    time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ExposureTotalUSDT != in.ExposureTotalUSDT {
		t.Fatalf("ExposureTotalUSDT: %v != %v", out.ExposureTotalUSDT, in.ExposureTotalUSDT)
	}
	if out.DailyPnL != in.DailyPnL {
		t.Fatalf("DailyPnL: %v != %v", out.DailyPnL, in.DailyPnL)
	}
	if out.OpenPositions != in.OpenPositions {
		t.Fatalf("OpenPositions: %v != %v", out.OpenPositions, in.OpenPositions)
	}
	if out.ExposureBySymbol["BTCUSDT"] != 100 {
		t.Fatalf("BTCUSDT: %v != 100", out.ExposureBySymbol["BTCUSDT"])
	}
}

func TestStateStoreLoadCorrupt(t *testing.T) {
	s, path := newStateStore(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("se esperaba error con JSON corrupto")
	}
}

func TestStateStoreSaveNilMap(t *testing.T) {
	s, _ := newStateStore(t)
	// State con mapa nil debe guardarse como {}.
	if err := s.Save(State{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ExposureBySymbol == nil {
		t.Fatal("ExposureBySymbol debe estar inicializado tras Load")
	}
}
