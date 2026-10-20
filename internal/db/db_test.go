//go:build integration

package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func testDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no configurada")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	db, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	return db, ctx
}

func TestOpenEmptyURL(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("se esperaba error con URL vacía")
	}
}

func TestOpenAndMigrate(t *testing.T) {
	db, ctx := testDB(t)

	// Verificar que la tabla existe.
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'orders'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Fatal("la tabla orders no existe")
	}

	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'system_logs'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("query system_logs: %v", err)
	}
	if !exists {
		t.Fatal("la tabla system_logs no existe")
	}
	var idType string
	if err := db.QueryRowContext(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_name = 'system_logs' AND column_name = 'id'
	`).Scan(&idType); err != nil {
		t.Fatalf("query system_logs.id: %v", err)
	}
	if idType != "uuid" {
		t.Fatalf("system_logs.id debe ser UUID, es %q", idType)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db, ctx := testDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate segunda vez: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate tercera vez: %v", err)
	}
}
