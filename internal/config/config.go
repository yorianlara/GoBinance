package config

import (
	"fmt"
	"math"
	"os"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	App      AppConfig      `yaml:"app"`
	Server   ServerConfig   `yaml:"server"`
	Exchange ExchangeConfig `yaml:"exchange"`
	Database DatabaseConfig `yaml:"database"`
	Risk     RiskConfig     `yaml:"risk"`
}

type AppConfig struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Environment string `yaml:"environment"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type ExchangeConfig struct {
	Name         string `yaml:"name"`
	Market       string `yaml:"market"`
	Testnet      bool   `yaml:"testnet"`
	APIKeyEnv    string `yaml:"api_key_env"`
	APISecretEnv string `yaml:"api_secret_env"`
}

type DatabaseConfig struct {
	URLEnv string `yaml:"url_env"`
}

type RiskConfig struct {
	MaxTotalAllocationPercent       float64 `yaml:"max_total_allocation_percent"`
	MaxDrawdownPercent              float64 `yaml:"max_drawdown_percent"`
	MaxOrderPercent                 float64 `yaml:"max_order_percent"`
	MaxAssetExposurePercent         float64 `yaml:"max_asset_exposure_percent"`
	MaxTotalExposurePercent         float64 `yaml:"max_total_exposure_percent"`
	MaxOpenPositions                int     `yaml:"max_open_positions"`
	MaxDailyLossEUR                 float64 `yaml:"max_daily_loss_eur"`
	MaxTotalLossEUR                 float64 `yaml:"max_total_loss_eur"`
	MaxAllocationPerStrategyPercent float64 `yaml:"max_allocation_per_strategy_percent"`
	MaxOrderAgeMinutes              int     `yaml:"max_order_age_minutes"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("leer configuración %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("interpretar configuración %q: %w", path, err)
	}
	if cfg.Risk.MaxOrderAgeMinutes == 0 {
		cfg.Risk.MaxOrderAgeMinutes = 60
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuración inválida: %w", err)
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.App.Name == "" {
		return fmt.Errorf("app.name es obligatorio")
	}
	if c.App.Version == "" {
		return fmt.Errorf("app.version es obligatorio")
	}
	if c.Server.Host == "" {
		return fmt.Errorf("server.host es obligatorio")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port debe estar entre 1 y 65535")
	}
	if c.Exchange.Name != "binance" {
		return fmt.Errorf("exchange.name debe ser binance")
	}
	if c.Exchange.Market != "spot" {
		return fmt.Errorf("exchange.market debe ser spot")
	}
	if c.Exchange.APIKeyEnv == "" || c.Exchange.APISecretEnv == "" {
		return fmt.Errorf("las variables de entorno de las credenciales son obligatorias")
	}
	if c.Database.URLEnv == "" {
		c.Database.URLEnv = "DATABASE_URL"
	}
	for _, field := range []struct {
		name  string
		value float64
	}{
		{"max_total_allocation_percent", c.Risk.MaxTotalAllocationPercent},
		{"max_drawdown_percent", c.Risk.MaxDrawdownPercent},
		{"max_order_percent", c.Risk.MaxOrderPercent},
		{"max_asset_exposure_percent", c.Risk.MaxAssetExposurePercent},
		{"max_total_exposure_percent", c.Risk.MaxTotalExposurePercent},
		{"max_daily_loss_eur", c.Risk.MaxDailyLossEUR},
		{"max_total_loss_eur", c.Risk.MaxTotalLossEUR},
		{"max_allocation_per_strategy_percent", c.Risk.MaxAllocationPerStrategyPercent},
	} {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return fmt.Errorf("risk.%s debe ser un número finito", field.name)
		}
	}
	if c.Risk.MaxTotalAllocationPercent <= 0 || c.Risk.MaxTotalAllocationPercent > 100 {
		return fmt.Errorf("risk.max_total_allocation_percent debe estar entre 0 y 100")
	}
	if c.Risk.MaxDrawdownPercent <= 0 || c.Risk.MaxDrawdownPercent > 100 {
		return fmt.Errorf("risk.max_drawdown_percent debe estar entre 0 y 100")
	}
	if c.Risk.MaxOrderPercent <= 0 || c.Risk.MaxOrderPercent > 100 {
		return fmt.Errorf("risk.max_order_percent debe estar entre 0 y 100")
	}
	if c.Risk.MaxAssetExposurePercent <= 0 || c.Risk.MaxAssetExposurePercent > 100 {
		return fmt.Errorf("risk.max_asset_exposure_percent debe estar entre 0 y 100")
	}
	if c.Risk.MaxTotalExposurePercent <= 0 || c.Risk.MaxTotalExposurePercent > 100 {
		return fmt.Errorf("risk.max_total_exposure_percent debe estar entre 0 y 100")
	}
	if c.Risk.MaxOpenPositions <= 0 {
		return fmt.Errorf("risk.max_open_positions debe ser mayor que cero")
	}
	if c.Risk.MaxDailyLossEUR <= 0 {
		return fmt.Errorf("risk.max_daily_loss_eur debe ser mayor que cero")
	}
	if c.Risk.MaxTotalLossEUR <= 0 {
		return fmt.Errorf("risk.max_total_loss_eur debe ser mayor que cero")
	}
	if c.Risk.MaxAllocationPerStrategyPercent <= 0 || c.Risk.MaxAllocationPerStrategyPercent > 100 {
		return fmt.Errorf("risk.max_allocation_per_strategy_percent debe estar entre 0 y 100")
	}
	if c.Risk.MaxOrderAgeMinutes <= 0 {
		return fmt.Errorf("risk.max_order_age_minutes debe ser mayor que cero")
	}
	return nil
}
