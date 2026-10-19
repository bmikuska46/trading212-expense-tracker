package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/notify"
	"budget-tracking/backend/internal/rates"
)

// Monthly import job states. A job without a report is pending, or
// requesting while a request whose outcome is unknown may have created one.
// It is polling until the report finishes, downloading until the file is
// staged, and validating until its batch is committed.
const (
	stateRequesting  = "requesting"
	statePolling     = "polling"
	stateDownloading = "downloading"
	stateValidating  = "validating"
	stateCanceled    = "canceled"
)

const (
	// DailyHour is when each day the open month is synced through the day
	// before, Europe/Bratislava time. On the 1st that finishes the previous
	// month.
	DailyHour = 6
	// The requested range is widened on both sides; rows outside the month
	// are dropped before staging.
	reportOverlap = time.Hour
	// maxRetries bounds consecutive transient failures of one job.
	maxRetries = 10
	// busyDelay waits for an import step the owner started in the app.
	busyDelay = 30 * time.Second
	// A report that is still unfinished after reportDeadline is given up on.
	// Polling slows down after quickPolling.
	reportDeadline = 24 * time.Hour
	quickPolling   = 10 * time.Minute
	slowPoll       = 5 * time.Minute
)

// Monthly keeps one job per month of Trading 212 transactions. Every day at
// 06:00 it requests the month's report through the day before, waits for
// it, and imports the new transactions without a review. Its state lives in
// scheduled_jobs, so a restart resumes where it stopped. One job runs at a
// time and shares the import pipeline with the app.
type Monthly struct {
	Store   *database.Store
	Client  *integrations.Client
	Imports *imports.Pipeline
	Now     func() time.Time
	wake    chan struct{}
}

func NewMonthly(store *database.Store, client *integrations.Client, pipeline *imports.Pipeline) *Monthly {
	return &Monthly{Store: store, Client: client, Imports: pipeline, Now: time.Now, wake: make(chan struct{}, 1)}
}

// Wake makes Run step right away, after the owner asks for a rerun.
func (m *Monthly) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run blocks until the context ends.
func (m *Monthly) Run(ctx context.Context) {
	if err := m.Store.Queries.AbandonMonthlyAttempts(ctx, stamp(m.Now())); err != nil {
		slog.Error("cannot recover monthly import attempts", "error", err)
	}
	for {
		next, err := m.Step(ctx)
		wait := time.Minute
		if err != nil {
			slog.Error("monthly import step failed", "error", err)
		} else {
			wait = min(max(next.Sub(m.Now()), time.Second), time.Hour)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// MonthBounds returns the UTC instants at which a Europe/Bratislava month
// starts and ends. The end is exclusive.
func MonthBounds(period string) (time.Time, time.Time, error) {
	month, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, rates.Location)
	return start.UTC(), start.AddDate(0, 1, 0).UTC(), nil
}

// RunAfter is 06:00 Europe/Bratislava on the day after the one an instant
// falls on: the first daily run that can sync past it.
func RunAfter(instant time.Time) time.Time {
	local := instant.In(rates.Location)
	return time.Date(local.Year(), local.Month(), local.Day()+1, DailyHour, 0, 0, 0, rates.Location)
}

// FirstRunAt is when a month is first synced: 06:00 on its 2nd day.
func FirstRunAt(period string) time.Time {
	start, _, _ := MonthBounds(period)
	return RunAfter(start)
}

// NextDailyRun is the next 06:00 after now.
func NextDailyRun(now time.Time) time.Time {
	local := now.In(rates.Location)
	today := time.Date(local.Year(), local.Month(), local.Day(), DailyHour, 0, 0, 0, rates.Location)
	if today.After(now) {
		return today
	}
	return RunAfter(now)
}

// syncThrough is where a report requested now may end: the start of the
// local day, so that with the overlap the range is in the past, but never
// past the month.
func syncThrough(now, end time.Time) time.Time {
	local := now.Add(-reportOverlap).In(rates.Location)
	if day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, rates.Location).UTC(); day.Before(end) {
		return day
	}
	return end
}

// syncedThrough is where a job's last report ended, or the month's start.
func syncedThrough(job dbgen.ScheduledJob) time.Time {
	if through, err := time.Parse(time.RFC3339, job.ReportTo.String); err == nil {
		return through
	}
	start, _ := time.Parse(time.RFC3339, job.PeriodStartUtc.String)
	return start
}

// Period is the Europe/Bratislava month of an instant.
func Period(instant time.Time) string { return instant.In(rates.Location).Format("2006-01") }

func shiftPeriod(period string, months int) string {
	month, _ := time.Parse("2006-01", period)
	return month.AddDate(0, months, 0).Format("2006-01")
}

// DuePeriods lists the months from earliest on whose first run has passed.
func DuePeriods(earliest string, now time.Time) []string {
	periods := []string{}
	if _, err := time.Parse("2006-01", earliest); err != nil {
		return periods
	}
	for period := earliest; !FirstRunAt(period).After(now); period = shiftPeriod(period, 1) {
		periods = append(periods, period)
	}
	return periods
}

type monthlySource struct {
	credentials integrations.Trading212Credentials
	account     sql.NullInt64
	earliest    string
}

// source returns the Trading 212 settings when automatic imports are on.
func (m *Monthly) source(ctx context.Context) (monthlySource, bool, error) {
	credentials, row, configured, err := Trading212Credentials(ctx, m.Store.Queries)
	if err != nil || !configured || row.Enabled != 1 || !row.SourceAccountID.Valid || !row.EarliestAutomaticPeriod.Valid {
		return monthlySource{}, false, err
	}
	return monthlySource{credentials: credentials, account: row.SourceAccountID, earliest: row.EarliestAutomaticPeriod.String}, true, nil
}

// Step creates the jobs that are due, reopens the months whose daily sync is
// due, advances the oldest open job by one stage, and returns when the next
// step should run.
func (m *Monthly) Step(ctx context.Context) (time.Time, error) {
	now := m.Now()
	source, enabled, err := m.source(ctx)
	if err != nil || !enabled {
		return now.Add(time.Hour), err
	}
	q := m.Store.Queries
	canceled, err := q.CancelStaleMonthlyJobs(ctx, dbgen.CancelStaleMonthlyJobsParams{
		Detail:          nullString("Automatic imports now use another account or start in a later month"),
		SourceAccountID: source.account, Earliest: source.earliest,
	})
	if err != nil {
		return time.Time{}, err
	}
	if len(canceled) > 0 {
		slog.Info("canceled monthly imports", "count", len(canceled))
	}
	for _, period := range DuePeriods(source.earliest, now) {
		if err := m.ensure(ctx, source.account, period, FirstRunAt(period)); err != nil {
			return time.Time{}, err
		}
	}
	partial, err := q.ListPartialMonthlyJobs(ctx, dbgen.ListPartialMonthlyJobsParams{
		SourceAccountID: source.account, PeriodKey: source.earliest})
	if err != nil {
		return time.Time{}, err
	}
	for _, job := range partial {
		if now.Before(RunAfter(syncedThrough(job))) {
			continue
		}
		if _, err := q.RerunMonthlyJob(ctx, dbgen.RerunMonthlyJobParams{NextAttemptAt: stamp(now).String, ID: job.ID}); err != nil {
			return time.Time{}, err
		}
	}
	job, err := q.NextMonthlyJob(ctx, source.account)
	if errors.Is(err, sql.ErrNoRows) {
		return NextDailyRun(now), nil
	}
	if err != nil {
		return time.Time{}, err
	}
	due, _ := time.Parse(time.RFC3339, job.NextAttemptAt)
	if now.Before(due) {
		return due, nil
	}
	if err := m.advance(ctx, job, source); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

func (m *Monthly) ensure(ctx context.Context, account sql.NullInt64, period string, due time.Time) error {
	start, end, err := MonthBounds(period)
	if err != nil {
		return err
	}
	return m.Store.Queries.EnsureMonthlyJob(ctx, dbgen.EnsureMonthlyJobParams{
		SourceAccountID: account, PeriodKey: period, Timezone: rates.Timezone,
		PeriodStartUtc: stamp(start), PeriodEndUtc: stamp(end), NextAttemptAt: stamp(due).String,
	})
}

// Rerun schedules a month again right away, from its start. Existing
// transaction IDs are skipped as duplicates, so a rerun or a daily sync never
// duplicates rows. It returns false when the month's job is still running.
func Rerun(ctx context.Context, q *dbgen.Queries, account int64, period string, now time.Time) (bool, error) {
	key := sql.NullInt64{Int64: account, Valid: true}
	job, err := q.GetMonthlyJob(ctx, dbgen.GetMonthlyJobParams{SourceAccountID: key, PeriodKey: period})
	if errors.Is(err, sql.ErrNoRows) {
		start, end, err := MonthBounds(period)
		if err != nil {
			return false, err
		}
		return true, q.EnsureMonthlyJob(ctx, dbgen.EnsureMonthlyJobParams{
			SourceAccountID: key, PeriodKey: period, Timezone: rates.Timezone,
			PeriodStartUtc: stamp(start), PeriodEndUtc: stamp(end), NextAttemptAt: stamp(now).String,
		})
	}
	if err != nil {
		return false, err
	}
	changed, err := q.RerunMonthlyJob(ctx, dbgen.RerunMonthlyJobParams{NextAttemptAt: stamp(now).String, ID: job.ID})
	if err != nil || changed == 0 {
		return false, err
	}
	// The failure is resolved by trying again; a new failure alerts again.
	return true, q.DismissJobFailure(ctx, dbgen.DismissJobFailureParams{DismissedAt: stamp(now), JobID: sql.NullInt64{Int64: job.ID, Valid: true}})
}

func (m *Monthly) advance(ctx context.Context, job dbgen.ScheduledJob, source monthlySource) error {
	switch job.State {
	case statePending, stateRequesting:
		return m.request(ctx, job, source)
	case statePolling, stateDownloading:
		return m.poll(ctx, job, source)
	}
	return m.importBatch(ctx, job)
}

func (m *Monthly) save(ctx context.Context, q *dbgen.Queries, job dbgen.ScheduledJob) error {
	return q.SaveMonthlyJob(ctx, dbgen.SaveMonthlyJobParams{
		State: job.State, ReportID: job.ReportID, ReportStatus: job.ReportStatus, ReportRequestedAt: job.ReportRequestedAt,
		ReportTo: job.ReportTo, BatchID: job.BatchID, RetryCount: job.RetryCount, NextAttemptAt: job.NextAttemptAt,
		LastErrorCode: job.LastErrorCode, LastErrorDetail: job.LastErrorDetail, ID: job.ID,
	})
}

// later saves the job to run again at a time, without counting a retry.
func (m *Monthly) later(ctx context.Context, job dbgen.ScheduledJob, at time.Time) error {
	job.NextAttemptAt = stamp(at).String
	return m.save(ctx, m.Store.Queries, job)
}

// progress saves a step that moved the job forward.
func (m *Monthly) progress(ctx context.Context, job dbgen.ScheduledJob, at time.Time) error {
	job.RetryCount, job.LastErrorCode, job.LastErrorDetail = 0, sql.NullString{}, sql.NullString{}
	return m.later(ctx, job, at)
}

// callableAt is when a rate-limited Trading 212 endpoint may be called
// again. Calls are recorded as job attempts, so the limit holds across
// restarts.
func (m *Monthly) callableAt(ctx context.Context, stage string, interval time.Duration) (time.Time, error) {
	return reportCallableAt(ctx, m.Store.Queries, stage, interval)
}

// reportCallableAt is when the report endpoint of a stage may be called
// again. Monthly imports and cashback reports share the Trading 212 limits.
func reportCallableAt(ctx context.Context, q *dbgen.Queries, stage string, interval time.Duration) (time.Time, error) {
	last, err := q.LastReportCall(ctx, stage)
	if err != nil || last == "" {
		return time.Time{}, err
	}
	at, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return time.Time{}, nil
	}
	// Stored times have whole seconds.
	return at.Add(interval + time.Second), nil
}

// call records one network call as a job attempt.
func (m *Monthly) call(ctx context.Context, job dbgen.ScheduledJob, stage string, fn func() error) error {
	q := m.Store.Queries
	number, err := q.StartJobAttempt(ctx, job.ID)
	if err != nil {
		return err
	}
	attemptID, err := q.CreateJobAttempt(ctx, dbgen.CreateJobAttemptParams{JobID: job.ID, AttemptNumber: number,
		Stage: stage, StartedAt: stamp(m.Now()).String})
	if err != nil {
		return err
	}
	callErr := fn()
	outcome, code, detail := "succeeded", "", ""
	var failed *integrations.ReportError
	if errors.As(callErr, &failed) {
		outcome, code, detail = "transient_failure", failed.Code, failed.Detail
		if failed.Permanent {
			outcome = "permanent_failure"
		}
	}
	if err := q.FinishJobAttempt(ctx, dbgen.FinishJobAttemptParams{FinishedAt: stamp(m.Now()),
		Outcome: nullString(outcome), ErrorCode: nullString(code), ErrorDetail: nullString(detail), ID: attemptID}); err != nil {
		return err
	}
	return callErr
}

// reportRange is the requested range: the month from its start through
// report_to, with a small overlap.
func reportRange(job dbgen.ScheduledJob) (time.Time, time.Time) {
	start, _ := time.Parse(time.RFC3339, job.PeriodStartUtc.String)
	return start.Add(-reportOverlap), syncedThrough(job).Add(reportOverlap)
}

// matching finds the newest usable report for a range.
func matching(reports []integrations.Report, from, to time.Time) (integrations.Report, bool) {
	var found integrations.Report
	var newest int64 = -1
	for _, report := range reports {
		if !report.TimeFrom.Equal(from) || !report.TimeTo.Equal(to) ||
			report.Status == integrations.ReportFailed || report.Status == integrations.ReportCanceled {
			continue
		}
		if id, err := strconv.ParseInt(report.ID, 10, 64); err == nil && id > newest {
			found, newest = report, id
		}
	}
	return found, newest >= 0
}

func (m *Monthly) request(ctx context.Context, job dbgen.ScheduledJob, source monthlySource) error {
	now := m.Now()
	if job.State == stateRequesting {
		// The last request may have created a report before its response
		// was lost. Look for it before asking for another one.
		at, err := m.callableAt(ctx, "poll", integrations.ReportListInterval)
		if err != nil {
			return err
		}
		if now.Before(at) {
			return m.later(ctx, job, at)
		}
		var reports []integrations.Report
		err = m.call(ctx, job, "poll", func() (err error) {
			reports, err = m.Client.ListReports(ctx, source.credentials)
			return err
		})
		if err != nil {
			return m.retry(ctx, job, err)
		}
		from, to := reportRange(job)
		if report, ok := matching(reports, from, to); ok {
			return m.polling(ctx, job, report.ID)
		}
		job.State = statePending
		now = m.Now()
	}
	at, err := m.callableAt(ctx, "request", integrations.ReportRequestInterval)
	if err != nil {
		return err
	}
	if now.Before(at) {
		return m.later(ctx, job, at)
	}
	end, _ := time.Parse(time.RFC3339, job.PeriodEndUtc.String)
	job.ReportTo = stamp(syncThrough(now, end))
	from, to := reportRange(job)
	// Saved before the call, so a lost response is looked up next time.
	job.State = stateRequesting
	if err := m.save(ctx, m.Store.Queries, job); err != nil {
		return err
	}
	var reportID string
	err = m.call(ctx, job, "request", func() (err error) {
		reportID, err = m.Client.RequestReport(ctx, source.credentials, from, to)
		return err
	})
	if err != nil {
		var failed *integrations.ReportError
		if errors.As(err, &failed) && failed.Code != "unreachable" && failed.Code != "unreadable" {
			// Trading 212 answered with an error, so no report exists.
			job.State = statePending
		}
		return m.retry(ctx, job, err)
	}
	slog.Info("requested monthly report", "period", job.PeriodKey)
	return m.polling(ctx, job, reportID)
}

// polling saves the report the job waits for.
func (m *Monthly) polling(ctx context.Context, job dbgen.ScheduledJob, reportID string) error {
	now := m.Now()
	job.State, job.ReportID, job.ReportStatus = statePolling, nullString(reportID), sql.NullString{}
	job.ReportRequestedAt = stamp(now)
	return m.progress(ctx, job, now.Add(integrations.ReportListInterval))
}

func (m *Monthly) poll(ctx context.Context, job dbgen.ScheduledJob, source monthlySource) error {
	now := m.Now()
	at, err := m.callableAt(ctx, "poll", integrations.ReportListInterval)
	if err != nil {
		return err
	}
	if now.Before(at) {
		return m.later(ctx, job, at)
	}
	var reports []integrations.Report
	err = m.call(ctx, job, "poll", func() (err error) {
		reports, err = m.Client.ListReports(ctx, source.credentials)
		return err
	})
	if err != nil {
		return m.retry(ctx, job, err)
	}
	now = m.Now()
	index := slices.IndexFunc(reports, func(report integrations.Report) bool { return report.ID == job.ReportID.String })
	if index < 0 {
		return m.retry(ctx, job, &integrations.ReportError{Code: "report_missing",
			Detail: "Trading 212 does not list the requested report."})
	}
	report := reports[index]
	job.ReportStatus = nullString(report.Status)
	switch report.Status {
	case integrations.ReportFailed:
		return m.fail(ctx, job, "report_failed", "Trading 212 could not generate the report. Run the month again.")
	case integrations.ReportCanceled:
		return m.fail(ctx, job, "report_canceled", "The report was canceled in Trading 212. Run the month again.")
	case integrations.ReportFinished:
	default:
		requested, _ := time.Parse(time.RFC3339, job.ReportRequestedAt.String)
		if now.Sub(requested) > reportDeadline {
			return m.fail(ctx, job, "report_unfinished", "Trading 212 did not finish the report within 24 hours. Run the month again.")
		}
		wait := integrations.ReportListInterval
		if now.Sub(requested) > quickPolling {
			wait = slowPoll
		}
		return m.progress(ctx, job, now.Add(wait))
	}
	// The download link expires, so a failed download lists the reports
	// again for a fresh one.
	job.State = stateDownloading
	if err := m.save(ctx, m.Store.Queries, job); err != nil {
		return err
	}
	var data []byte
	err = m.call(ctx, job, "download", func() (err error) {
		data, err = m.Client.DownloadReport(ctx, report.DownloadLink)
		return err
	})
	if err != nil {
		return m.retry(ctx, job, err)
	}
	return m.stage(ctx, job, data)
}

// monthRows keeps the header and the rows whose time falls in [start, end),
// leaving out the rows the owner already handled. Rows without a readable
// time stay, so validation reports them.
func monthRows(data []byte, start, end time.Time, handled map[string]bool) ([]byte, int, error) {
	reader, err := imports.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	header := reader.Header()
	column := slices.Index(header, "Time (UTC)")
	if column < 0 {
		column = slices.Index(header, "Time")
	}
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write(header); err != nil {
		return nil, 0, err
	}
	count := 0
	for {
		record, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if column >= 0 && column < len(record) {
			if at, ok := imports.ParseUTC(record[column]); ok && (at.Before(start) || !at.Before(end)) {
				continue
			}
		}
		if key, _ := json.Marshal(record); handled[string(key)] {
			continue
		}
		if err := writer.Write(record); err != nil {
			return nil, 0, err
		}
		count++
	}
	writer.Flush()
	return output.Bytes(), count, writer.Error()
}

func reportFilename(job dbgen.ScheduledJob) string {
	return "trading212-" + job.PeriodKey + ".csv"
}

func (m *Monthly) location(ctx context.Context) *time.Location {
	name, err := m.Store.Queries.GetOwnerTimezone(ctx)
	if err != nil {
		return rates.Location
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return rates.Location
	}
	return location
}

func (m *Monthly) stage(ctx context.Context, job dbgen.ScheduledJob, data []byte) error {
	start, _ := time.Parse(time.RFC3339, job.PeriodStartUtc.String)
	end, _ := time.Parse(time.RFC3339, job.PeriodEndUtc.String)
	rows, err := m.Store.Queries.HandledReportRows(ctx, job.SourceAccountID.Int64)
	if err != nil {
		return err
	}
	handled := map[string]bool{}
	for _, row := range rows {
		handled[row] = true
	}
	filtered, count, err := monthRows(data, start, end, handled)
	if err != nil {
		return m.fail(ctx, job, "unreadable_report", "The report is not a CSV file this app can read.")
	}
	if count == 0 {
		slog.Info("monthly report has no new transactions", "period", job.PeriodKey)
		return m.complete(ctx, job)
	}
	batchID, err := m.Imports.Stage(ctx, job.SourceAccountID.Int64, reportFilename(job),
		bytes.NewReader(filtered), m.location(ctx))
	var known *imports.Error
	switch {
	case errors.As(err, &known) && known.Kind == imports.Busy:
		return m.later(ctx, job, m.Now().Add(busyDelay))
	case batchID == 0 && errors.As(err, &known):
		return m.fail(ctx, job, "import_rejected", known.Message)
	case batchID == 0 && err != nil:
		return err
	}
	job.State, job.BatchID = stateValidating, sql.NullInt64{Int64: batchID, Valid: true}
	if saveErr := m.progress(ctx, job, m.Now()); saveErr != nil || err != nil {
		return errors.Join(err, saveErr)
	}
	return m.importBatch(ctx, job)
}

// importBatch commits a staged report when it validated cleanly, already
// confirmed. A report that needs a mapping or row corrections waits for the
// owner, who commits it in the app; that commit completes the job.
func (m *Monthly) importBatch(ctx context.Context, job dbgen.ScheduledJob) error {
	removed := "The import was removed before it was committed. Run the month again to import it."
	if !job.BatchID.Valid {
		return m.fail(ctx, job, "import_removed", removed)
	}
	batch, err := m.Store.Queries.GetImportBatch(ctx, job.BatchID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return m.fail(ctx, job, "import_removed", removed)
	}
	if err != nil {
		return err
	}
	ready := batch.StagedAt.Valid && batch.MappingID.Valid
	var stepErr error
	switch {
	case batch.Status == "committed":
		return m.complete(ctx, job)
	case ready && batch.Status == "validating":
		if stepErr = m.Imports.CommitConfirmed(ctx, batch.ID, m.location(ctx)); stepErr == nil {
			return m.complete(ctx, job)
		}
	case ready && batch.Status == "staging":
		// Staged but not validated, after an interruption.
		if stepErr = m.Imports.Validate(ctx, batch.ID, m.location(ctx)); stepErr == nil {
			return m.later(ctx, job, m.Now())
		}
	default:
		return m.fail(ctx, job, "needs_attention", attentionReason(batch))
	}
	var known *imports.Error
	switch {
	case errors.As(stepErr, &known) && known.Kind == imports.Busy:
		return m.later(ctx, job, m.Now().Add(busyDelay))
	case errors.As(stepErr, &known) && known.Kind == imports.Conflict:
		// The ledger changed and the batch was validated again.
		return m.retry(ctx, job, &integrations.ReportError{Code: "changed", Detail: known.Message})
	case errors.As(stepErr, &known):
		return m.fail(ctx, job, "import_rejected", known.Message)
	}
	return stepErr
}

func attentionReason(batch dbgen.ImportBatch) string {
	switch {
	case !batch.StagedAt.Valid:
		var listed []imports.ListedError
		_ = json.Unmarshal([]byte(batch.ValidationErrorsJson), &listed)
		if len(listed) > 0 {
			return "The report could not be read: " + listed[0].Message
		}
		return "The report could not be read."
	case !batch.MappingID.Valid:
		return "The report columns do not match the Trading 212 format. Open the import, choose a mapping, and commit it."
	}
	return fmt.Sprintf("%d rows need a correction or exclusion. Open the import, resolve them, and commit it.", batch.ErrorCount)
}

// complete finishes the job. A batch that added nothing is deleted, so a
// daily sync without new transactions leaves no import behind, and the month
// points at its last batch that added some.
func (m *Monthly) complete(ctx context.Context, job dbgen.ScheduledJob) error {
	job.State, job.NextAttemptAt = stateCompleted, stamp(m.Now()).String
	job.RetryCount, job.LastErrorCode, job.LastErrorDetail = 0, sql.NullString{}, sql.NullString{}
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := m.Store.Queries.WithTx(tx)
	if job.BatchID.Valid {
		batch, err := q.GetImportBatch(ctx, job.BatchID.Int64)
		if err != nil {
			return err
		}
		if batch.Status == "committed" && batch.InsertedCount == 0 {
			empty := job.BatchID
			job.BatchID = sql.NullInt64{}
			if err := m.save(ctx, q, job); err != nil {
				return err
			}
			// A batch that recorded a source conflict stays.
			if deleted, err := q.DeleteEmptyBatch(ctx, empty.Int64); err != nil || deleted == 0 {
				job.BatchID = empty
				if err != nil {
					return err
				}
			}
		}
	}
	if !job.BatchID.Valid {
		previous, err := q.PreviousMonthlyBatch(ctx, dbgen.PreviousMonthlyBatchParams{
			SourceAccountID: job.SourceAccountID.Int64, Filename: reportFilename(job)})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		job.BatchID = sql.NullInt64{Int64: previous, Valid: err == nil}
	}
	if err := m.save(ctx, q, job); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("monthly import completed", "period", job.PeriodKey)
	return nil
}

// retry schedules another attempt after a transient failure, with bounded
// backoff. Errors other than report failures come from the database and
// stop the step.
func (m *Monthly) retry(ctx context.Context, job dbgen.ScheduledJob, err error) error {
	var failed *integrations.ReportError
	if !errors.As(err, &failed) {
		return err
	}
	if failed.Permanent {
		return m.fail(ctx, job, failed.Code, failed.Detail)
	}
	job.RetryCount++
	if job.RetryCount > maxRetries {
		return m.fail(ctx, job, failed.Code, fmt.Sprintf("%s Retries stopped after %d attempts.", failed.Detail, maxRetries))
	}
	slog.Warn("monthly import step failed", "period", job.PeriodKey, "code", failed.Code, "retry", job.RetryCount)
	backoff := min(firstBackoff<<min(job.RetryCount-1, 8), maxRetryBackoff)
	job.LastErrorCode, job.LastErrorDetail = nullString(failed.Code), nullString(failed.Detail)
	return m.later(ctx, job, m.Now().Add(backoff))
}

// fail stops the job and records its failure notification in the same
// transaction. The batch, if any, stays for the owner.
func (m *Monthly) fail(ctx context.Context, job dbgen.ScheduledJob, code, detail string) error {
	job.State, job.RetryCount = stateFailed, 0
	job.LastErrorCode, job.LastErrorDetail = nullString(code), nullString(detail)
	now := m.Now()
	job.NextAttemptAt = stamp(now).String
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := m.Store.Queries.WithTx(tx)
	if err := m.save(ctx, q, job); err != nil {
		return err
	}
	if err := notify.ReportJobFailure(ctx, q, job, detail, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Warn("monthly import failed", "period", job.PeriodKey, "code", code)
	return nil
}
