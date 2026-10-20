package domain

import (
	"math"
	"testing"
)

func validStrategyConfig() *StrategyConfig {
	return &StrategyConfig{
		Type: "trend-following",
		Parameters: map[string]float64{
			"fast_period":     10,
			"slow_period":     30,
			"entry_threshold": 1.5,
		},
	}
}

func TestStrategyConfigValidateValidConfig(t *testing.T) {
	config := validStrategyConfig()

	if err := config.Validate(); err != nil {
		t.Fatalf("se esperaba una configuración válida, se obtuvo: %v", err)
	}
}

func TestStrategyConfigValidateNilConfig(t *testing.T) {
	var config *StrategyConfig

	if err := config.Validate(); err == nil {
		t.Fatal("se esperaba un error para una configuración nil")
	}
}

func TestStrategyConfigValidateEmptyType(t *testing.T) {
	config := validStrategyConfig()
	config.Type = "   "

	if err := config.Validate(); err == nil {
		t.Fatal("se esperaba un error por tipo de estrategia vacío")
	}
}

func TestStrategyConfigValidateEmptyParameterName(t *testing.T) {
	config := validStrategyConfig()
	config.Parameters[" "] = 1

	if err := config.Validate(); err == nil {
		t.Fatal("se esperaba un error por nombre de parámetro vacío")
	}
}

func TestStrategyConfigValidateNaNParameter(t *testing.T) {
	config := validStrategyConfig()
	config.Parameters["invalid"] = math.NaN()

	if err := config.Validate(); err == nil {
		t.Fatal("se esperaba un error para un parámetro NaN")
	}
}

func TestStrategyConfigValidateEmptyParameters(t *testing.T) {
	config := &StrategyConfig{
		Type: "trend-following",
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("una configuración sin parámetros debe ser válida: %v", err)
	}
}

func TestStrategyConfigValidatePositiveInfinity(t *testing.T) {
	config := validStrategyConfig()
	config.Parameters["invalid"] = math.Inf(1)

	if err := config.Validate(); err == nil {
		t.Fatal("se esperaba un error para un parámetro infinito positivo")
	}
}

func TestStrategyConfigValidateNegativeInfinity(t *testing.T) {
	config := validStrategyConfig()
	config.Parameters["invalid"] = math.Inf(-1)

	if err := config.Validate(); err == nil {
		t.Fatal("se esperaba un error para un parámetro infinito negativo")
	}
}
