package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// dbRecord is a log entry queued for async database insert.
type dbRecord struct {
	Level   string
	Message string
	Attrs   string
	Time    time.Time
}

// DBHandler is a slog.Handler that writes to both stderr (text) and
// a SQLite database (async via buffered channel).  Dropped on full.
// Safe to use after Shutdown — database writes are silently skipped.
type DBHandler struct {
	db     *sql.DB
	text   slog.Handler
	ch     chan dbRecord
	wg     sync.WaitGroup
	closed atomic.Bool
}

// NewDBHandler creates a DBHandler. Call Shutdown() before closing the
// database to drain remaining records.
func NewDBHandler(db *sql.DB, level slog.Level) *DBHandler {
	text := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	h := &DBHandler{
		db:   db,
		text: text,
		ch:   make(chan dbRecord, 256),
	}
	h.wg.Add(1)
	go h.worker()
	return h
}

// worker drains the channel and inserts records into the database.
func (h *DBHandler) worker() {
	defer h.wg.Done()
	for r := range h.ch {
		// Best-effort insert; ignore errors to avoid log loops.
		_ = InsertLog(h.db, r.Level, r.Message, r.Attrs)
	}
}

// Shutdown closes the channel and waits for pending inserts to complete.
// After Shutdown, Handle() still writes to stderr but silently skips the
// database insert (no panic).
func (h *DBHandler) Shutdown() {
	h.closed.Store(true)
	close(h.ch)
	h.wg.Wait()
}

// Enabled reports whether the handler is enabled for the given level.
func (h *DBHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.text.Enabled(nil, level)
}

// Handle writes the record to stderr and queues it for database insertion.
// If Shutdown has been called the database write is silently skipped —
// this avoids a panic when send on a closed channel.
func (h *DBHandler) Handle(_ context.Context, r slog.Record) error {
	// Always write to stderr first (synchronous).
	if err := h.text.Handle(nil, r); err != nil {
		return err
	}

	// If the handler has been shut down, don't attempt to send on the
	// closed channel — it would panic.
	if h.closed.Load() {
		return nil
	}

	// Extract attrs as JSON for the database.
	attrs := recordAttrs(r)

	rec := dbRecord{
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
		Time:    r.Time,
	}

	// Non-blocking send — drop if the channel is full to avoid
	// blocking the application on database writes.
	select {
	case h.ch <- rec:
	default:
		// Channel full: log dropped. We intentionally do NOT log
		// this to stderr to avoid cascading log storms.
	}
	return nil
}

// WithAttrs returns a new handler with the given attrs baked in.
func (h *DBHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &DBHandler{
		db:   h.db,
		text: h.text.WithAttrs(attrs),
		ch:   h.ch, // share the same channel
	}
}

// WithGroup returns a new handler with the given group name.
func (h *DBHandler) WithGroup(name string) slog.Handler {
	return &DBHandler{
		db:   h.db,
		text: h.text.WithGroup(name),
		ch:   h.ch,
	}
}

// recordAttrs converts the record's attributes to a compact JSON string.
func recordAttrs(r slog.Record) string {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	if len(attrs) == 0 {
		return ""
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		return ""
	}
	return string(b)
}

// Ensure DBHandler implements slog.Handler.
var _ slog.Handler = (*DBHandler)(nil)
