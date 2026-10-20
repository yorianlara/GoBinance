package manager

import (
	"log/slog"

	"mi-bot/internal/bots"
	"mi-bot/internal/config"
	"mi-bot/internal/registry"
	"mi-bot/internal/risk"
)

type Deps struct {
	StrategyFactory bots.Factory
	Capital         risk.CapitalProvider
	RiskState       *risk.StatefulManager
	Registry        registry.OrderRegistry
	ConfigStore     *config.Store
	Allocations     AllocationProvider
	Market          MarketProvider
	Klines          KlineProvider
	Executor        OrderExecutor

	// PersistBots, si no es nil, se invoca al cambiar el catálogo de bots o el
	// estado de un bot (create/update/delete/enable/pause/stop) para que el
	// llamador persista el estado (p. ej. bots.json). La persistencia es
	// best-effort: los errores se registran y no interrumpen la operación.
	PersistBots func() error
}

// notifyPersistBots llama a PersistBots si está configurada.
func (d Deps) notifyPersistBots() {
	if d.PersistBots == nil {
		return
	}
	if err := d.PersistBots(); err != nil {
		slog.Error("manager: persistir estado de bots tras un cambio", "err", err)
	}
}

type AllocationProvider interface {
	AllocationPercentages(strategyType string) (total, strategy float64)
}

func (d Deps) validate() error {
	if d.StrategyFactory == nil {
		return errMissing("StrategyFactory")
	}
	if d.Capital == nil {
		return errMissing("Capital")
	}
	if d.RiskState == nil {
		return errMissing("RiskState")
	}
	if d.Registry == nil {
		return errMissing("Registry")
	}
	if d.ConfigStore == nil {
		return errMissing("ConfigStore")
	}
	if d.Market == nil {
		return errMissing("Market")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string {
	return "manager.Deps: falta " + string(e)
}
