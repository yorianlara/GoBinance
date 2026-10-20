package registry

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"mi-bot/internal/domain"

	"github.com/google/uuid"
)

// MemoryRegistry implementa OrderRegistry en memoria.
type MemoryRegistry struct {
	mu     sync.Mutex
	orders map[string]*TrackedOrder
	byKey  map[string]string
}

// NewMemory devuelve un MemoryRegistry vacío.
func NewMemory() *MemoryRegistry {
	return &MemoryRegistry{
		orders: make(map[string]*TrackedOrder),
		byKey:  make(map[string]string),
	}
}

func proposalKey(p domain.OrderProposal) string {
	return fmt.Sprintf("%s|%s|%s|%s|%.8f|%.8f",
		p.BotID, p.Symbol, p.Side, p.Type, p.Quantity, p.LimitPrice,
	)
}

func (r *MemoryRegistry) Reserve(p domain.OrderProposal) (string, error) {
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("propuesta inválida: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	k := proposalKey(p)
	if existingID, ok := r.byKey[k]; ok {
		if existing, ok := r.orders[existingID]; ok {
			switch existing.State {
			case StateReserved, StateSubmitted:
				return "", fmt.Errorf("ya existe una orden activa para %s", k)
			}
		}
		delete(r.byKey, k)
	}

	id := uuid.NewString()
	now := time.Now()
	r.orders[id] = &TrackedOrder{
		ClientOrderID: id,
		Proposal:      p,
		State:         StateReserved,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	r.byKey[k] = id
	return id, nil
}

func (r *MemoryRegistry) MarkSubmitted(clientOrderID string, exchangeOrderID int64) error {
	return r.transition(clientOrderID, []OrderState{StateReserved}, StateSubmitted, func(o *TrackedOrder) {
		o.ExchangeID = exchangeOrderID
	})
}

func (r *MemoryRegistry) MarkFilled(clientOrderID string, filledQty, filledValue float64) error {
	if filledQty < 0 || filledValue < 0 {
		return fmt.Errorf("filledQty y filledValue no pueden ser negativos")
	}
	return r.transition(clientOrderID, []OrderState{StateSubmitted}, StateFilled, func(o *TrackedOrder) {
		o.FilledQty = filledQty
		o.FilledValue = filledValue
	})
}

func (r *MemoryRegistry) SetPositionCloseDetails(clientOrderID string, costBasisUSDT float64, positionsClosed int) error {
	if costBasisUSDT < 0 || positionsClosed < 0 {
		return fmt.Errorf("costBasisUSDT y positionsClosed no pueden ser negativos")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	order, ok := r.orders[clientOrderID]
	if !ok {
		return fmt.Errorf("no existe orden %q", clientOrderID)
	}
	if order.State != StateSubmitted {
		return fmt.Errorf("orden %q no está en estado submitted", clientOrderID)
	}
	order.Proposal.PositionCostBasisUSDT = costBasisUSDT
	order.Proposal.PositionsClosed = positionsClosed
	order.UpdatedAt = time.Now()
	return nil
}

func (r *MemoryRegistry) MarkCancelled(clientOrderID string) error {
	return r.transition(clientOrderID, []OrderState{StateReserved, StateSubmitted}, StateCancelled, nil)
}

func (r *MemoryRegistry) MarkFailed(clientOrderID string) error {
	return r.transition(clientOrderID, []OrderState{StateReserved, StateSubmitted}, StateFailed, nil)
}

func (r *MemoryRegistry) transition(clientOrderID string, from []OrderState, to OrderState, mutate func(*TrackedOrder)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	o, ok := r.orders[clientOrderID]
	if !ok {
		return fmt.Errorf("no existe orden %q", clientOrderID)
	}
	allowed := false
	for _, s := range from {
		if o.State == s {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("transición no permitida: %s → %s", o.State, to)
	}
	o.State = to
	o.UpdatedAt = time.Now()
	if mutate != nil {
		mutate(o)
	}
	return nil
}

func (r *MemoryRegistry) Get(clientOrderID string) (TrackedOrder, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[clientOrderID]
	if !ok {
		return TrackedOrder{}, false
	}
	return *o, true
}

func (r *MemoryRegistry) List() ([]TrackedOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TrackedOrder, 0, len(r.orders))
	for _, o := range r.orders {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *MemoryRegistry) ListActiveByBot(botID string) ([]TrackedOrder, error) {
	if botID == "" {
		return nil, fmt.Errorf("botID es obligatorio")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TrackedOrder, 0)
	for _, o := range r.orders {
		if o.Proposal.BotID == botID && (o.State == StateReserved || o.State == StateSubmitted) {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *MemoryRegistry) Recent(limit int) ([]TrackedOrder, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit debe ser mayor que cero")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TrackedOrder, 0, len(r.orders))
	for _, o := range r.orders {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *MemoryRegistry) PendingExposureUSDT(priceBySymbol map[string]float64) (float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0.0
	for _, o := range r.orders {
		if o.State != StateReserved && o.State != StateSubmitted {
			continue
		}
		price := o.Proposal.LimitPrice
		if price <= 0 {
			price = priceBySymbol[o.Proposal.Symbol]
		}
		if price <= 0 {
			slog.Warn("orden pendiente sin precio, se ignora",
				"symbol", o.Proposal.Symbol, "client_order_id", o.ClientOrderID)
			continue
		}
		total += o.Proposal.Quantity * price
	}
	return total, nil
}

func (r *MemoryRegistry) Len() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.orders), nil
}
