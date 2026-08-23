package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrate brings db up to date by applying every embedded migrations/*.sql
// file that isn't yet recorded in schema_migrations, in filename order
// (0001_..., 0002_..., ...), each inside its own transaction. This is
// swarmdash's whole migration story for SQLiteStore: forward-only, plain
// .sql files checked into internal/store/migrations, no down migrations -
// add a new NNNN_description.sql file (next number, never edit a past one)
// whenever the schema needs to change in a future release, and this runs it
// automatically the next time swarmdash starts against that data directory.
func migrate(db *sql.DB) error {
	return migrateFS(db, migrationFiles, "migrations")
}

// migrateFS does the actual work; migrate calls it against the real
// embedded migrations, and tests call it against a synthetic fs.FS to
// exercise "a new version ships a migration" without touching the real
// migrations directory.
func migrateFS(db *sql.DB, fsys fs.FS, root string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version     INTEGER PRIMARY KEY,
		applied_at  DATETIME NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	rows.Close()

	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		name := entry.Name()
		version, err := migrationVersion(name)
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if applied[version] {
			continue
		}

		script, err := fs.ReadFile(fsys, root+"/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(script)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

// migrationVersion extracts the leading "NNNN" from a "NNNN_description.sql"
// filename.
func migrationVersion(filename string) (int, error) {
	base := strings.TrimSuffix(filename, ".sql")
	prefix, _, _ := strings.Cut(base, "_")
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("filename must start with a numeric version, got %q", filename)
	}
	return version, nil
}
