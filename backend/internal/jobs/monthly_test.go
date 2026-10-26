package jobs

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/rates"
	"budget-tracking/backend/migrations"
)

// augustReport is a Trading 212 transactions export for August 2026 with the
// requested one-hour overlap on both sides. Europe/Bratislava is UTC+2 then,
// so the month runs from 2026-07-31 22:00 to 2026-08-31 22:00 UTC.
const augustReport = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n" +
	"Card debit,2026-07-31 21:30:00,,t212-july,-1.00,EUR,Early Shop,RETAIL_STORES\n" +
	"Card debit,2026-07-31 22:00:00,,t212-first,-12.40,EUR,Example Bistro,RESTAURANTS\n" +
	"Spending cashback,2026-08-03 17:45:11,,t212-cashback,0.27,EUR,,\n" +
	"Deposit,2026-08-05 08:00:00,Bank transfer,t212-deposit,500.00,EUR,,\n" +
	"Card debit,2026-08-31 21:59:59,,t212-last,-8.10,EUR,Sample Kiosk,MISCELLANEOUS\n" +
	"Card debit,2026-08-31 22:00:00,,t212-september,-3.00,EUR,Late Shop,RETAIL_STORES\n"

type fakeReport struct {
	id       string
	status   string
	from, to string
}

type call struct {
	method, path string
	at           time.Time
	auth         string
}

// fakeTrading212 serves the history export endpoints and the download links.
type fakeTrading212 struct {
	mu      sync.Mutex
	server  *httptest.Server
	now     func() time.Time
	reports []*fakeReport
	calls   []call
	bodies  []map[string]any
	csv     string
	// status is given to new reports; postStatus fails the next request;
	// dropPost creates the report but loses the response.
	status     string
	postStatus int
	dropPost   bool
	downloads  int
	// override replaces the API while set.
	override http.HandlerFunc
}

func (f *fakeTrading212) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	override := f.override
	f.mu.Unlock()
	if override != nil {
		override(w, r)
		return
	}
	f.serve(w, r)
}

func (f *fakeTrading212) replace(handler http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override = handler
}

func (f *fakeTrading212) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{method: r.Method, path: r.URL.Path, at: f.now(), auth: r.Header.Get("Authorization")})
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v0/equity/history/exports":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
		if f.postStatus != 0 {
			w.WriteHeader(f.postStatus)
			f.postStatus = 0
			return
		}
		report := &fakeReport{id: fmt.Sprint(len(f.reports) + 101), status: f.status,
			from: fmt.Sprint(body["timeFrom"]), to: fmt.Sprint(body["timeTo"])}
		f.reports = append(f.reports, report)
		if f.dropPost {
			f.dropPost = false
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		fmt.Fprintf(w, `{"reportId":%s}`, report.id)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v0/equity/history/exports":
		items := []map[string]any{}
		for _, report := range f.reports {
			item := map[string]any{"reportId": json.Number(report.id), "status": report.status,
				"timeFrom": report.from, "timeTo": report.to}
			if report.status == integrations.ReportFinished {
				item["downloadLink"] = f.server.URL + "/download/" + report.id
			}
			items = append(items, item)
		}
		_ = json.NewEncoder(w).Encode(items)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/download/"):
		f.downloads++
		for _, report := range f.reports {
			if report.id == strings.TrimPrefix(r.URL.Path, "/download/") {
				io.WriteString(w, inRange(f.csv, report.from, report.to))
			}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// inRange keeps the rows of a report whose time is within its range.
func inRange(report, from, to string) string {
	rows, err := csv.NewReader(strings.NewReader(report)).ReadAll()
	if err != nil || len(rows) == 0 {
		return report
	}
	start, end := utc(from), utc(to)
	column := slices.Index(rows[0], "Time (UTC)")
	var out strings.Builder
	writer := csv.NewWriter(&out)
	_ = writer.Write(rows[0])
	for _, row := range rows[1:] {
		at, err := time.Parse(time.DateTime, row[column])
		if err == nil && !at.Before(start) && !at.After(end) {
			_ = writer.Write(row)
		}
	}
	writer.Flush()
	return out.String()
}

func (f *fakeTrading212) setStatus(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, report := range f.reports {
		report.status = status
	}
	f.status = status
}

func (f *fakeTrading212) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, entry := range f.calls {
		if entry.method == method && entry.path == "/api/v0/equity/history/exports" {
			count++
		}
	}
	return count
}

type monthlyHarness struct {
	t       *testing.T
	store   *database.Store
	fake    *fakeTrading212
	client  *integrations.Client
	now     time.Time
	job     *Monthly
	account int64
}

func openStore(t *testing.T) *database.Store {
	t.Helper()
	h := newRateHarness(t)
	return h.store
}

func newMonthlyHarness(t *testing.T, earliest string) *monthlyHarness {
	t.Helper()
	store := openStore(t)
	h := &monthlyHarness{t: t, store: store}
	h.fake = &fakeTrading212{now: func() time.Time { return h.now }, status: integrations.ReportQueued, csv: augustReport}
	h.fake.server = httptest.NewTLSServer(h.fake)
	t.Cleanup(h.fake.server.Close)
	h.client = integrations.NewClient()
	h.client.HTTP.Transport = h.fake.server.Client().Transport
	h.client.Trading212Live = h.fake.server.URL + "/api/v0"
	ctx := context.Background()
	account, err := store.Queries.CreateAccount(ctx, dbgen.CreateAccountParams{Name: "Trading 212", Currency: "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	h.account = account.ID
	if err := store.Queries.SaveIntegration(ctx, dbgen.SaveIntegrationParams{Provider: "trading212",
		CredentialsJson: `{"environment":"live","apiKey":"key-1","apiSecret":"secret-1"}`, Enabled: 1,
		EarliestAutomaticPeriod: nullString(earliest), SourceAccountID: sql.NullInt64{Int64: account.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	return h
}

// restart replaces the worker as a server restart would.
func (h *monthlyHarness) restart() {
	pipeline := imports.New(h.store)
	pipeline.Now = func() time.Time { return h.now }
	h.job = NewMonthly(h.store, h.client, pipeline)
	h.job.Now = func() time.Time { return h.now }
	if err := h.store.Queries.AbandonMonthlyAttempts(context.Background(), stamp(h.now)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *monthlyHarness) step(now time.Time) time.Time {
	h.t.Helper()
	h.now = now
	next, err := h.job.Step(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return next
}

// settle steps until nothing more is due at now and returns the next time.
func (h *monthlyHarness) settle(now time.Time) time.Time {
	h.t.Helper()
	for range 10 {
		if next := h.step(now); !next.Equal(now) {
			return next
		}
	}
	h.t.Fatal("the worker never settled")
	return now
}

func (h *monthlyHarness) monthJob(period string) dbgen.ScheduledJob {
	h.t.Helper()
	job, err := h.store.Queries.GetMonthlyJob(context.Background(), dbgen.GetMonthlyJobParams{
		SourceAccountID: sql.NullInt64{Int64: h.account, Valid: true}, PeriodKey: period})
	if err != nil {
		h.t.Fatal(err)
	}
	return job
}

func (h *monthlyHarness) count(query string, args ...any) int64 {
	h.t.Helper()
	var count int64
	if err := h.store.DB.QueryRow(query, args...).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func (h *monthlyHarness) externalIDs() []string {
	h.t.Helper()
	rows, err := h.store.DB.Query("SELECT external_id FROM transactions WHERE external_id IS NOT NULL ORDER BY external_id")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			h.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func utc(text string) time.Time {
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		panic(err)
	}
	return value
}

func TestMonthBoundsAcrossDST(t *testing.T) {
	cases := []struct {
		period, start, end, firstRun string
	}{
		// Summer time starts on 29 March and ends on 25 October 2026.
		{"2026-03", "2026-02-28T23:00:00Z", "2026-03-31T22:00:00Z", "2026-03-02T05:00:00Z"},
		{"2026-08", "2026-07-31T22:00:00Z", "2026-08-31T22:00:00Z", "2026-08-02T04:00:00Z"},
		{"2026-10", "2026-09-30T22:00:00Z", "2026-10-31T23:00:00Z", "2026-10-02T04:00:00Z"},
		{"2026-12", "2026-11-30T23:00:00Z", "2026-12-31T23:00:00Z", "2026-12-02T05:00:00Z"},
	}
	for _, c := range cases {
		start, end, err := MonthBounds(c.period)
		if err != nil || !start.Equal(utc(c.start)) || !end.Equal(utc(c.end)) {
			t.Fatalf("%s bounds = %s, %s, %v", c.period, start, end, err)
		}
		if firstRun := FirstRunAt(c.period); !firstRun.Equal(utc(c.firstRun)) {
			t.Fatalf("%s first runs at %s", c.period, firstRun.UTC())
		}
	}
	if _, _, err := MonthBounds("2026-13"); err == nil {
		t.Fatal("invalid month accepted")
	}

	expectTime(t, NextDailyRun(at("2026-09-30", 12, 0)), at("2026-10-01", 6, 0))
	expectTime(t, NextDailyRun(at("2026-10-01", 5, 59)), at("2026-10-01", 6, 0))
	expectTime(t, NextDailyRun(at("2026-10-01", 6, 0)), at("2026-10-02", 6, 0))
	expectTime(t, NextDailyRun(at("2026-10-24", 23, 0)), at("2026-10-25", 6, 0))
	expectTime(t, NextDailyRun(at("2026-12-31", 23, 0)), at("2027-01-01", 6, 0))

	if due := DuePeriods("2026-06", at("2026-09-02", 5, 59)); !slices.Equal(due, []string{"2026-06", "2026-07", "2026-08"}) {
		t.Fatalf("due before 06:00 = %v", due)
	}
	if due := DuePeriods("2026-06", at("2026-09-02", 6, 0)); !slices.Equal(due, []string{"2026-06", "2026-07", "2026-08", "2026-09"}) {
		t.Fatalf("due at 06:00 = %v", due)
	}
	if due := DuePeriods("2026-10", at("2026-10-01", 12, 0)); len(due) != 0 {
		t.Fatalf("a month is due on its 1st: %v", due)
	}
}

func TestReportRowsFilterHalfOpenRange(t *testing.T) {
	start, end, _ := MonthBounds("2026-10")
	report := "Action,Time (UTC),ID,Total,Currency (Total)\n" +
		"Deposit,2026-09-30 21:59:59,before,1.00,EUR\n" +
		"Deposit,2026-09-30 22:00:00,first,1.00,EUR\n" +
		// 02:30 local on 25 October happens twice; both are in October.
		"Deposit,2026-10-25 00:30:00,dst-first,1.00,EUR\n" +
		"Deposit,2026-10-25 01:30:00,dst-second,1.00,EUR\n" +
		"Deposit,2026-10-31 22:59:59,last,1.00,EUR\n" +
		"Deposit,2026-10-31 23:00:00,after,1.00,EUR\n" +
		"Deposit,not a time,unreadable,1.00,EUR\n"
	filtered, count, err := reportRows([]byte(report), start, end, nil)
	if err != nil || count != 5 {
		t.Fatalf("count = %d, %v", count, err)
	}
	for _, id := range []string{"first", "dst-first", "dst-second", "last", "unreadable"} {
		if !strings.Contains(string(filtered), ","+id+",") {
			t.Fatalf("%s dropped:\n%s", id, filtered)
		}
	}
	for _, id := range []string{"before", "after"} {
		if strings.Contains(string(filtered), ","+id+",") {
			t.Fatalf("%s kept:\n%s", id, filtered)
		}
	}
}

func TestMonthlyImportLifecycle(t *testing.T) {
	h := newMonthlyHarness(t, "2026-08")
	ctx := context.Background()

	// At 06:00 on the 1st all of August is requested with a one-hour overlap, transactions only.
	h.step(at("2026-09-01", 6, 0))
	if h.fake.count("POST") != 1 {
		t.Fatalf("posts = %d", h.fake.count("POST"))
	}
	body := h.fake.bodies[0]
	included, _ := json.Marshal(body["dataIncluded"])
	if body["timeFrom"] != "2026-07-31T21:00:00Z" || body["timeTo"] != "2026-08-31T23:00:00Z" ||
		string(included) != `{"includeDividends":false,"includeInterest":false,"includeOrders":false,"includeTransactions":true}` {
		t.Fatalf("request body = %v", body)
	}
	job := h.monthJob("2026-08")
	if job.State != statePolling || job.ReportID.String != "101" || job.PeriodStartUtc.String != "2026-07-31T22:00:00Z" ||
		job.PeriodEndUtc.String != "2026-08-31T22:00:00Z" || job.Timezone != rates.Timezone {
		t.Fatalf("job = %+v", job)
	}
	// The report list is not called before a minute has passed.
	expectTime(t, h.step(at("2026-09-01", 6, 0).Add(30*time.Second)), at("2026-09-01", 6, 1))
	if h.fake.count("GET") != 0 {
		t.Fatal("listed too early")
	}

	// A restart resumes polling the saved report without requesting another.
	h.restart()
	h.step(at("2026-09-01", 6, 1))
	if job := h.monthJob("2026-08"); job.State != statePolling || job.ReportStatus.String != integrations.ReportQueued {
		t.Fatalf("job = %+v", job)
	}
	h.fake.setStatus(integrations.ReportProcessing)
	h.restart()
	h.step(at("2026-09-01", 6, 2).Add(time.Second))
	if h.fake.count("POST") != 1 || h.fake.count("GET") != 2 {
		t.Fatalf("posts = %d, lists = %d", h.fake.count("POST"), h.fake.count("GET"))
	}

	// The finished report is downloaded, filtered to August, and committed.
	h.fake.setStatus(integrations.ReportFinished)
	h.step(at("2026-09-01", 6, 4))
	job = h.monthJob("2026-08")
	if job.State != stateCompleted || !job.BatchID.Valid || job.ReportStatus.String != integrations.ReportFinished {
		t.Fatalf("job = %+v", job)
	}
	batch, err := h.store.Queries.GetImportBatch(ctx, job.BatchID.Int64)
	if err != nil || batch.Status != "committed" || batch.InsertedCount != 3 || batch.RowCount != 4 ||
		batch.ReviewStatus != "confirmed" || batch.Filename != "trading212-2026-08.csv" {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
	ids := h.externalIDs()
	if !slices.Equal(ids, []string{"t212-deposit", "t212-first", "t212-last"}) {
		t.Fatalf("ids = %v", ids)
	}
	if first := h.count("SELECT count(*) FROM transactions WHERE external_id = 't212-first' AND local_date = '2026-08-01'"); first != 1 {
		t.Fatal("the boundary row is not dated 1 August")
	}
	if events := h.count("SELECT count(*) FROM notification_events"); events != 0 {
		t.Fatalf("events = %d", events)
	}
	// Credentials go to the API but never to the download link.
	h.fake.mu.Lock()
	for _, entry := range h.fake.calls {
		isDownload := strings.HasPrefix(entry.path, "/download/")
		if isDownload == (entry.auth != "") {
			t.Fatalf("authorization on %s = %q", entry.path, entry.auth)
		}
	}
	h.fake.mu.Unlock()

	// The completed month is not requested again, and September waits for
	// its first daily sync.
	expectTime(t, h.step(at("2026-09-01", 7, 0)), at("2026-09-02", 6, 0))
	if h.fake.count("POST") != 1 {
		t.Fatal("completed month requested again")
	}

	// A manual rerun requests the month again and skips the existing IDs.
	// Its batch added nothing, so it is deleted and the month keeps the first.
	h.now = at("2026-09-01", 9, 0)
	started, err := Rerun(ctx, h.store.Queries, h.account, "2026-08", h.now)
	if err != nil || !started {
		t.Fatalf("rerun = %v, %v", started, err)
	}
	if again, err := Rerun(ctx, h.store.Queries, h.account, "2026-08", h.now); err != nil || again {
		t.Fatalf("rerun while open = %v, %v", again, err)
	}
	h.step(at("2026-09-01", 9, 0))
	h.step(at("2026-09-01", 9, 1).Add(time.Second))
	job = h.monthJob("2026-08")
	if job.State != stateCompleted || job.ReportID.String != "102" || h.fake.count("POST") != 2 {
		t.Fatalf("rerun job = %+v", job)
	}
	if job.BatchID.Int64 != batch.ID || h.count("SELECT count(*) FROM import_batches") != 1 {
		t.Fatalf("rerun job = %+v, batches = %d", job, h.count("SELECT count(*) FROM import_batches"))
	}
	if ids := h.externalIDs(); !slices.Equal(ids, []string{"t212-deposit", "t212-first", "t212-last"}) {
		t.Fatalf("ids after rerun = %v", ids)
	}
	if events := h.count("SELECT count(*) FROM notification_events"); events != 0 {
		t.Fatalf("events after duplicate-only rerun = %d", events)
	}
}

// run steps on the worker's own schedule through until and returns the
// next time it asks for.
func (h *monthlyHarness) run(from, until time.Time) time.Time {
	h.t.Helper()
	now := from
	for range 100 {
		if now.After(until) {
			return now
		}
		now = h.step(now)
	}
	h.t.Fatal("the worker never stopped")
	return now
}

func TestMonthlyImportSyncsDaily(t *testing.T) {
	h := newMonthlyHarness(t, "2026-08")
	h.fake.status = integrations.ReportFinished
	posts := func() []string {
		h.fake.mu.Lock()
		defer h.fake.mu.Unlock()
		ranges := []string{}
		for _, body := range h.fake.bodies {
			ranges = append(ranges, fmt.Sprint(body["timeFrom"], " ", body["timeTo"]))
		}
		return ranges
	}

	// August is first synced at 06:00 on the 2nd, through the 1st.
	expectTime(t, h.step(at("2026-08-02", 5, 0)), at("2026-08-02", 6, 0))
	if len(posts()) != 0 {
		t.Fatal("requested before the first run")
	}
	expectTime(t, h.run(at("2026-08-02", 6, 0), at("2026-08-02", 7, 0)), at("2026-08-03", 6, 0))
	job := h.monthJob("2026-08")
	if job.State != stateCompleted || job.ReportTo.String != "2026-08-01T22:00:00Z" {
		t.Fatalf("job = %+v", job)
	}
	if ids := h.externalIDs(); !slices.Equal(ids, []string{"t212-first"}) {
		t.Fatalf("ids = %v", ids)
	}
	batch, err := h.store.Queries.GetImportBatch(context.Background(), job.BatchID.Int64)
	if err != nil || batch.ReviewStatus != "confirmed" || h.count("SELECT count(*) FROM notification_events") != 0 {
		t.Fatalf("batch = %+v, %v", batch, err)
	}

	// Each day only the days since the last sync are requested.
	h.run(at("2026-08-03", 6, 0), at("2026-08-03", 7, 0))
	if job := h.monthJob("2026-08"); job.State != stateCompleted || job.ReportTo.String != "2026-08-02T22:00:00Z" {
		t.Fatalf("job = %+v", job)
	}
	// Missed days are caught up by the next run.
	h.run(at("2026-08-06", 6, 0), at("2026-08-06", 7, 0))
	if ids := h.externalIDs(); !slices.Equal(ids, []string{"t212-deposit", "t212-first"}) {
		t.Fatalf("ids = %v", ids)
	}

	// On the 1st the month is synced through its end and then left alone.
	h.run(at("2026-09-01", 6, 0), at("2026-09-01", 7, 0))
	if job := h.monthJob("2026-08"); job.State != stateCompleted || job.ReportTo.String != job.PeriodEndUtc.String {
		t.Fatalf("job = %+v", job)
	}
	if ids := h.externalIDs(); !slices.Equal(ids, []string{"t212-deposit", "t212-first", "t212-last"}) {
		t.Fatalf("ids = %v", ids)
	}
	h.run(at("2026-09-02", 6, 0), at("2026-09-03", 7, 0))
	want := []string{
		"2026-07-31T21:00:00Z 2026-08-01T23:00:00Z",
		"2026-08-01T21:00:00Z 2026-08-02T23:00:00Z",
		"2026-08-02T21:00:00Z 2026-08-05T23:00:00Z",
		"2026-08-05T21:00:00Z 2026-08-31T23:00:00Z",
		"2026-08-31T21:00:00Z 2026-09-01T23:00:00Z",
		"2026-09-01T21:00:00Z 2026-09-02T23:00:00Z",
	}
	if got := posts(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v", got)
	}
	if h.count("SELECT count(*) FROM notification_events") != 0 {
		t.Fatal("a daily sync notified")
	}
	if dupes := h.count("SELECT coalesce(sum(duplicate_count), 0) FROM import_batches"); dupes != 0 {
		t.Fatalf("daily syncs staged %d duplicates", dupes)
	}
	// Only the syncs that added transactions keep a batch.
	if batches := h.count("SELECT count(*) FROM import_batches WHERE inserted_count > 0"); batches != 4 ||
		h.count("SELECT count(*) FROM import_batches") != 4 {
		t.Fatalf("batches = %d", h.count("SELECT count(*) FROM import_batches"))
	}
	if job := h.monthJob("2026-08"); h.count("SELECT inserted_count FROM import_batches WHERE id = ?", job.BatchID.Int64) != 1 {
		t.Fatalf("august points at batch %d", job.BatchID.Int64)
	}
}

func TestRerunSkipsRowsTheOwnerHandled(t *testing.T) {
	h := newMonthlyHarness(t, "2026-08")
	ctx := context.Background()
	h.fake.status = integrations.ReportFinished
	h.fake.csv = augustReport + "Unknown action,2026-08-01 10:00:00,,t212-unmapped,-5.00,EUR,Shop,RETAIL_STORES\n"

	// An unknown action stops the first sync for the owner.
	h.run(at("2026-08-02", 6, 0), at("2026-08-02", 7, 0))
	job := h.monthJob("2026-08")
	if job.State != stateFailed || job.LastErrorCode.String != "needs_attention" {
		t.Fatalf("job = %+v", job)
	}
	// The owner excludes it and commits the import, which completes the job.
	pipeline := imports.New(h.store)
	var row int64
	if err := h.store.DB.QueryRow("SELECT row_number FROM staging_rows WHERE batch_id = ? AND raw_record_json LIKE '%t212-unmapped%'",
		job.BatchID.Int64).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.CorrectRow(ctx, job.BatchID.Int64, row, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Validate(ctx, job.BatchID.Int64, rates.Location); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.CommitReviewed(ctx, job.BatchID.Int64, rates.Location); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Queries.CompleteJobsForBatch(ctx, job.BatchID); err != nil {
		t.Fatal(err)
	}

	// A rerun's report still has the row, but the import leaves it out.
	h.now = at("2026-08-03", 9, 0)
	if started, err := Rerun(ctx, h.store.Queries, h.account, "2026-08", h.now); err != nil || !started {
		t.Fatalf("rerun = %v, %v", started, err)
	}
	h.run(h.now, at("2026-08-03", 10, 0))
	synced := h.monthJob("2026-08")
	if synced.State != stateCompleted || synced.ReportTo.String != "2026-08-02T22:00:00Z" || synced.BatchID != job.BatchID {
		t.Fatalf("job = %+v", synced)
	}
	if failures := h.count("SELECT count(*) FROM notification_events WHERE kind = 'import_failure'"); failures != 1 {
		t.Fatalf("failures = %d", failures)
	}
	if ids := h.externalIDs(); !slices.Equal(ids, []string{"t212-first"}) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestDailySyncStartsMidMonthAfterUpgrade(t *testing.T) {
	h := newMonthlyHarness(t, "2026-09")
	h.fake.status = integrations.ReportFinished
	h.fake.csv = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n" +
		"Card debit,2026-09-10 10:00:00,,t212-september,-4.00,EUR,Example Bistro,RESTAURANTS\n" +
		"Card debit,2026-10-05 10:00:00,,t212-october-5,-6.00,EUR,Example Bistro,RESTAURANTS\n" +
		"Card debit,2026-10-19 21:30:00,,t212-october-19,-7.00,EUR,Late Shop,RETAIL_STORES\n" +
		"Card debit,2026-10-20 07:00:00,,t212-october-20,-8.00,EUR,Sample Kiosk,MISCELLANEOUS\n"

	// The monthly version imported September on 1 October, then the upgrade
	// filled report_to.
	h.run(at("2026-10-01", 6, 0), at("2026-10-01", 7, 0))
	migration, err := fs.ReadFile(migrations.Files, "000019_daily_sync.sql")
	if err != nil {
		t.Fatal(err)
	}
	backfill := string(migration[strings.Index(string(migration), "UPDATE"):])
	if _, err := h.store.DB.Exec("UPDATE scheduled_jobs SET report_to = NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.Exec(backfill); err != nil {
		t.Fatal(err)
	}

	// Deployed on 20 October, the server syncs October through the 19th at
	// once and then waits for the next morning.
	h.restart()
	expectTime(t, h.run(at("2026-10-20", 10, 0), at("2026-10-20", 11, 0)), at("2026-10-21", 6, 0))
	h.fake.mu.Lock()
	bodies := slices.Clone(h.fake.bodies)
	h.fake.mu.Unlock()
	if len(bodies) != 2 || bodies[1]["timeFrom"] != "2026-09-30T21:00:00Z" || bodies[1]["timeTo"] != "2026-10-19T23:00:00Z" {
		t.Fatalf("requests = %v", bodies)
	}
	if ids := h.externalIDs(); !slices.Equal(ids, []string{"t212-october-19", "t212-october-5", "t212-september"}) {
		t.Fatalf("ids = %v", ids)
	}
	if job := h.monthJob("2026-09"); job.State != stateCompleted || job.ReportTo.String != job.PeriodEndUtc.String {
		t.Fatalf("september = %+v", job)
	}

	h.run(at("2026-10-21", 6, 0), at("2026-10-21", 7, 0))
	if ids := h.externalIDs(); !slices.Contains(ids, "t212-october-20") || len(ids) != 4 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestMonthlyReportFailures(t *testing.T) {
	h := newMonthlyHarness(t, "2026-08")
	ctx := context.Background()

	// A failed report stops the job and records one failure notification.
	h.fake.status = integrations.ReportFailed
	h.step(at("2026-09-01", 6, 0))
	h.step(at("2026-09-01", 6, 1))
	job := h.monthJob("2026-08")
	if job.State != stateFailed || job.LastErrorCode.String != "report_failed" {
		t.Fatalf("job = %+v", job)
	}
	failure, err := h.store.Queries.GetJobFailure(ctx, sql.NullInt64{Int64: job.ID, Valid: true})
	if err != nil || failure.DeliveryState != "pending" || !strings.Contains(failure.PayloadJson, "could not generate") {
		t.Fatalf("failure = %+v, %v", failure, err)
	}
	// The failed job stays failed; later steps neither retry nor alert again.
	for minute := 2; minute < 10; minute++ {
		h.step(at("2026-09-01", 6, minute))
	}
	if h.fake.count("POST") != 1 || h.count("SELECT count(*) FROM notification_events") != 1 {
		t.Fatalf("posts = %d", h.fake.count("POST"))
	}
	if _, err := h.store.DB.Exec("UPDATE notification_events SET delivery_state = 'delivered', email_id = 'email-1'"); err != nil {
		t.Fatal(err)
	}

	// A rerun resolves the failure; a canceled report fails again and alerts
	// again with a new idempotency key.
	h.now = at("2026-09-01", 7, 0)
	if started, err := Rerun(ctx, h.store.Queries, h.account, "2026-08", h.now); err != nil || !started {
		t.Fatalf("rerun = %v, %v", started, err)
	}
	h.fake.status = integrations.ReportCanceled
	h.step(at("2026-09-01", 7, 0))
	h.step(at("2026-09-01", 7, 1).Add(time.Second))
	job = h.monthJob("2026-08")
	again, err := h.store.Queries.GetJobFailure(ctx, sql.NullInt64{Int64: job.ID, Valid: true})
	if job.State != stateFailed || job.LastErrorCode.String != "report_canceled" || err != nil ||
		again.ID != failure.ID || again.DeliveryState != "pending" || again.DismissedAt.Valid ||
		again.IdempotencyKey == failure.IdempotencyKey || again.EmailID.Valid {
		t.Fatalf("job = %+v, failure = %+v, %v", job, again, err)
	}

	// A report that never finishes is given up after 24 hours.
	h.now = at("2026-09-01", 8, 0)
	if started, err := Rerun(ctx, h.store.Queries, h.account, "2026-08", h.now); err != nil || !started {
		t.Fatalf("rerun = %v, %v", started, err)
	}
	h.fake.status = integrations.ReportRunning
	h.step(at("2026-09-01", 8, 0))
	h.step(at("2026-09-01", 8, 1).Add(time.Second))
	// Polling slows to every five minutes after ten minutes.
	next := h.settle(at("2026-09-01", 8, 20))
	expectTime(t, next, at("2026-09-01", 8, 25))
	h.step(at("2026-09-02", 8, 1))
	if job := h.monthJob("2026-08"); job.State != stateFailed || job.LastErrorCode.String != "report_unfinished" {
		t.Fatalf("job = %+v", job)
	}
	if h.fake.count("POST") != 3 {
		t.Fatalf("posts = %d", h.fake.count("POST"))
	}
}

func TestMonthlyTransientFailuresRetryWithBackoff(t *testing.T) {
	h := newMonthlyHarness(t, "2026-08")
	h.fake.postStatus = http.StatusTooManyRequests
	expectTime(t, h.step(at("2026-09-01", 6, 0)), at("2026-09-01", 6, 0))
	job := h.monthJob("2026-08")
	if job.State != statePending || job.RetryCount != 1 || job.LastErrorCode.String != "rate_limited" ||
		job.NextAttemptAt != stamp(at("2026-09-01", 6, 5)).String {
		t.Fatalf("job = %+v", job)
	}
	if h.count("SELECT count(*) FROM notification_events") != 0 {
		t.Fatal("a transient failure alerted")
	}
	// The retry succeeds and resets the count.
	h.step(at("2026-09-01", 6, 5))
	if job := h.monthJob("2026-08"); job.State != statePolling || job.RetryCount != 0 || job.LastErrorCode.Valid {
		t.Fatalf("job = %+v", job)
	}

	// A permanent failure stops at once with its cause.
	h = newMonthlyHarness(t, "2026-08")
	h.fake.postStatus = http.StatusForbidden
	h.step(at("2026-09-01", 6, 0))
	if job := h.monthJob("2026-08"); job.State != stateFailed || job.LastErrorCode.String != "forbidden" ||
		!strings.Contains(job.LastErrorDetail.String, "History reports permission") {
		t.Fatalf("job = %+v", job)
	}
	if h.count("SELECT count(*) FROM notification_events WHERE kind = 'import_failure'") != 1 {
		t.Fatal("permanent failure not notified")
	}

	// Transient failures stop after the retry limit.
	h = newMonthlyHarness(t, "2026-08")
	h.step(at("2026-09-01", 6, 0))
	h.fake.replace(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusRequestTimeout) })
	now := at("2026-09-01", 6, 1).Add(time.Second)
	for range 4 * maxRetries {
		if now = h.step(now); h.monthJob("2026-08").State == stateFailed {
			break
		}
	}
	if job := h.monthJob("2026-08"); job.State != stateFailed || job.LastErrorCode.String != "timeout" ||
		!strings.Contains(job.LastErrorDetail.String, "Retries stopped") {
		t.Fatalf("job = %+v", job)
	}
}

func TestMonthlyRequestResumesAfterLostResponse(t *testing.T) {
	h := newMonthlyHarness(t, "2026-08")
	// The report is created but the response is lost.
	h.fake.dropPost = true
	h.step(at("2026-09-01", 6, 0))
	job := h.monthJob("2026-08")
	if job.State != stateRequesting || job.ReportID.Valid || job.LastErrorCode.String != "unreachable" {
		t.Fatalf("job = %+v", job)
	}
	// After a restart the job finds the report instead of requesting another.
	h.restart()
	h.step(at("2026-09-01", 6, 5))
	job = h.monthJob("2026-08")
	if job.State != statePolling || job.ReportID.String != "101" || h.fake.count("POST") != 1 {
		t.Fatalf("job = %+v, posts = %d", job, h.fake.count("POST"))
	}

	// When the lost request created nothing, the next attempt requests again.
	h = newMonthlyHarness(t, "2026-08")
	h.fake.replace(func(w http.ResponseWriter, r *http.Request) {
		connection, _, _ := w.(http.Hijacker).Hijack()
		connection.Close()
	})
	h.step(at("2026-09-01", 6, 0))
	if job := h.monthJob("2026-08"); job.State != stateRequesting {
		t.Fatalf("job = %+v", job)
	}
	h.fake.replace(nil)
	h.step(at("2026-09-01", 6, 5))
	if job := h.monthJob("2026-08"); job.State != statePolling || job.ReportID.String != "101" || h.fake.count("POST") != 1 {
		t.Fatalf("job = %+v", job)
	}
}

func TestMonthlyCatchUpRespectsRateLimits(t *testing.T) {
	h := newMonthlyHarness(t, "2026-06")
	h.fake.status = integrations.ReportFinished
	h.fake.csv = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n"
	// June's request is rejected; July may not be requested within 30 seconds.
	h.fake.postStatus = http.StatusBadRequest
	h.step(at("2026-09-01", 6, 0))
	if job := h.monthJob("2026-06"); job.State != stateFailed || job.LastErrorCode.String != "rejected" {
		t.Fatalf("june = %+v", job)
	}
	next := h.settle(at("2026-09-01", 6, 0))
	expectTime(t, next, at("2026-09-01", 6, 0).Add(31*time.Second))
	if h.fake.count("POST") != 1 {
		t.Fatalf("posts = %d", h.fake.count("POST"))
	}
	// Run the worker until every month is done, following its schedule.
	now := next
	for range 40 {
		now = h.step(now)
		if now.After(at("2026-09-01", 12, 0)) {
			break
		}
	}
	for _, period := range []string{"2026-07", "2026-08"} {
		if job := h.monthJob(period); job.State != stateCompleted {
			t.Fatalf("%s = %+v", period, job)
		}
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	last := map[string]time.Time{}
	for _, entry := range h.fake.calls {
		if strings.HasPrefix(entry.path, "/download/") {
			continue
		}
		limit := integrations.ReportListInterval
		if entry.method == http.MethodPost {
			limit = integrations.ReportRequestInterval
		}
		if previous, ok := last[entry.method]; ok && entry.at.Sub(previous) < limit {
			t.Fatalf("%s at %s follows %s", entry.method, entry.at, previous)
		}
		last[entry.method] = entry.at
	}
	// Months before the earliest period are never requested.
	jobs, err := h.store.Queries.ListMonthlyJobs(context.Background(), 10)
	if err != nil || len(jobs) != 3 || jobs[2].PeriodKey != "2026-06" {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
}
