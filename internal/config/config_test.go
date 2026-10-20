package config

import "testing"

func TestLoadConfig(t *testing.T) {
	cfg, err := Load("../../config.yaml")
	if err != nil {
		t.Fatalf("no se pudo cargar config.yaml: %v", err)
	}

	if cfg.App.Name != "Multi-Bot Trading" {
		t.Errorf("nombre inesperado: %q", cfg.App.Name)
	}

	if cfg.Exchange.Name != "binance" {
		t.Errorf("exchange inesperado: %q", cfg.Exchange.Name)
	}

	if cfg.Exchange.Market != "spot" {
		t.Errorf("mercado inesperado: %q", cfg.Exchange.Market)
	}

	if !cfg.Exchange.Testnet {
		t.Error("Testnet debe estar activado durante el desarrollo")
	}

	if cfg.Risk.MaxOpenPositions <= 0 {
		t.Errorf("max_open_positions inválido: %v", cfg.Risk.MaxOpenPositions)
	}

	if cfg.Risk.MaxDailyLossEUR <= 0 {
		t.Errorf("max_daily_loss_eur inválido: %v", cfg.Risk.MaxDailyLossEUR)
	}

	if cfg.Risk.MaxTotalLossEUR <= 0 {
		t.Errorf("max_total_loss_eur inválido: %v", cfg.Risk.MaxTotalLossEUR)
	}

	if cfg.Risk.MaxTotalExposurePercent <= 0 || cfg.Risk.MaxTotalExposurePercent > 100 {
		t.Errorf("max_total_exposure_percent inválido: %v", cfg.Risk.MaxTotalExposurePercent)
	}

	if cfg.Risk.MaxAllocationPerStrategyPercent <= 0 || cfg.Risk.MaxAllocationPerStrategyPercent > 100 {
		t.Errorf("max_allocation_per_strategy_percent inválido: %v", cfg.Risk.MaxAllocationPerStrategyPercent)
	}
}
