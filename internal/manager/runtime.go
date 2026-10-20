package manager

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mi-bot/internal/bots"
	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
)

const (
	TickInterval         = 5 * time.Second
	DefaultKlineInterval = "1m"
	DefaultKlineLimit    = 100
)

type BotRuntime struct {
	mu       sync.RWMutex
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bot.Status == domain.BotStatusRunning {
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
	go r.run(ctx, ticker, done)
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
		return
	}

	pending, err := r.deps.Registry.PendingExposureUSDT(map[string]float64{bot.Symbol: price})
	if err != nil {
		slog.Warn("tick: pending exposure", "bot", bot.ID, "err", err)
		return
	}

	for i := range props {
		props[i].BotID = bot.ID

		d := r.deps.RiskState.Evaluate(props[i], r.deps.ConfigStore, capital, price, pending)
		if !d.Approved {
			slog.Info("tick: rechazada", "bot", bot.ID, "symbol", props[i].Symbol, "reason", d.Reason)
			continue
		}
		clientOrderID, err := r.deps.Registry.Reserve(props[i])
		if err != nil {
			slog.Warn("tick: reserve", "bot", bot.ID, "err", err)
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
			if errMark := r.deps.Registry.MarkFailed(clientOrderID); errMark != nil {
				slog.Warn("tick: mark failed", "bot", bot.ID, "err", errMark)
			}
			r.notifyOrderClosed()
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
func (r *BotRuntime) Stop() error  { return r.stopAndSet(domain.BotStatusStopped) }

func (r *BotRuntime) stopAndSet(next domain.BotStatus) error {
	r.mu.Lock()
	cancel := r.cancel
	ticker := r.ticker
	done := r.done
	r.cancel = nil
	r.ticker = nil
	r.done = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ticker != nil {
		ticker.Stop()
	}
	if done != nil {
		<-done
	}
	r.setStatus(next)
	return nil
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
