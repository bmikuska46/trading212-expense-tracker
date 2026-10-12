package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"budget-tracking/backend/migrations"
)

func (s *Store) migrate(ctx context.Context) error {
	return applyMigrations(ctx, s.DB, migrations.Files)
}

// All pending migrations and their version records commit together. The driver
// starts an IMMEDIATE transaction so concurrent startup cannot race the version check.
func applyMigrations(ctx context.Context, db *sql.DB, files fs.FS) error {
	names, err := fs.Glob(files, "*.sql")
	if err != nil || len(names) == 0 {
		return errors.New("no database migrations available")
	}
	slices.Sort(names)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot start database migration")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
	) STRICT`); err != nil {
		return errors.New("cannot initialize migration tracking")
	}
	rows, err := tx.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return errors.New("cannot read migration versions")
	}
	type applied struct{ name, checksum string }
	history := make(map[int]applied)
	for rows.Next() {
		var version int
		var record applied
		if err := rows.Scan(&version, &record.name, &record.checksum); err != nil {
			rows.Close()
			return errors.New("cannot read migration history")
		}
		history[version] = record
		if version != len(history) {
			rows.Close()
			return errors.New("database migration history is not consecutive")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("cannot read migration history")
	}
	for index, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version != index+1 {
			return errors.New("migration versions must be consecutive starting at 1")
		}
		content, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("cannot read migration %d", version)
		}
		sum := sha256.Sum256(content)
		checksum := hex.EncodeToString(sum[:])
		if record, exists := history[version]; exists {
			if record.name != name || record.checksum != checksum {
				return fmt.Errorf("applied migration %d has changed", version)
			}
			delete(history, version)
			continue
		}
		// Do not expose SQL or driver errors, which may contain stored private values.
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("database migration %d failed; pending changes rolled back", version)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)", version, name, checksum); err != nil {
			return fmt.Errorf("cannot record migration %d", version)
		}
	}
	if len(history) != 0 {
		return errors.New("database contains migrations unknown to this application")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit database migrations")
	}
	return nil
}
