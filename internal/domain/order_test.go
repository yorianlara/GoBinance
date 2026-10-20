package domain

import (
	"math"
	"testing"
	"time"
)

func validProposal() *OrderProposal {
	return &OrderProposal{
		BotID:      "bot-001",
		Symbol:     "BTCUSDT",
		Side:       OrderSideBuy,
		Type:       OrderTypeMarket,
		Quantity:   0.001,
		Reason:     "señal de entrada",
		ProposedAt: time.Now(),
	}
}

func TestOrderProposalValidMarket(t *testing.T) {
	if err := validProposal().Validate(); err != nil {
		t.Fatalf("se esperaba propuesta válida, se obtuvo: %v", err)
	}
}

func TestOrderProposalValidLimit(t *testing.T) {
	p := validProposal()
	p.Type = OrderTypeLimit
	p.LimitPrice = 60000

	if err := p.Validate(); err != nil {
		t.Fatalf("se esperaba propuesta límite válida, se obtuvo: %v", err)
	}
}

func TestOrderProposalNil(t *testing.T) {
	var p *OrderProposal
	if err := p.Validate(); err == nil {
		t.Fatal("se esperaba error para propuesta nil")
	}
}

func TestOrderProposalRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OrderProposal)
	}{
		{"bot_id vacío", func(p *OrderProposal) { p.BotID = "" }},
		{"símbolo vacío", func(p *OrderProposal) { p.Symbol = "" }},
		{"lado inválido", func(p *OrderProposal) { p.Side = "hold" }},
		{"tipo inválido", func(p *OrderProposal) { p.Type = "stop" }},
		{"cantidad cero", func(p *OrderProposal) { p.Quantity = 0 }},
		{"cantidad negativa", func(p *OrderProposal) { p.Quantity = -1 }},
		{"cantidad NaN", func(p *OrderProposal) { p.Quantity = math.NaN() }},
		{"cantidad infinita", func(p *OrderProposal) { p.Quantity = math.Inf(1) }},
		{"limit sin precio", func(p *OrderProposal) { p.Type = OrderTypeLimit }},
		{"precio límite infinito", func(p *OrderProposal) { p.Type = OrderTypeLimit; p.LimitPrice = math.Inf(1) }},
		{"market con precio", func(p *OrderProposal) { p.LimitPrice = 100 }},
		{"sin fecha", func(p *OrderProposal) { p.ProposedAt = time.Time{} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validProposal()
			tt.mutate(p)
			if err := p.Validate(); err == nil {
				t.Fatal("se esperaba error de validación")
			}
		})
	}
}
