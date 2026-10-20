package domain

import (
	"fmt"
	"strings"
	"time"
)

type BotStatus string

const (
	BotStatusCreated BotStatus = "created"
	BotStatusRunning BotStatus = "running"
	BotStatusPaused  BotStatus = "paused"
	BotStatusStopped BotStatus = "stopped"
	BotStatusError   BotStatus = "error"
)

type Bot struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Strategy          StrategyConfig `json:"strategy"`
	Symbol            string         `json:"symbol"`
	AllocationPercent float64        `json:"allocation_percent"`
	Status            BotStatus      `json:"status"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

func (b *Bot) Validate() error {
	if b == nil {
		return fmt.Errorf("el bot no puede ser nil")
	}

	if strings.TrimSpace(b.ID) == "" {
		return fmt.Errorf("el ID del bot es obligatorio")
	}

	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("el nombre del bot es obligatorio")
	}

	if err := b.Strategy.Validate(); err != nil {
		return fmt.Errorf("configuración de estrategia inválida: %w", err)
	}

	if strings.TrimSpace(b.Symbol) == "" {
		return fmt.Errorf("el símbolo de mercado es obligatorio")
	}

	if b.AllocationPercent <= 0 || b.AllocationPercent > 100 {
		return fmt.Errorf("la asignación debe ser mayor que 0 y menor o igual que 100")
	}

	switch b.Status {
	case BotStatusCreated, BotStatusRunning, BotStatusPaused,
		BotStatusStopped, BotStatusError:
	default:
		return fmt.Errorf("estado del bot no válido: %q", b.Status)
	}

	if b.CreatedAt.IsZero() {
		return fmt.Errorf("la fecha de creación es obligatoria")
	}

	if b.UpdatedAt.IsZero() {
		return fmt.Errorf("la fecha de actualización es obligatoria")
	}

	if b.UpdatedAt.Before(b.CreatedAt) {
		return fmt.Errorf("la fecha de actualización no puede ser anterior a la creación")
	}

	return nil
}
