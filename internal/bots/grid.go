package bots

import (
	"context"
	"fmt"
	"sync"

	"mi-bot/internal/domain"
)

// GridStrategy implementa un Grid simplificado:
//   - Fija un centro en el primer tick.
//   - Si el precio baja N niveles respecto al centro, propone una COMPRA
//     límite en el nivel inferior más cercano.
//   - Si el precio sube N niveles respecto al centro, propone una VENTA
//     límite en el nivel superior más cercano (si tiene inventario).
//   - Solo mantiene UNA orden activa a la vez (hasta Fase F, donde habrá
//     órdenes con estado persistente y se podrá hacer Grid completo).
//   - Si el precio se aleja demasiado del centro, recentra.
//
// Parámetros (StrategyConfig.Parameters, todos float64):
//   - grid_levels:         número de niveles por encima/debajo (obligatorio > 0)
//   - grid_spacing_percent: separación entre niveles en % (obligatorio > 0)
//   - order_size_usdt:     importe por orden (obligatorio > 0)
//   - max_orders:          máximo de órdenes activas simultáneas (opcional, 0 = 1)
type GridStrategy struct {
	levels     int
	spacingPct float64
	sizeUSDT   float64
	maxOrders  int

	mu        sync.Mutex
	center    float64
	hasOrder  bool
	lastSide  domain.OrderSide
	lastLevel int
}

func NewGridStrategy(cfg domain.StrategyConfig) (*GridStrategy, error) {
	if cfg.Type != "grid" {
		return nil, fmt.Errorf("tipo de estrategia inesperado: %q", cfg.Type)
	}

	levels, ok := cfg.Parameters["grid_levels"]
	if !ok || levels <= 0 {
		return nil, fmt.Errorf("grid: grid_levels debe ser > 0")
	}

	spacing, ok := cfg.Parameters["grid_spacing_percent"]
	if !ok || spacing <= 0 {
		return nil, fmt.Errorf("grid: grid_spacing_percent debe ser > 0")
	}

	size, ok := cfg.Parameters["order_size_usdt"]
	if !ok || size <= 0 {
		return nil, fmt.Errorf("grid: order_size_usdt debe ser > 0")
	}

	maxOrders := 1
	if v, ok := cfg.Parameters["max_orders"]; ok {
		if v <= 0 {
			return nil, fmt.Errorf("grid: max_orders debe ser > 0 si se especifica")
		}
		maxOrders = int(v)
	}
	if maxOrders > 1 {
		// Hasta Fase F solo soportamos 1 orden activa; lo forzamos.
		maxOrders = 1
	}

	return &GridStrategy{
		levels:     int(levels),
		spacingPct: spacing,
		sizeUSDT:   size,
		maxOrders:  maxOrders,
	}, nil
}

func (g *GridStrategy) Type() string { return "grid" }

func (g *GridStrategy) Evaluate(ctx context.Context, data MarketData) ([]domain.OrderProposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if data.Price <= 0 {
		return nil, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.center == 0 {
		g.center = data.Price
		return nil, nil
	}

	// Si el precio se aleja más de 2 * levels * spacing del centro, recentramos.
	maxDriftPct := 2 * float64(g.levels) * g.spacingPct
	driftPct := (data.Price - g.center) / g.center * 100
	if driftPct > maxDriftPct || driftPct < -maxDriftPct {
		g.center = data.Price
		g.hasOrder = false
		return nil, nil
	}

	if g.hasOrder {
		return nil, nil
	}

	// Calcular cuántos niveles ha bajado/subido el precio respecto al centro.
	stepPct := (data.Price - g.center) / g.center * 100
	steps := int(stepPct / g.spacingPct)

	var side domain.OrderSide
	var level int
	if steps < 0 {
		side = domain.OrderSideBuy
		level = steps // negativo
	} else if steps > 0 {
		side = domain.OrderSideSell
		level = steps
	} else {
		// Precio en el centro: no hay señal.
		return nil, nil
	}

	// Comprobar que el nivel está dentro de la rejilla.
	if level < -g.levels || level > g.levels {
		return nil, nil
	}

	// Calcular precio límite del nivel de ejecución.
	var limitPrice float64
	if side == domain.OrderSideBuy {
		limitPrice = g.center * (1 + float64(level)*g.spacingPct/100)
	} else {
		limitPrice = g.center * (1 + float64(level)*g.spacingPct/100)
	}
	if limitPrice <= 0 {
		return nil, nil
	}

	qty := g.sizeUSDT / limitPrice
	if qty <= 0 {
		return nil, nil
	}

	g.hasOrder = true
	g.lastSide = side
	g.lastLevel = level

	return []domain.OrderProposal{
		{
			Symbol:     data.Symbol,
			Side:       side,
			Type:       domain.OrderTypeLimit,
			Quantity:   qty,
			LimitPrice: limitPrice,
			Reason:     fmt.Sprintf("grid level %d", level),
		},
	}, nil
}

// OnOrderClosed debe llamarse cuando una orden de Grid se ejecuta o se
// cancela, para permitir la siguiente. En Fase F se integrará con el
// estado real de órdenes; por ahora es un hook.
func (g *GridStrategy) OnOrderClosed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hasOrder = false
}
