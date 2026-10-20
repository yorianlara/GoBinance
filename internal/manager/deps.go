package manager

import (
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
