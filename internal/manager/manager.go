package manager

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"mi-bot/internal/domain"
	"mi-bot/internal/registry"
)

type BotManager struct {
	opsMu    sync.Mutex
	mu       sync.RWMutex
	runtimes map[string]*BotRuntime
	deps     Deps

	ctx    context.Context
	cancel context.CancelFunc
}

func New(deps Deps) (*BotManager, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &BotManager{
		runtimes: make(map[string]*BotRuntime),
		deps:     deps,
		ctx:      ctx,
		cancel:   cancel,
	}
	m.deps.Allocations = m
	return m, nil
}

func (m *BotManager) Start() error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	m.mu.RLock()
	pending := make([]*BotRuntime, 0, len(m.runtimes))
	for _, rt := range m.runtimes {
		if rt.Status() == domain.BotStatusRunning {
			pending = append(pending, rt)
		}
	}
	m.mu.RUnlock()

	for _, rt := range pending {
		if err := rt.Start(m.ctx); err != nil {
			return fmt.Errorf("arrancar bot %q: %w", rt.Bot().ID, err)
		}
	}
	return nil
}

func (m *BotManager) Shutdown() { m.cancel() }

func (m *BotManager) Create(bot domain.Bot) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	if err := bot.Validate(); err != nil {
		return fmt.Errorf("bot inválido: %w", err)
	}

	strategy, err := m.deps.StrategyFactory(bot.Strategy)
	if err != nil {
		return fmt.Errorf("construir estrategia: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.runtimes[bot.ID]; exists {
		return fmt.Errorf("ya existe un bot con ID %q", bot.ID)
	}

	bot.Status = domain.BotStatusCreated
	rt := NewBotRuntime(bot, strategy, m.deps)
	m.runtimes[bot.ID] = rt
	return nil
}

func (m *BotManager) Update(bot domain.Bot) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	if err := bot.Validate(); err != nil {
		return fmt.Errorf("bot inválido: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rt, ok := m.runtimes[bot.ID]
	if !ok {
		return fmt.Errorf("no existe bot con ID %q", bot.ID)
	}
	if rt.Status() == domain.BotStatusRunning {
		return fmt.Errorf("no se puede editar un bot en estado %q", rt.Status())
	}
	rt.SetBot(bot)
	return nil
}

func (m *BotManager) Delete(id string) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	rt, ok := m.runtimes[id]
	if !ok {
		return fmt.Errorf("no existe bot con ID %q", id)
	}
	if rt.Status() == domain.BotStatusRunning {
		return fmt.Errorf("no se puede eliminar un bot en estado %q", rt.Status())
	}
	delete(m.runtimes, id)
	return nil
}

func (m *BotManager) Enable(id string) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	m.mu.RLock()
	rt, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no existe bot con ID %q", id)
	}
	return rt.Start(m.ctx)
}

func (m *BotManager) Pause(id string) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	m.mu.RLock()
	rt, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no existe bot con ID %q", id)
	}
	return rt.Pause()
}

func (m *BotManager) StopBot(id string) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	m.mu.RLock()
	rt, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no existe bot con ID %q", id)
	}
	return rt.Stop()
}

func (m *BotManager) ClosePosition(ctx context.Context, id string) (PositionCloseResult, error) {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	m.mu.RLock()
	rt, ok := m.runtimes[id]
	m.mu.RUnlock()
	if !ok {
		return PositionCloseResult{}, fmt.Errorf("no existe bot con ID %q", id)
	}
	return rt.ClosePosition(ctx)
}

type PositionCloseAttempt struct {
	BotID  string
	Symbol string
	Result PositionCloseResult
	Err    error
}

func (m *BotManager) CloseAllPositions(ctx context.Context) ([]PositionCloseAttempt, error) {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	orders, err := m.deps.Registry.List()
	if err != nil {
		return nil, fmt.Errorf("listar órdenes para cierre global: %w", err)
	}

	type positionKey struct {
		botID  string
		symbol string
	}
	pairs := make(map[positionKey]struct{})
	for _, bot := range m.List() {
		pairs[positionKey{botID: bot.ID, symbol: bot.Symbol}] = struct{}{}
	}
	for _, order := range orders {
		if (order.State != registry.StateReserved &&
			order.State != registry.StateSubmitted &&
			order.State != registry.StateFilled) ||
			(order.Proposal.Side != domain.OrderSideBuy && order.Proposal.Side != domain.OrderSideSell) {
			continue
		}
		pairs[positionKey{botID: order.Proposal.BotID, symbol: order.Proposal.Symbol}] = struct{}{}
	}

	keys := make([]positionKey, 0, len(pairs))
	for pair := range pairs {
		keys = append(keys, pair)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].botID == keys[j].botID {
			return keys[i].symbol < keys[j].symbol
		}
		return keys[i].botID < keys[j].botID
	})

	runtimes := make(map[string]*BotRuntime)
	m.mu.RLock()
	for id, runtime := range m.runtimes {
		runtimes[id] = runtime
	}
	m.mu.RUnlock()
	for _, pair := range keys {
		if runtimes[pair.botID] == nil {
			runtimes[pair.botID] = NewBotRuntime(domain.Bot{
				ID: pair.botID, Symbol: pair.symbol, Status: domain.BotStatusStopped,
			}, nil, m.deps)
		}
	}
	botIDs := make([]string, 0, len(runtimes))
	for id := range runtimes {
		botIDs = append(botIDs, id)
	}
	sort.Strings(botIDs)
	for _, id := range botIDs {
		runtime := runtimes[id]
		if err := runtime.Stop(); err != nil {
			slog.Warn("cierre global: error al detener bot antes de liquidar",
				"bot", runtime.Bot().ID, "err", err)
		}
	}

	attempts := make([]PositionCloseAttempt, 0, len(keys))
	for _, pair := range keys {
		result, closeErr := runtimes[pair.botID].ClosePositionFor(ctx, pair.symbol)
		attempts = append(attempts, PositionCloseAttempt{
			BotID: pair.botID, Symbol: pair.symbol, Result: result, Err: closeErr,
		})
	}
	return attempts, nil
}

func (m *BotManager) Get(id string) (domain.Bot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt, ok := m.runtimes[id]
	if !ok {
		return domain.Bot{}, false
	}
	return rt.Bot(), true
}

func (m *BotManager) List() []domain.Bot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bots := make([]domain.Bot, 0, len(m.runtimes))
	for _, rt := range m.runtimes {
		bots = append(bots, rt.Bot())
	}
	sort.Slice(bots, func(i, j int) bool { return bots[i].ID < bots[j].ID })
	return bots
}

func (m *BotManager) AllocationPercentages(strategyType string) (total, strategy float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, rt := range m.runtimes {
		bot := rt.Bot()
		total += bot.AllocationPercent
		if bot.Strategy.Type == strategyType {
			strategy += bot.AllocationPercent
		}
	}
	return total, strategy
}

func (m *BotManager) Status(id string) (domain.BotStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt, ok := m.runtimes[id]
	if !ok {
		return "", false
	}
	return rt.Status(), true
}
