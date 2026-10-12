package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
)

const (
	// InvestingInterval is how often the portfolio is snapshotted.
	InvestingInterval = time.Hour
	investingRetry    = 5 * time.Minute
	pieNameMaxAge     = 24 * time.Hour
)

// Investing saves an hourly snapshot of the Trading 212 account and its pies
// whenever a key is saved, then refreshes pie names that are a day old.
type Investing struct {
	Store  *database.Store
	Client *integrations.Client
	Now    func() time.Time
	// Pause waits between pie name requests and reports false when the
	// context ends first. Tests replace it to skip the wait.
	Pause func(context.Context, time.Duration) bool
	wake  chan struct{}
}

func NewInvesting(store *database.Store, client *integrations.Client) *Investing {
	return &Investing{Store: store, Client: client, Now: time.Now, Pause: pause, wake: make(chan struct{}, 1)}
}

func pause(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Wake makes Run step right away, after the Trading 212 key changes.
func (i *Investing) Wake() {
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

// Run blocks until the context ends.
func (i *Investing) Run(ctx context.Context) {
	for {
		next, err := i.Step(ctx)
		wait := time.Minute
		if err != nil {
			slog.Error("investing snapshot step failed", "error", err)
		} else {
			wait = min(max(next.Sub(i.Now()), time.Second), time.Hour)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-i.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Trading212Credentials reads the saved Trading 212 key. Snapshots only need
// the key, so they run whether or not monthly imports are enabled.
func Trading212Credentials(ctx context.Context, q *dbgen.Queries) (integrations.Trading212Credentials, dbgen.Integration, bool, error) {
	row, err := q.GetIntegration(ctx, "trading212")
	if errors.Is(err, sql.ErrNoRows) {
		return integrations.Trading212Credentials{}, row, false, nil
	}
	if err != nil {
		return integrations.Trading212Credentials{}, row, false, err
	}
	var stored struct {
		Environment string                       `json:"environment"`
		APIKey      string                       `json:"apiKey"`
		APISecret   string                       `json:"apiSecret"`
		Sync        *integrations.Trading212Sync `json:"sync"`
	}
	if json.Unmarshal([]byte(row.CredentialsJson), &stored) != nil || stored.APIKey == "" || stored.APISecret == "" {
		return integrations.Trading212Credentials{}, row, false, nil
	}
	return integrations.Trading212Credentials{Environment: stored.Environment, APIKey: stored.APIKey, APISecret: stored.APISecret,
		Sync: integrations.Trading212SyncOrAll(stored.Sync)}, row, true, nil
}

// Step saves a snapshot when the last one is an interval old and returns
// when the next step should run.
func (i *Investing) Step(ctx context.Context) (time.Time, error) {
	now := i.Now()
	q := i.Store.Queries
	credentials, _, configured, err := Trading212Credentials(ctx, q)
	if err != nil || !configured || !credentials.Sync.Portfolio {
		return now.Add(time.Hour), err
	}
	latest, err := q.LatestInvestingSnapshotAt(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if last, err := time.Parse(time.RFC3339, latest); err == nil {
		// A restart shortly before the hour reuses the last snapshot.
		if due := last.Add(InvestingInterval); now.Before(due.Add(-time.Minute)) {
			return due, nil
		}
	}
	saved, fetchErr := i.snapshot(ctx, credentials, now)
	var failed *integrations.FetchError
	if fetchErr != nil && !errors.As(fetchErr, &failed) {
		return time.Time{}, fetchErr
	}
	detail := ""
	if failed != nil {
		detail = failed.Detail
		slog.Warn("investing snapshot incomplete", "permanent", failed.Permanent)
	}
	if err := q.RecordInvestingAttempt(ctx, dbgen.RecordInvestingAttemptParams{
		LastAttemptAt: stamp(now), LastError: nullString(detail)}); err != nil {
		return time.Time{}, err
	}
	if saved {
		if failed == nil && credentials.Sync.Pies {
			i.refreshNames(ctx, credentials)
		}
		return now.Add(InvestingInterval), nil
	}
	if failed.Permanent {
		return now.Add(InvestingInterval), nil
	}
	return now.Add(investingRetry), nil
}

// snapshot saves the account and its pies. When only the pies fail, the
// account snapshot is still kept and the pie error is returned.
func (i *Investing) snapshot(ctx context.Context, credentials integrations.Trading212Credentials, now time.Time) (bool, error) {
	summary, err := i.Client.FetchAccountSummary(ctx, credentials)
	if err != nil {
		return false, err
	}
	var pies []integrations.PieSummary
	var piesErr error
	if credentials.Sync.Pies {
		pies, piesErr = i.Client.FetchPies(ctx, credentials, summary.Currency)
	}
	tx, err := i.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := i.Store.Queries.WithTx(tx)
	id, err := q.CreateInvestingSnapshot(ctx, dbgen.CreateInvestingSnapshotParams{
		FetchedAt: stamp(now).String, AccountID: summary.AccountID, Currency: summary.Currency,
		TotalValueMinor: summary.TotalValue, InvestmentsValueMinor: summary.InvestmentsValue, CostBasisMinor: summary.CostBasis,
		UnrealizedMinor: summary.Unrealized, RealizedMinor: summary.Realized, CashAvailableMinor: summary.CashAvailable,
		CashInPiesMinor: summary.CashInPies, CashReservedMinor: summary.CashReserved,
	})
	if err != nil {
		return false, err
	}
	for _, pie := range pies {
		if err := q.EnsureInvestingPie(ctx, pie.ID); err != nil {
			return false, err
		}
		if err := q.CreateInvestingPieSnapshot(ctx, dbgen.CreateInvestingPieSnapshotParams{
			SnapshotID: id, PieID: pie.ID, InvestedMinor: pie.Invested, ValueMinor: pie.Value, ResultMinor: pie.Result,
			CashMinor: pie.Cash, DividendsGainedMinor: pie.DividendsGained, DividendsReinvestedMinor: pie.DividendsReinvested,
			DividendsInCashMinor: pie.DividendsInCash,
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	slog.Info("investing snapshot saved", "pies", len(pies))
	return true, piesErr
}

// refreshNames reads the names of current pies that are missing or a day
// old, one request per PieNameInterval. It stops at the first failure and
// tries again after the next snapshot.
func (i *Investing) refreshNames(ctx context.Context, credentials integrations.Trading212Credentials) {
	q := i.Store.Queries
	stale, err := q.StaleInvestingPies(ctx, stamp(i.Now().Add(-pieNameMaxAge)))
	if err != nil {
		slog.Error("cannot list stale pie names", "error", err)
		return
	}
	for index, id := range stale {
		if index > 0 && !i.Pause(ctx, integrations.PieNameInterval) {
			return
		}
		name, err := i.Client.FetchPieName(ctx, credentials, id)
		if err != nil {
			slog.Warn("pie name refresh stopped", "pie", id)
			return
		}
		if err := q.SetInvestingPieName(ctx, dbgen.SetInvestingPieNameParams{
			Name: nullString(name), CheckedAt: stamp(i.Now()), ID: id}); err != nil {
			slog.Error("cannot save pie name", "error", err)
			return
		}
	}
}
