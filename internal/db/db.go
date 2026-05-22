// Package db owns the SQLite connection, schema migrations, and pragmas.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

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
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	// Serialize writes within this process (watcher + catch-up ingest share the
	// pool); WAL still allows the read-only pool to read concurrently.
	sqlDB.SetMaxOpenConns(1)

	// Tighten permissions on the freshly created file.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
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
		var exists int
		if err := d.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}
		stmts, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := d.Exec(string(stmts)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := d.Exec(`INSERT INTO schema_migrations(name, applied_at) VALUES (?, strftime('%s','now'))`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}
