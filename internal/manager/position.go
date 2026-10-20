package manager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"mi-bot/internal/domain"
	"mi-bot/internal/exchange"
	"mi-bot/internal/registry"
)

const positionEpsilon = 1e-10

type PositionCloseResult struct {
	BotID           string
	Symbol          string
	SoldQuantity    float64
	ProceedsUSDT    float64
	CostBasisUSDT   float64
	RealizedPnLUSDT float64
	NoPosition      bool
	Partial         bool
}

type positionLot struct {
	quantity float64
	cost     float64
}

type positionInventory struct {
	lots []positionLot
}

func (p *positionInventory) quantity() float64 {
	total := 0.0
	for _, lot := range p.lots {
		total += lot.quantity
	}
	return total
}

func (p *positionInventory) costBasis() float64 {
	total := 0.0
	for _, lot := range p.lots {
		total += lot.cost
	}
	return total
}

func (p *positionInventory) openLots() int {
	count := 0
	for _, lot := range p.lots {
		if lot.quantity > positionEpsilon {
			count++
		}
	}
	return count
}

func (p *positionInventory) sell(quantity float64) (costBasis float64, positionsClosed int, err error) {
	if quantity < 0 || quantity-p.quantity() > positionEpsilon {
		return 0, 0, fmt.Errorf("cantidad a vender %.12f excede la posición registrada %.12f", quantity, p.quantity())
	}
	remaining := quantity
	for i := range p.lots {
		if remaining <= positionEpsilon {
			break
		}
		lot := &p.lots[i]
		if lot.quantity <= positionEpsilon {
			continue
		}
		used := math.Min(lot.quantity, remaining)
		allocatedCost := lot.cost * used / lot.quantity
		lot.quantity -= used
		lot.cost -= allocatedCost
		remaining -= used
		costBasis += allocatedCost
		if lot.quantity <= positionEpsilon {
			lot.quantity = 0
			lot.cost = 0
			positionsClosed++
		}
	}
	if remaining > positionEpsilon {
		return 0, 0, fmt.Errorf("no se pudo asignar la cantidad vendida; quedan %.12f sin asignar", remaining)
	}
	return costBasis, positionsClosed, nil
}

func inventoryFor(orders []registry.TrackedOrder, botID, symbol string) (positionInventory, error) {
	inventory := positionInventory{}
	for _, order := range orders {
		if order.State != registry.StateFilled || order.Proposal.BotID != botID || order.Proposal.Symbol != symbol {
			continue
		}
		switch order.Proposal.Side {
		case domain.OrderSideBuy:
			if order.FilledQty > positionEpsilon {
				inventory.lots = append(inventory.lots, positionLot{
					quantity: order.FilledQty,
					cost:     order.FilledValue,
				})
			}
		case domain.OrderSideSell:
			if order.FilledQty > positionEpsilon {
				if _, _, err := inventory.sell(order.FilledQty); err != nil {
					return positionInventory{}, fmt.Errorf("reconstruir inventario del bot %q en %s desde la orden %q: %w",
						botID, symbol, order.ClientOrderID, err)
				}
			}
		}
	}
	return inventory, nil
}

func (r *BotRuntime) ClosePosition(ctx context.Context) (PositionCloseResult, error) {
	return r.ClosePositionFor(ctx, r.Bot().Symbol)
}

func (r *BotRuntime) ClosePositionFor(ctx context.Context, symbol string) (PositionCloseResult, error) {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()

	if err := r.stopAndSetLocked(domain.BotStatusStopped); err != nil {
		return PositionCloseResult{}, fmt.Errorf("detener el bot y cancelar sus órdenes pendientes: %w", err)
	}
	return r.liquidatePosition(ctx, symbol)
}

// liquidatePosition vende el inventario registrado del bot en `symbol`.
// Debe llamarse con el bot ya detenido (sin tick activo) y requiere executor.
func (r *BotRuntime) liquidatePosition(ctx context.Context, symbol string) (PositionCloseResult, error) {
	if r.deps.Executor == nil {
		return PositionCloseResult{}, fmt.Errorf("no está configurado el ejecutor de órdenes")
	}
	if symbol == "" {
		return PositionCloseResult{}, fmt.Errorf("símbolo de posición obligatorio")
	}

	bot := r.Bot()
	result := PositionCloseResult{BotID: bot.ID, Symbol: symbol}
	orders, err := r.deps.Registry.List()
	if err != nil {
		return result, fmt.Errorf("listar órdenes para calcular posición: %w", err)
	}
	inventory, err := inventoryFor(orders, bot.ID, symbol)
	if err != nil {
		return result, err
	}
	quantity := inventory.quantity()
	if quantity <= positionEpsilon {
		result.NoPosition = true
		return result, nil
	}

	baseAsset, err := r.deps.Executor.GetSymbolBaseAsset(ctx, symbol)
	if err != nil {
		return result, fmt.Errorf("obtener el activo base de %s: %w", symbol, err)
	}
	freeBalance, err := r.deps.Executor.GetFreeBalance(ctx, baseAsset)
	if err != nil {
		return result, fmt.Errorf("consultar saldo libre de %s: %w", baseAsset, err)
	}
	managedQuantity, err := trackedQuantityForAsset(ctx, orders, r.deps.Executor, baseAsset)
	if err != nil {
		return result, err
	}
	if freeBalance+positionEpsilon < managedQuantity {
		return result, fmt.Errorf("saldo libre de %s (%.12f) menor que el inventario registrado de los bots (%.12f); no se vende para evitar liquidar fondos no atribuibles", baseAsset, freeBalance, managedQuantity)
	}

	proposal := domain.OrderProposal{
		BotID:                 bot.ID,
		Symbol:                symbol,
		Side:                  domain.OrderSideSell,
		Type:                  domain.OrderTypeMarket,
		Quantity:              quantity,
		Reason:                "cierre manual de posición del bot",
		ProposedAt:            time.Now(),
		PositionCostBasisUSDT: inventory.costBasis(),
		PositionsClosed:       inventory.openLots(),
	}
	clientOrderID, err := r.deps.Registry.Reserve(proposal)
	if err != nil {
		return result, fmt.Errorf("reservar orden de cierre: %w", err)
	}
	response, err := r.deps.Executor.PlaceOrder(ctx, exchange.OrderRequest{
		Symbol:        proposal.Symbol,
		Side:          proposal.Side,
		Type:          proposal.Type,
		Quantity:      proposal.Quantity,
		ClientOrderID: clientOrderID,
	})
	if err != nil {
		if errors.Is(err, exchange.ErrOrderSubmissionUncertain) {
			return result, fmt.Errorf(
				"resultado del envío de la orden de cierre %s incierto; se conserva su reserva para impedir una venta duplicada: %w",
				clientOrderID, err,
			)
		}
		if markErr := r.deps.Registry.MarkFailed(clientOrderID); markErr != nil {
			return result, errors.Join(fmt.Errorf("vender posición: %w", err), fmt.Errorf("marcar orden fallida: %w", markErr))
		}
		return result, fmt.Errorf("vender posición: %w", err)
	}
	if response.OrderID <= 0 {
		return result, fmt.Errorf(
			"Binance respondió sin un ID válido para la orden de cierre %s; se conserva la reserva para impedir una venta duplicada",
			clientOrderID,
		)
	}
	if err := r.deps.Registry.MarkSubmitted(clientOrderID, response.OrderID); err != nil {
		cancelErr := r.deps.Executor.CancelOrder(ctx, proposal.Symbol, response.OrderID)
		var registryErr error
		if cancelErr == nil {
			registryErr = r.deps.Registry.MarkCancelled(clientOrderID)
		}
		return result, errors.Join(
			fmt.Errorf("registrar orden de venta enviada: %w", err),
			cancelErr,
			registryErr,
		)
	}

	finalResponse, err := r.resolveCloseOrder(ctx, proposal.Symbol, response)
	if err != nil {
		return result, fmt.Errorf("orden de cierre %s enviada, pero no se pudo confirmar su resultado: %w", clientOrderID, err)
	}
	if finalResponse.ExecutedQty <= positionEpsilon {
		if err := r.deps.Registry.MarkCancelled(clientOrderID); err != nil {
			return result, fmt.Errorf("la orden no se ejecutó y no se pudo cerrar su registro: %w", err)
		}
		return result, fmt.Errorf("Binance no ejecutó la venta; estado final: %s", finalResponse.Status)
	}
	if finalResponse.CummulativeQuoteQty < 0 {
		return result, fmt.Errorf("Binance devolvió un importe ejecutado inválido: %v", finalResponse.CummulativeQuoteQty)
	}

	actualInventory, err := inventoryFor(orders, bot.ID, symbol)
	if err != nil {
		return result, err
	}
	costBasis, positionsClosed, err := actualInventory.sell(finalResponse.ExecutedQty)
	if err != nil {
		return result, fmt.Errorf("venta ejecutada por Binance pero cantidad incompatible con el inventario: %w", err)
	}
	if err := r.deps.Registry.SetPositionCloseDetails(clientOrderID, costBasis, positionsClosed); err != nil {
		return result, fmt.Errorf("venta ejecutada por Binance pero no se pudo guardar su coste base: %w", err)
	}
	if err := r.deps.Registry.MarkFilled(clientOrderID, finalResponse.ExecutedQty, finalResponse.CummulativeQuoteQty); err != nil {
		return result, fmt.Errorf("venta ejecutada por Binance pero no se pudo cerrar su registro: %w", err)
	}

	result.SoldQuantity = finalResponse.ExecutedQty
	result.ProceedsUSDT = finalResponse.CummulativeQuoteQty
	result.CostBasisUSDT = costBasis
	result.RealizedPnLUSDT = result.ProceedsUSDT - result.CostBasisUSDT
	result.Partial = finalResponse.ExecutedQty+positionEpsilon < quantity
	r.deps.RiskState.OnPositionsClosed(symbol, costBasis, result.RealizedPnLUSDT, positionsClosed)
	if err := r.deps.RiskState.Persist(); err != nil {
		return result, fmt.Errorf("posición vendida, pero no se pudo persistir el estado de riesgo: %w", err)
	}
	if result.Partial {
		return result, fmt.Errorf("Binance ejecutó parcialmente la venta; queda posición registrada para cerrar")
	}
	return result, nil
}

func (r *BotRuntime) resolveCloseOrder(ctx context.Context, symbol string, response exchange.OrderResponse) (exchange.OrderResponse, error) {
	switch response.Status {
	case "FILLED", "CANCELED", "EXPIRED", "REJECTED":
		return response, nil
	case "NEW", "PARTIALLY_FILLED":
		if err := r.deps.Executor.CancelOrder(ctx, symbol, response.OrderID); err != nil {
			final, queryErr := r.deps.Executor.GetOrder(ctx, symbol, response.OrderID)
			if queryErr != nil {
				return exchange.OrderResponse{}, errors.Join(fmt.Errorf("cancelar remanente: %w", err), fmt.Errorf("consultar orden final: %w", queryErr))
			}
			if !isTerminalOrderStatus(final.Status) {
				return exchange.OrderResponse{}, fmt.Errorf("la orden sigue abierta tras el intento de cancelación: %w", err)
			}
			return final, nil
		}
		final, err := r.deps.Executor.GetOrder(ctx, symbol, response.OrderID)
		if err != nil {
			return exchange.OrderResponse{}, err
		}
		if !isTerminalOrderStatus(final.Status) {
			return exchange.OrderResponse{}, fmt.Errorf("Binance no confirmó estado terminal tras cancelar; estado %q", final.Status)
		}
		return final, nil
	default:
		return exchange.OrderResponse{}, fmt.Errorf("estado de orden inesperado %q", response.Status)
	}
}

func isTerminalOrderStatus(status string) bool {
	switch status {
	case "FILLED", "CANCELED", "EXPIRED", "REJECTED":
		return true
	default:
		return false
	}
}

// InventorySnapshot refleja la posición abierta de un bot en un símbolo,
// reconstruida desde las órdenes del registro (la contabilidad autoritativa).
type InventorySnapshot struct {
	BotID         string
	Symbol        string
	Quantity      float64
	CostBasisUSDT float64
}

// Inventories reconstruye todas las posiciones abiertas a partir del
// registro de órdenes: por cada par bot/símbolo con lotes sin vender devuelve
// la cantidad y su coste base. Devuelve error si algún par presenta una
// contabilidad inconsistente (p. ej. ventas sin compras que las respalden),
// en cuyo caso el llamador no debe fiarse de una reconstrucción parcial.
func (m *BotManager) Inventories() ([]InventorySnapshot, error) {
	orders, err := m.deps.Registry.List()
	if err != nil {
		return nil, fmt.Errorf("listar órdenes para inventarios: %w", err)
	}

	type pairKey struct{ botID, symbol string }
	pairs := make(map[pairKey]struct{})
	for _, order := range orders {
		switch order.Proposal.Side {
		case domain.OrderSideBuy, domain.OrderSideSell:
			pairs[pairKey{order.Proposal.BotID, order.Proposal.Symbol}] = struct{}{}
		}
	}

	snapshots := make([]InventorySnapshot, 0, len(pairs))
	for pair := range pairs {
		inventory, err := inventoryFor(orders, pair.botID, pair.symbol)
		if err != nil {
			return nil, fmt.Errorf("inventario del bot %q en %s: %w", pair.botID, pair.symbol, err)
		}
		if inventory.quantity() <= positionEpsilon {
			continue
		}
		snapshots = append(snapshots, InventorySnapshot{
			BotID:         pair.botID,
			Symbol:        pair.symbol,
			Quantity:      inventory.quantity(),
			CostBasisUSDT: inventory.costBasis(),
		})
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].BotID != snapshots[j].BotID {
			return snapshots[i].BotID < snapshots[j].BotID
		}
		return snapshots[i].Symbol < snapshots[j].Symbol
	})
	return snapshots, nil
}

func trackedQuantityForAsset(ctx context.Context, orders []registry.TrackedOrder, executor OrderExecutor, asset string) (float64, error) {
	type botSymbol struct {
		botID  string
		symbol string
	}
	pairs := make(map[botSymbol]struct{})
	for _, order := range orders {
		if order.State == registry.StateFilled &&
			(order.Proposal.Side == domain.OrderSideBuy || order.Proposal.Side == domain.OrderSideSell) {
			pairs[botSymbol{botID: order.Proposal.BotID, symbol: order.Proposal.Symbol}] = struct{}{}
		}
	}
	total := 0.0
	for pair := range pairs {
		baseAsset, err := executor.GetSymbolBaseAsset(ctx, pair.symbol)
		if err != nil {
			return 0, fmt.Errorf("obtener activo base para verificar inventario de %s: %w", pair.symbol, err)
		}
		if baseAsset != asset {
			continue
		}
		inventory, err := inventoryFor(orders, pair.botID, pair.symbol)
		if err != nil {
			return 0, err
		}
		total += inventory.quantity()
	}
	return total, nil
}
