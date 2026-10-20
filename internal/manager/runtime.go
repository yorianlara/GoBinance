package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/registry"
)

const (
	TickInterval             = 5 * time.Second
	DefaultKlineInterval     = "1m"
	DefaultKlineLimit        = 100
	orderCancellationTimeout = 10 * time.Second
	liquidationTimeout       = 60 * time.Second
)

type BotRuntime struct {
	actionMu sync.Mutex
	mu       sync.RWMutex
	tickMu   sync.Mutex
	bot      domain.Bot
	strategy bots.Strategy
	cancel   context.CancelFunc
	ticker   *time.Ticker
	done     chan struct{}

	deps Deps
}

func NewBotRuntime(bot domain.Bot, strategy bots.Strategy, deps Deps) *BotRuntime {
	return &BotRuntime{bot: bot, strategy: strategy, deps: deps}
}

func (r *BotRuntime) Bot() domain.Bot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bot
}

func (r *BotRuntime) SetBot(bot domain.Bot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bot = bot
}

func (r *BotRuntime) Status() domain.BotStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bot.Status
}

func (r *BotRuntime) setStatus(s domain.BotStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bot.Status = s
	r.bot.UpdatedAt = time.Now()
}

func (r *BotRuntime) Start(parent context.Context) error {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()

	r.mu.Lock()
	if r.bot.Status == domain.BotStatusRunning {
		r.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	ticker := time.NewTicker(TickInterval)
	done := make(chan struct{})
	r.cancel = cancel
	r.ticker = ticker
	r.done = done
	r.bot.Status = domain.BotStatusRunning
	r.bot.UpdatedAt = time.Now()
	r.mu.Unlock()
	go r.run(ctx, ticker, done)

	r.deps.notifyPersistBots()
	return nil
}

func (r *BotRuntime) run(ctx context.Context, ticker *time.Ticker, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *BotRuntime) tick(ctx context.Context) {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()

	if ctx.Err() != nil {
		return
	}
	bot := r.Bot()
	slog.Info("tick: inicio", "bot", bot.ID, "symbol", bot.Symbol)

	// Reconciliar órdenes anteriores antes de pedir nuevas propuestas.
	r.reconcile(ctx, bot)

	price, err := r.deps.Market.Price(ctx, bot.Symbol)
	if err != nil {
		slog.Warn("tick: precio", "bot", bot.ID, "err", err)
		return
	}

	data := bots.MarketData{Symbol: bot.Symbol, Price: price}

	// Klines opcional: si falla, seguimos con Candles vacío.
	if r.deps.Klines != nil {
		candles, err := r.deps.Klines.Klines(ctx, bot.Symbol, DefaultKlineInterval, DefaultKlineLimit)
		if err != nil {
			slog.Warn("tick: klines", "bot", bot.ID, "err", err)
		} else {
			data.Candles = candles
		}
	}

	props, err := r.strategy.Evaluate(ctx, data)
	if err != nil {
		slog.Warn("tick: estrategia", "bot", bot.ID, "err", err)
		return
	}
	if len(props) == 0 {
		return
	}

	capital, err := r.deps.Capital.CapitalUSDT(ctx)
	if err != nil {
		slog.Warn("tick: capital", "bot", bot.ID, "err", err)
		r.notifyOrderRejected()
		return
	}

	pending, err := r.deps.Registry.PendingExposureUSDT(map[string]float64{bot.Symbol: price})
	if err != nil {
		slog.Warn("tick: pending exposure", "bot", bot.ID, "err", err)
		r.notifyOrderRejected()
		return
	}

	for i := range props {
		props[i].BotID = bot.ID

		if props[i].Side != domain.OrderSideBuy {
			slog.Warn("tick: lado de orden no habilitado", "bot", bot.ID, "side", props[i].Side)
			r.notifyOrderRejected()
			continue
		}

		var botsAllocatedPct, strategyAllocatedPct float64
		if r.deps.Allocations != nil {
			botsAllocatedPct, strategyAllocatedPct = r.deps.Allocations.AllocationPercentages(bot.Strategy.Type)
		}
		d := r.deps.RiskState.EvaluateWithAllocations(
			props[i], r.deps.ConfigStore, capital, price, pending,
			botsAllocatedPct, strategyAllocatedPct,
		)
		if !d.Approved {
			slog.Info("tick: rechazada", "bot", bot.ID, "symbol", props[i].Symbol, "reason", d.Reason)
			r.notifyOrderRejected()
			continue
		}
		clientOrderID, err := r.deps.Registry.Reserve(props[i])
		if err != nil {
			slog.Warn("tick: reserve", "bot", bot.ID, "err", err)
			r.notifyOrderRejected()
			continue
		}
		slog.Info("tick: reservada",
			"bot", bot.ID, "symbol", props[i].Symbol, "qty", props[i].Quantity,
			"client_order_id", clientOrderID)

		// Si no hay executor, estamos en modo "solo reservar" (tests o sin credenciales).
		if r.deps.Executor == nil {
			continue
		}

		resp, err := r.deps.Executor.PlaceOrder(ctx, exchange.OrderRequest{
			Symbol:        props[i].Symbol,
			Side:          props[i].Side,
			Type:          props[i].Type,
			Quantity:      props[i].Quantity,
			LimitPrice:    props[i].LimitPrice,
			ClientOrderID: clientOrderID,
		})
		if err != nil {
			slog.Warn("tick: place order", "bot", bot.ID, "client_order_id", clientOrderID, "err", err)
			if errors.Is(err, exchange.ErrOrderSubmissionUncertain) {
				// El resultado del envío se perdió (red, error de servidor tras
				// reintentos...): confirmar al exchange qué fue de la orden
				// antes de liberar la reserva.
				r.resolveUncertainOutcome(ctx, registry.TrackedOrder{
					ClientOrderID: clientOrderID,
					Proposal:      props[i],
					State:         registry.StateReserved,
				})
				continue
			}
			if errMark := r.deps.Registry.MarkFailed(clientOrderID); errMark != nil {
				slog.Warn("tick: mark failed", "bot", bot.ID, "err", errMark)
			}
			r.notifyOrderClosed(false)
			continue
		}
		if resp.OrderID <= 0 {
			// El exchange respondió sin ID fiable: consultar por clientOrderID
			// para reclamar la orden real o liberar la reserva.
			slog.Warn("tick: respuesta sin ID de orden válido", "bot", bot.ID, "client_order_id", clientOrderID)
			r.resolveUncertainOutcome(ctx, registry.TrackedOrder{
				ClientOrderID: clientOrderID,
				Proposal:      props[i],
				State:         registry.StateReserved,
			})
			continue
		}
		if err := r.deps.Registry.MarkSubmitted(clientOrderID, resp.OrderID); err != nil {
			slog.Warn("tick: mark submitted", "bot", bot.ID, "err", err)
			continue
		}
		slog.Info("tick: order submitted",
			"bot", bot.ID, "client_order_id", clientOrderID,
			"exchange_order_id", resp.OrderID, "status", resp.Status)

		pending, err = r.deps.Registry.PendingExposureUSDT(map[string]float64{bot.Symbol: price})
		if err != nil {
			slog.Warn("tick: pending exposure post-submit", "bot", bot.ID, "err", err)
			return
		}
	}
}

func (r *BotRuntime) Pause() error { return r.stopAndSet(domain.BotStatusPaused) }

// Stop detiene el bot: cancela sus órdenes pendientes en el exchange y vende
// la posición abierta (los lotes registrados por órdenes de compra finaledas)
// para que no queden lotes por cerrar ni órdenes activas. Con executor nulo
// (modo solo-reservar, pruebas) solo detiene el runtime.
//
// Devuelve el resultado de la liquidación y el error acumulado de cancelación
// y venta. Un bot recién detenido sin actividad devuelve NoPosition y nil.
func (r *BotRuntime) Stop() (PositionCloseResult, error) {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()

	bot := r.Bot()
	result := PositionCloseResult{BotID: bot.ID, Symbol: bot.Symbol}
	if err := r.stopAndSetLocked(domain.BotStatusStopped); err != nil {
		return result, fmt.Errorf("detener el bot y cancelar sus órdenes pendientes: %w", err)
	}
	if r.deps.Executor == nil {
		// Sin ejecutor no hay orden abierta ni posición en el exchange.
		result.NoPosition = true
		return result, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), liquidationTimeout)
	defer cancel()
	return r.liquidatePosition(ctx, bot.Symbol)
}

// stopOnly detiene el runtime y cancela las órdenes pendientes sin liquidar
// la posición; se usa cuando la venta se hace por símbolo aparte, como en el
// cierre global de posiciones.
func (r *BotRuntime) stopOnly() error {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()
	return r.stopAndSetLocked(domain.BotStatusStopped)
}

func (r *BotRuntime) stopAndSet(next domain.BotStatus) error {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()
	return r.stopAndSetLocked(next)
}

func (r *BotRuntime) stopAndSetLocked(next domain.BotStatus) error {
	r.tickMu.Lock()
	botID := r.Bot().ID
	cancelErr := r.cancelSubmittedOrders(botID)

	r.mu.Lock()
	cancel := r.cancel
	ticker := r.ticker
	done := r.done
	r.cancel = nil
	r.ticker = nil
	r.done = nil
	r.bot.Status = next
	r.bot.UpdatedAt = time.Now()
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ticker != nil {
		ticker.Stop()
	}
	r.tickMu.Unlock()

	if done != nil {
		<-done
	}

	r.deps.notifyPersistBots()
	return cancelErr
}

// cancelPendingOrders cancela en el exchange las órdenes pendientes
// (reserved/submitted) del bot, contabilizando fills parciales, SIN alterar
// el estado del runtime (no detiene ticks ni toca la liquidación). Toma
// tickMu para que ningún tick en vuelo compita con las cancelaciones.
func (r *BotRuntime) cancelPendingOrders(botID string) error {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	return r.cancelSubmittedOrders(botID)
}

func (r *BotRuntime) cancelSubmittedOrders(botID string) error {
	orders, err := r.deps.Registry.ListActiveByBot(botID)
	if err != nil {
		return fmt.Errorf("listar órdenes activas del bot %q: %w", botID, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), orderCancellationTimeout)
	defer cancel()

	var cancelErrs []error
	for _, order := range orders {
		switch order.State {
		case registry.StateReserved:
			// Reserva de resultado incierto: confirmar con el exchange qué fue
			// de la orden antes de terminar. Si quedó submitted (la orden
			// existe) sigue el flujo normal de cancelación; si se liberó o se
			// contabilizó como fill ya quedó registrado por
			// resolveUncertainOutcome; si no se pudo confirmar se conserva y
			// se avisa, para resolverla en un tick futuro.
			resolved, settled := r.settleReservedForStop(ctx, order)
			if !settled {
				cancelErrs = append(cancelErrs, fmt.Errorf(
					"no se pudo confirmar en el exchange la orden reservada %q; se conserva su reserva para no duplicar la operación",
					order.ClientOrderID,
				))
				continue
			}
			if resolved.State != registry.StateSubmitted {
				// failed/cancelled/filled: ya contabilizada, no hay nada que cancelar.
				continue
			}
			order = resolved
		case registry.StateSubmitted:
			// Flujo normal de cancelación.
		default:
			continue
		}
		if r.deps.Executor == nil {
			cancelErrs = append(cancelErrs, fmt.Errorf("cancelar orden %q: executor no configurado", order.ClientOrderID))
			continue
		}

		var finalOrder exchange.OrderResponse
		err := r.deps.Executor.CancelOrder(ctx, order.Proposal.Symbol, order.ExchangeID)
		if err == nil {
			finalOrder, err = r.deps.Executor.GetOrder(ctx, order.Proposal.Symbol, order.ExchangeID)
			if err != nil {
				slog.Warn("bot: verificar cancelación", "bot", botID, "client_order_id", order.ClientOrderID, "err", err)
				cancelErrs = append(cancelErrs, fmt.Errorf("verificar cancelación de orden %q: %w", order.ClientOrderID, err))
				continue
			}
		} else {
			// La cancelación puede fallar porque la orden ya terminó por su
			// cuenta, como ocurre al parar tras una compra market que llena
			// en instantes: consultar el estado real antes de dar la orden
			// por incancelable para no dejarla atascada como submitted.
			var queryErr error
			finalOrder, queryErr = r.deps.Executor.GetOrder(ctx, order.Proposal.Symbol, order.ExchangeID)
			if queryErr != nil || !isTerminalOrderStatus(finalOrder.Status) {
				slog.Warn("bot: cancelar orden", "bot", botID, "client_order_id", order.ClientOrderID,
					"err", err, "query_err", queryErr, "status", finalOrder.Status)
				cancelErrs = append(cancelErrs, fmt.Errorf(
					"cancelar orden %q en el exchange (%v) y su estado sigue sin confirmarse (consulta: %v, estado: %q)",
					order.ClientOrderID, err, queryErr, finalOrder.Status,
				))
				continue
			}
			slog.Info("bot: orden ya terminada en el exchange; se registra su resultado",
				"bot", botID, "client_order_id", order.ClientOrderID, "status", finalOrder.Status)
		}
		if !isTerminalOrderStatus(finalOrder.Status) {
			cancelErrs = append(cancelErrs, fmt.Errorf(
				"Binance no confirmó la cancelación de la orden %q; estado final %q",
				order.ClientOrderID, finalOrder.Status,
			))
			continue
		}
		if finalOrder.ExecutedQty > positionEpsilon {
			fills, err := r.deps.Executor.GetTrades(ctx, order.Proposal.Symbol, order.ExchangeID)
			if err != nil {
				cancelErrs = append(cancelErrs, fmt.Errorf(
					"consultar ejecuciones de orden parcialmente ejecutada %q: %w",
					order.ClientOrderID, err,
				))
				continue
			}
			quantity, value := 0.0, 0.0
			for _, fill := range fills {
				quantity += fill.Quantity
				value += fill.Quantity * fill.Price
			}
			if err := r.recordExecution(ctx, botID, order, quantity, value); err != nil {
				cancelErrs = append(cancelErrs, fmt.Errorf(
					"registrar ejecución de orden %q: %w", order.ClientOrderID, err,
				))
				continue
			}
			slog.Info("bot: orden ejecutada al detener (parcial o total)",
				"bot", botID, "client_order_id", order.ClientOrderID,
				"executed_qty", quantity, "value_usdt", value)
			continue
		}
		if err := r.deps.Registry.MarkCancelled(order.ClientOrderID); err != nil {
			slog.Error("bot: marcar orden cancelada", "bot", botID, "client_order_id", order.ClientOrderID, "err", err)
			cancelErrs = append(cancelErrs, fmt.Errorf("marcar orden %q como cancelada: %w", order.ClientOrderID, err))
			continue
		}
		slog.Info("bot: orden cancelada al detener", "bot", botID, "client_order_id", order.ClientOrderID, "exchange_order_id", order.ExchangeID)
	}
	return errors.Join(cancelErrs...)
}

// settleReservedForStop intenta resolver una reserva huérfana antes de parar
// el bot: pregunta al exchange (vía resolveUncertainOutcome) si la orden
// existe. Devuelve la orden refrescada y true cuando su resultado quedó
// conocido y contabilizado en el registro (failed/cancelled/filled, o
// submitted lista para el flujo de cancelación). Devuelve false solo si el
// resultado sigue sin poder confirmarse (la reserva permanece).
func (r *BotRuntime) settleReservedForStop(ctx context.Context, o registry.TrackedOrder) (registry.TrackedOrder, bool) {
	if r.deps.Executor == nil {
		return o, false
	}
	r.resolveUncertainOutcome(ctx, o)
	refreshed, ok := r.deps.Registry.Get(o.ClientOrderID)
	if !ok {
		return o, false
	}
	switch refreshed.State {
	case registry.StateReserved:
		// El exchange no fue alcanzable o no respondió con certeza: la
		// reserva permanece para una resolución futura.
		return refreshed, false
	default:
		return refreshed, true
	}
}

func (r *BotRuntime) WaitDone() error {
	r.mu.RLock()
	done := r.done
	r.mu.RUnlock()
	if done == nil {
		return fmt.Errorf("el bot no tiene goroutine activa")
	}
	<-done
	return nil
}
