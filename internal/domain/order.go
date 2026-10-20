package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

// OrderProposal es la petición que un bot envía al RiskManager.
// El bot NUNCA habla con Binance directamente: solo propone.
type OrderProposal struct {
	BotID      string    `json:"bot_id"`
	Symbol     string    `json:"symbol"`
	Side       OrderSide `json:"side"`
	Type       OrderType `json:"type"`
	Quantity   float64   `json:"quantity"`
	LimitPrice float64   `json:"limit_price,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	ProposedAt time.Time `json:"proposed_at"`
}

func (p *OrderProposal) Validate() error {
	if p == nil {
		return fmt.Errorf("la propuesta de orden no puede ser nil")
	}

	if strings.TrimSpace(p.BotID) == "" {
		return fmt.Errorf("el bot_id es obligatorio")
	}

	if strings.TrimSpace(p.Symbol) == "" {
		return fmt.Errorf("el símbolo es obligatorio")
	}

	switch p.Side {
	case OrderSideBuy, OrderSideSell:
	default:
		return fmt.Errorf("lado de orden no válido: %q", p.Side)
	}

	switch p.Type {
	case OrderTypeMarket, OrderTypeLimit:
	default:
		return fmt.Errorf("tipo de orden no válido: %q", p.Type)
	}

	if math.IsNaN(p.Quantity) || math.IsInf(p.Quantity, 0) || p.Quantity <= 0 {
		return fmt.Errorf("la cantidad debe ser mayor que cero")
	}

	if math.IsNaN(p.LimitPrice) || math.IsInf(p.LimitPrice, 0) {
		return fmt.Errorf("el precio límite debe ser finito")
	}
	if p.Type == OrderTypeLimit && p.LimitPrice <= 0 {
		return fmt.Errorf("una orden limit requiere limit_price mayor que cero")
	}

	if p.Type == OrderTypeMarket && p.LimitPrice != 0 {
		return fmt.Errorf("una orden market no debe llevar limit_price")
	}

	if p.ProposedAt.IsZero() {
		return fmt.Errorf("la fecha de propuesta es obligatoria")
	}

	return nil
}
