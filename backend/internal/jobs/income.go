package jobs

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/income"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/nbs"
	"budget-tracking/backend/internal/rates"
)

const (
	// IncomeInterval is how often dividend and interest history and NBS
	// rates are read again.
	IncomeInterval = 12 * time.Hour
	// CashbackInterval is how often a cashback report is requested.
	CashbackInterval = 24 * time.Hour
	incomeRetry      = 15 * time.Minute
	// Recent history is read again, so records Trading 212 adds late are
	// still found. Records are saved once, so overlaps change nothing.
	historyOverlap  = 7 * 24 * time.Hour
	cashbackOverlap = 3 * 24 * time.Hour
	// maxHistoryPages bounds one read of a history endpoint.
	maxHistoryPages = 200
	// A rate limited page is tried again after a minute, a few times.
	rateLimitWait  = time.Minute
	rateLimitTries = 3
	// maxReportRange keeps one cashback report within a year; a longer gap
	// is caught up a report at a time.
	maxReportRange = 360 * 24 * time.Hour
)

// interestTypes are the cash transactions that pay interest: on free cash
// and from lending shares.
var interestTypes = []string{"INTEREST_ON_FREE_CASH", "LENDING_INTEREST"}

// Income imports dividends, interest, and cashback from Trading 212, from the
// 1st of January of the year of the first sync, and keeps the NBS rates the
// tax conversion needs. Dividends and interest come from the paged history
// endpoints; cashback is only in transaction reports.
type Income struct {
	Store  *database.Store
	Client *integrations.Client
	NBS    *nbs.Client
	Now    func() time.Time
	// Pause waits between rate limited requests and reports false when the
	// context ends first. Tests replace it to skip the wait.
	Pause func(context.Context, time.Duration) bool
	wake  chan struct{}
}

func NewIncome(store *database.Store, client *integrations.Client) *Income {
	return &Income{Store: store, Client: client, NBS: nbs.NewClient(), Now: time.Now, Pause: pause,
		wake: make(chan struct{}, 1)}
}

// Wake makes Run step right away, after the Trading 212 key changes.
func (i *Income) Wake() {
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

// Run blocks until the context ends.
func (i *Income) Run(ctx context.Context) {
	for {
		next, err := i.Step(ctx)
		wait := time.Minute
		if err != nil {
			slog.Error("investment income step failed", "error", err)
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

// due reports whether a part runs now: its time has come, or the key was
// saved after its last attempt.
func due(next, attempt sql.NullString, saved, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, next.String)
	if err != nil || !now.Before(at) {
		return true
	}
	last, err := time.Parse(time.RFC3339, attempt.String)
	return !saved.IsZero() && (err != nil || saved.After(last))
}

// Step runs each part that is due and returns when the next one is.
func (i *Income) Step(ctx context.Context) (time.Time, error) {
	now := i.Now()
	q := i.Store.Queries
	credentials, row, configured, err := Trading212Credentials(ctx, q)
	if err != nil {
		return time.Time{}, err
	}
	state, err := q.GetIncomeState(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if configured {
		if !state.StartedOn.Valid {
			state.StartedOn = nullString(rates.LocalDate(now)[:4] + "-01-01")
			if err := q.StartIncomeHistory(ctx, state.StartedOn); err != nil {
				return time.Time{}, err
			}
		}
		start, err := time.ParseInLocation(time.DateOnly, state.StartedOn.String, rates.Location)
		if err != nil {
			return time.Time{}, err
		}
		saved, _ := time.Parse(time.RFC3339, row.UpdatedAt)
		if readsHistory(credentials.Sync) && due(state.HistoryNextAt, state.HistoryAttemptAt, saved, now) {
			if err := i.history(ctx, credentials, state, start, now); err != nil {
				return time.Time{}, err
			}
		}
		if credentials.Sync.Cashback && due(state.CashbackNextAt, state.CashbackAttemptAt, saved, now) {
			if err := i.cashback(ctx, credentials, state, start, now); err != nil {
				return time.Time{}, err
			}
		}
	}
	if err := i.informative(ctx, now); err != nil {
		return time.Time{}, err
	}
	if due(state.RatesNextAt, sql.NullString{}, time.Time{}, now) {
		if err := i.refreshRates(ctx, now); err != nil {
			return time.Time{}, err
		}
	}
	state, err = q.GetIncomeState(ctx)
	if err != nil {
		return time.Time{}, err
	}
	next := now.Add(time.Hour)
	times := []sql.NullString{state.RatesNextAt}
	if configured && readsHistory(credentials.Sync) {
		times = append(times, state.HistoryNextAt)
	}
	if configured && credentials.Sync.Cashback {
		times = append(times, state.CashbackNextAt)
	}
	for _, value := range times {
		if at, err := time.Parse(time.RFC3339, value.String); err == nil && at.Before(next) {
			next = at
		}
	}
	return next, nil
}

// readsHistory reports whether any part read from the history endpoints is on.
func readsHistory(sync integrations.Trading212Sync) bool { return sync.Dividends || sync.Interest }

// since is where a history read may stop: a week before the last full read,
// or the first imported day.
func since(synced sql.NullString, start time.Time) time.Time {
	at, err := time.Parse(time.RFC3339, synced.String)
	if err != nil || at.Add(-historyOverlap).Before(start) {
		return start
	}
	return at.Add(-historyOverlap)
}

// history reads the dividends and interest that are on. Each is read in
// full on its own, so a missing permission for one does not hold the other
// back.
func (i *Income) history(ctx context.Context, credentials integrations.Trading212Credentials, state dbgen.InvestmentIncomeState,
	start, now time.Time) error {
	q := i.Store.Queries
	var details []string
	permanent := true
	type part struct {
		synced sql.NullString
		read   func(context.Context, integrations.Trading212Credentials, time.Time, time.Time, time.Time) error
		save   func(context.Context, sql.NullString) error
	}
	var parts []part
	if credentials.Sync.Dividends {
		parts = append(parts, part{state.DividendsSyncedAt, i.dividends, q.SaveDividendsSync})
	}
	if credentials.Sync.Interest {
		parts = append(parts, part{state.InterestSyncedAt, i.interest, q.SaveInterestSync})
	}
	for index, part := range parts {
		if index > 0 && !i.Pause(ctx, integrations.HistoryPageInterval) {
			return ctx.Err()
		}
		err := part.read(ctx, credentials, start, since(part.synced, start), now)
		var failed *integrations.FetchError
		switch {
		case err == nil:
			if err := part.save(ctx, stamp(now)); err != nil {
				return err
			}
		case errors.As(err, &failed) && ctx.Err() == nil:
			details = append(details, failed.Detail)
			permanent = permanent && failed.Permanent
		default:
			return err
		}
	}
	next := now.Add(IncomeInterval)
	if len(details) > 0 {
		slog.Warn("investment income history incomplete", "permanent", permanent)
		if !permanent {
			next = now.Add(incomeRetry)
		}
	}
	return q.RecordIncomeHistory(ctx, dbgen.RecordIncomeHistoryParams{HistoryAttemptAt: stamp(now),
		HistoryError: nullString(strings.Join(details, " ")), HistoryNextAt: stamp(next)})
}

// page reads one history page, waiting out a rate limit a few times.
func (i *Income) page(ctx context.Context, fetch func() error) error {
	for try := 1; ; try++ {
		err := fetch()
		var failed *integrations.FetchError
		if err == nil || !errors.As(err, &failed) || !failed.RateLimited || try > rateLimitTries {
			return err
		}
		if !i.Pause(ctx, rateLimitWait) {
			return ctx.Err()
		}
	}
}

// pages reads pages newest first until one holds only records before
// cutoff, or the last page. read returns the times on the page and the next
// page.
func (i *Income) pages(ctx context.Context, cutoff time.Time, read func(next string) ([]time.Time, string, error)) error {
	next := ""
	seen := map[string]bool{}
	for count := 0; count < maxHistoryPages; count++ {
		if count > 0 && !i.Pause(ctx, integrations.HistoryPageInterval) {
			return ctx.Err()
		}
		var times []time.Time
		var following string
		if err := i.page(ctx, func() (err error) {
			times, following, err = read(next)
			return err
		}); err != nil {
			return err
		}
		older := !slices.ContainsFunc(times, func(at time.Time) bool { return !at.Before(cutoff) })
		if older || following == "" || seen[following] {
			return nil
		}
		seen[following], next = true, following
	}
	return &integrations.FetchError{Detail: "Trading 212 returned more history pages than expected."}
}

func (i *Income) dividends(ctx context.Context, credentials integrations.Trading212Credentials, start, cutoff, now time.Time) error {
	q := i.Store.Queries
	saved := 0
	err := i.pages(ctx, cutoff, func(next string) ([]time.Time, string, error) {
		page, err := i.Client.FetchDividends(ctx, credentials, next)
		if err != nil {
			return nil, "", err
		}
		times := make([]time.Time, 0, len(page.Items))
		for _, dividend := range page.Items {
			times = append(times, dividend.PaidAt)
			if dividend.PaidAt.Before(start) {
				continue
			}
			rows, err := q.SaveIncome(ctx, dbgen.SaveIncomeParams{SourceID: "dividend:" + dividend.Reference,
				Kind: income.KindDividend, PaidAt: stamp(dividend.PaidAt).String, PaidOn: rates.LocalDate(dividend.PaidAt),
				Currency: dividend.Currency, AmountMinor: dividend.Gross, SourceType: dividend.Type,
				Ticker: nullString(dividend.Ticker), Isin: nullString(dividend.ISIN), Name: nullString(dividend.Name),
				Quantity: nullString(dividend.Quantity), PerShare: nullString(dividend.PerShare),
				ReceivedCurrency: dividend.ReceivedCurrency, ReceivedMinor: dividend.Received, ImportedAt: stamp(now).String})
			if err != nil {
				return nil, "", err
			}
			saved += int(rows)
		}
		return times, page.Next, nil
	})
	if saved > 0 {
		slog.Info("imported dividends", "count", saved)
	}
	return err
}

func (i *Income) interest(ctx context.Context, credentials integrations.Trading212Credentials, start, cutoff, now time.Time) error {
	q := i.Store.Queries
	saved := 0
	err := i.pages(ctx, cutoff, func(next string) ([]time.Time, string, error) {
		page, err := i.Client.FetchTransactions(ctx, credentials, next)
		if err != nil {
			return nil, "", err
		}
		times := make([]time.Time, 0, len(page.Items))
		for _, transaction := range page.Items {
			times = append(times, transaction.At)
			if transaction.At.Before(start) || !slices.Contains(interestTypes, transaction.Type) {
				continue
			}
			rows, err := q.SaveIncome(ctx, dbgen.SaveIncomeParams{SourceID: "transaction:" + transaction.Reference,
				Kind: income.KindInterest, PaidAt: stamp(transaction.At).String, PaidOn: rates.LocalDate(transaction.At),
				Currency: transaction.Currency, AmountMinor: transaction.Amount, SourceType: transaction.Type,
				ReceivedCurrency: transaction.Currency, ReceivedMinor: transaction.Amount, ImportedAt: stamp(now).String})
			if err != nil {
				return nil, "", err
			}
			saved += int(rows)
		}
		return times, page.Next, nil
	})
	if saved > 0 {
		slog.Info("imported interest", "count", saved)
	}
	return err
}

// cashback requests a transactions report from shortly before the last one
// ended, or polls the report it waits for.
func (i *Income) cashback(ctx context.Context, credentials integrations.Trading212Credentials, state dbgen.InvestmentIncomeState,
	start, now time.Time) error {
	q := i.Store.Queries
	if state.ReportFrom.Valid {
		return i.pollCashback(ctx, credentials, state, start, now)
	}
	at, err := reportCallableAt(ctx, q, "request", integrations.ReportRequestInterval)
	if err != nil {
		return err
	}
	if now.Before(at) {
		return q.ScheduleIncomeCashback(ctx, stamp(at))
	}
	from := start
	if through, err := time.Parse(time.RFC3339, state.CashbackThrough.String); err == nil && through.Add(-cashbackOverlap).After(from) {
		from = through.Add(-cashbackOverlap)
	}
	to := now.Truncate(time.Second)
	if to.Sub(from) > maxReportRange {
		to = from.Add(maxReportRange)
	}
	// Saved before the call, so a lost response is looked up next time.
	if err := q.RequestIncomeReport(ctx, dbgen.RequestIncomeReportParams{ReportFrom: stamp(from), ReportTo: stamp(to),
		RequestedAt: stamp(now)}); err != nil {
		return err
	}
	reportID, err := i.Client.RequestReport(ctx, credentials, from, to)
	if err != nil {
		var failed *integrations.ReportError
		if errors.As(err, &failed) && failed.Code != "unreachable" && failed.Code != "unreadable" {
			// Trading 212 answered with an error, so no report exists.
			if err := q.DropIncomeReport(ctx); err != nil {
				return err
			}
		}
		return i.cashbackFailed(ctx, now, err)
	}
	if err := q.SetIncomeReportID(ctx, nullString(reportID)); err != nil {
		return err
	}
	slog.Info("requested cashback report")
	return q.ScheduleIncomeCashback(ctx, stamp(now.Add(integrations.ReportListInterval)))
}

func (i *Income) pollCashback(ctx context.Context, credentials integrations.Trading212Credentials, state dbgen.InvestmentIncomeState,
	start, now time.Time) error {
	q := i.Store.Queries
	at, err := reportCallableAt(ctx, q, "poll", integrations.ReportListInterval)
	if err != nil {
		return err
	}
	if now.Before(at) {
		return q.ScheduleIncomeCashback(ctx, stamp(at))
	}
	if err := q.PollIncomeReport(ctx, stamp(now)); err != nil {
		return err
	}
	reports, err := i.Client.ListReports(ctx, credentials)
	if err != nil {
		return i.cashbackFailed(ctx, now, err)
	}
	now = i.Now()
	var report integrations.Report
	found := false
	if state.ReportID.Valid {
		index := slices.IndexFunc(reports, func(report integrations.Report) bool { return report.ID == state.ReportID.String })
		if found = index >= 0; found {
			report = reports[index]
		}
	} else {
		// The response to the request was lost; it may still have created
		// the report.
		from, _ := time.Parse(time.RFC3339, state.ReportFrom.String)
		to, _ := time.Parse(time.RFC3339, state.ReportTo.String)
		if report, found = matching(reports, from, to); !found {
			if err := q.DropIncomeReport(ctx); err != nil {
				return err
			}
			return q.ScheduleIncomeCashback(ctx, stamp(now))
		}
		if err := q.SetIncomeReportID(ctx, nullString(report.ID)); err != nil {
			return err
		}
	}
	requested, _ := time.Parse(time.RFC3339, state.ReportRequestedAt.String)
	drop := func(detail string) error {
		if err := q.DropIncomeReport(ctx); err != nil {
			return err
		}
		return i.cashbackFailed(ctx, now, &integrations.ReportError{Code: "report_dropped", Detail: detail})
	}
	switch {
	case !found:
		return drop("Trading 212 does not list the requested cashback report. It is requested again later.")
	case report.Status == integrations.ReportFailed || report.Status == integrations.ReportCanceled:
		return drop("Trading 212 could not generate the cashback report. It is requested again later.")
	case report.Status != integrations.ReportFinished:
		if now.Sub(requested) > reportDeadline {
			return drop("Trading 212 did not finish the cashback report within 24 hours. It is requested again later.")
		}
		wait := integrations.ReportListInterval
		if now.Sub(requested) > quickPolling {
			wait = slowPoll
		}
		return q.ScheduleIncomeCashback(ctx, stamp(now.Add(wait)))
	}
	// A failed download keeps the report; the next poll gets a fresh link.
	data, err := i.Client.DownloadReport(ctx, report.DownloadLink)
	if err != nil {
		return i.cashbackFailed(ctx, now, err)
	}
	credits, err := income.ParseCashback(data)
	if err != nil {
		return drop("The cashback report is not a CSV file this app can read.")
	}
	return i.saveCashback(ctx, credits, start, now)
}

func (i *Income) saveCashback(ctx context.Context, credits []income.Cashback, start, now time.Time) error {
	tx, err := i.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := i.Store.Queries.WithTx(tx)
	saved := 0
	for _, credit := range credits {
		if credit.At.Before(start) {
			continue
		}
		rows, err := q.SaveIncome(ctx, dbgen.SaveIncomeParams{SourceID: "cashback:" + credit.ID, Kind: income.KindCashback,
			PaidAt: stamp(credit.At).String, PaidOn: rates.LocalDate(credit.At), Currency: credit.Currency,
			AmountMinor: credit.Amount, SourceType: credit.Action, ReceivedCurrency: credit.Currency,
			ReceivedMinor: credit.Amount, ImportedAt: stamp(now).String})
		if err != nil {
			return err
		}
		saved += int(rows)
	}
	if err := q.FinishIncomeReport(ctx); err != nil {
		return err
	}
	if err := q.RecordIncomeCashback(ctx, dbgen.RecordIncomeCashbackParams{CashbackAttemptAt: stamp(now),
		CashbackNextAt: stamp(now.Add(CashbackInterval))}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("imported cashback", "count", saved)
	return nil
}

// cashbackFailed records a failed report call. Other errors stop the step.
func (i *Income) cashbackFailed(ctx context.Context, now time.Time, err error) error {
	var failed *integrations.ReportError
	if !errors.As(err, &failed) || ctx.Err() != nil {
		return err
	}
	slog.Warn("cashback report failed", "code", failed.Code, "permanent", failed.Permanent)
	next := now.Add(incomeRetry)
	if failed.Permanent {
		next = now.Add(CashbackInterval)
	}
	return i.Store.Queries.RecordIncomeCashback(ctx, dbgen.RecordIncomeCashbackParams{CashbackAttemptAt: stamp(now),
		CashbackError: nullString(failed.Detail), CashbackNextAt: stamp(next)})
}

// informative adds the EUR value at the latest cached rate to records that
// have none. It is shown for orientation only and never used for tax.
func (i *Income) informative(ctx context.Context, now time.Time) error {
	q := i.Store.Queries
	missing, err := q.IncomeMissingInformative(ctx)
	if err != nil {
		return err
	}
	today := rates.LocalDate(now)
	for _, row := range missing {
		value, date := "1", today
		if row.Currency != "EUR" {
			cached, err := q.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: row.Currency, LocalDate: today})
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			value, date = cached.Rate, cached.PublicationDate
		}
		amount, err := ledger.NewMoney(row.AmountMinor, row.Currency)
		if err != nil {
			continue
		}
		rate, err := ledger.ParseRate(value)
		if err != nil {
			continue
		}
		cents, err := ledger.EURCents(amount, rate)
		if err != nil {
			continue
		}
		if err := q.SetIncomeInformative(ctx, dbgen.SetIncomeInformativeParams{
			InformativeEurCents: sql.NullInt64{Int64: cents, Valid: true}, InformativeRate: nullString(value),
			InformativeRateDate: nullString(date), ID: row.ID}); err != nil {
			return err
		}
	}
	return nil
}

// refreshRates downloads the NBS month files of every year with income in
// another currency, and December before it, and the annual file once a year
// has ended. Files read after their period ended are kept and not read again.
func (i *Income) refreshRates(ctx context.Context, now time.Time) error {
	q := i.Store.Queries
	years, err := q.ForeignIncomeYears(ctx)
	if err != nil {
		return err
	}
	files, err := q.ListNBSFiles(ctx)
	if err != nil {
		return err
	}
	complete := map[string]bool{}
	for _, file := range files {
		complete[file.Period] = file.Complete == 1
	}
	local := now.In(rates.Location)
	current := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, rates.Location)
	var months []time.Time
	var ended []int
	for _, text := range years {
		year, err := strconv.Atoi(text)
		if err != nil {
			continue
		}
		for month := time.Date(year-1, time.December, 1, 0, 0, 0, 0, rates.Location); month.Year() <= year && !month.After(current); month = month.AddDate(0, 1, 0) {
			if !complete[month.Format("2006-01")] && !slices.ContainsFunc(months, month.Equal) {
				months = append(months, month)
			}
		}
		if year < local.Year() && !complete[text] {
			ended = append(ended, year)
		}
	}
	requests := 0
	wait := func() bool {
		requests++
		return requests == 1 || i.Pause(ctx, nbs.RequestInterval)
	}
	failure := ""
	for _, month := range months {
		if !wait() {
			return ctx.Err()
		}
		daily, average, err := i.NBS.Month(ctx, month.Year(), month.Month())
		if err != nil {
			failure = nbsFailure(err)
			break
		}
		// Only a month read after it ended has its official average.
		done := !now.Before(month.AddDate(0, 1, 0)) && len(average) > 0
		if !done {
			average = nil
		}
		if err := i.saveRates(ctx, month.Format("2006-01"), append(daily, average...), done, now); err != nil {
			return err
		}
	}
	for _, year := range ended {
		if failure != "" {
			break
		}
		if !wait() {
			return ctx.Err()
		}
		annual, err := i.NBS.Year(ctx, year)
		if err != nil {
			failure = nbsFailure(err)
			break
		}
		if err := i.saveRates(ctx, strconv.Itoa(year), annual, len(annual) > 0, now); err != nil {
			return err
		}
	}
	next := now.Add(IncomeInterval)
	if failure != "" {
		slog.Warn("NBS rates incomplete")
		next = now.Add(incomeRetry)
	}
	return q.RecordIncomeRates(ctx, dbgen.RecordIncomeRatesParams{RatesCheckedAt: stamp(now), RatesError: nullString(failure),
		RatesNextAt: stamp(next)})
}

func nbsFailure(err error) string {
	var failed *nbs.FetchError
	if errors.As(err, &failed) {
		return failed.Detail
	}
	return "Národná banka Slovenska published a rate file this app cannot read."
}

func (i *Income) saveRates(ctx context.Context, period string, found []nbs.Rate, done bool, now time.Time) error {
	tx, err := i.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := i.Store.Queries.WithTx(tx)
	for _, rate := range found {
		if err := q.SaveNBSRate(ctx, dbgen.SaveNBSRateParams{Currency: rate.Currency, Period: rate.Period, Rate: rate.Rate,
			FetchedAt: stamp(now).String}); err != nil {
			return err
		}
	}
	complete := int64(0)
	if done {
		complete = 1
	}
	if err := q.SaveNBSFile(ctx, dbgen.SaveNBSFileParams{Period: period, FetchedAt: stamp(now).String, Complete: complete}); err != nil {
		return err
	}
	return tx.Commit()
}
