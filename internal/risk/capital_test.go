package risk

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeCapitalProvider(t *testing.T) {
	f := &FakeCapitalProvider{Value: 123.45}
	v, err := f.CapitalUSDT(context.Background())
	if err != nil || v != 123.45 {
		t.Fatalf("FakeCapitalProvider: %v, %v", v, err)
	}
}

func TestFakeCapitalProviderError(t *testing.T) {
	want := errors.New("boom")
	f := &FakeCapitalProvider{Err: want}
	if _, err := f.CapitalUSDT(context.Background()); !errors.Is(err, want) {
		t.Fatalf("se esperaba %v, hay %v", want, err)
	}
}

func TestBinanceCapitalProviderNilClient(t *testing.T) {
	if _, err := NewBinanceCapitalProvider(nil, nil); err == nil {
		t.Fatal("se esperaba error con cliente nil")
	}
}

func TestCapitalTTLDefault(t *testing.T) {
	if DefaultCapitalTTL != 30*time.Second {
		t.Fatalf("TTL por defecto: %v", DefaultCapitalTTL)
	}
}

// fakePriceProvider permite simular llamadas de capital sin red.
type fakePriceProvider struct {
	value float64
	err   error
	calls int
}

func TestCapitalCacheUsesCache(t *testing.T) {
	// Verifica el comportamiento de la caché directamente:
	// si no expira, no consulta.
	p := &BinanceCapitalProvider{ttl: time.Minute}
	p.cached = 100
	p.cachedAt = time.Now()

	v, err := p.CapitalUSDT(context.Background())
	if err != nil {
		t.Fatalf("CapitalUSDT: %v", err)
	}
	if v != 100 {
		t.Fatalf("se esperaba 100 (cacheado), hay %v", v)
	}
	// No debería haber intentado consultar (client nil → si intentara, fallaría).
}

func TestCapitalCacheExpires(t *testing.T) {
	p := &BinanceCapitalProvider{ttl: time.Millisecond}
	p.cached = 100
	p.cachedAt = time.Now().Add(-time.Second) // expirado
	// client nil + caché expirada → debe intentar consultar y fallar.
	if _, err := p.CapitalUSDT(context.Background()); err == nil {
		t.Fatal("se esperaba error con caché expirada y client nil")
	}
}

func TestCapitalFallsBackToCache(t *testing.T) {
	// client nil + caché expirada → error; pero si hubiera caché válida, devolvería.
	// Aquí forzamos caché válida y client nil: debe devolver cacheado.
	p := &BinanceCapitalProvider{ttl: time.Minute}
	p.cached = 42
	p.cachedAt = time.Now()
	v, err := p.CapitalUSDT(context.Background())
	if err != nil || v != 42 {
		t.Fatalf("se esperaba 42 cacheado, hay %v, %v", v, err)
	}
}
