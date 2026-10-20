package registry

import (
	"time"

	"mi-bot/internal/domain"
)

// OrderState representa el estado de una orden en el registro.
type OrderState string

const (
	StateReserved  OrderState = "reserved"
	StateSubmitted OrderState = "submitted"
	StateFilled    OrderState = "filled"
	StateCancelled OrderState = "cancelled"
	StateFailed    OrderState = "failed"
)

// TrackedOrder es una orden registrada con su estado.
type TrackedOrder struct {
	ClientOrderID string
	Proposal      domain.OrderProposal
	State         OrderState
	ExchangeID    int64
	FilledQty     float64
	FilledValue   float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OrderRegistry abstrae el registro de órdenes.
// MemoryRegistry e (implementación en memoria) y PostgresRegistry
// (implementación en PostgreSQL) la satisfacen.
type OrderRegistry interface {
	Reserve(p domain.OrderProposal) (string, error)
	MarkSubmitted(clientOrderID string, exchangeOrderID int64) error
	MarkFilled(clientOrderID string, filledQty, filledValue float64) error
	MarkCancelled(clientOrderID string) error
	MarkFailed(clientOrderID string) error
	Get(clientOrderID string) (TrackedOrder, bool)
	List() ([]TrackedOrder, error)
	ListActiveByBot(botID string) ([]TrackedOrder, error)
	Recent(limit int) ([]TrackedOrder, error)
	PendingExposureUSDT(priceBySymbol map[string]float64) (float64, error)
	Len() (int, error)
}
