package manager

import (
	"context"
	"fmt"
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

	orders, err := r.deps.Registry.ListActiveByBot(bot.ID)
	if err != nil {
		slog.Warn("reconcile: list", "bot", bot.ID, "err", err)
		return
	}

	maxAge := time.Duration(r.deps.ConfigStore.Risk().MaxOrderAgeMinutes) * time.Minute

	for _, o := range orders {
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
			r.notifyOrderClosed(false)
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
		if err := r.recordExecution(ctx, bot.ID, o, qty, value); err != nil {
			slog.Warn("reconcile: record filled execution", "bot", bot.ID, "client_order_id", o.ClientOrderID, "err", err)
			return
		}
		slog.Info("reconcile: order filled",
			"bot", bot.ID, "client_order_id", o.ClientOrderID,
			"qty", qty, "value_usdt", value)

	case "CANCELED", "EXPIRED", "REJECTED":
		if ord.ExecutedQty > positionEpsilon {
			fills, err := r.deps.Executor.GetTrades(ctx, o.Proposal.Symbol, o.ExchangeID)
			if err != nil {
				slog.Warn("reconcile: get trades for partially filled cancelled order", "bot", bot.ID, "client_order_id", o.ClientOrderID, "err", err)
				return
			}
			qty, value := 0.0, 0.0
			for _, f := range fills {
				qty += f.Quantity
				value += f.Quantity * f.Price
			}
			if err := r.recordExecution(ctx, bot.ID, o, qty, value); err != nil {
				slog.Warn("reconcile: record partial execution", "bot", bot.ID, "client_order_id", o.ClientOrderID, "err", err)
				return
			}
			slog.Info("reconcile: partially filled order cancelled",
				"bot", bot.ID, "client_order_id", o.ClientOrderID,
				"qty", qty, "value_usdt", value, "status", ord.Status)
			return
		}
		if err := r.deps.Registry.MarkCancelled(o.ClientOrderID); err != nil {
			slog.Warn("reconcile: mark cancelled", "bot", bot.ID, "err", err)
			return
		}
		r.deps.RiskState.OnOrderCancelled(o.Proposal.Symbol, o.Proposal.Quantity)
		r.notifyOrderClosed(false)
		slog.Info("reconcile: order cancelled",
			"bot", bot.ID, "client_order_id", o.ClientOrderID, "status", ord.Status)

	case "NEW", "PARTIALLY_FILLED":
		// Seguir esperando.
	}
}

func (r *BotRuntime) recordExecution(ctx context.Context, botID string, order registry.TrackedOrder, quantity, value float64) error {
	if quantity <= positionEpsilon || value < 0 {
		return fmt.Errorf("ejecución inválida: quantity=%v value=%v", quantity, value)
	}
	if order.Proposal.Side == domain.OrderSideSell {
		orders, err := r.deps.Registry.List()
		if err != nil {
			return fmt.Errorf("listar órdenes para calcular coste base: %w", err)
		}
		inventory, err := inventoryFor(orders, botID, order.Proposal.Symbol)
		if err != nil {
			return err
		}
		costBasis, positionsClosed, err := inventory.sell(quantity)
		if err != nil {
			return err
		}
		if err := r.deps.Registry.SetPositionCloseDetails(order.ClientOrderID, costBasis, positionsClosed); err != nil {
			return err
		}
		if err := r.deps.Registry.MarkFilled(order.ClientOrderID, quantity, value); err != nil {
			return err
		}
		r.deps.RiskState.OnPositionsClosed(
			order.Proposal.Symbol,
			costBasis,
			value-costBasis,
			positionsClosed,
		)
	} else {
		if err := r.deps.Registry.MarkFilled(order.ClientOrderID, quantity, value); err != nil {
			return err
		}
		r.deps.RiskState.OnOrderFilled(order.Proposal.Symbol, value)
	}
	if err := r.deps.RiskState.Persist(); err != nil {
		return fmt.Errorf("persistir estado de riesgo tras ejecución: %w", err)
	}
	if order.Proposal.Side == domain.OrderSideBuy {
		r.notifyOrderClosed(true)
	}
	return nil
}

func (r *BotRuntime) notifyOrderRejected() {
	if l, ok := r.strategy.(bots.OrderLifecycleListener); ok {
		l.OnOrderRejected()
	}
}

// notifyOrderClosed avisa a la estrategia si implementa OrderLifecycleListener.
func (r *BotRuntime) notifyOrderClosed(filled bool) {
	if l, ok := r.strategy.(bots.OrderLifecycleListener); ok {
		l.OnOrderClosed(filled)
	}
}
