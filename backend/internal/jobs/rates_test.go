package jobs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/rates"
)

// fakeRates serves a response for the requested base and records each
// request URL. A request for failBase answers 404.
type fakeRates struct {
	mu       sync.Mutex
	date     string
	status   int
	failBase string
	requests []string
}

func (f *fakeRates) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.String())
	base := r.URL.Query().Get("base")
	if f.status != http.StatusOK || base == f.failBase {
		w.WriteHeader(max(f.status, http.StatusNotFound))
		return
	}
	if base == "EUR" {
		fmt.Fprintf(w, `[{"date":%q,"base":"EUR","quote":"EUR","rate":1},{"date":%q,"base":"EUR","quote":"USD","rate":1.0842}]`, f.date, f.date)
		return
	}
	fmt.Fprintf(w, `[{"date":%q,"base":%q,"quote":"EUR","rate":0.9223}]`, f.date, base)
}

func (f *fakeRates) set(date string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.date, f.status = date, status
}

type rateHarness struct {
	t     *testing.T
	store *database.Store
	fake  *fakeRates
	now   time.Time
	job   *Rates
}

func newRateHarness(t *testing.T) *rateHarness {
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
	fake := &fakeRates{status: http.StatusOK}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	h := &rateHarness{t: t, store: store, fake: fake}
	h.job = &Rates{Store: store, Client: &rates.Client{HTTP: server.Client(), URL: server.URL + "/v2/rates"},
		Now: func() time.Time { return h.now }}
	return h
}

func at(date string, hour, minute int) time.Time {
	day, _ := time.Parse(time.DateOnly, date)
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, rates.Location)
}

func (h *rateHarness) step(now time.Time, startup bool) time.Time {
	h.t.Helper()
	h.now = now
	next, err := h.job.Step(context.Background(), startup)
	if err != nil {
		h.t.Fatal(err)
	}
	return next
}

func (h *rateHarness) jobFor(date string) dbgen.ScheduledJob {
	h.t.Helper()
	job, err := h.store.Queries.GetRatesJob(context.Background(), date)
	if err != nil {
		h.t.Fatal(err)
	}
	return job
}

func (h *rateHarness) requests() int {
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	return len(h.fake.requests)
}

func mustUSD(t *testing.T) ledger.Money {
	t.Helper()
	amount, err := ledger.ParseMoney("10.00", "USD")
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func expectTime(t *testing.T, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("time = %s, want %s", got.In(rates.Location), want)
	}
}

func TestDailyRateSchedule(t *testing.T) {
	h := newRateHarness(t)
	ctx := context.Background()

	// Startup on Monday morning fetches once; Friday's rates come back.
	h.fake.set("2026-09-11", http.StatusOK)
	next := h.step(at("2026-09-14", 10, 0), true)
	expectTime(t, next, at("2026-09-14", 18, 0))
	if job := h.jobFor("2026-09-14"); job.State != "pending" || job.AttemptCount != 1 {
		t.Fatalf("job = %+v", job)
	}
	// Nothing is due before 18:00.
	expectTime(t, h.step(at("2026-09-14", 12, 0), false), at("2026-09-14", 18, 0))
	if h.requests() != 1 {
		t.Fatalf("requests = %d", h.requests())
	}

	// The 18:00 fetch fails and backs off.
	h.fake.set("2026-09-14", http.StatusServiceUnavailable)
	next = h.step(at("2026-09-14", 18, 0), false)
	expectTime(t, next, at("2026-09-14", 18, 10))
	job := h.jobFor("2026-09-14")
	if job.State != "pending" || job.LastErrorCode.String != "http_status" {
		t.Fatalf("job = %+v", job)
	}
	// The retry succeeds and completes the day; the next run is tomorrow.
	h.fake.set("2026-09-14", http.StatusOK)
	expectTime(t, h.step(at("2026-09-14", 18, 10), false), at("2026-09-15", 18, 0))
	if job := h.jobFor("2026-09-14"); job.State != "completed" {
		t.Fatalf("job = %+v", job)
	}
	expectTime(t, h.step(at("2026-09-14", 20, 0), false), at("2026-09-15", 18, 0))
	next, err := NextRun(ctx, h.store.Queries, at("2026-09-14", 20, 0))
	if err != nil {
		t.Fatal(err)
	}
	expectTime(t, next, at("2026-09-15", 18, 0))
	row, err := h.store.Queries.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: "USD", LocalDate: "2026-09-14"})
	if err != nil || row.PublicationDate != "2026-09-14" || row.ConfirmedThrough.String != "2026-09-14" {
		t.Fatalf("row = %+v, %v", row, err)
	}

	// Tuesday's job starts but the server stops before 18:00.
	h.step(at("2026-09-15", 9, 0), false)
	if h.requests() != 3 {
		t.Fatalf("requests = %d", h.requests())
	}
	// Thursday's restart marks Tuesday missed and fetches the latest rates
	// once, without assigning them to Tuesday or Wednesday.
	h.fake.set("2026-09-16", http.StatusOK)
	h.step(at("2026-09-17", 8, 0), true)
	missed := h.jobFor("2026-09-15")
	if missed.State != "failed" || missed.LastErrorCode.String != "missed" {
		t.Fatalf("missed job = %+v", missed)
	}
	_, err = rates.Convert(ctx, h.store.Queries, mustUSD(t), "2026-09-15", at("2026-09-17", 8, 0))
	if err == nil || !strings.Contains(err.Error(), "never downloaded") {
		t.Fatalf("Tuesday conversion error = %v", err)
	}
	if _, err := rates.Convert(ctx, h.store.Queries, mustUSD(t), "2026-09-16", at("2026-09-17", 8, 0)); err != nil {
		t.Fatalf("Wednesday's publication covers Wednesday: %v", err)
	}

	// Failures near midnight close the day as failed.
	h.fake.set("2026-09-17", http.StatusBadGateway)
	h.step(at("2026-09-17", 18, 0), false)
	h.step(at("2026-09-17", 23, 55), false)
	if job := h.jobFor("2026-09-17"); job.State != "failed" || job.LastErrorCode.String != "http_status" {
		t.Fatalf("job = %+v", job)
	}
	next, err = NextRun(ctx, h.store.Queries, at("2026-09-17", 23, 56))
	if err != nil {
		t.Fatal(err)
	}
	expectTime(t, next, at("2026-09-18", 18, 0))

	// Every request is the plain latest-rates URL with no dates.
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	for _, request := range h.fake.requests {
		if request != "/v2/rates?base=EUR" {
			t.Fatalf("request %q", request)
		}
	}
}

func TestRatesFetchEveryAccountCurrency(t *testing.T) {
	h := newRateHarness(t)
	ctx := context.Background()
	for _, account := range []string{"INSERT INTO accounts (name, currency) VALUES ('Euro', 'EUR')",
		"INSERT INTO accounts (name, currency) VALUES ('Dollar', 'USD')",
		"INSERT INTO accounts (name, currency, archived) VALUES ('Old dollar', 'USD', 1)",
		"INSERT INTO accounts (name, currency) VALUES ('Koruna', 'CZK')"} {
		if _, err := h.store.DB.ExecContext(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	countRates := func(base string) int {
		var count int
		if err := h.store.DB.QueryRowContext(ctx, "SELECT count(*) FROM exchange_rates WHERE base_currency = ?", base).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	h.fake.set("2026-09-14", http.StatusOK)
	h.step(at("2026-09-14", 18, 0), true)
	h.fake.mu.Lock()
	requests := strings.Join(h.fake.requests, " ")
	h.fake.mu.Unlock()
	if requests != "/v2/rates?base=EUR /v2/rates?base=CZK /v2/rates?base=USD" {
		t.Fatalf("requests = %s", requests)
	}
	if job := h.jobFor("2026-09-14"); job.State != "completed" {
		t.Fatalf("job = %+v", job)
	}
	if countRates("EUR") != 2 || countRates("USD") != 1 || countRates("CZK") != 1 {
		t.Fatalf("rates EUR %d USD %d CZK %d", countRates("EUR"), countRates("USD"), countRates("CZK"))
	}
	// The EUR conversion ignores the rates of the other bases.
	row, err := h.store.Queries.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: "EUR", LocalDate: "2026-09-14"})
	if err != nil || row.BaseCurrency != "EUR" || row.Rate != "1" {
		t.Fatalf("row = %+v, %v", row, err)
	}

	// A base the service rejects fails the attempt but keeps the others.
	h.fake.set("2026-09-15", http.StatusOK)
	h.fake.failBase = "CZK"
	h.step(at("2026-09-15", 18, 0), false)
	job := h.jobFor("2026-09-15")
	if job.State != "pending" || job.LastErrorCode.String != "http_status" || !strings.HasPrefix(job.LastErrorDetail.String, "CZK: ") {
		t.Fatalf("job = %+v", job)
	}
	if countRates("EUR") != 4 || countRates("USD") != 2 || countRates("CZK") != 1 {
		t.Fatalf("rates EUR %d USD %d CZK %d", countRates("EUR"), countRates("USD"), countRates("CZK"))
	}
}

func TestFailedMorningFetchRetriesAt1800(t *testing.T) {
	h := newRateHarness(t)
	h.fake.set("2026-09-14", http.StatusInternalServerError)
	// A startup failure at 17:58 retries at 18:00, not 18:03.
	expectTime(t, h.step(at("2026-09-14", 17, 58), true), at("2026-09-14", 18, 0))
	attempts, err := h.store.Queries.LatestRatesAttempts(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Stage != "startup" || attempts[0].Outcome.String != "transient_failure" ||
		attempts[0].ErrorCode.String != "http_status" {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRunRecoversInterruptedAttempts(t *testing.T) {
	h := newRateHarness(t)
	ctx := context.Background()
	h.now = at("2026-09-14", 10, 0)
	if err := h.store.Queries.EnsureRatesJob(ctx, dbgen.EnsureRatesJobParams{PeriodKey: "2026-09-14",
		Timezone: rates.Timezone, NextAttemptAt: "2026-09-14T16:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	job := h.jobFor("2026-09-14")
	if _, err := h.store.Queries.StartRatesAttempt(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Queries.CreateJobAttempt(ctx, dbgen.CreateJobAttemptParams{JobID: job.ID, AttemptNumber: 1,
		Stage: "scheduled", StartedAt: "2026-09-14T07:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	h.fake.set("2026-09-11", http.StatusOK)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.job.Run(runCtx) }()
	deadline := time.Now().Add(5 * time.Second)
	for h.requests() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	attempts, err := h.store.Queries.LatestRatesAttempts(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]string{}
	for _, attempt := range attempts {
		outcomes[attempt.Stage] = attempt.Outcome.String
	}
	if outcomes["scheduled"] != "uncertain" || outcomes["startup"] != "succeeded" {
		t.Fatalf("attempts = %+v", attempts)
	}
}
