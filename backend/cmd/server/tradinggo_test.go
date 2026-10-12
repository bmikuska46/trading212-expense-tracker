package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
)

func testStore(t *testing.T) *database.Store {
	t.Helper()
	directory := t.TempDir()
	keyFile := filepath.Join(directory, "key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(context.Background(), filepath.Join(directory, "budget.db"), keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	return store
}

// legacyDatabase writes a trading-go database with only the columns the
// import reads.
func legacyDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trading.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	latest := time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)
	years := latest.Sub(time.Date(2024, 2, 22, 0, 0, 0, 0, time.UTC)).Hours() / 24 / 365.25
	statements := []string{
		`CREATE TABLE account_snapshots (id INTEGER PRIMARY KEY AUTOINCREMENT, fetched_at TEXT, account_id INTEGER,
			currency TEXT, total_value REAL, current_value REAL, total_cost REAL, unrealized_pnl REAL, realized_pnl REAL,
			cash_available REAL, cash_in_pies REAL, cash_reserved REAL, years_elapsed REAL)`,
		`CREATE TABLE pie_snapshots (id INTEGER PRIMARY KEY AUTOINCREMENT, fetched_at TEXT, pie_id INTEGER, invested REAL,
			value REAL, result REAL, cash REAL, dividends_gained REAL, dividends_reinvested REAL, dividends_in_cash REAL)`,
		`CREATE TABLE pies (id INTEGER PRIMARY KEY, name TEXT, name_updated_at TEXT)`,
		`CREATE TABLE categories (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`,
		`CREATE TABLE category_pies (category_id INTEGER, pie_id INTEGER)`,
		`INSERT INTO account_snapshots (fetched_at, account_id, currency, total_value, current_value, total_cost,
			unrealized_pnl, realized_pnl, cash_available, cash_in_pies, cash_reserved, years_elapsed) VALUES
			('2026-09-29T00:00:00Z', 42, 'EUR', 1000.005, 990, 900, 90, -1.005, 10, 0, 0, 2.5),
			('2026-09-29T01:00:00Z', 42, 'EUR', 1010.5, 1000, 900, 100, 0, 10.5, 0, 0, 2.5),
			('2026-09-29T01:00:00Z', 42, 'EUR', 9999, 0, 0, 0, 0, 0, 0, 0, 2.5),
			('2026-09-29T02:00:00Z', 42, 'EUR', 1020, 1010, 900, 110, 0, 10, 0, 0, ?)`,
		`INSERT INTO pie_snapshots (fetched_at, pie_id, invested, value, result, cash, dividends_gained,
			dividends_reinvested, dividends_in_cash) VALUES
			('2026-09-29T00:00:00Z', 7, 500, 510.125, 10.125, 0.5, 1.25, 1, 0.25),
			('2026-09-29T00:00:00Z', 8, 400, 390, -10, 0, 0, 0, 0),
			('2026-09-29T02:00:00Z', 7, 500, 520, 20, 0, 1.25, 1, 0.25),
			('2026-09-28T00:00:00Z', 9, 1, 1, 0, 0, 0, 0, 0)`,
		`INSERT INTO pies VALUES (7, 'Main ETF', '2026-09-29T00:00:00Z'), (8, 'Bi-Weekly', '2026-09-29T00:00:00Z')`,
		`INSERT INTO categories (name) VALUES ('Main Pies'), ('Empty')`,
		`INSERT INTO category_pies VALUES (1, 7), (1, 8)`,
	}
	for _, statement := range statements {
		var args []any
		if strings.Contains(statement, "?") {
			args = append(args, years)
		}
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

type importedSnapshot struct {
	id    int64
	at    string
	total int64
}

func importedSnapshots(t *testing.T, store *database.Store) []importedSnapshot {
	t.Helper()
	rows, err := store.DB.Query(`SELECT id, fetched_at, total_value_minor FROM investing_snapshots ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var snapshots []importedSnapshot
	for rows.Next() {
		var snapshot importedSnapshot
		if err := rows.Scan(&snapshot.id, &snapshot.at, &snapshot.total); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

func importLegacy(t *testing.T, store *database.Store, path string) tradingGoResult {
	t.Helper()
	ctx := context.Background()
	history, err := readTradingGo(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := importTradingGo(ctx, store, history)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestImportTradingGoIntoEmptyStore(t *testing.T) {
	store := testStore(t)
	path := legacyDatabase(t)
	result := importLegacy(t, store, path)
	want := tradingGoResult{Snapshots: 3, PieSnapshots: 3, Names: 2, Groups: 2, StartedOn: "2024-02-22"}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	// The first of two snapshots at one time is kept, and amounts round half
	// away from zero.
	snapshots := importedSnapshots(t, store)
	if len(snapshots) != 3 || snapshots[0] != (importedSnapshot{1, "2026-09-29T00:00:00Z", 100001}) ||
		snapshots[1] != (importedSnapshot{2, "2026-09-29T01:00:00Z", 101050}) || snapshots[2].id != 3 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	var realized, value, account string
	if err := store.DB.QueryRow(`SELECT realized_minor, account_id FROM investing_snapshots WHERE id = 1`).Scan(&realized, &account); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT value_minor FROM investing_pie_snapshots WHERE snapshot_id = 1 AND pie_id = 7`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if realized != "-101" || account != "42" || value != "51013" {
		t.Fatalf("realized = %s, account = %s, pie value = %s", realized, account, value)
	}

	ctx := context.Background()
	pies, err := store.Queries.ListInvestingPies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pies) != 2 || pies[0].Name.String != "Bi-Weekly" && pies[1].Name.String != "Bi-Weekly" {
		t.Fatalf("pies = %+v", pies)
	}
	groups, err := store.Queries.ListInvestingGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	members, err := store.Queries.ListInvestingGroupPies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || len(members) != 2 {
		t.Fatalf("groups = %+v, members = %+v", groups, members)
	}

	if again := importLegacy(t, store, path); again != (tradingGoResult{Skipped: 3}) {
		t.Fatalf("second import = %+v", again)
	}
	if len(importedSnapshots(t, store)) != 3 {
		t.Fatal("the second import added snapshots")
	}
}

func TestImportTradingGoBeforeSavedSnapshots(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	q := store.Queries
	// This app took its first snapshot at the legacy database's second hour.
	id, err := q.CreateInvestingSnapshot(ctx, dbgen.CreateInvestingSnapshotParams{FetchedAt: "2026-09-29T01:00:00Z",
		AccountID: "42", Currency: "EUR", TotalValueMinor: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.EnsureInvestingPie(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if err := q.SetInvestingPieName(ctx, dbgen.SetInvestingPieNameParams{ID: 7,
		Name: sql.NullString{String: "Renamed", Valid: true}, CheckedAt: sql.NullString{String: "2026-09-29T01:00:00Z", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateInvestingPieSnapshot(ctx, dbgen.CreateInvestingPieSnapshotParams{SnapshotID: id, PieID: 7, ValueMinor: 3}); err != nil {
		t.Fatal(err)
	}
	group, err := q.CreateInvestingGroup(ctx, dbgen.CreateInvestingGroupParams{Name: "main pies", NormalizedName: "main pies"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AddInvestingGroupPie(ctx, dbgen.AddInvestingGroupPieParams{GroupID: group, PieID: 7}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetInvestingStart(ctx, sql.NullString{String: "2024-01-01", Valid: true}); err != nil {
		t.Fatal(err)
	}

	result := importLegacy(t, store, legacyDatabase(t))
	want := tradingGoResult{Snapshots: 1, Skipped: 2, PieSnapshots: 2, Names: 1, Groups: 1}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	// The saved snapshot moves after the imported one and keeps its pies.
	snapshots := importedSnapshots(t, store)
	if len(snapshots) != 2 || snapshots[0] != (importedSnapshot{1, "2026-09-29T00:00:00Z", 100001}) ||
		snapshots[1].at != "2026-09-29T01:00:00Z" || snapshots[1].total != 7 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	var value int64
	if err := store.DB.QueryRow(`SELECT value_minor FROM investing_pie_snapshots WHERE snapshot_id = ? AND pie_id = 7`,
		snapshots[1].id).Scan(&value); err != nil || value != 3 {
		t.Fatalf("saved pie snapshot value = %d, %v", value, err)
	}
	var problems int
	if err := store.DB.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&problems); err != nil || problems != 0 {
		t.Fatalf("foreign key problems = %d, %v", problems, err)
	}

	pies, err := q.ListInvestingPies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pie := range pies {
		if pie.ID == 7 && pie.Name.String != "Renamed" || pie.ID == 8 && pie.Name.String != "Bi-Weekly" {
			t.Fatalf("pies = %+v", pies)
		}
	}
	groups, err := q.ListInvestingGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	members, err := q.ListInvestingGroupPies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].ID != group && groups[1].ID != group || len(members) != 2 {
		t.Fatalf("groups = %+v, members = %+v", groups, members)
	}
	state, err := q.GetInvestingState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.StartedOn.String != "2024-01-01" {
		t.Fatalf("started on = %s", state.StartedOn.String)
	}
}
