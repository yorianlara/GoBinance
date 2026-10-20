package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type capturedExec struct {
	mu      sync.Mutex
	queries []string
	args    [][]any
}

type result int64

func (r result) LastInsertId() (int64, error) { return 0, nil }
func (r result) RowsAffected() (int64, error) { return int64(r), nil }

func (c *capturedExec) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, query)
	c.args = append(c.args, append([]any(nil), args...))
	return result(1), nil
}

func TestPostgresHandlerPersistsStructuredRecordAndRedactsSecrets(t *testing.T) {
	db := &capturedExec{}
	handler, err := NewPostgresHandler(db)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(handler.WithAttrs([]slog.Attr{slog.String("component", "test")}))
	logger.Info("bot event",
		slog.String("bot", "btc-dca"),
		slog.String("api_secret", "must-not-persist"),
		slog.Group("exchange", slog.String("api-key", "also-secret"), slog.String("status", "ok")),
		slog.Any("metadata", map[string]any{"password": "nested-secret", "safe": "value"}),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := handler.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.queries) != 1 {
		t.Fatalf("esperaba un insert, hay %d", len(db.queries))
	}
	if !strings.Contains(db.queries[0], "INSERT INTO system_logs") {
		t.Fatalf("query inesperado: %s", db.queries[0])
	}
	if len(db.args[0]) != 6 {
		t.Fatalf("esperados 6 argumentos, hay %d", len(db.args[0]))
	}
	if _, err := uuid.Parse(db.args[0][0].(string)); err != nil {
		t.Fatalf("id no es UUID: %v", err)
	}
	if got := db.args[0][2]; got != "INFO" {
		t.Fatalf("nivel inesperado: %v", got)
	}
	if got := db.args[0][3]; got != "bot event" {
		t.Fatalf("mensaje inesperado: %v", got)
	}
	if source, ok := db.args[0][4].(string); !ok || source == "" {
		t.Fatalf("el evento debe incluir origen, hay %v", db.args[0][4])
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(db.args[0][5].(string)), &attrs); err != nil {
		t.Fatalf("atributos JSON inválidos: %v", err)
	}
	if attrs["component"] != "test" || attrs["bot"] != "btc-dca" || attrs["exchange.status"] != "ok" {
		t.Fatalf("atributos esperados ausentes: %+v", attrs)
	}
	if attrs["api_secret"] != "[REDACTED]" || attrs["exchange.api-key"] != "[REDACTED]" {
		t.Fatalf("atributos sensibles no fueron redactados: %+v", attrs)
	}
	metadata := attrs["metadata"].(map[string]any)
	if metadata["password"] != "[REDACTED]" || metadata["safe"] != "value" {
		t.Fatalf("atributos sensibles anidados no fueron redactados: %+v", metadata)
	}
}

func TestPostgresHandlerPreservesWithGroupScope(t *testing.T) {
	db := &capturedExec{}
	handler, err := NewPostgresHandler(db)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(handler).With("service", "mi-bot").WithGroup("http").With("method", "GET")
	logger.Info("request", "path", "/health")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := handler.Close(ctx); err != nil {
		t.Fatal(err)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	var attrs map[string]any
	if err := json.Unmarshal([]byte(db.args[0][5].(string)), &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["service"] != "mi-bot" || attrs["http.method"] != "GET" || attrs["http.path"] != "/health" {
		t.Fatalf("alcance de grupo incorrecto: %+v", attrs)
	}
}

func TestPostgresHandlerBatchesRecords(t *testing.T) {
	db := &capturedExec{}
	handler, err := NewPostgresHandler(db)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(handler)
	for i := 0; i < batchSize+5; i++ {
		logger.Info("event")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := handler.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	total := 0
	for _, args := range db.args {
		if len(args) == 0 || len(args)%6 != 0 || len(args) > batchSize*6 {
			t.Fatalf("tamaño de lote inesperado: %d args", len(args))
		}
		total += len(args) / 6
	}
	if total != batchSize+5 {
		t.Fatalf("esperados %d registros, hay %d", batchSize+5, total)
	}
}
