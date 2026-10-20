package config

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleConfig() Config {
	return Config{
		App: AppConfig{
			Name:        "Multi-Bot Trading",
			Version:     "0.1.0",
			Environment: "test",
		},
		Server: ServerConfig{Host: "127.0.0.1", Port: 8080},
		Exchange: ExchangeConfig{
			Name:         "binance",
			Market:       "spot",
			Testnet:      true,
			APIKeyEnv:    "BINANCE_API_KEY",
			APISecretEnv: "BINANCE_API_SECRET",
		},
		Database: DatabaseConfig{URLEnv: "DATABASE_URL"},
		Risk: RiskConfig{
			MaxTotalAllocationPercent:       100,
			MaxDrawdownPercent:              10,
			MaxOrderPercent:                 10,
			MaxAssetExposurePercent:         25,
			MaxTotalExposurePercent:         80,
			MaxOpenPositions:                5,
			MaxDailyLossEUR:                 5,
			MaxTotalLossEUR:                 20,
			MaxAllocationPerStrategyPercent: 40,
			MaxOrderAgeMinutes:              60,
		},
	}
}

func TestStoreNewAndGet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeConfigFile(path, sampleConfig()); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := s.Risk().MaxOpenPositions; got != 5 {
		t.Fatalf("MaxOpenPositions: %v", got)
	}
}

func TestStoreUpdateRisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeConfigFile(path, sampleConfig()); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	r := s.Risk()
	r.MaxOpenPositions = 20
	if err := s.UpdateRisk(r); err != nil {
		t.Fatalf("UpdateRisk: %v", err)
	}
	if got := s.Risk().MaxOpenPositions; got != 20 {
		t.Fatalf("en memoria: %v", got)
	}

	// Recargar desde disco.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Risk().MaxOpenPositions; got != 20 {
		t.Fatalf("en disco: %v", got)
	}
}

func TestStoreUpdateRiskInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeConfigFile(path, sampleConfig()); err != nil {
		t.Fatal(err)
	}
	s, _ := NewStore(path)

	r := s.Risk()
	r.MaxOpenPositions = 0 // inválido
	if err := s.UpdateRisk(r); err == nil {
		t.Fatal("se esperaba error con MaxOpenPositions=0")
	}
	// En memoria no debe haber cambiado.
	if got := s.Risk().MaxOpenPositions; got != 5 {
		t.Fatalf("no debía cambiar: %v", got)
	}
}

func TestStorePersistAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeConfigFile(path, sampleConfig()); err != nil {
		t.Fatal(err)
	}
	s, _ := NewStore(path)

	r := s.Risk()
	r.MaxOrderPercent = 50
	if err := s.UpdateRisk(r); err != nil {
		t.Fatalf("UpdateRisk: %v", err)
	}

	// El archivo debe existir y contener el nuevo valor.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), "max_order_percent: 50") {
		t.Fatalf("no contiene el cambio:\n%s", data)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s[:len(sub)] == sub || contains(s[1:], sub)))
}

func writeConfigFile(path string, cfg Config) error {
	data, err := yamlMarshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
