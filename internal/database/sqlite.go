package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"remotebash/internal/models"
)

// Open opens (or creates) the SQLite database at the given path,
// applies schema migrations, and returns the *sql.DB handle.
func Open(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Connection pool settings — SQLite is single-writer so keep these tight.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// migrate runs all schema DDL using IF NOT EXISTS so it is safe to
// call on every startup.
func migrate(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS clients (
		name TEXT PRIMARY KEY,
		host TEXT NOT NULL,
		port INTEGER NOT NULL DEFAULT 22,
		"user" TEXT NOT NULL DEFAULT 'root',
		password TEXT NOT NULL DEFAULT '',
		label TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		safe_rm INTEGER NOT NULL DEFAULT 0,
		via TEXT DEFAULT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client_name TEXT NOT NULL REFERENCES clients(name),
		command TEXT NOT NULL,
		output TEXT NOT NULL DEFAULT '',
		exit_code INTEGER NOT NULL DEFAULT 0,
		cwd TEXT NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0,
		success INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_audit_client ON audit_log(client_name);
	CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);

	CREATE TABLE IF NOT EXISTS server_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		level TEXT NOT NULL DEFAULT 'INFO',
		message TEXT NOT NULL DEFAULT '',
		attrs TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_log_level ON server_log(level);
	CREATE INDEX IF NOT EXISTS idx_log_created ON server_log(created_at);
	`
	_, err := db.Exec(schema)
	return err
}

// ── Client CRUD ────────────────────────────────────────────────────────

// allowedColumns is the set of updatable client columns.
var allowedColumns = map[string]bool{
	"host": true, "port": true, "user": true, "password": true,
	"enabled": true, "safe_rm": true, "via": true,
}

// LoadClients returns all persisted clients ordered by creation time.
func LoadClients(db *sql.DB) ([]models.Client, error) {
	rows, err := db.Query(`SELECT name, host, port, "user", password, label,
		enabled, safe_rm, via, created_at, updated_at
		FROM clients ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []models.Client
	for rows.Next() {
		var c models.Client
		var via sql.NullString
		var label sql.NullString
		if err := rows.Scan(&c.Name, &c.Host, &c.Port, &c.User, &c.Password,
			&label, &c.Enabled, &c.SafeRm, &via, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Label = label.String
		c.Via = via.String
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

// InsertClient persists a new client configuration.
func InsertClient(db *sql.DB, c *models.Client) error {
	_, err := db.Exec(`INSERT INTO clients (name, host, port, "user", password,
		label, enabled, safe_rm, via)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Host, c.Port, c.User, c.Password,
		c.Label, boolToInt(c.Enabled), boolToInt(c.SafeRm), nullString(c.Via),
	)
	return err
}

// UpdateClient persists changes to an existing client.
// fields is a map of column → value. Only allowedColumns are accepted.
func UpdateClient(db *sql.DB, name string, fields map[string]any) error {
	var cols []string
	vals := make([]any, 0)
	for k, v := range fields {
		if !allowedColumns[k] {
			continue
		}
		cols = append(cols, `"`+k+`"=?`)
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return nil
	}
	vals = append(vals, name)

	query := "UPDATE clients SET " + strings.Join(cols, ", ") + ", updated_at=datetime('now') WHERE name=?"
	_, err := db.Exec(query, vals...)
	return err
}

// DeleteClient removes a client by name.
func DeleteClient(db *sql.DB, name string) error {
	_, err := db.Exec("DELETE FROM clients WHERE name=?", name)
	return err
}

// ── Audit ──────────────────────────────────────────────────────────────

// InsertAudit writes a command audit entry.
func InsertAudit(db *sql.DB, clientName, command, output string, exitCode int,
	cwd string, durationMs int, success bool) error {
	_, err := db.Exec(`INSERT INTO audit_log (client_name, command, output,
		exit_code, cwd, duration_ms, success, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		clientName, command, output, exitCode, cwd, durationMs, boolToInt(success),
	)
	return err
}

// QueryAudit returns paginated audit entries with optional filters.
func QueryAudit(db *sql.DB, clientName *string, after, before *string,
	limit, offset int) ([]models.AuditEntry, error) {

	conditions, args := buildAuditConditions(clientName, after, before)
	query := "SELECT id, client_name, command, output, exit_code, cwd, duration_ms, success, created_at FROM audit_log"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries = make([]models.AuditEntry, 0)
	for rows.Next() {
		var e models.AuditEntry
		if err := rows.Scan(&e.ID, &e.ClientName, &e.Command, &e.Output,
			&e.ExitCode, &e.Cwd, &e.DurationMs, &e.Success, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// CountAudit returns the total number of matching audit entries.
func CountAudit(db *sql.DB, clientName *string, after, before *string) (int, error) {
	conditions, args := buildAuditConditions(clientName, after, before)
	query := "SELECT COUNT(*) FROM audit_log"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	var count int
	err := db.QueryRow(query, args...).Scan(&count)
	return count, err
}

func buildAuditConditions(clientName *string, after, before *string) ([]string, []any) {
	var conditions []string
	var args []any
	if clientName != nil && *clientName != "" {
		conditions = append(conditions, "client_name=?")
		args = append(args, *clientName)
	}
	if after != nil && *after != "" {
		conditions = append(conditions, "created_at>=?")
		args = append(args, isoToSQLite(*after))
	}
	if before != nil && *before != "" {
		conditions = append(conditions, "created_at<?")
		args = append(args, isoToSQLite(*before))
	}
	return conditions, args
}
func DeleteAuditByID(db *sql.DB, id int) (int64, error) {
	r, err := db.Exec("DELETE FROM audit_log WHERE id=?", id)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// DeleteAuditByIDs deletes audit entries by a list of IDs.
func DeleteAuditByIDs(db *sql.DB, ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := ""
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	r, err := db.Exec("DELETE FROM audit_log WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// DeleteAuditByClient deletes all audit entries for a client.
func DeleteAuditByClient(db *sql.DB, clientName string) (int64, error) {
	r, err := db.Exec("DELETE FROM audit_log WHERE client_name=?", clientName)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// DeleteAuditBeforeID deletes all audit entries with id < beforeID.
func DeleteAuditBeforeID(db *sql.DB, beforeID int) (int64, error) {
	r, err := db.Exec("DELETE FROM audit_log WHERE id < ?", beforeID)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ── Server Log ─────────────────────────────────────────────────────────

// InsertLog writes a server log entry to the database.
func InsertLog(db *sql.DB, level, message, attrs string) error {
	_, err := db.Exec(`INSERT INTO server_log (level, message, attrs, created_at)
		VALUES (?, ?, ?, datetime('now'))`, level, message, attrs)
	return err
}

// QueryLog returns paginated log entries with optional level and time filters.
func QueryLog(db *sql.DB, level *string, after, before *string,
	limit, offset int) ([]models.LogEntry, error) {

	conditions, args := buildLogConditions(level, after, before)
	query := "SELECT id, level, message, attrs, created_at FROM server_log"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries = make([]models.LogEntry, 0)
	for rows.Next() {
		var e models.LogEntry
		if err := rows.Scan(&e.ID, &e.Level, &e.Message, &e.Attrs, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// CountLog returns the total number of matching log entries.
func CountLog(db *sql.DB, level *string, after, before *string) (int, error) {
	conditions, args := buildLogConditions(level, after, before)
	query := "SELECT COUNT(*) FROM server_log"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	var count int
	err := db.QueryRow(query, args...).Scan(&count)
	return count, err
}

func buildLogConditions(level *string, after, before *string) ([]string, []any) {
	var conditions []string
	var args []any
	if level != nil && *level != "" {
		conditions = append(conditions, "level=?")
		args = append(args, *level)
	}
	if after != nil && *after != "" {
		conditions = append(conditions, "created_at>=?")
		args = append(args, isoToSQLite(*after))
	}
	if before != nil && *before != "" {
		conditions = append(conditions, "created_at<?")
		args = append(args, isoToSQLite(*before))
	}
	return conditions, args
}

// DeleteLogBeforeID deletes all log entries with id < beforeID.
func DeleteLogBeforeID(db *sql.DB, beforeID int) (int64, error) {
	r, err := db.Exec("DELETE FROM server_log WHERE id < ?", beforeID)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ── Helpers ────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isoToSQLite normalizes ISO 8601 to SQLite datetime format.
// "2026-06-12T11:30:00.123Z" → "2026-06-12 11:30:00"
func isoToSQLite(iso string) string {
	if len(iso) >= 19 && iso[10] == 'T' {
		return iso[:10] + " " + iso[11:19]
	}
	return iso
}
