package database

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"budget-tracking/backend/migrations"
)

func migrationFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		content, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: content}
	}
	return files
}

// migrationCount is the number of embedded migrations a current database records.
func migrationCount(t *testing.T) int {
	t.Helper()
	return len(migrationFiles(t))
}

func migrationName(version int, name string) string {
	return fmt.Sprintf("%06d_%s.sql", version, name)
}

func TestPendingMigrationsRollbackTogetherAndRetry(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	files := migrationFiles(t)
	current := len(files)
	valid, invalid := migrationName(current+1, "valid"), migrationName(current+2, "invalid")
	files[valid] = &fstest.MapFile{Data: []byte(`
		CREATE TABLE rollback_probe (value TEXT) STRICT;
		INSERT INTO rollback_probe VALUES ('synthetic-private-migration-marker');
		UPDATE runtime_meta SET created_at = 'should-roll-back';
	`)}
	files[invalid] = &fstest.MapFile{Data: []byte(`
		CREATE TABLE partial_probe (value TEXT) STRICT;
		SELECT synthetic_private_migration_error();
	`)}
	created, err := store.Queries.GetRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = applyMigrations(ctx, store.DB, files)
	if err == nil || strings.Contains(err.Error(), "synthetic_private") {
		t.Fatalf("expected sanitized migration error, got %v", err)
	}
	var tables, versions int
	if err := store.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('rollback_probe', 'partial_probe')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("partial schema survived: %d, %v", tables, err)
	}
	if err := store.DB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&versions); err != nil || versions != current {
		t.Fatalf("partial version history survived: %d, %v", versions, err)
	}
	actual, err := store.Queries.GetRuntime(ctx)
	if err != nil || actual != created {
		t.Fatal("failed migration changed existing data")
	}
	files[invalid] = &fstest.MapFile{Data: []byte("CREATE TABLE completed_probe (id INTEGER PRIMARY KEY) STRICT;")}
	if err := applyMigrations(ctx, store.DB, files); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, store.DB, files); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
	if err := store.DB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&versions); err != nil || versions != current+2 {
		t.Fatalf("retry did not record all versions: %d, %v", versions, err)
	}
}

func TestMigrationsRejectChangedUnknownOrMissingVersions(t *testing.T) {
	for _, scenario := range []string{"changed", "unknown", "gap"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, _ := testStore(t)
			files := migrationFiles(t)
			switch scenario {
			case "changed":
				files["000001_runtime.sql"].Data = append(files["000001_runtime.sql"].Data, []byte("\n-- changed\n")...)
			case "unknown":
				// The database records the newest migration, which this build no longer has.
				names, _ := fs.Glob(files, "*.sql")
				slices.Sort(names)
				delete(files, names[len(names)-1])
			case "gap":
				if _, err := store.DB.Exec("DELETE FROM schema_migrations WHERE version = 2"); err != nil {
					t.Fatal(err)
				}
			}
			if err := applyMigrations(context.Background(), store.DB, files); err == nil {
				t.Fatal("incompatible migration history accepted")
			}
		})
	}
}

func runtimeOnlyDatabase(t *testing.T, store *Store) {
	t.Helper()
	// Recreate the milestone 1 database on the existing keyed connection.
	// Turning off foreign keys is limited to this test fixture construction.
	if _, err := store.DB.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.DB.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name != 'runtime_meta' AND name NOT LIKE 'sqlite~_%' ESCAPE '~'")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, name := range names {
		if _, err := store.DB.Exec(`DROP TABLE "` + name + `"`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptRuntimeOnlyDatabase(t *testing.T) {
	store, path, keyFile := testStore(t)
	ctx := context.Background()
	runtimeOnlyDatabase(t, store)
	created, err := store.Queries.GetRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.DB.Close()
	reopened, err := Open(ctx, path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	actual, err := reopened.Queries.GetRuntime(ctx)
	if err != nil || actual != created {
		t.Fatal("upgrade changed milestone 1 runtime metadata")
	}
	var versions int
	if err := reopened.DB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&versions); err != nil || versions != migrationCount(t) {
		t.Fatalf("legacy upgrade: %d, %v", versions, err)
	}
}

func TestTypeSafeMigrationDropsTheOpenAIKey(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	files := migrationFiles(t)
	typeSafe := migrationName(11, "typesafe")
	// Rebuild the table as it was before the migration, drop what later
	// migrations added, then apply them again.
	previous := strings.NewReplacer("provider != 'openai'", "1", "'typesafe'", "'openai'").Replace(string(files[typeSafe].Data))
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{previous, "DROP TABLE category_suggestions", "DROP TABLE chat_messages", "DROP TABLE chats",
		"DROP TABLE investment_income", "DROP TABLE nbs_rates", "DROP TABLE nbs_rate_files", "DROP TABLE investment_income_state",
		"DROP TABLE sessions", "DROP TABLE users", `CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, owner_id INTEGER NOT NULL REFERENCES owner(id),
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL) STRICT`,
		"ALTER TABLE owner DROP COLUMN dividend_tax_percent", "ALTER TABLE owner DROP COLUMN interest_tax_percent",
		"ALTER TABLE owner DROP COLUMN cashback_tax_percent", "DELETE FROM schema_migrations WHERE version >= 11"} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execSQL(t, store, `INSERT INTO integrations (provider, credentials_json, enabled) VALUES
		('trading212', '{}', 1), ('openai', '{"apiKey":"synthetic-openai-key"}', 1), ('resend', '{}', 0)`)
	execSQL(t, store, "INSERT INTO accounts (name, currency, integration_provider, external_account_id) VALUES ('Broker', 'EUR', 'trading212', 'synthetic-1')")
	execSQL(t, store, "UPDATE integrations SET source_account_id = (SELECT id FROM accounts) WHERE provider = 'trading212'")
	execSQL(t, store, `INSERT INTO notification_config (id, sender, recipient, application_url)
		VALUES (1, 'budget@example.com', 'owner@example.com', 'http://localhost')`)

	if err := applyMigrations(ctx, store.DB, files); err != nil {
		t.Fatal(err)
	}
	var providers string
	if err := store.DB.QueryRow("SELECT group_concat(provider, ',') FROM (SELECT provider FROM integrations ORDER BY provider)").Scan(&providers); err != nil || providers != "resend,trading212" {
		t.Fatalf("providers: %q %v", providers, err)
	}
	var linked, violations int
	store.DB.QueryRow("SELECT count(*) FROM integrations WHERE source_account_id IS NOT NULL").Scan(&linked)
	store.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations)
	if linked != 1 || violations != 0 {
		t.Fatalf("linked %d, foreign key violations %d", linked, violations)
	}
	execSQL(t, store, "INSERT INTO integrations (provider, credentials_json) VALUES ('typesafe', '{}')")
	if _, err := store.DB.Exec("INSERT INTO integrations (provider, credentials_json) VALUES ('openai', '{}')"); err == nil {
		t.Fatal("the openai provider must no longer be accepted")
	}
}
