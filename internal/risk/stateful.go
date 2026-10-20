package risk

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mi-bot/internal/domain"
)

// StatefulManager envuelve un RiskManager puro y mantiene el State
// acumulado (exposición, PnL, drawdown, posiciones abiertas).
// Es el punto de entrada para los bots: recibe propuestas y decide
// con el estado real, no con un Context externo.
type StatefulManager struct {
	rm    *RiskManager
	store *StateStore

	mu    sync.RWMutex
	state State
}

// NewStatefulManager carga el State desde el store y devuelve el manager listo.
func NewStatefulManager(rm *RiskManager, store *StateStore) (*StatefulManager, error) {
	if rm == nil {
		return nil, fmt.Errorf("el RiskManager no puede ser nil")
	}
	if store == nil {
		return nil, fmt.Errorf("el StateStore no puede ser nil")
	}
	st, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("cargar state: %w", err)
	}
	return &StatefulManager{rm: rm, store: store, state: st}, nil
}

// State devuelve una copia del estado actual.
func (sm *StatefulManager) State() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.clone()
}

// RiskManager expone el evaluador puro (útil en tests).
func (sm *StatefulManager) RiskManager() *RiskManager {
	return sm.rm
}

// Evaluate construye el Context desde el State + argumentos y delega en
// RiskManager.Evaluate. Además, actualiza el PeakCapital y hace reset
// diario si corresponde.
func (sm *StatefulManager) Evaluate(p domain.OrderProposal, provider RiskConfigProvider, capital, price, pending float64) Decision {
	return sm.EvaluateWithAllocations(p, provider, capital, price, pending, 0, 0)
}

func (sm *StatefulManager) EvaluateWithAllocations(
	p domain.OrderProposal,
	provider RiskConfigProvider,
	capital, price, pending, botsAllocatedPct, strategyAllocatedPct float64,
) Decision {
	sm.mu.Lock()
	sm.maybeResetDailyLocked(time.Now())
	if capital > sm.state.PeakCapitalUSDT {
		sm.state.PeakCapitalUSDT = capital
	}
	ctx := sm.buildContextLocked(capital, price, p.Symbol, pending, botsAllocatedPct, strategyAllocatedPct)
	sm.mu.Unlock()

	return sm.rm.Evaluate(p, provider, ctx)
}

func (sm *StatefulManager) buildContextLocked(
	capital, price float64,
	symbol string,
	pending, botsAllocatedPct, strategyAllocatedPct float64,
) Context {
	drawdown := 0.0
	if sm.state.PeakCapitalUSDT > 0 {
		drawdown = (sm.state.PeakCapitalUSDT - capital) / sm.state.PeakCapitalUSDT * 100
	}
	return Context{
		CapitalTotalUSDT:     capital,
		CurrentPrice:         price,
		CurrentExposureUSDT:  sm.state.ExposureTotalUSDT,
		CurrentExposureSym:   sm.state.ExposureBySymbol[symbol],
		CurrentDrawdownPct:   drawdown,
		CurrentDailyPnL:      sm.state.DailyPnL,
		CurrentTotalPnL:      sm.state.TotalPnL,
		OpenPositions:        sm.state.OpenPositions,
		BotsAllocatedPct:     botsAllocatedPct,
		StrategyAllocatedPct: strategyAllocatedPct,
		PendingExposureUSDT:  pending,
	}
}

// maybeResetDailyLocked reinicia DailyPnL si cambia el día UTC.
func (sm *StatefulManager) maybeResetDailyLocked(now time.Time) {
	if sm.state.LastDailyReset.IsZero() {
		sm.state.LastDailyReset = now
		return
	}
	lastDay := sm.state.LastDailyReset.UTC().Truncate(24 * time.Hour)
	nowDay := now.UTC().Truncate(24 * time.Hour)
	if !lastDay.Equal(nowDay) {
		sm.state.DailyPnL = 0
		sm.state.LastDailyReset = now
	}
}

// OnOrderFilled registra una orden ejecutada: suma exposición al símbolo
// y al total, e incrementa OpenPositions.
func (sm *StatefulManager) OnOrderFilled(symbol string, valueUSDT float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.ExposureBySymbol == nil {
		sm.state.ExposureBySymbol = make(map[string]float64)
	}
	sm.state.ExposureBySymbol[symbol] += valueUSDT
	sm.state.ExposureTotalUSDT += valueUSDT
	sm.state.OpenPositions++
}

// OnPositionClosed registra el cierre de una posición: resta exposición y
// suma PnL (diario y total), decrementando OpenPositions.
func (sm *StatefulManager) OnPositionClosed(symbol string, valueUSDT, pnl float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.ExposureBySymbol == nil {
		sm.state.ExposureBySymbol = make(map[string]float64)
	}
	sm.state.ExposureBySymbol[symbol] -= valueUSDT
	if sm.state.ExposureBySymbol[symbol] < 0 {
		sm.state.ExposureBySymbol[symbol] = 0
	}
	sm.state.ExposureTotalUSDT -= valueUSDT
	if sm.state.ExposureTotalUSDT < 0 {
		sm.state.ExposureTotalUSDT = 0
	}
	sm.state.DailyPnL += pnl
	sm.state.TotalPnL += pnl
	if sm.state.OpenPositions > 0 {
		sm.state.OpenPositions--
	}
}

// Persist guarda el State actual en disco.
func (sm *StatefulManager) Persist() error {
	sm.mu.RLock()
	st := sm.state.clone()
	sm.mu.RUnlock()
	return sm.store.Save(st)
}

// OnOrderCancelled registra la cancelación de una orden pendiente.
// No toca ExposureTotalUSDT ni OpenPositions (la orden nunca se ejecutó);
// solo es informativo para logs y auditoría.
func (sm *StatefulManager) OnOrderCancelled(symbol string, valueUSDT float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	slog.Info("orden cancelada", "symbol", symbol, "value_usdt", valueUSDT)
}
