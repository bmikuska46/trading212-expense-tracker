package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/jobs"
	"budget-tracking/backend/internal/notify"
)

type notificationList struct {
	Notifications []notificationView `json:"notifications"`
}

func (app *testApp) notifications(cookie *http.Cookie) []notificationView {
	app.t.Helper()
	return decode[notificationList](app.t, app.expect(http.StatusOK, "GET", "/api/notifications", nil, cookie)).Notifications
}

// commitUnreviewed exercises the automatic import path, which still needs review.
func (app *testApp) commitUnreviewed(cookie *http.Cookie, id int64) importDetail {
	app.t.Helper()
	pipeline := imports.New(app.store)
	pipeline.CommitHook = app.options.ImportCommitHook
	pipeline.TypeSafe = app.options.Integrations
	if app.options.Now != nil {
		pipeline.Now = app.options.Now
	}
	timezone, err := app.store.Queries.GetOwnerTimezone(context.Background())
	if err != nil {
		app.t.Fatal(err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		app.t.Fatal(err)
	}
	if err := pipeline.Commit(context.Background(), id, location); err != nil {
		app.t.Fatal(err)
	}
	return decode[importDetail](app.t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/imports/%d", id), nil, cookie))
}

func (app *testApp) commit(cookie *http.Cookie, id int64) importDetail {
	app.t.Helper()
	return decode[importDetail](app.t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", id), nil, cookie))
}

const cashbackOnly = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n" +
	"Spending cashback,2026-08-03 17:45:11,,cashback-only-1,0.27,EUR,,\n"

func TestCommitRecordsReviewNotificationAtomically(t *testing.T) {
	var interrupt atomic.Bool
	app := newTestApp(t, Options{ImportCommitHook: func(inserted int) error {
		if inserted == 6 && interrupt.Load() {
			return errors.New("simulated crash")
		}
		return nil
	}})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")

	// A failed commit leaves neither transactions nor a notification.
	staged := app.imported(cookie, account, "trading212-august.csv", fixture(t))
	interrupt.Store(true)
	if response := app.request("POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie); response.Code != http.StatusInternalServerError {
		t.Fatalf("interrupted commit: %d", response.Code)
	}
	if app.count("SELECT count(*) FROM transactions")+app.count("SELECT count(*) FROM notification_events") != 0 {
		t.Fatal("an interrupted commit left rows or a notification")
	}
	interrupt.Store(false)

	// The commit writes its review notification in the same transaction.
	committed := app.commitUnreviewed(cookie, staged.ID)
	if committed.ReviewStatus != "needs_review" || committed.ConfirmedAt != nil {
		t.Fatalf("committed: %+v", committed)
	}
	listed := app.notifications(cookie)
	if len(listed) != 1 {
		t.Fatalf("notifications: %+v", listed)
	}
	review := listed[0]
	if review.Kind != notify.KindReview || review.Status != "Needs review" || review.NewTransactions != 6 ||
		review.Link != fmt.Sprintf("/imports/?batch=%d", staged.ID) || review.BatchID == nil || *review.BatchID != staged.ID ||
		review.CanDismiss || review.DeliveryState != "pending" || review.Source != fmt.Sprintf("CSV import #%d", staged.ID) {
		t.Fatalf("review notification: %+v", review)
	}
	// The stored email facts carry no merchant names, amounts, or filename.
	var payload string
	if err := app.store.DB.QueryRow("SELECT payload_json FROM notification_events").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Example", "Sample", "12.40", "500", "trading212-august"} {
		if strings.Contains(payload, private) {
			t.Fatalf("payload contains %q: %s", private, payload)
		}
	}
	// The imported rows are in the ledger right away, awaiting review.
	rows := app.batchTransactions(cookie, staged.ID)
	if len(rows) != 6 || rows[0].ReviewStatus != "needs_review" {
		t.Fatalf("ledger rows: %+v", rows)
	}

	// Duplicate-only and cashback-only imports need no review.
	again := app.commitUnreviewed(cookie, app.imported(cookie, account, "again.csv", fixture(t)).ID)
	cashback := app.commitUnreviewed(cookie, app.imported(cookie, account, "cashback.csv", []byte(cashbackOnly)).ID)
	for _, batch := range []importDetail{again, cashback} {
		if batch.InsertedCount != 0 || batch.ReviewStatus != "not_required" {
			t.Fatalf("batch without new rows: %+v", batch)
		}
	}
	if count := app.count("SELECT count(*) FROM notification_events"); count != 1 {
		t.Fatalf("events = %d", count)
	}
}

func TestConfirmImportKeepsTransactions(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	staged := app.imported(cookie, account, "august.csv", fixture(t))

	// Only committed imports with new transactions can be confirmed.
	target := fmt.Sprintf("/api/imports/%d/confirm", staged.ID)
	if response := app.request("POST", target, nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("confirming a staged import: %d", response.Code)
	}
	if response := app.request("POST", "/api/imports/999/confirm", nil, cookie); response.Code != http.StatusNotFound {
		t.Fatalf("confirming a missing import: %d", response.Code)
	}
	app.commitUnreviewed(cookie, staged.ID)
	rows := app.batchTransactions(cookie, staged.ID)

	// An ordinary edit keeps the review notification.
	bistro := byNotesOrPayee(rows, "Example Bistro")
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", bistro.ID),
		editInput(bistro, map[string]any{"notes": "team lunch"}), cookie)).Transaction
	if len(app.notifications(cookie)) != 1 {
		t.Fatal("an ordinary edit dismissed the review")
	}
	if detail := app.detail(cookie, bistro.ID); detail.ImportBatch == nil || detail.ImportBatch.ID != staged.ID ||
		detail.ImportBatch.ReviewStatus != "needs_review" {
		t.Fatalf("import batch in detail: %+v", detail.ImportBatch)
	}
	before := map[int64]int64{}
	for _, row := range app.batchTransactions(cookie, staged.ID) {
		before[row.ID] = row.Revision
	}
	history := app.count("SELECT count(*) FROM transaction_edit_history")
	review := app.notifications(cookie)[0]
	if response := app.request("POST", fmt.Sprintf("/api/notifications/%d/dismiss", review.ID), nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("dismissing a review directly: %d", response.Code)
	}

	// Confirming dismisses the notification without touching transactions.
	confirmed := decode[importDetail](t, app.expect(http.StatusOK, "POST", target, nil, cookie))
	if confirmed.ReviewStatus != "confirmed" || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed: %+v", confirmed)
	}
	if listed := app.notifications(cookie); len(listed) != 0 {
		t.Fatalf("notifications after confirming: %+v", listed)
	}
	after := app.batchTransactions(cookie, staged.ID)
	for _, row := range after {
		if row.Revision != before[row.ID] || row.ReviewStatus != "confirmed" {
			t.Fatalf("row changed: %+v", row)
		}
	}
	if app.count("SELECT count(*) FROM transaction_edit_history") != history || app.count("SELECT count(*) FROM transactions WHERE review_status = 'confirmed'") != 0 {
		t.Fatal("confirming wrote transaction revisions")
	}
	if kept := app.detail(cookie, bistro.ID); kept.Notes != "team lunch" || kept.Revision != edited.Revision {
		t.Fatalf("confirming changed an edit: %+v", kept)
	}
	// The register filter follows the confirmation.
	pending := decode[registerPage](t, app.expect(http.StatusOK, "GET", "/api/transactions?review=needs_review", nil, cookie))
	if pending.Total != 0 {
		t.Fatalf("still needs review: %d", pending.Total)
	}
	// Confirming again is harmless.
	app.expect(http.StatusOK, "POST", target, nil, cookie)
}

func TestSaveEditsAndConfirm(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	staged := app.imported(cookie, account, "august.csv", fixture(t))
	app.commitUnreviewed(cookie, staged.ID)
	bistro := byNotesOrPayee(app.batchTransactions(cookie, staged.ID), "Example Bistro")

	manual := app.transaction(cookie, map[string]any{"accountId": account, "date": "2026-08-20", "kind": "expense", "amount": "3"})
	if response := app.request("PATCH", fmt.Sprintf("/api/transactions/%d", manual.ID),
		editInput(manual, map[string]any{"confirmImport": true}), cookie); response.Code != http.StatusConflict {
		t.Fatalf("confirming a manual transaction: %d", response.Code)
	}

	saved := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", bistro.ID),
		editInput(bistro, map[string]any{"notes": "checked", "confirmImport": true}), cookie)).Transaction
	if saved.Notes != "checked" || saved.ReviewStatus != "confirmed" {
		t.Fatalf("saved: %+v", saved)
	}
	if detail := app.detail(cookie, bistro.ID); detail.ImportBatch == nil || detail.ImportBatch.ReviewStatus != "confirmed" {
		t.Fatalf("batch after save and confirm: %+v", detail.ImportBatch)
	}
	if listed := app.notifications(cookie); len(listed) != 0 {
		t.Fatalf("notifications: %+v", listed)
	}

	// A stale save confirms nothing.
	second := app.imported(cookie, account, "september.csv", []byte(strings.ReplaceAll(strings.ReplaceAll(string(fixture(t)),
		"2026-08", "2026-09"), "anon", "next")))
	app.commitUnreviewed(cookie, second.ID)
	row := app.batchTransactions(cookie, second.ID)[0]
	stale := editInput(row, map[string]any{"confirmImport": true, "revision": row.Revision + 5})
	if response := app.request("PATCH", fmt.Sprintf("/api/transactions/%d", row.ID), stale, cookie); response.Code != http.StatusConflict {
		t.Fatalf("stale save: %d", response.Code)
	}
	if len(app.notifications(cookie)) != 1 {
		t.Fatal("a stale save confirmed the import")
	}
}

// resendServer answers each email with the next queued status, then 200.
type resendServer struct {
	mu       sync.Mutex
	statuses []int
	keys     []string
}

func (s *resendServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)
	s.keys = append(s.keys, r.Header.Get("Idempotency-Key"))
	status := http.StatusOK
	if len(s.statuses) > 0 {
		status, s.statuses = s.statuses[0], s.statuses[1:]
	}
	w.WriteHeader(status)
	if status == http.StatusOK {
		fmt.Fprintf(w, `{"id":"email-%d"}`, len(s.keys))
	}
}

func TestImportSurvivesEmailFailure(t *testing.T) {
	now := localTime("2026-09-01", 9, 0)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	cookie := app.setup()
	ctx := context.Background()
	fake := &resendServer{statuses: []int{http.StatusServiceUnavailable, http.StatusUnprocessableEntity}}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := integrations.NewClient()
	client.Resend = server.URL
	if err := app.store.Queries.SaveIntegration(ctx, dbgen.SaveIntegrationParams{Provider: resend,
		CredentialsJson: `{"apiKey":"re_synthetic"}`, Enabled: 1}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.Queries.SaveNotificationConfig(ctx, dbgen.SaveNotificationConfigParams{Sender: "budget@example.com",
		Recipient: ownerEmail, ApplicationUrl: "https://budget.example.com", Enabled: 1}); err != nil {
		t.Fatal(err)
	}
	worker := jobs.NewNotifications(app.store, client)
	worker.Now = func() time.Time { return now }

	account := app.account(cookie, "Trading 212")
	staged := app.imported(cookie, account, "august.csv", fixture(t))
	app.commitUnreviewed(cookie, staged.ID)

	// Resend fails; the import stays committed and the email is retried on
	// its own schedule.
	if _, err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	review := app.notifications(cookie)[0]
	if review.DeliveryState != "pending" || review.DeliveryError == nil || review.CanResend {
		t.Fatalf("after a failed send: %+v", review)
	}
	if detail := app.detail(cookie, app.batchTransactions(cookie, staged.ID)[0].ID); detail.ImportBatch.ReviewStatus != "needs_review" ||
		app.count("SELECT count(*) FROM transactions") != 6 {
		t.Fatal("an email failure changed the import")
	}
	resendTarget := fmt.Sprintf("/api/notifications/%d/resend", review.ID)
	if response := app.request("POST", resendTarget, nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("resending a pending email: %d", response.Code)
	}
	// The retry is rejected permanently.
	now = now.Add(10 * time.Minute)
	if _, err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	failed := app.notifications(cookie)[0]
	if failed.DeliveryState != "failed" || !failed.CanResend {
		t.Fatalf("after a rejected send: %+v", failed)
	}
	// The owner sends it again, with a new idempotency key.
	resent := decode[notificationView](t, app.expect(http.StatusOK, "POST", resendTarget, nil, cookie))
	if resent.DeliveryState != "pending" || resent.DeliveryError != nil {
		t.Fatalf("resent: %+v", resent)
	}
	if _, err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered := app.notifications(cookie)[0]; delivered.DeliveryState != "delivered" {
		t.Fatalf("delivered: %+v", delivered)
	}
	fake.mu.Lock()
	keys := fake.keys
	fake.mu.Unlock()
	if len(keys) != 3 || keys[0] != keys[1] || keys[2] == keys[1] {
		t.Fatalf("idempotency keys: %v", keys)
	}
}

func TestScheduledImportsAndFailureNotifications(t *testing.T) {
	now := localTime("2026-09-15", 12, 0)
	var woken atomic.Int32
	app := newTestApp(t, Options{Now: func() time.Time { return now }, WakeMonthly: func() { woken.Add(1) }})
	cookie := app.setup()
	ctx := context.Background()

	status := decode[scheduledImportsView](t, app.expect(http.StatusOK, "GET", "/api/scheduled-imports", nil, cookie))
	if status.Enabled || len(status.Jobs) != 0 || status.NextRunAt != "2026-10-01T04:00:00Z" || status.Timezone != "Europe/Bratislava" {
		t.Fatalf("status: %+v", status)
	}
	if response := app.request("POST", "/api/scheduled-imports/2026-08/run", nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("rerun while disabled: %d", response.Code)
	}

	account := app.account(cookie, "Trading 212")
	if err := app.store.Queries.SaveIntegration(ctx, dbgen.SaveIntegrationParams{Provider: trading212,
		CredentialsJson: fmt.Sprintf(`{"environment":"live","apiKey":%q,"apiSecret":%q}`, syntheticKey, syntheticSecret),
		Enabled:         1, EarliestAutomaticPeriod: nullString("2026-08"), SourceAccountID: sql.NullInt64{Int64: account, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"2026-07", "2026-09", "2026-8", "latest"} {
		if response := app.request("POST", "/api/scheduled-imports/"+period+"/run", nil, cookie); response.Code != http.StatusNotFound {
			t.Fatalf("rerun %s: %d", period, response.Code)
		}
	}
	started := decode[scheduledJobView](t, app.expect(http.StatusAccepted, "POST", "/api/scheduled-imports/2026-08/run", nil, cookie))
	if started.Period != "2026-08" || started.State != "pending" || woken.Load() != 1 {
		t.Fatalf("started: %+v, woken %d", started, woken.Load())
	}
	if response := app.request("POST", "/api/scheduled-imports/2026-08/run", nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("rerun while open: %d", response.Code)
	}

	// A failed job shows its cause and appears as a failure notification.
	job, err := app.store.Queries.GetScheduledJob(ctx, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.DB.Exec("UPDATE scheduled_jobs SET state = 'failed', last_error_code = 'forbidden', last_error_detail = 'Missing permission' WHERE id = ?", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := notify.ReportJobFailure(ctx, app.store.Queries, job, "Missing permission", now); err != nil {
		t.Fatal(err)
	}
	status = decode[scheduledImportsView](t, app.expect(http.StatusOK, "GET", "/api/scheduled-imports", nil, cookie))
	if !status.Enabled || len(status.Jobs) != 1 || status.Jobs[0].State != "failed" || !status.Jobs[0].CanRerun ||
		*status.Jobs[0].ErrorDetail != "Missing permission" || status.EarliestPeriod != "2026-08" {
		t.Fatalf("status: %+v", status)
	}
	failure := app.notifications(cookie)[0]
	if failure.Kind != notify.KindFailure || failure.Reason != "Missing permission" || !failure.CanDismiss ||
		failure.Link != "/imports/" || failure.Source != "Trading 212 report for August 2026" {
		t.Fatalf("failure: %+v", failure)
	}
	app.expect(http.StatusNoContent, "POST", fmt.Sprintf("/api/notifications/%d/dismiss", failure.ID), nil, cookie)
	if listed := app.notifications(cookie); len(listed) != 0 {
		t.Fatalf("after dismissing: %+v", listed)
	}
	if response := app.request("POST", "/api/notifications/999/dismiss", nil, cookie); response.Code != http.StatusNotFound {
		t.Fatalf("dismissing a missing notification: %d", response.Code)
	}
	app.expect(http.StatusAccepted, "POST", "/api/scheduled-imports/2026-08/run", nil, cookie)
}

// The email links to /imports/?batch=ID. The page is served to anyone and
// asks for a session; after sign-in it loads the import through the API.
func TestEmailLinkOpensImportAfterSignIn(t *testing.T) {
	app := newTestApp(t, Options{})
	imports := filepath.Join(app.directory, "web", "imports")
	if err := os.MkdirAll(imports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imports, "index.html"), []byte("imports page"), 0600); err != nil {
		t.Fatal(err)
	}
	app.restart()
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	staged := app.imported(cookie, account, "august.csv", fixture(t))
	app.commitUnreviewed(cookie, staged.ID)
	link := app.notifications(cookie)[0].Link

	page := app.request("GET", link, nil, nil)
	if page.Code != http.StatusOK || page.Body.String() != "imports page" {
		t.Fatalf("link page: %d %s", page.Code, page.Body)
	}
	target := fmt.Sprintf("/api/imports/%d", staged.ID)
	if response := app.request("GET", target, nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous import: %d", response.Code)
	}
	signedIn := sessionCookie(t, app.expect(http.StatusOK, "POST", "/api/session",
		map[string]string{"email": ownerEmail, "password": ownerPassword}, nil))
	if detail := decode[importDetail](t, app.expect(http.StatusOK, "GET", target, nil, signedIn)); detail.ReviewStatus != "needs_review" {
		t.Fatalf("import after sign-in: %+v", detail)
	}
}
