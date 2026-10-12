package jobs

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/rates"
)

// Job states used by the daily rate job. A day's job is pending until its
// 18:00 fetch succeeds, and failed when retries run past the end of the day
// or the server was not running. Missed days are never fetched later.
const (
	statePending    = "pending"
	stateCompleted  = "completed"
	stateFailed     = "failed"
	maxRetryBackoff = time.Hour
	firstBackoff    = 5 * time.Minute
)

// Rates fetches the latest rates against EUR and every account currency at
// startup and every day at 18:00 Europe/Bratislava, retrying failures until
// the end of the day.
type Rates struct {
	Store  *database.Store
	Client *rates.Client
	Now    func() time.Time
}

func NewRates(store *database.Store) *Rates {
	return &Rates{Store: store, Client: rates.NewClient(), Now: time.Now}
}

// Run blocks until the context ends.
func (r *Rates) Run(ctx context.Context) {
	if err := r.Store.Queries.AbandonRatesAttempts(ctx, stamp(r.Now())); err != nil {
		slog.Error("cannot recover rate attempts", "error", err)
	}
	next, err := r.Step(ctx, true)
	for {
		wait := time.Minute
		if err != nil {
			slog.Error("rate schedule failed", "error", err)
		} else {
			// Waking at least hourly keeps the schedule right across clock changes.
			wait = min(max(next.Sub(r.Now()), time.Second), time.Hour)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		next, err = r.Step(ctx, false)
	}
}

func stamp(instant time.Time) sql.NullString {
	return sql.NullString{String: instant.UTC().Format(time.RFC3339), Valid: true}
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// scheduledAt is 18:00 local time on a date.
func scheduledAt(date string) time.Time {
	day, _ := time.Parse(time.DateOnly, date)
	return time.Date(day.Year(), day.Month(), day.Day(), rates.PublishHour, 0, 0, 0, rates.Location)
}

func endOfDay(date string) time.Time {
	day, _ := time.Parse(time.DateOnly, date)
	return time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, rates.Location)
}

// Step creates the day's job, runs a fetch when one is due, and returns
// when the next step should run. At startup it always fetches once, which
// refreshes the latest rates without assigning them to missed dates.
func (r *Rates) Step(ctx context.Context, startup bool) (time.Time, error) {
	now := r.Now()
	today := rates.LocalDate(now)
	q := r.Store.Queries
	err := q.CloseMissedRatesJobs(ctx, dbgen.CloseMissedRatesJobsParams{Today: today,
		LastErrorDetail: nullString("The server was not running or could not fetch rates that day; missed days are not fetched later")})
	if err != nil {
		return time.Time{}, err
	}
	err = q.EnsureRatesJob(ctx, dbgen.EnsureRatesJobParams{PeriodKey: today, Timezone: rates.Timezone,
		NextAttemptAt: scheduledAt(today).UTC().Format(time.RFC3339)})
	if err != nil {
		return time.Time{}, err
	}
	job, err := q.GetRatesJob(ctx, today)
	if err != nil {
		return time.Time{}, err
	}
	open := job.State != stateCompleted && job.State != stateFailed && job.State != "canceled"
	due, _ := time.Parse(time.RFC3339, job.NextAttemptAt)
	if startup || (open && !now.Before(due)) {
		stage := "scheduled"
		if startup {
			stage = "startup"
		}
		if job, err = r.attempt(ctx, job, stage); err != nil {
			return time.Time{}, err
		}
		open = job.State != stateCompleted && job.State != stateFailed
		due, _ = time.Parse(time.RFC3339, job.NextAttemptAt)
	}
	if open {
		return due, nil
	}
	return scheduledAt(rates.LocalDate(endOfDay(today))), nil
}

// attempt fetches once and records the outcome on the job. The download
// happens outside any database transaction.
func (r *Rates) attempt(ctx context.Context, job dbgen.ScheduledJob, stage string) (dbgen.ScheduledJob, error) {
	q := r.Store.Queries
	started := r.Now()
	number, err := q.StartRatesAttempt(ctx, job.ID)
	if err != nil {
		return job, err
	}
	attemptID, err := q.CreateJobAttempt(ctx, dbgen.CreateJobAttemptParams{JobID: job.ID, AttemptNumber: number,
		Stage: stage, StartedAt: stamp(started).String})
	if err != nil {
		return job, err
	}
	quotes, fetchErr := r.fetch(ctx)
	finished := r.Now()
	if len(quotes) > 0 {
		if err := r.save(ctx, quotes, finished); err != nil {
			fetchErr = err
		}
	}
	outcome, code, detail := "succeeded", "", ""
	if fetchErr != nil {
		outcome, code, detail = "transient_failure", "storage", "The rates could not be saved"
		var failed *rates.FetchError
		if errors.As(fetchErr, &failed) {
			code, detail = failed.Code, failed.Detail
		}
		slog.Warn("exchange rate fetch failed", "stage", stage, "code", code)
	} else {
		slog.Info("exchange rates updated", "stage", stage, "currencies", len(quotes))
	}
	if err := q.FinishJobAttempt(ctx, dbgen.FinishJobAttemptParams{FinishedAt: stamp(finished),
		Outcome: nullString(outcome), ErrorCode: nullString(code), ErrorDetail: nullString(detail), ID: attemptID}); err != nil {
		return job, err
	}

	params := dbgen.FinishRatesJobParams{State: job.State, NextAttemptAt: job.NextAttemptAt, ID: job.ID}
	scheduled := scheduledAt(job.PeriodKey)
	switch {
	case job.State == stateCompleted:
		// A startup refresh after the day's fetch keeps the job complete.
		params.LastErrorCode, params.LastErrorDetail = job.LastErrorCode, job.LastErrorDetail
	case fetchErr == nil && !finished.Before(scheduled):
		params.State = stateCompleted
	case fetchErr == nil:
		// A startup fetch before 18:00 does not replace the day's fetch.
		params.State, params.NextAttemptAt = statePending, scheduled.UTC().Format(time.RFC3339)
	default:
		backoff := min(firstBackoff<<min(number-1, 8), maxRetryBackoff)
		next := finished.Add(backoff)
		if finished.Before(scheduled) && next.After(scheduled) {
			next = scheduled
		}
		params.State, params.NextAttemptAt = statePending, next.UTC().Format(time.RFC3339)
		if !next.Before(endOfDay(job.PeriodKey)) {
			params.State = stateFailed
		}
		params.LastErrorCode, params.LastErrorDetail = nullString(code), nullString(detail)
	}
	if err := q.FinishRatesJob(ctx, params); err != nil {
		return job, err
	}
	return q.GetRatesJob(ctx, job.PeriodKey)
}

// fetch downloads the rates against EUR and against every account currency.
// A failed base does not stop the others, so one currency the service cannot
// use as a base never holds back the EUR rates the ledger converts with. The
// first failure is returned with the quotes that did arrive.
func (r *Rates) fetch(ctx context.Context) ([]rates.Quote, error) {
	currencies, err := r.Store.Queries.ListAccountCurrencies(ctx)
	if err != nil {
		return nil, err
	}
	var quotes []rates.Quote
	var failure error
	for _, base := range rates.Bases(currencies) {
		fetched, err := r.Client.Latest(ctx, base)
		if err == nil {
			quotes = append(quotes, fetched...)
			continue
		}
		var failed *rates.FetchError
		if failure == nil && errors.As(err, &failed) {
			failure = &rates.FetchError{Code: failed.Code, Detail: base + ": " + failed.Detail}
		}
	}
	return quotes, failure
}

// save stores the response and then removes cached rates nothing needs.
func (r *Rates) save(ctx context.Context, quotes []rates.Quote, fetchedAt time.Time) error {
	tx, err := r.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rates.Save(ctx, r.Store.Queries.WithTx(tx), quotes, fetchedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if deleted, err := rates.Cleanup(ctx, r.Store, fetchedAt); err != nil {
		slog.Error("rate cache cleanup failed", "error", err)
	} else if deleted > 0 {
		slog.Info("removed cached rates", "count", deleted)
	}
	return nil
}

// NextRun is when the next scheduled fetch is due: a pending retry or
// the next 18:00 Europe/Bratislava.
func NextRun(ctx context.Context, q *dbgen.Queries, now time.Time) (time.Time, error) {
	today := rates.LocalDate(now)
	job, err := q.GetRatesJob(ctx, today)
	switch {
	case err == nil && job.State != stateCompleted && job.State != stateFailed && job.State != "canceled":
		return time.Parse(time.RFC3339, job.NextAttemptAt)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return time.Time{}, err
	case err != nil && now.Before(scheduledAt(today)):
		return scheduledAt(today), nil
	}
	return scheduledAt(rates.LocalDate(endOfDay(today))), nil
}
