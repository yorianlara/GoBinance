package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/registry"
)

// reconcile revisa las órdenes activas del bot y las reconcilia con el exchange.
// Se llama al principio de cada tick, antes de pedir nuevas propuestas a la estrategia.
//
// Reglas de seguridad:
//   - submitted: si supera el timeout se cancela/examina en el exchange; la
//     reserva solo se libera cuando el estado terminal en Binance es conocido
//     (podría haberse rellenado justo antes del timeout).
//   - reserved: desconocemos si el exchange llegó a crear la orden; en cada
//     tick se pregunta por clientOrderID y solo se libera cuando el exchange
//     confirma que no existe (o se reclama y contabiliza si existe).
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
		switch o.State {
		case registry.StateReserved:
			// Reserva huérfana: se reintenta en cada tick en lugar de esperar
			// a max_order_age_minutes. Con el timeout desactivado (0) la
			// reserva quedaría en limbo para siempre, y mientras esté activa
			// cuenta en la exposición pendiente y bloquea nuevas compras.
			r.resolveUncertainOutcome(ctx, o)
		case registry.StateSubmitted:
			if maxAge > 0 && time.Since(o.CreatedAt) > maxAge {
				ord, ready := r.cancelExpiredSubmittedOrder(ctx, o)
				if !ready {
					continue // se reintenta en el próximo tick
				}
				r.processOrderStatus(ctx, bot.ID, o, ord)
				continue
			}
			r.reconcileOne(ctx, bot, o)
		default:
			continue
		}
	}
}

// fetchExchangeOrder consulta el estado actual de una orden en el exchange.
func (r *BotRuntime) fetchExchangeOrder(ctx context.Context, o registry.TrackedOrder) (exchange.OrderResponse, bool) {
	if o.ExchangeID <= 0 {
		slog.Warn("reconcile: orden submitted sin exchangeID",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID)
		return exchange.OrderResponse{}, false
	}

	ord, err := r.deps.Executor.GetOrder(ctx, o.Proposal.Symbol, o.ExchangeID)
	if err != nil {
		slog.Warn("reconcile: get order",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "err", err)
		return exchange.OrderResponse{}, false
	}
	return ord, true
}

// reconcileOne consulta una orden submitted en el exchange y procesa su estado.
func (r *BotRuntime) reconcileOne(ctx context.Context, bot domain.Bot, o registry.TrackedOrder) {
	ord, ok := r.fetchExchangeOrder(ctx, o)
	if !ok {
		return
	}
	r.processOrderStatus(ctx, bot.ID, o, ord)
}

// cancelExpiredSubmittedOrder gestiona una orden submitted que superó el
// timeout de max_order_age_minutes: intenta cancelarla en el exchange. Solo
// devuelve un estado (FILLED/CANCELED/...) si el exchange confirma que ya es
// terminal: la orden pudo, por ejemplo, haberse rellenado justo antes del
// timeout. Devuelve false cuando el resultado sigue siendo desconocido.
func (r *BotRuntime) cancelExpiredSubmittedOrder(ctx context.Context, o registry.TrackedOrder) (exchange.OrderResponse, bool) {
	ord, ok := r.fetchExchangeOrder(ctx, o)
	if !ok {
		return exchange.OrderResponse{}, false
	}

	slog.Warn("reconcile: orden expirada por timeout",
		"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID,
		"age_min", time.Since(o.CreatedAt).Minutes(), "status", ord.Status)

	if isTerminalOrderStatus(ord.Status) {
		// La orden ya terminó por sí sola (quizá se rellenó antes del timeout).
		return ord, true
	}

	if cancelErr := r.deps.Executor.CancelOrder(ctx, o.Proposal.Symbol, ord.OrderID); cancelErr != nil {
		slog.Warn("reconcile: no se pudo cancelar la orden expirada",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "err", cancelErr)
	}
	final, err := r.deps.Executor.GetOrder(ctx, o.Proposal.Symbol, ord.OrderID)
	if err != nil {
		slog.Warn("reconcile: consultar estado final tras cancelar",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "err", err)
		return exchange.OrderResponse{}, false
	}
	if !isTerminalOrderStatus(final.Status) {
		slog.Warn("reconcile: la orden sigue viva tras el intento de cancelación",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "status", final.Status)
		return exchange.OrderResponse{}, false
	}
	return final, true
}

// resolveUncertainOutcome pregunta al exchange qué fue de una orden cuyo
// resultado de envío se desconoce (reserva huérfana). Si la orden existe se
// registra como submitted y se procesa su estado; si el exchange confirma
// que no existe se libera la reserva; si sigue sin poder confirmarse, la
// reserva se conserva y se volverá a intentar más adelante.
func (r *BotRuntime) resolveUncertainOutcome(ctx context.Context, o registry.TrackedOrder) {
	ord, err := r.deps.Executor.GetOrderByClientID(ctx, o.Proposal.Symbol, o.ClientOrderID)
	switch {
	case errors.Is(err, exchange.ErrOrderNotFound):
		// Confirmado: el exchange nunca creó la orden. Liberar la reserva
		// permite que la estrategia proponga de nuevo en el próximo tick.
		slog.Warn("reconcile: reserva huérfana liberada; el exchange no tiene la orden",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID)
		if errMark := r.deps.Registry.MarkFailed(o.ClientOrderID); errMark != nil {
			slog.Warn("reconcile: liberar reserva huérfana",
				"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "err", errMark)
			return
		}
		r.deps.RiskState.OnOrderCancelled(o.Proposal.Symbol, o.Proposal.Quantity)
		r.notifyOrderClosed(false)

	case err != nil:
		slog.Warn("reconcile: no se puede confirmar la reserva huérfana; se conserva",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "err", err)

	default:
		r.claimExistingOrder(ctx, o, ord)
	}
}

// claimExistingOrder registra en el registro una orden que sí existe en el
// exchange (el resultado de su envío se perdió) y procesa su estado, para que
// fills, exposición y PnL queden contabilizados como cualquier otra orden.
func (r *BotRuntime) claimExistingOrder(ctx context.Context, o registry.TrackedOrder, ord exchange.OrderResponse) {
	slog.Info("reconcile: orden localizada en el exchange por clientOrderID",
		"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID,
		"exchange_order_id", ord.OrderID, "status", ord.Status)

	if ord.OrderID <= 0 {
		slog.Warn("reconcile: orden existente sin ID válido; se conserva la reserva",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID)
		return
	}
	if errMark := r.deps.Registry.MarkSubmitted(o.ClientOrderID, ord.OrderID); errMark != nil {
		slog.Warn("reconcile: registrar orden existente",
			"bot", o.Proposal.BotID, "client_order_id", o.ClientOrderID, "err", errMark)
		return
	}
	tracked := registry.TrackedOrder{
		ClientOrderID: o.ClientOrderID,
		Proposal:      o.Proposal,
		State:         registry.StateSubmitted,
		ExchangeID:    ord.OrderID,
		CreatedAt:     o.CreatedAt,
		UpdatedAt:     time.Now(),
	}
	r.processOrderStatus(ctx, o.Proposal.BotID, tracked, ord)
}

// processOrderStatus actualiza el registro y el riesgo según el estado de una
// orden en el exchange. `ord` siempre refleja una consulta reciente.
func (r *BotRuntime) processOrderStatus(ctx context.Context, botID string, o registry.TrackedOrder, ord exchange.OrderResponse) {
	switch ord.Status {
	case "FILLED":
		fills, err := r.deps.Executor.GetTrades(ctx, o.Proposal.Symbol, ord.OrderID)
		if err != nil {
			slog.Warn("reconcile: get trades", "bot", botID, "client_order_id", o.ClientOrderID, "err", err)
			return
		}
		qty, value := 0.0, 0.0
		for _, f := range fills {
			qty += f.Quantity
			value += f.Quantity * f.Price
		}
		if err := r.recordExecution(ctx, botID, o, qty, value); err != nil {
			slog.Warn("reconcile: record filled execution", "bot", botID, "client_order_id", o.ClientOrderID, "err", err)
			return
		}
		slog.Info("reconcile: order filled",
			"bot", botID, "client_order_id", o.ClientOrderID,
			"qty", qty, "value_usdt", value)

	case "CANCELED", "EXPIRED", "REJECTED":
		if ord.ExecutedQty > positionEpsilon {
			fills, err := r.deps.Executor.GetTrades(ctx, o.Proposal.Symbol, ord.OrderID)
			if err != nil {
				slog.Warn("reconcile: get trades for partially filled cancelled order", "bot", botID, "client_order_id", o.ClientOrderID, "err", err)
				return
			}
			qty, value := 0.0, 0.0
			for _, f := range fills {
				qty += f.Quantity
				value += f.Quantity * f.Price
			}
			if err := r.recordExecution(ctx, botID, o, qty, value); err != nil {
				slog.Warn("reconcile: record partial execution", "bot", botID, "client_order_id", o.ClientOrderID, "err", err)
				return
			}
			slog.Info("reconcile: partially filled order cancelled",
				"bot", botID, "client_order_id", o.ClientOrderID,
				"qty", qty, "value_usdt", value, "status", ord.Status)
			return
		}
		if err := r.deps.Registry.MarkCancelled(o.ClientOrderID); err != nil {
			slog.Warn("reconcile: mark cancelled", "bot", botID, "err", err)
			return
		}
		r.deps.RiskState.OnOrderCancelled(o.Proposal.Symbol, o.Proposal.Quantity)
		r.notifyOrderClosed(false)
		slog.Info("reconcile: order cancelled",
			"bot", botID, "client_order_id", o.ClientOrderID, "status", ord.Status)

	case "NEW", "PENDING_CANCEL", "PARTIALLY_FILLED":
		// Seguir esperando (PENDING_CANCEL: Binance está procesando la cancelación).

	default:
		slog.Warn("reconcile: estado de orden inesperado",
			"bot", botID, "client_order_id", o.ClientOrderID, "status", ord.Status)
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
