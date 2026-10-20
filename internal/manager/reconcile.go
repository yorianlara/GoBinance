package manager

import (
	"context"
	"log/slog"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/domain"
	"mi-bot/internal/registry"
)

// reconcile revisa las órdenes submitted del bot y las reconcilia con el exchange.
// Se llama al principio de cada tick, antes de pedir propuestas a la estrategia.
func (r *BotRuntime) reconcile(ctx context.Context, bot domain.Bot) {
	if r.deps.Executor == nil {
		return
	}

	orders, err := r.deps.Registry.List()
	if err != nil {
		slog.Warn("reconcile: list", "bot", bot.ID, "err", err)
		return
	}

	maxAge := time.Duration(r.deps.ConfigStore.Risk().MaxOrderAgeMinutes) * time.Minute

	for _, o := range orders {
		if o.Proposal.BotID != bot.ID {
			continue
		}
		if o.State != registry.StateSubmitted {
			continue
		}

		// Timeout: si la orden lleva demasiado tiempo submitted, liberarla.
		if maxAge > 0 && time.Since(o.CreatedAt) > maxAge {
			slog.Warn("reconcile: orden expirada por timeout",
				"bot", bot.ID, "client_order_id", o.ClientOrderID,
				"age_min", time.Since(o.CreatedAt).Minutes())
			if err := r.deps.Registry.MarkFailed(o.ClientOrderID); err != nil {
				slog.Warn("reconcile: mark failed (timeout)", "bot", bot.ID, "err", err)
				continue
			}
			r.deps.RiskState.OnOrderCancelled(o.Proposal.Symbol, o.Proposal.Quantity)
			r.notifyOrderClosed()
			continue
		}

		r.reconcileOne(ctx, bot, o)
	}
}

func (r *BotRuntime) reconcileOne(ctx context.Context, bot domain.Bot, o registry.TrackedOrder) {
	if o.ExchangeID == 0 {
		slog.Warn("reconcile: orden submitted sin exchangeID", "bot", bot.ID, "client_order_id", o.ClientOrderID)
		return
	}

	ord, err := r.deps.Executor.GetOrder(ctx, o.Proposal.Symbol, o.ExchangeID)
	if err != nil {
		slog.Warn("reconcile: get order", "bot", bot.ID, "client_order_id", o.ClientOrderID, "err", err)
		return
	}

	switch ord.Status {
	case "FILLED":
		fills, err := r.deps.Executor.GetTrades(ctx, o.Proposal.Symbol, o.ExchangeID)
		if err != nil {
			slog.Warn("reconcile: get trades", "bot", bot.ID, "client_order_id", o.ClientOrderID, "err", err)
			return
		}
		qty, value := 0.0, 0.0
		for _, f := range fills {
			qty += f.Quantity
			value += f.Quantity * f.Price
		}
		if err := r.deps.Registry.MarkFilled(o.ClientOrderID, qty, value); err != nil {
			slog.Warn("reconcile: mark filled", "bot", bot.ID, "err", err)
			return
		}
		r.deps.RiskState.OnOrderFilled(o.Proposal.Symbol, value)
		r.notifyOrderClosed()
		slog.Info("reconcile: order filled",
			"bot", bot.ID, "client_order_id", o.ClientOrderID,
			"qty", qty, "value_usdt", value)

	case "CANCELED", "EXPIRED", "REJECTED":
		if err := r.deps.Registry.MarkCancelled(o.ClientOrderID); err != nil {
			slog.Warn("reconcile: mark cancelled", "bot", bot.ID, "err", err)
			return
		}
		r.deps.RiskState.OnOrderCancelled(o.Proposal.Symbol, o.Proposal.Quantity)
		r.notifyOrderClosed()
		slog.Info("reconcile: order cancelled",
			"bot", bot.ID, "client_order_id", o.ClientOrderID, "status", ord.Status)

	case "NEW", "PARTIALLY_FILLED":
		// Seguir esperando.
	}
}

// notifyOrderClosed avisa a la estrategia si implementa OrderLifecycleListener.
func (r *BotRuntime) notifyOrderClosed() {
	if l, ok := r.strategy.(bots.OrderLifecycleListener); ok {
		l.OnOrderClosed()
	}
}
