package risk

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mi-bot/internal/exchange"
)

// BinanceCapitalProvider obtiene el capital real consultando Binance
// con caché de TTL para evitar llamadas excesivas.
type BinanceCapitalProvider struct {
	client  *exchange.BinanceClient
	symbols []string

	mu       sync.RWMutex
	cached   float64
	cachedAt time.Time
	ttl      time.Duration
}

// DefaultCapitalTTL es el tiempo por defecto de caché del capital.
const DefaultCapitalTTL = 30 * time.Second

func NewBinanceCapitalProvider(c *exchange.BinanceClient, symbols []string) (*BinanceCapitalProvider, error) {
	if c == nil {
		return nil, fmt.Errorf("el cliente de Binance no puede ser nil")
	}
	return &BinanceCapitalProvider{
		client:  c,
		symbols: symbols,
		ttl:     DefaultCapitalTTL,
	}, nil
}

// SetSymbols actualiza la lista de símbolos a valorar.
func (p *BinanceCapitalProvider) SetSymbols(symbols []string) {
	p.symbols = symbols
}

// SetTTL ajusta el tiempo de caché (útil en tests).
func (p *BinanceCapitalProvider) SetTTL(ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ttl = ttl
}

func (p *BinanceCapitalProvider) CapitalUSDT(ctx context.Context) (float64, error) {
	if p == nil {
		return 0, fmt.Errorf("el proveedor de capital no está inicializado")
	}

	p.mu.RLock()
	if p.cached > 0 && time.Since(p.cachedAt) < p.ttl {
		v := p.cached
		p.mu.RUnlock()
		return v, nil
	}
	p.mu.RUnlock()

	if p.client == nil {
		return 0, fmt.Errorf("el cliente de Binance no está inicializado")
	}

	v, err := p.client.GetCapitalUSDT(ctx, p.symbols)
	if err != nil {
		// Si falla pero hay cacheado, devolver el último valor conocido.
		p.mu.RLock()
		cached := p.cached
		p.mu.RUnlock()
		if cached > 0 {
			slog.Warn("capital: fallo al consultar Binance, se usa valor cacheado", "err", err, "cached", cached)
			return cached, nil
		}
		return 0, err
	}

	p.mu.Lock()
	p.cached = v
	p.cachedAt = time.Now()
	p.mu.Unlock()
	return v, nil
}
