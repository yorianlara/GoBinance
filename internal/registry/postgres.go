package registry

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mi-bot/internal/domain"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresRegistry implementa OrderRegistry con PostgreSQL.
type PostgresRegistry struct {
	db *sql.DB
}

func NewPostgres(db *sql.DB) (*PostgresRegistry, error) {
	if db == nil {
		return nil, fmt.Errorf("db no puede ser nil")
	}
	return &PostgresRegistry{db: db}, nil
}

func (r *PostgresRegistry) Reserve(p domain.OrderProposal) (string, error) {
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("propuesta inválida: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC()

	_, err := r.db.Exec(`
		INSERT INTO orders (
			client_order_id, bot_id, symbol, side, type,
			quantity, limit_price, reason, proposed_at,
			state, exchange_id, filled_qty, filled_value,
			position_cost_basis_usdt, positions_closed,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,0,0,$11,$12,$13,$13)
	`,
		id, p.BotID, p.Symbol, string(p.Side), string(p.Type),
		p.Quantity, p.LimitPrice, p.Reason, p.ProposedAt,
		string(StateReserved), p.PositionCostBasisUSDT, p.PositionsClosed, now,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", fmt.Errorf("ya existe una orden activa para %s/%s", p.BotID, p.Symbol)
		}
		return "", fmt.Errorf("insertar orden: %w", err)
	}

	return id, nil
}

func (r *PostgresRegistry) MarkSubmitted(clientOrderID string, exchangeOrderID int64) error {
	res, err := r.db.Exec(`
		UPDATE orders
		SET state = $1, exchange_id = $2, updated_at = $3
		WHERE client_order_id = $4 AND state = $5
	`, string(StateSubmitted), exchangeOrderID, time.Now().UTC(), clientOrderID, string(StateReserved))
	if err != nil {
		return fmt.Errorf("mark submitted: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no existe orden %q o no está en estado reserved", clientOrderID)
	}
	return nil
}

func (r *PostgresRegistry) MarkFilled(clientOrderID string, filledQty, filledValue float64) error {
	if filledQty < 0 || filledValue < 0 {
		return fmt.Errorf("filledQty y filledValue no pueden ser negativos")
	}
	res, err := r.db.Exec(`
		UPDATE orders
		SET state = $1, filled_qty = $2, filled_value = $3, updated_at = $4
		WHERE client_order_id = $5 AND state = $6
	`, string(StateFilled), filledQty, filledValue, time.Now().UTC(), clientOrderID, string(StateSubmitted))
	if err != nil {
		return fmt.Errorf("mark filled: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no existe orden %q o no está en estado submitted", clientOrderID)
	}
	return nil
}

func (r *PostgresRegistry) SetPositionCloseDetails(clientOrderID string, costBasisUSDT float64, positionsClosed int) error {
	if costBasisUSDT < 0 || positionsClosed < 0 {
		return fmt.Errorf("costBasisUSDT y positionsClosed no pueden ser negativos")
	}
	res, err := r.db.Exec(`
		UPDATE orders
		SET position_cost_basis_usdt = $1, positions_closed = $2, updated_at = $3
		WHERE client_order_id = $4 AND state = $5 AND side = $6
	`, costBasisUSDT, positionsClosed, time.Now().UTC(), clientOrderID, string(StateSubmitted), string(domain.OrderSideSell))
	if err != nil {
		return fmt.Errorf("actualizar datos del cierre de posición: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no existe orden de cierre %q o no está submitted", clientOrderID)
	}
	return nil
}

func (r *PostgresRegistry) MarkCancelled(clientOrderID string) error {
	return r.markTerminal(clientOrderID, StateCancelled)
}

func (r *PostgresRegistry) MarkFailed(clientOrderID string) error {
	return r.markTerminal(clientOrderID, StateFailed)
}

func (r *PostgresRegistry) markTerminal(clientOrderID string, to OrderState) error {
	res, err := r.db.Exec(`
		UPDATE orders
		SET state = $1, updated_at = $2
		WHERE client_order_id = $3 AND state IN ($4, $5)
	`, string(to), time.Now().UTC(), clientOrderID, string(StateReserved), string(StateSubmitted))
	if err != nil {
		return fmt.Errorf("marcar %s: %w", to, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no existe orden %q o no está en estado reservado/enviado", clientOrderID)
	}
	return nil
}

func (r *PostgresRegistry) Get(clientOrderID string) (TrackedOrder, bool) {
	row := r.db.QueryRow(`
		SELECT client_order_id, bot_id, symbol, side, type,
		       quantity, limit_price, reason, proposed_at,
		       state, exchange_id, filled_qty, filled_value,
		       position_cost_basis_usdt, positions_closed,
		       created_at, updated_at
		FROM orders WHERE client_order_id = $1
	`, clientOrderID)

	var o TrackedOrder
	var side, typ string
	err := row.Scan(
		&o.ClientOrderID, &o.Proposal.BotID, &o.Proposal.Symbol, &side, &typ,
		&o.Proposal.Quantity, &o.Proposal.LimitPrice, &o.Proposal.Reason,
		&o.Proposal.ProposedAt,
		(*string)(&o.State), &o.ExchangeID, &o.FilledQty, &o.FilledValue,
		&o.Proposal.PositionCostBasisUSDT, &o.Proposal.PositionsClosed,
		&o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrackedOrder{}, false
		}
		slog.Warn("Get order", "err", err)
		return TrackedOrder{}, false
	}
	o.Proposal.Side = domain.OrderSide(side)
	o.Proposal.Type = domain.OrderType(typ)
	return o, true
}

func (r *PostgresRegistry) List() ([]TrackedOrder, error) {
	return r.queryOrders(`
		SELECT client_order_id, bot_id, symbol, side, type,
		       quantity, limit_price, reason, proposed_at,
		       state, exchange_id, filled_qty, filled_value,
		       position_cost_basis_usdt, positions_closed,
		       created_at, updated_at
		FROM orders ORDER BY created_at ASC
	`)
}

func (r *PostgresRegistry) ListActiveByBot(botID string) ([]TrackedOrder, error) {
	if botID == "" {
		return nil, fmt.Errorf("botID es obligatorio")
	}
	return r.queryOrders(`
		SELECT client_order_id, bot_id, symbol, side, type,
		       quantity, limit_price, reason, proposed_at,
		       state, exchange_id, filled_qty, filled_value,
		       position_cost_basis_usdt, positions_closed,
		       created_at, updated_at
		FROM orders
		WHERE bot_id = $1 AND state IN ($2, $3)
		ORDER BY created_at ASC
	`, botID, string(StateReserved), string(StateSubmitted))
}

func (r *PostgresRegistry) Recent(limit int) ([]TrackedOrder, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit debe ser mayor que cero")
	}
	return r.queryOrders(`
		SELECT client_order_id, bot_id, symbol, side, type,
		       quantity, limit_price, reason, proposed_at,
		       state, exchange_id, filled_qty, filled_value,
		       position_cost_basis_usdt, positions_closed,
		       created_at, updated_at
		FROM orders ORDER BY created_at DESC LIMIT $1
	`, limit)
}

func (r *PostgresRegistry) queryOrders(query string, args ...any) ([]TrackedOrder, error) {
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listar órdenes: %w", err)
	}
	defer rows.Close()

	var out []TrackedOrder
	for rows.Next() {
		var o TrackedOrder
		var side, typ string
		if err := rows.Scan(
			&o.ClientOrderID, &o.Proposal.BotID, &o.Proposal.Symbol, &side, &typ,
			&o.Proposal.Quantity, &o.Proposal.LimitPrice, &o.Proposal.Reason,
			&o.Proposal.ProposedAt,
			(*string)(&o.State), &o.ExchangeID, &o.FilledQty, &o.FilledValue,
			&o.Proposal.PositionCostBasisUSDT, &o.Proposal.PositionsClosed,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan orden: %w", err)
		}
		o.Proposal.Side = domain.OrderSide(side)
		o.Proposal.Type = domain.OrderType(typ)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar órdenes: %w", err)
	}
	return out, nil
}

func (r *PostgresRegistry) PendingExposureUSDT(priceBySymbol map[string]float64) (float64, error) {
	rows, err := r.db.Query(`
		SELECT symbol, quantity, limit_price
		FROM orders WHERE state IN ($1, $2)
	`, string(StateReserved), string(StateSubmitted))
	if err != nil {
		return 0, fmt.Errorf("pending exposure: %w", err)
	}
	defer rows.Close()

	total := 0.0
	for rows.Next() {
		var symbol string
		var qty, limit float64
		if err := rows.Scan(&symbol, &qty, &limit); err != nil {
			return 0, fmt.Errorf("scan pending: %w", err)
		}
		price := limit
		if price <= 0 {
			price = priceBySymbol[symbol]
		}
		if price <= 0 {
			slog.Warn("orden pendiente sin precio, se ignora", "symbol", symbol)
			continue
		}
		total += qty * price
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterar pending: %w", err)
	}
	return total, nil
}

func (r *PostgresRegistry) Len() (int, error) {
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}
