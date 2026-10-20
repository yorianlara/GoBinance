//go:build integration

package registry

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	dbpkg "mi-bot/internal/db"
	"mi-bot/internal/domain"
)

func newTestRegistry(t *testing.T) (*PostgresRegistry, *sql.DB) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no configurada")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := dbpkg.Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := dbpkg.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE orders`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	r, err := NewPostgres(db)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	return r, db
}

func sampleProposal(id, symbol string, qty float64) domain.OrderProposal {
	return domain.OrderProposal{
		BotID:      id,
		Symbol:     symbol,
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeMarket,
		Quantity:   qty,
		ProposedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestPostgresReserveAndGet(t *testing.T) {
	r, _ := newTestRegistry(t)

	id, err := r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.1))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if id == "" {
		t.Fatal("clientOrderID vacío")
	}

	o, ok := r.Get(id)
	if !ok {
		t.Fatal("Get: no encontrada")
	}
	if o.State != StateReserved {
		t.Fatalf("state: %v", o.State)
	}
	if o.Proposal.Symbol != "BTCUSDT" {
		t.Fatalf("symbol: %v", o.Proposal.Symbol)
	}
}

func TestPostgresReserveDuplicateActive(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := sampleProposal("bot1", "BTCUSDT", 0.1)

	if _, err := r.Reserve(p); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := r.Reserve(p); err == nil {
		t.Fatal("se esperaba error con duplicado activo")
	}
}

func TestPostgresMarkLifecycle(t *testing.T) {
	r, _ := newTestRegistry(t)
	id, _ := r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.1))

	if err := r.MarkSubmitted(id, 999); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	o, _ := r.Get(id)
	if o.State != StateSubmitted || o.ExchangeID != 999 {
		t.Fatalf("tras submit: %+v", o)
	}

	if err := r.MarkFilled(id, 0.1, 6000); err != nil {
		t.Fatalf("MarkFilled: %v", err)
	}
	o, _ = r.Get(id)
	if o.State != StateFilled || o.FilledQty != 0.1 || o.FilledValue != 6000 {
		t.Fatalf("tras fill: %+v", o)
	}
}

func TestPostgresMarkInvalidTransition(t *testing.T) {
	r, _ := newTestRegistry(t)
	id, _ := r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.1))

	// No se puede fill sin submit.
	if err := r.MarkFilled(id, 0.1, 6000); err == nil {
		t.Fatal("se esperaba error al fill sin submit")
	}
}

func TestPostgresCancelAndFail(t *testing.T) {
	r, _ := newTestRegistry(t)
	id1, _ := r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.1))
	if err := r.MarkCancelled(id1); err != nil {
		t.Fatalf("MarkCancelled: %v", err)
	}

	id2, _ := r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.2))
	_ = r.MarkSubmitted(id2, 1)
	if err := r.MarkFailed(id2); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
}

func TestPostgresPendingExposureIgnoresClosed(t *testing.T) {
	r, _ := newTestRegistry(t)
	id1, _ := r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.1))
	_, _ = r.Reserve(sampleProposal("bot2", "BTCUSDT", 0.2))
	id3, _ := r.Reserve(sampleProposal("bot3", "ETHUSDT", 1))
	_ = r.MarkCancelled(id1)
	_ = r.MarkCancelled(id3)

	got, err := r.PendingExposureUSDT(map[string]float64{"BTCUSDT": 60000, "ETHUSDT": 3000})
	if err != nil {
		t.Fatalf("PendingExposureUSDT: %v", err)
	}
	if got != 12000 {
		t.Fatalf("se esperaba 12000, hay %v", got)
	}
}

func TestPostgresLen(t *testing.T) {
	r, _ := newTestRegistry(t)
	_, _ = r.Reserve(sampleProposal("bot1", "BTCUSDT", 0.1))
	_, _ = r.Reserve(sampleProposal("bot2", "ETHUSDT", 1))

	n, err := r.Len()
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 2 {
		t.Fatalf("se esperaba 2, hay %d", n)
	}
}

func TestPostgresListActiveByBotAndRecent(t *testing.T) {
	r, _ := newTestRegistry(t)
	bot1 := sampleProposal("bot1", "BTCUSDT", 0.1)
	bot2 := sampleProposal("bot2", "ETHUSDT", 1)
	activeID, err := r.Reserve(bot1)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	recentID, err := r.Reserve(bot2)
	if err != nil {
		t.Fatal(err)
	}
	closedID, err := r.Reserve(sampleProposal("bot1", "SOLUSDT", 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkCancelled(closedID); err != nil {
		t.Fatal(err)
	}

	active, err := r.ListActiveByBot("bot1")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ClientOrderID != activeID {
		t.Fatalf("órdenes activas inesperadas: %+v", active)
	}

	recent, err := r.Recent(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].ClientOrderID != closedID {
		t.Fatalf("orden reciente inesperada: %+v (segunda reserva %s)", recent, recentID)
	}
}

func TestPostgresConcurrentReserveSameProposal(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := sampleProposal("bot1", "BTCUSDT", 0.1)

	var wg sync.WaitGroup
	ok := 0
	var mu sync.Mutex
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Reserve(p); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if ok != 1 {
		t.Fatalf("se esperaba 1 reserva exitosa, hay %d", ok)
	}
	n, _ := r.Len()
	if n != 1 {
		t.Fatalf("se esperaba 1 fila, hay %d", n)
	}
}
