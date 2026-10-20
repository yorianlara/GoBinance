package domain

import (
	"fmt"
	"math"
	"strings"
)

type StrategyConfig struct {
	Type       string             `json:"type"`
	Parameters map[string]float64 `json:"parameters,omitempty"`
}

func (s *StrategyConfig) Validate() error {
	if s == nil {
		return fmt.Errorf("la configuración de estrategia no puede ser nil")
	}

	if strings.TrimSpace(s.Type) == "" {
		return fmt.Errorf("el tipo de estrategia es obligatorio")
	}

	for name, value := range s.Parameters {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("el nombre de un parámetro no puede estar vacío")
		}

		if math.IsNaN(value) {
			return fmt.Errorf("el parámetro %q no puede ser NaN", name)
		}

		if math.IsInf(value, 0) {
			return fmt.Errorf("el parámetro %q no puede ser infinito", name)
		}
	}

	return nil
}
