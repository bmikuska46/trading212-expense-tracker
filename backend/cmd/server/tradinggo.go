package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"budget-tracking/backend/internal/config"
	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

// The trading-go dashboard recorded the same Trading 212 account and pies
// before this app did. Its history is imported once, so the Investing page
// starts with it. trading-go stored decimals as floats; the shortest text of
// each float is the decimal Trading 212 sent, which is then rounded like a
// live snapshot.

type tradingGoSnapshot struct {
	at       time.Time
	account  string
	currency string
	// Total, investments, cost, unrealized, realized, cash available, in
	// pies, and reserved.
	amounts [8]float64
	pies    []tradingGoPie
}

type tradingGoPie struct {
	id int64
	// Invested, value, result, cash, and dividends gained, reinvested, and
	// in cash.
	amounts [7]float64
}

type tradingGoHistory struct {
	snapshots []tradingGoSnapshot
	names     map[int64]string
	groups    map[string][]int64
	// startedOn is the investment start trading-go measured its yearly
	// return from.
	startedOn string
}

// tradingGoResult counts what an import added. Skipped snapshots are at or
// after the first snapshot this app took, which already covers them.
type tradingGoResult struct {
	Snapshots, Skipped, PieSnapshots, Names, Groups int
	StartedOn                                       string
}

// importTradingGoFile opens the configured database and imports a copy of
// the trading-go database at path.
func importTradingGoFile(path string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := database.Open(ctx, cfg.DatabasePath, cfg.KeyFile)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	return runTradingGoImport(ctx, store, path)
}

func runTradingGoImport(ctx context.Context, store *database.Store, path string) error {
	history, err := readTradingGo(ctx, path)
	if err != nil {
		return err
	}
	result, err := importTradingGo(ctx, store, history)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %d snapshots with %d pie snapshots, skipped %d already covered, named %d pies, added %d groups.\n",
		result.Snapshots, result.PieSnapshots, result.Skipped, result.Names, result.Groups)
	if result.StartedOn != "" {
		fmt.Printf("Set the first deposit to %s.\n", result.StartedOn)
	}
	return nil
}

// readTradingGo reads a copy of the trading-go database without changing it.
func readTradingGo(ctx context.Context, path string) (tradingGoHistory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return tradingGoHistory{}, err
	}
	if _, err := os.Stat(absolute); err != nil {
		return tradingGoHistory{}, errors.New("the trading-go database does not exist")
	}
	db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String())
	if err != nil {
		return tradingGoHistory{}, err
	}
	defer db.Close()
	history := tradingGoHistory{names: map[int64]string{}, groups: map[string][]int64{}}

	rows, err := db.QueryContext(ctx, `SELECT fetched_at, account_id, currency, total_value, current_value, total_cost,
		unrealized_pnl, realized_pnl, cash_available, cash_in_pies, cash_reserved, years_elapsed
		FROM account_snapshots ORDER BY fetched_at, id`)
	if err != nil {
		return tradingGoHistory{}, fmt.Errorf("read account snapshots: %w", err)
	}
	byTime := map[time.Time]int{}
	var years float64
	for rows.Next() {
		var snapshot tradingGoSnapshot
		var fetched string
		var account int64
		a := &snapshot.amounts
		if err := rows.Scan(&fetched, &account, &snapshot.currency, &a[0], &a[1], &a[2], &a[3], &a[4], &a[5], &a[6], &a[7], &years); err != nil {
			rows.Close()
			return tradingGoHistory{}, fmt.Errorf("read account snapshots: %w", err)
		}
		if snapshot.at, err = time.Parse(time.RFC3339, fetched); err != nil {
			rows.Close()
			return tradingGoHistory{}, fmt.Errorf("account snapshot time %q is not RFC 3339", fetched)
		}
		snapshot.at = snapshot.at.UTC()
		snapshot.account = strconv.FormatInt(account, 10)
		if _, seen := byTime[snapshot.at]; seen {
			continue
		}
		byTime[snapshot.at] = len(history.snapshots)
		history.snapshots = append(history.snapshots, snapshot)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return tradingGoHistory{}, err
	}
	// trading-go counted years of 365.25 days from a midnight, so the start
	// is the nearest day to the latest snapshot minus its years.
	if count := len(history.snapshots); count > 0 && years > 0 {
		latest := history.snapshots[count-1].at
		start := latest.Add(-time.Duration(years * 365.25 * 24 * float64(time.Hour)))
		history.startedOn = start.Add(12 * time.Hour).Format(time.DateOnly)
	}

	// Pie snapshots were saved with the time of their account snapshot. Any
	// without one cannot be placed and are left out.
	rows, err = db.QueryContext(ctx, `SELECT fetched_at, pie_id, invested, value, result, cash,
		dividends_gained, dividends_reinvested, dividends_in_cash FROM pie_snapshots ORDER BY fetched_at, pie_id, id`)
	if err != nil {
		return tradingGoHistory{}, fmt.Errorf("read pie snapshots: %w", err)
	}
	for rows.Next() {
		var pie tradingGoPie
		var fetched string
		a := &pie.amounts
		if err := rows.Scan(&fetched, &pie.id, &a[0], &a[1], &a[2], &a[3], &a[4], &a[5], &a[6]); err != nil {
			rows.Close()
			return tradingGoHistory{}, fmt.Errorf("read pie snapshots: %w", err)
		}
		at, err := time.Parse(time.RFC3339, fetched)
		index, found := byTime[at.UTC()]
		if err != nil || !found || pie.id <= 0 {
			continue
		}
		snapshot := &history.snapshots[index]
		if !slices.ContainsFunc(snapshot.pies, func(p tradingGoPie) bool { return p.id == pie.id }) {
			snapshot.pies = append(snapshot.pies, pie)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return tradingGoHistory{}, err
	}

	rows, err = db.QueryContext(ctx, `SELECT id, name FROM pies`)
	if err != nil {
		return tradingGoHistory{}, fmt.Errorf("read pie names: %w", err)
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return tradingGoHistory{}, fmt.Errorf("read pie names: %w", err)
		}
		history.names[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return tradingGoHistory{}, err
	}

	rows, err = db.QueryContext(ctx, `SELECT c.name, p.pie_id FROM categories c
		LEFT JOIN category_pies p ON p.category_id = c.id ORDER BY c.id, p.pie_id`)
	if err != nil {
		return tradingGoHistory{}, fmt.Errorf("read categories: %w", err)
	}
	for rows.Next() {
		var name string
		var pie sql.NullInt64
		if err := rows.Scan(&name, &pie); err != nil {
			rows.Close()
			return tradingGoHistory{}, fmt.Errorf("read categories: %w", err)
		}
		// An empty category still becomes a group.
		pies := history.groups[name]
		if pie.Valid {
			pies = append(pies, pie.Int64)
		}
		history.groups[name] = pies
	}
	rows.Close()
	return history, rows.Err()
}

// minor rounds a stored float to minor units of the currency.
func minor(value float64, currency string) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("an amount is not a number")
	}
	amount, err := ledger.RoundMoney(strconv.FormatFloat(value, 'f', -1, 64), currency)
	if err != nil {
		return 0, err
	}
	return amount.Minor(), nil
}

func minors(currency string, values []float64) ([]int64, error) {
	amounts := make([]int64, len(values))
	for i, value := range values {
		var err error
		if amounts[i], err = minor(value, currency); err != nil {
			return nil, err
		}
	}
	return amounts, nil
}

// importTradingGo saves the history older than this app's first snapshot in
// one transaction, so running it again adds nothing. Pie names and the
// start date are kept when already set, and groups merge by name.
func importTradingGo(ctx context.Context, store *database.Store, history tradingGoHistory) (tradingGoResult, error) {
	var result tradingGoResult
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	q := store.Queries.WithTx(tx)
	earliest, err := q.EarliestInvestingSnapshotAt(ctx)
	if err != nil {
		return result, err
	}
	first, hasFirst := time.Time{}, false
	if earliest != "" {
		if first, err = time.Parse(time.RFC3339, earliest); err != nil {
			return result, err
		}
		hasFirst = true
	}
	var older []tradingGoSnapshot
	for _, snapshot := range history.snapshots {
		if hasFirst && !snapshot.at.Before(first) {
			result.Skipped++
			continue
		}
		older = append(older, snapshot)
	}

	if len(older) > 0 {
		// Snapshot IDs grow with fetched_at, which ranges rely on, so the saved
		// snapshots move above the imported ones. Moving them past the current
		// maximum keeps every ID unique on the way.
		highest, err := q.MaxInvestingSnapshotID(ctx)
		if err != nil {
			return result, err
		}
		if highest > 0 {
			shift := highest + int64(len(older))
			if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
				return result, err
			}
			if err := q.ShiftInvestingPieSnapshots(ctx, shift); err != nil {
				return result, err
			}
			if err := q.ShiftInvestingSnapshots(ctx, shift); err != nil {
				return result, err
			}
		}
	}
	for index, snapshot := range older {
		amounts, err := minors(snapshot.currency, snapshot.amounts[:])
		if err != nil {
			return result, fmt.Errorf("snapshot at %s: %w", snapshot.at.Format(time.RFC3339), err)
		}
		id := int64(index + 1)
		if err := q.ImportInvestingSnapshot(ctx, dbgen.ImportInvestingSnapshotParams{
			ID: id, FetchedAt: snapshot.at.Format(time.RFC3339), AccountID: snapshot.account, Currency: snapshot.currency,
			TotalValueMinor: amounts[0], InvestmentsValueMinor: amounts[1], CostBasisMinor: amounts[2], UnrealizedMinor: amounts[3],
			RealizedMinor: amounts[4], CashAvailableMinor: amounts[5], CashInPiesMinor: amounts[6], CashReservedMinor: amounts[7],
		}); err != nil {
			return result, err
		}
		for _, pie := range snapshot.pies {
			values, err := minors(snapshot.currency, pie.amounts[:])
			if err != nil {
				return result, fmt.Errorf("pie %d at %s: %w", pie.id, snapshot.at.Format(time.RFC3339), err)
			}
			if err := q.EnsureInvestingPie(ctx, pie.id); err != nil {
				return result, err
			}
			if err := q.CreateInvestingPieSnapshot(ctx, dbgen.CreateInvestingPieSnapshotParams{
				SnapshotID: id, PieID: pie.id, InvestedMinor: values[0], ValueMinor: values[1], ResultMinor: values[2],
				CashMinor: values[3], DividendsGainedMinor: values[4], DividendsReinvestedMinor: values[5],
				DividendsInCashMinor: values[6],
			}); err != nil {
				return result, err
			}
			result.PieSnapshots++
		}
		result.Snapshots++
	}

	// A name counts as checked long ago, so the snapshot job confirms it soon.
	pies, err := q.ListInvestingPies(ctx)
	if err != nil {
		return result, err
	}
	for _, pie := range pies {
		name, found := history.names[pie.ID]
		if pie.Name.Valid || !found || name == "" {
			continue
		}
		if err := q.NameInvestingPieIfMissing(ctx, dbgen.NameInvestingPieIfMissingParams{
			Name: sql.NullString{String: name, Valid: true}, CheckedAt: sql.NullString{String: time.Unix(0, 0).UTC().Format(time.RFC3339), Valid: true},
			ID: pie.ID}); err != nil {
			return result, err
		}
		result.Names++
	}

	groups, err := q.ListInvestingGroups(ctx)
	if err != nil {
		return result, err
	}
	members, err := q.ListInvestingGroupPies(ctx)
	if err != nil {
		return result, err
	}
	names := make([]string, 0, len(history.groups))
	for name := range history.groups {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		normalized := ledger.NormalizeName(name)
		if normalized == "" {
			continue
		}
		index := slices.IndexFunc(groups, func(group dbgen.InvestingGroup) bool { return group.NormalizedName == normalized })
		var id int64
		if index >= 0 {
			id = groups[index].ID
		} else {
			if id, err = q.CreateInvestingGroup(ctx, dbgen.CreateInvestingGroupParams{Name: name, NormalizedName: normalized}); err != nil {
				return result, err
			}
			groups = append(groups, dbgen.InvestingGroup{ID: id, Name: name, NormalizedName: normalized})
			result.Groups++
		}
		for _, pie := range history.groups[name] {
			member := dbgen.InvestingGroupPie{GroupID: id, PieID: pie}
			if pie <= 0 || slices.Contains(members, member) {
				continue
			}
			if err := q.EnsureInvestingPie(ctx, pie); err != nil {
				return result, err
			}
			if err := q.AddInvestingGroupPie(ctx, dbgen.AddInvestingGroupPieParams(member)); err != nil {
				return result, err
			}
			members = append(members, member)
		}
	}

	state, err := q.GetInvestingState(ctx)
	if err != nil {
		return result, err
	}
	if !state.StartedOn.Valid && history.startedOn != "" {
		if err := q.SetInvestingStart(ctx, sql.NullString{String: history.startedOn, Valid: true}); err != nil {
			return result, err
		}
		result.StartedOn = history.startedOn
	}
	return result, tx.Commit()
}
