package bots

import (
	"context"

	"mi-bot/internal/domain"
)

// NoopStrategy no propone nada. Sirve para arrancar el BotRuntime sin una
// estrategia real, permitiendo verificar el flujo end-to-end.
type NoopStrategy struct{}

func (NoopStrategy) Type() string { return "noop" }

func (NoopStrategy) Evaluate(ctx context.Context, data MarketData) ([]domain.OrderProposal, error) {
	return nil, nil
}
