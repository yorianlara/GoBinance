package risk

import (
	"fmt"

	"mi-bot/internal/config"
	"mi-bot/internal/domain"
)

// RiskConfigProvider abstrae la obtención de la configuración de riesgo.
// *config.Store la satisface.
type RiskConfigProvider interface {
	Risk() config.RiskConfig
}

type Context struct {
	CapitalTotalUSDT     float64
	CurrentPrice         float64
	CurrentExposureUSDT  float64
	CurrentExposureSym   float64
	CurrentDrawdownPct   float64
	CurrentDailyPnL      float64
	CurrentTotalPnL      float64
	OpenPositions        int
	BotsAllocatedPct     float64
	StrategyAllocatedPct float64
	PendingExposureUSDT  float64
}

type Decision struct {
	Approved     bool
	Reason       string
	SuggestedQty float64
}

type RiskManager struct{}

func New() *RiskManager { return &RiskManager{} }

func (rm *RiskManager) Evaluate(p domain.OrderProposal, provider RiskConfigProvider, ctx Context) Decision {
	cfg := provider.Risk()

	if err := p.Validate(); err != nil {
		return reject("propuesta inválida: " + err.Error())
	}
	if ctx.CapitalTotalUSDT <= 0 {
		return reject("capital total no disponible o no positivo")
	}

	priceRef := ctx.CurrentPrice
	if p.Type == domain.OrderTypeLimit {
		priceRef = p.LimitPrice
	}
	if priceRef <= 0 {
		return reject("precio de referencia no positivo")
	}

	if ctx.CurrentDrawdownPct >= cfg.MaxDrawdownPercent {
		return reject("drawdown alcanzado")
	}
	if -ctx.CurrentDailyPnL >= cfg.MaxDailyLossEUR {
		return reject("límite de pérdida diaria alcanzado")
	}
	if -ctx.CurrentTotalPnL >= cfg.MaxTotalLossEUR {
		return reject("límite de pérdida total alcanzado")
	}
	if ctx.OpenPositions >= cfg.MaxOpenPositions {
		return reject("límite de posiciones abiertas alcanzado")
	}
	if ctx.BotsAllocatedPct > cfg.MaxTotalAllocationPercent {
		return reject("asignación global de bots supera el máximo")
	}
	if ctx.StrategyAllocatedPct > cfg.MaxAllocationPerStrategyPercent {
		return reject("asignación de la estrategia supera el máximo")
	}

	valorOrden := p.Quantity * priceRef
	maxPorOrden := ctx.CapitalTotalUSDT * cfg.MaxOrderPercent / 100
	if valorOrden > maxPorOrden {
		return rejectWithQty(
			fmt.Sprintf("tamaño de orden (%.4f USDT) supera el máximo por orden (%.4f USDT)", valorOrden, maxPorOrden),
			maxPorOrden/priceRef,
		)
	}

	delta := valorOrden
	if p.Side == domain.OrderSideSell {
		delta = -valorOrden
	}

	nuevaExpSym := ctx.CurrentExposureSym + delta
	maxExpSym := ctx.CapitalTotalUSDT * cfg.MaxAssetExposurePercent / 100
	if nuevaExpSym > maxExpSym {
		sugerida := (maxExpSym - ctx.CurrentExposureSym) / priceRef
		return rejectWithQty(
			fmt.Sprintf("exposición al símbolo (%.4f USDT) supera el máximo (%.4f USDT)", nuevaExpSym, maxExpSym),
			sugerida,
		)
	}

	nuevaExpTot := ctx.CurrentExposureUSDT + ctx.PendingExposureUSDT + delta
	maxExpTot := ctx.CapitalTotalUSDT * cfg.MaxTotalExposurePercent / 100
	if nuevaExpTot > maxExpTot {
		sugerida := (maxExpTot - ctx.CurrentExposureUSDT - ctx.PendingExposureUSDT) / priceRef
		return rejectWithQty(
			fmt.Sprintf("exposición total (%.4f USDT) supera el máximo (%.4f USDT)", nuevaExpTot, maxExpTot),
			sugerida,
		)
	}

	return Decision{Approved: true, Reason: "aprobada"}
}

func reject(reason string) Decision {
	return Decision{Approved: false, Reason: reason}
}

func rejectWithQty(reason string, qty float64) Decision {
	d := Decision{Approved: false, Reason: reason}
	if qty > 0 {
		d.SuggestedQty = qty
	}
	return d
}
