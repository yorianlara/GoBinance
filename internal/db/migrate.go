package db

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS orders (
    client_order_id TEXT PRIMARY KEY,
    bot_id          TEXT NOT NULL,
    symbol          TEXT NOT NULL,
    side            TEXT NOT NULL,
    type            TEXT NOT NULL,
    quantity        DOUBLE PRECISION NOT NULL,
    limit_price     DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason          TEXT NOT NULL DEFAULT '',
    proposed_at     TIMESTAMPTZ NOT NULL,
    state           TEXT NOT NULL,
    exchange_id     BIGINT NOT NULL DEFAULT 0,
    filled_qty      DOUBLE PRECISION NOT NULL DEFAULT 0,
    filled_value    DOUBLE PRECISION NOT NULL DEFAULT 0,
    position_cost_basis_usdt DOUBLE PRECISION NOT NULL DEFAULT 0,
    positions_closed INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS position_cost_basis_usdt DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS positions_closed INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_orders_state ON orders(state);
CREATE INDEX IF NOT EXISTS idx_orders_symbol ON orders(symbol);
CREATE INDEX IF NOT EXISTS idx_orders_bot ON orders(bot_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_active_proposal
    ON orders(bot_id, symbol, side, type, quantity, limit_price)
    WHERE state IN ('reserved', 'submitted');

CREATE TABLE IF NOT EXISTS system_logs (
    id          UUID PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    level       TEXT NOT NULL,
    message     TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT '',
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_system_logs_occurred_at
    ON system_logs(occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_system_logs_level_occurred_at
    ON system_logs(level, occurred_at DESC);
`

// Migrate crea el esquema si no existe. Idempotente.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db no puede ser nil")
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("aplicar migración: %w", err)
	}
	return nil
}
