package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Open abre una conexión a PostgreSQL usando DATABASE_URL.
// Configura un pool razonable para un bot.
func Open(ctx context.Context, url string) (*sql.DB, error) {
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL vacío")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("abrir conexión: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return db, nil
}
