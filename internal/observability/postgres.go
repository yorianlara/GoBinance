package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	batchSize     = 100
	insertTimeout = 5 * time.Second
	retryInterval = time.Second
)

type execContext interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type logEntry struct {
	id         string
	occurredAt time.Time
	level      string
	message    string
	source     string
	attributes []byte
}

type queue struct {
	db       execContext
	mu       sync.Mutex
	cond     *sync.Cond
	entries  []logEntry
	closed   bool
	done     chan struct{}
	cancel   context.CancelFunc
	closeOne sync.Once
}

// PostgresHandler guarda los eventos estructurados en PostgreSQL en segundo
// plano, para no añadir latencia de base de datos al ciclo de trading.
type PostgresHandler struct {
	queue  *queue
	attrs  []slog.Attr
	groups []string
}

func NewPostgresHandler(db execContext) (*PostgresHandler, error) {
	if db == nil {
		return nil, fmt.Errorf("la conexión PostgreSQL no puede ser nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &queue{db: db, done: make(chan struct{}), cancel: cancel}
	q.cond = sync.NewCond(&q.mu)
	go q.run(ctx)
	return &PostgresHandler{queue: q}, nil
}

func (h *PostgresHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *PostgresHandler) Handle(_ context.Context, record slog.Record) error {
	attributes := make(map[string]any)
	for _, attr := range h.attrs {
		addAttr(attributes, nil, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addAttr(attributes, h.groups, attr)
		return true
	})
	payload, err := json.Marshal(attributes)
	if err != nil {
		return fmt.Errorf("serializar atributos del log: %w", err)
	}

	source := ""
	if record.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
		if frame.File != "" {
			source = fmt.Sprintf("%s:%d", filepath.Base(frame.File), frame.Line)
		}
	}
	entry := logEntry{
		id:         uuid.NewString(),
		occurredAt: record.Time,
		level:      record.Level.String(),
		message:    record.Message,
		source:     source,
		attributes: payload,
	}
	if entry.occurredAt.IsZero() {
		entry.occurredAt = time.Now().UTC()
	}

	h.queue.mu.Lock()
	defer h.queue.mu.Unlock()
	if h.queue.closed {
		return errors.New("el logger PostgreSQL está cerrado")
	}
	h.queue.entries = append(h.queue.entries, entry)
	h.queue.cond.Signal()
	return nil
}

func (h *PostgresHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := &PostgresHandler{queue: h.queue, attrs: append([]slog.Attr(nil), h.attrs...), groups: append([]string(nil), h.groups...)}
	for _, attr := range attrs {
		for i := len(h.groups) - 1; i >= 0; i-- {
			attr = slog.Group(h.groups[i], attr)
		}
		out.attrs = append(out.attrs, attr)
	}
	return out
}

func (h *PostgresHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := &PostgresHandler{queue: h.queue, attrs: append([]slog.Attr(nil), h.attrs...), groups: append([]string(nil), h.groups...)}
	out.groups = append(out.groups, name)
	return out
}

// Close deja de aceptar logs y espera a que la cola se escriba o venza ctx.
func (h *PostgresHandler) Close(ctx context.Context) error {
	h.queue.closeOne.Do(func() {
		h.queue.mu.Lock()
		h.queue.closed = true
		h.queue.cond.Broadcast()
		h.queue.mu.Unlock()
	})

	select {
	case <-h.queue.done:
		return nil
	default:
	}
	select {
	case <-h.queue.done:
		return nil
	case <-ctx.Done():
		select {
		case <-h.queue.done:
			return nil
		default:
		}
		h.queue.cancel()
		<-h.queue.done
		h.queue.mu.Lock()
		pending := len(h.queue.entries)
		h.queue.mu.Unlock()
		return fmt.Errorf("tiempo agotado esperando logs PostgreSQL; %d eventos no confirmados: %w", pending, ctx.Err())
	}
}

func (q *queue) run(ctx context.Context) {
	defer close(q.done)
	for {
		q.mu.Lock()
		for len(q.entries) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.entries) == 0 && q.closed {
			q.mu.Unlock()
			return
		}
		n := len(q.entries)
		if n > batchSize {
			n = batchSize
		}
		batch := append([]logEntry(nil), q.entries[:n]...)
		q.mu.Unlock()

		if err := q.insertBatch(ctx, batch); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "error persistiendo logs en PostgreSQL: %v\n", err)
			timer := time.NewTimer(retryInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}

		q.mu.Lock()
		q.entries = q.entries[n:]
		q.mu.Unlock()
	}
}

func (q *queue) insertBatch(parent context.Context, entries []logEntry) error {
	ctx, cancel := context.WithTimeout(parent, insertTimeout)
	defer cancel()

	var query strings.Builder
	query.WriteString("INSERT INTO system_logs (id, occurred_at, level, message, source, attributes) VALUES ")
	args := make([]any, 0, len(entries)*6)
	for i, entry := range entries {
		if i > 0 {
			query.WriteByte(',')
		}
		base := i*6 + 1
		fmt.Fprintf(&query, "($%d,$%d,$%d,$%d,$%d,$%d::jsonb)", base, base+1, base+2, base+3, base+4, base+5)
		args = append(args, entry.id, entry.occurredAt, entry.level, entry.message, entry.source, string(entry.attributes))
	}
	query.WriteString(" ON CONFLICT (id) DO NOTHING")
	if _, err := q.db.ExecContext(ctx, query.String(), args...); err != nil {
		return fmt.Errorf("insertar lote de %d logs: %w", len(entries), err)
	}
	return nil
}

func addAttr(dst map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	key := attr.Key
	if key == "" {
		key = "value"
	}
	if isSensitiveKey(key) {
		dst[strings.Join(append(append([]string(nil), groups...), key), ".")] = "[REDACTED]"
		return
	}

	value := attr.Value
	if value.Kind() == slog.KindGroup {
		nextGroups := append(append([]string(nil), groups...), key)
		for _, child := range value.Group() {
			addAttr(dst, nextGroups, child)
		}
		return
	}

	fullKey := strings.Join(append(append([]string(nil), groups...), key), ".")
	switch value.Kind() {
	case slog.KindString:
		dst[fullKey] = value.String()
	case slog.KindBool:
		dst[fullKey] = value.Bool()
	case slog.KindInt64:
		dst[fullKey] = value.Int64()
	case slog.KindUint64:
		dst[fullKey] = value.Uint64()
	case slog.KindFloat64:
		number := value.Float64()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			dst[fullKey] = fmt.Sprint(number)
		} else {
			dst[fullKey] = number
		}
	case slog.KindDuration:
		dst[fullKey] = value.Duration().String()
	case slog.KindTime:
		dst[fullKey] = value.Time()
	case slog.KindAny:
		dst[fullKey] = redactAny(key, value.Any())
	default:
		dst[fullKey] = value.String()
	}
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, token := range []string{"password", "secret", "api_key", "apikey", "token", "authorization", "cookie"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func redactAny(key string, value any) any {
	if isSensitiveKey(key) {
		return "[REDACTED]"
	}
	switch v := value.(type) {
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	default:
		payload, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		var normalized any
		if err := json.Unmarshal(payload, &normalized); err != nil {
			return fmt.Sprint(v)
		}
		return redactJSON(normalized)
	}
}

func redactJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if isSensitiveKey(key) {
				v[key] = "[REDACTED]"
			} else {
				v[key] = redactJSON(child)
			}
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = redactJSON(child)
		}
		return v
	default:
		return v
	}
}
