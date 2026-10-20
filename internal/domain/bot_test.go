package domain

import (
	"testing"
	"time"
)

func validBot() *Bot {
	now := time.Now()

	return &Bot{
		ID:   "bot-001",
		Name: "BTC Trend",
		Strategy: StrategyConfig{
			Type: "trend-following",
			Parameters: map[string]float64{
				"fast_period": 10,
				"slow_period": 30,
			},
		},
		Symbol:            "BTCUSDT",
		AllocationPercent: 10,
		Status:            BotStatusCreated,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func TestBotValidateValidBot(t *testing.T) {
	bot := validBot()

	if err := bot.Validate(); err != nil {
		t.Fatalf("se esperaba un bot válido, se obtuvo: %v", err)
	}
}

func TestBotValidateNilBot(t *testing.T) {
	var bot *Bot

	if err := bot.Validate(); err == nil {
		t.Fatal("se esperaba un error para un bot nil")
	}
}

func TestBotValidateRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Bot)
	}{
		{"ID vacío", func(b *Bot) { b.ID = "" }},
		{"nombre vacío", func(b *Bot) { b.Name = "" }},
		{"estrategia vacía", func(b *Bot) { b.Strategy.Type = "" }},
		{"símbolo vacío", func(b *Bot) { b.Symbol = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot := validBot()
			tt.mutate(bot)

			if err := bot.Validate(); err == nil {
				t.Fatal("se esperaba un error de validación")
			}
		})
	}
}

func TestBotValidateAllocation(t *testing.T) {
	tests := []struct {
		name       string
		allocation float64
	}{
		{"asignación cero", 0},
		{"asignación negativa", -1},
		{"asignación superior al máximo", 100.01},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot := validBot()
			bot.AllocationPercent = tt.allocation

			if err := bot.Validate(); err == nil {
				t.Fatal("se esperaba un error de asignación")
			}
		})
	}
}

func TestBotValidateInvalidStatus(t *testing.T) {
	bot := validBot()
	bot.Status = "unknown"

	if err := bot.Validate(); err == nil {
		t.Fatal("se esperaba un error por estado no válido")
	}
}

func TestBotValidateInvalidTimestamps(t *testing.T) {
	t.Run("creación sin fecha", func(t *testing.T) {
		bot := validBot()
		bot.CreatedAt = time.Time{}

		if err := bot.Validate(); err == nil {
			t.Fatal("se esperaba un error por fecha de creación")
		}
	})

	t.Run("actualización anterior a creación", func(t *testing.T) {
		bot := validBot()
		bot.UpdatedAt = bot.CreatedAt.Add(-time.Second)

		if err := bot.Validate(); err == nil {
			t.Fatal("se esperaba un error por fechas incoherentes")
		}
	})
}
