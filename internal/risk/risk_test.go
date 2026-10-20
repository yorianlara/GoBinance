package risk

import (
	"strings"
	"testing"

	"mi-bot/internal/config"
	"time"

	"mi-bot/internal/domain"
)

// staticConfigProvider devuelve siempre el mismo RiskConfig.
type staticConfigProvider struct {
	cfg config.RiskConfig
}

func (p *staticConfigProvider) Risk() config.RiskConfig { return p.cfg }

func happyCtx() Context {
	return Context{
		CapitalTotalUSDT:     1000,
		CurrentPrice:         100,
		CurrentExposureUSDT:  0,
		CurrentExposureSym:   0,
		CurrentDrawdownPct:   0,
		CurrentDailyPnL:      0,
		CurrentTotalPnL:      0,
		OpenPositions:        0,
		BotsAllocatedPct:     50,
		StrategyAllocatedPct: 10,
		PendingExposureUSDT:  0,
	}
}

func validProposal() domain.OrderProposal {
	return domain.OrderProposal{
		BotID:      "bot-001",
		Symbol:     "BTCUSDT",
		Side:       domain.OrderSideBuy,
		Type:       domain.OrderTypeMarket,
		Quantity:   0.5,
		ProposedAt: time.Now(),
	}
}

func TestEvaluateApproved(t *testing.T) {
	rm := New()
	if d := rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, happyCtx()); !d.Approved {
		t.Fatalf("se esperaba aprobada, motivo: %s", d.Reason)
	}
}

func TestEvaluateInvalidProposal(t *testing.T) {
	rm := New()
	p := validProposal()
	p.Symbol = ""
	d := rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, happyCtx())
	if d.Approved {
		t.Fatal("se esperaba rechazo por propuesta inválida")
	}
	if !strings.Contains(d.Reason, "propuesta inválida") {
		t.Fatalf("motivo inesperado: %s", d.Reason)
	}
}

func TestEvaluateCapitalCero(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CapitalTotalUSDT = 0
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluatePrecioCero(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentPrice = 0
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateDrawdownExacto(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentDrawdownPct = 10
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateDrawdownJustoDebajo(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentDrawdownPct = 9.99
	if d := rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx); !d.Approved {
		t.Fatalf("motivo: %s", d.Reason)
	}
}

func TestEvaluatePerdidaDiariaExacta(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentDailyPnL = -5
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluatePerdidaTotalExacta(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentTotalPnL = -20
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluatePosicionesLlenas(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.OpenPositions = 5
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateAsignacionGlobalExcedida(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.BotsAllocatedPct = 101
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateAsignacionEstrategiaExcedida(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.StrategyAllocatedPct = 41
	if rm.Evaluate(validProposal(), &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateTamanoOrdenExcedido(t *testing.T) {
	rm := New()
	p := validProposal()
	p.Quantity = 2
	d := rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, happyCtx())
	if d.Approved {
		t.Fatal("se esperaba rechazo")
	}
	if d.SuggestedQty != 1 {
		t.Fatalf("sugerencia esperada 1, hay %v", d.SuggestedQty)
	}
}

func TestEvaluateExposicionSimboloBuy(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentExposureSym = 200
	p := validProposal()
	p.Quantity = 1
	if rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateExposicionSimboloSellReduce(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentExposureSym = 200
	p := validProposal()
	p.Side = domain.OrderSideSell
	p.Quantity = 1
	if d := rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, ctx); !d.Approved {
		t.Fatalf("motivo: %s", d.Reason)
	}
}

func TestEvaluateExposicionTotalExcedida(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.CurrentExposureUSDT = 750
	p := validProposal()
	p.Quantity = 1
	if rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}

func TestEvaluateLimiteExactoAprobado(t *testing.T) {
	rm := New()
	p := validProposal()
	p.Quantity = 1
	if d := rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, happyCtx()); !d.Approved {
		t.Fatalf("motivo: %s", d.Reason)
	}
}

func TestEvaluatePendingExposureReduce(t *testing.T) {
	rm := New()
	ctx := happyCtx()
	ctx.PendingExposureUSDT = 750
	p := validProposal()
	p.Quantity = 1
	if rm.Evaluate(p, &staticConfigProvider{defaultCfg()}, ctx).Approved {
		t.Fatal("se esperaba rechazo")
	}
}
