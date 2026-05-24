// Package db owns the SQLite connection, schema migrations, and pragmas.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlitelib "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	pingMaxAttempts = 50 // ~5s ceiling, above busy_timeout, for cold-start WAL-init races
	pingRetryDelay  = 100 * time.Millisecond
)

// DB wraps a *sql.DB configured for clio.
type DB struct {
	*sql.DB
	path string
}

// Open opens (creating if needed) the SQLite database at path, applies pragmas
// for multi-process concurrency, and runs migrations. The database file is
// created with 0600 permissions.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	var pingErr error
	for attempt := 0; attempt < pingMaxAttempts; attempt++ {
		pingErr = sqlDB.Ping()
		if pingErr == nil || !isBusyErr(pingErr) {
			break
		}
		time.Sleep(pingRetryDelay)
	}
	if pingErr != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", pingErr)
	}
	// Serialize writes within this process (watcher + catch-up ingest share the
	// pool); WAL still allows the read-only pool to read concurrently.
	sqlDB.SetMaxOpenConns(1)

	// Tighten permissions on the freshly created file.
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		sqlDB.Close()
		return nil, fmt.Errorf("chmod db: %w", err)
	}

	d := &DB{DB: sqlDB, path: path}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// OpenReadOnly opens the database without running migrations, for query-only use
// (e.g. CLI deferring to a running MCP writer).
func OpenReadOnly(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)&mode=ro"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite (ro): %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite (ro): %w", err)
	}
	return &DB{DB: sqlDB, path: path}, nil
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

func (d *DB) migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		applied, err := d.migrationApplied(name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		stmts, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := d.applyMigration(name, string(stmts)); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) migrationApplied(name string) (bool, error) {
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("check migration %s: %w", name, err)
	}
	return n > 0, nil
}

// isBusyErr reports whether err is a SQLite "database is locked" / SQLITE_BUSY
// error as returned by modernc.org/sqlite. busy_timeout(3000) in the DSN
// covers normal write-lock contention but the one-time WAL-mode conversion on
// a brand-new file can still surface SQLITE_BUSY to Ping(); this helper lets
// the caller retry at the Go level.
func isBusyErr(err error) bool {
	if err == nil {
		return false
	}
	var se *sqlitelib.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xFF { // mask extended result codes to the primary code
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	// Fallback for wrapped/other error shapes.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy")
}

// EscapeLike escapes %, _, and \ for use in a LIKE pattern with ESCAPE '\'.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// applyMigration runs one migration inside an IMMEDIATE transaction. It
// re-checks the marker inside the transaction so a concurrent process that
// already applied it is a clean no-op rather than a primary-key failure.
func (d *DB) applyMigration(name, stmts string) error {
	tx, err := d.Begin() // IMMEDIATE via _txlock=immediate
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
		return fmt.Errorf("recheck migration %s: %w", name, err)
	}
	if n > 0 {
		return tx.Commit() // applied by someone else while we waited for the lock
	}
	if _, err := tx.Exec(stmts); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(name, applied_at) VALUES (?, strftime('%s','now'))`, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return tx.Commit()
}
