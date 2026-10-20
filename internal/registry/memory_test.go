package registry

import (
	"testing"
	"time"

	"mi-bot/internal/domain"
)

func proposal(id, symbol string, qty float64) domain.OrderProposal {
	return domain.OrderProposal{
		BotID:      id,
		Symbol:     symbol,
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeMarket,
		Quantity:   qty,
		ProposedAt: time.Now(),
	}
}

func TestMemoryReserveReturnsClientOrderID(t *testing.T) {
	r := NewMemory()
	id, err := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if id == "" {
		t.Fatal("clientOrderID vacío")
	}
	if n, _ := r.Len(); n != 1 {
		t.Fatalf("se esperaba 1, hay %d", n)
	}
}

func TestMemoryReserveDuplicateActive(t *testing.T) {
	r := NewMemory()
	p := proposal("bot1", "BTCUSDT", 0.1)
	_, _ = r.Reserve(p)
	if _, err := r.Reserve(p); err == nil {
		t.Fatal("se esperaba error con duplicado activo")
	}
}

func TestMemoryReserveAfterClosedAllowsNew(t *testing.T) {
	r := NewMemory()
	p := proposal("bot1", "BTCUSDT", 0.1)
	id, _ := r.Reserve(p)
	_ = r.MarkCancelled(id)
	if _, err := r.Reserve(p); err != nil {
		t.Fatalf("tras cerrar la anterior debe permitirse: %v", err)
	}
}

func TestMemoryMarkSubmitted(t *testing.T) {
	r := NewMemory()
	id, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	if err := r.MarkSubmitted(id, 12345); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	o, _ := r.Get(id)
	if o.State != StateSubmitted || o.ExchangeID != 12345 {
		t.Fatalf("orden inesperada: %+v", o)
	}
}

func TestMemoryMarkSubmittedFromWrongState(t *testing.T) {
	r := NewMemory()
	id, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	_ = r.MarkSubmitted(id, 1)
	if err := r.MarkSubmitted(id, 2); err == nil {
		t.Fatal("se esperaba error al resubmit")
	}
}

func TestMemoryMarkFilled(t *testing.T) {
	r := NewMemory()
	id, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	_ = r.MarkSubmitted(id, 1)
	if err := r.MarkFilled(id, 0.1, 6000); err != nil {
		t.Fatalf("MarkFilled: %v", err)
	}
	o, _ := r.Get(id)
	if o.State != StateFilled || o.FilledQty != 0.1 || o.FilledValue != 6000 {
		t.Fatalf("orden inesperada: %+v", o)
	}
}

func TestMemoryMarkFilledFromReservedFails(t *testing.T) {
	r := NewMemory()
	id, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	if err := r.MarkFilled(id, 0.1, 6000); err == nil {
		t.Fatal("se esperaba error al fill sin submit")
	}
}

func TestMemoryMarkCancelledAndFailed(t *testing.T) {
	r := NewMemory()
	id1, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	if err := r.MarkCancelled(id1); err != nil {
		t.Fatalf("MarkCancelled desde reserved: %v", err)
	}
	id2, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.2))
	_ = r.MarkSubmitted(id2, 1)
	if err := r.MarkFailed(id2); err != nil {
		t.Fatalf("MarkFailed desde submitted: %v", err)
	}
}

func TestMemoryPendingExposureIgnoresClosed(t *testing.T) {
	r := NewMemory()
	id1, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))
	_, _ = r.Reserve(proposal("bot2", "BTCUSDT", 0.2))
	id3, _ := r.Reserve(proposal("bot3", "ETHUSDT", 1))
	_ = r.MarkCancelled(id1)
	_ = r.MarkCancelled(id3)

	got, _ := r.PendingExposureUSDT(map[string]float64{"BTCUSDT": 60000, "ETHUSDT": 3000})
	if got != 12000 {
		t.Fatalf("se esperaba 12000, hay %v", got)
	}
}

func TestMemoryPendingExposureUsesLimitPrice(t *testing.T) {
	r := NewMemory()
	p := proposal("bot1", "BTCUSDT", 1)
	p.Type = domain.OrderTypeLimit
	p.LimitPrice = 50000
	_, _ = r.Reserve(p)
	got, _ := r.PendingExposureUSDT(map[string]float64{"BTCUSDT": 99999})
	if got != 50000 {
		t.Fatalf("se esperaba 50000, hay %v", got)
	}
}

func TestMemoryGetAndList(t *testing.T) {
	r := NewMemory()
	id, _ := r.Reserve(proposal("bot1", "BTCUSDT", 0.1))

	o, ok := r.Get(id)
	if !ok || o.ClientOrderID != id {
		t.Fatalf("Get(%s) = %+v, %v", id, o, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get(nope) debía devolver false")
	}
	list, _ := r.List()
	if len(list) != 1 {
		t.Fatalf("List: %d", len(list))
	}
}
