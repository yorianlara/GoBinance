package persistence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mi-bot/internal/domain"
)

func sampleBot(id string) domain.Bot {
	now := time.Now().UTC().Truncate(time.Second)
	return domain.Bot{
		ID:                id,
		Name:              "Bot " + id,
		Strategy:          domain.StrategyConfig{Type: "trend-following", Parameters: map[string]float64{"fast": 10, "slow": 30}},
		Symbol:            "BTCUSDT",
		AllocationPercent: 10,
		Status:            domain.BotStatusCreated,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bots.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, path
}

func TestStoreNewInvalidPath(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("se esperaba error con ruta vacía")
	}
	if _, err := New("/ruta/que/no/existe/bots.json"); err == nil {
		t.Fatal("se esperaba error con directorio inexistente")
	}
}

func TestStoreLoadMissingFile(t *testing.T) {
	s, _ := newStore(t)

	bots, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(bots) != 0 {
		t.Fatalf("se esperaba slice vacío, hay %d", len(bots))
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s, path := newStore(t)

	in := []domain.Bot{sampleBot("b1"), sampleBot("b2")}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !s.Exists() {
		t.Fatal("el archivo debía existir tras Save")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("se esperaban %d bots, hay %d", len(in), len(out))
	}
	for i := range in {
		if out[i].ID != in[i].ID {
			t.Fatalf("ID %d: %q != %q", i, out[i].ID, in[i].ID)
		}
		if out[i].AllocationPercent != in[i].AllocationPercent {
			t.Fatalf("asignación %d: %v != %v", i, out[i].AllocationPercent, in[i].AllocationPercent)
		}
	}
}

func TestStoreSaveEmpty(t *testing.T) {
	s, _ := newStore(t)

	if err := s.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("se esperaba vacío, hay %d", len(out))
	}
}

func TestStoreLoadCorruptJSON(t *testing.T) {
	s, path := newStore(t)
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("se esperaba error con JSON corrupto")
	}
}

func TestStoreLoadMissingVersion(t *testing.T) {
	s, path := newStore(t)
	payload := []byte(`{"bots": []}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("se esperaba error sin campo version")
	}
}

func TestStoreLoadUnknownVersion(t *testing.T) {
	s, path := newStore(t)
	payload := []byte(`{"version": 999, "bots": []}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("se esperaba error con versión desconocida")
	}
}

func TestStoreLoadInvalidBot(t *testing.T) {
	s, path := newStore(t)
	bad := sampleBot("b1")
	bad.Name = ""
	payload := fileFormat{Version: CurrentVersion, Bots: []domain.Bot{bad}}
	data, _ := json.Marshal(payload)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("se esperaba error con bot inválido")
	}
}

func TestStoreLoadDuplicateIDs(t *testing.T) {
	s, path := newStore(t)
	dup := []domain.Bot{sampleBot("b1"), sampleBot("b1")}
	payload := fileFormat{Version: CurrentVersion, Bots: dup}
	data, _ := json.Marshal(payload)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("se esperaba error con IDs duplicados")
	}
}

func TestStoreSaveInvalidBotDoesNotTouchDisk(t *testing.T) {
	s, path := newStore(t)

	// Primer save válido.
	if err := s.Save([]domain.Bot{sampleBot("b1")}); err != nil {
		t.Fatalf("Save inicial: %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Intento de save con bot inválido.
	bad := sampleBot("b2")
	bad.Name = ""
	if err := s.Save([]domain.Bot{bad}); err == nil {
		t.Fatal("se esperaba error con bot inválido")
	}

	// El archivo original debe seguir intacto.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Fatal("el archivo original se corrompió tras un Save fallido")
	}
}

func TestStoreFileHasCanonicalFormat(t *testing.T) {
	s, path := newStore(t)

	if err := s.Save([]domain.Bot{sampleBot("b1")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil {
		t.Fatalf("el archivo no es JSON válido: %v", err)
	}
	if ff.Version != CurrentVersion {
		t.Fatalf("version %d != %d", ff.Version, CurrentVersion)
	}
	if len(ff.Bots) != 1 || ff.Bots[0].ID != "b1" {
		t.Fatalf("contenido inesperado: %+v", ff.Bots)
	}
}
