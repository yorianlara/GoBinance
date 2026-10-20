package bots

import (
	"fmt"

	"mi-bot/internal/domain"
)

type Factory func(cfg domain.StrategyConfig) (Strategy, error)

func DefaultFactory(cfg domain.StrategyConfig) (Strategy, error) {
	switch cfg.Type {
	case "noop":
		return NoopStrategy{}, nil
	case "dca":
		return NewDCAStrategy(cfg)
	case "grid":
		return NewGridStrategy(cfg)
	case "ema":
		return NewEMAStrategy(cfg)
	case "rsi":
		return NewRSIStrategy(cfg)
	default:
		return nil, fmt.Errorf("estrategia desconocida: %q", cfg.Type)
	}
}
