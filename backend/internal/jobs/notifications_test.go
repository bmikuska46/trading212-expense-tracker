package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/notify"
)

type sentEmail struct {
	key  string
	body string
}

// fakeResend answers each email with the next queued status, then with 200.
type fakeResend struct {
	mu       sync.Mutex
	statuses []int
	sent     []sentEmail
}

func (f *fakeResend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.sent = append(f.sent, sentEmail{key: r.Header.Get("Idempotency-Key"), body: string(body)})
	status := http.StatusOK
	if len(f.statuses) > 0 {
		status, f.statuses = f.statuses[0], f.statuses[1:]
	}
	w.WriteHeader(status)
	if status == http.StatusOK {
		fmt.Fprintf(w, `{"id":"email-%d"}`, len(f.sent))
	}
}

func (f *fakeResend) emails() []sentEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentEmail(nil), f.sent...)
}

type notificationHarness struct {
	t      *testing.T
	store  *database.Store
	fake   *fakeResend
	now    time.Time
	worker *Notifications
}

func newNotificationHarness(t *testing.T) *notificationHarness {
	t.Helper()
	h := &notificationHarness{t: t, store: openStore(t), fake: &fakeResend{}}
	server := httptest.NewServer(h.fake)
	t.Cleanup(server.Close)
	client := integrations.NewClient()
	client.Resend = server.URL
	h.worker = NewNotifications(h.store, client)
	h.worker.Now = func() time.Time { return h.now }
	return h
}

func (h *notificationHarness) configure() {
	h.t.Helper()
	ctx := context.Background()
	if err := h.store.Queries.SaveIntegration(ctx, dbgen.SaveIntegrationParams{Provider: "resend",
		CredentialsJson: `{"apiKey":"re_test"}`, Enabled: 1}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.Queries.SaveNotificationConfig(ctx, dbgen.SaveNotificationConfigParams{Sender: "Budget <budget@example.com>",
		Recipient: "owner@example.com", ApplicationUrl: "https://budget.example.com/", Enabled: 1}); err != nil {
		h.t.Fatal(err)
	}
}

// failure records a failed monthly import and returns its event.
func (h *notificationHarness) failure(period string) dbgen.NotificationEvent {
	h.t.Helper()
	ctx := context.Background()
	start, end, _ := MonthBounds(period)
	account, err := h.store.Queries.CreateAccount(ctx, dbgen.CreateAccountParams{Name: "Trading 212 " + period, Currency: "EUR"})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.Queries.EnsureMonthlyJob(ctx, dbgen.EnsureMonthlyJobParams{PeriodKey: period, Timezone: "Europe/Bratislava",
		SourceAccountID: sql.NullInt64{Int64: account.ID, Valid: true},
		PeriodStartUtc: stamp(start), PeriodEndUtc: stamp(end), NextAttemptAt: stamp(h.now).String}); err != nil {
		h.t.Fatal(err)
	}
	var job dbgen.ScheduledJob
	if err := h.store.DB.QueryRow("SELECT id FROM scheduled_jobs WHERE period_key = ?", period).Scan(&job.ID); err != nil {
		h.t.Fatal(err)
	}
	job.PeriodKey = period
	if err := notify.ReportJobFailure(ctx, h.store.Queries, job, "Trading 212 rejected the API key or secret.", h.now); err != nil {
		h.t.Fatal(err)
	}
	event, err := h.store.Queries.GetJobFailure(ctx, sql.NullInt64{Int64: job.ID, Valid: true})
	if err != nil {
		h.t.Fatal(err)
	}
	return event
}

func (h *notificationHarness) step(now time.Time) time.Time {
	h.t.Helper()
	h.now = now
	next, err := h.worker.Step(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return next
}

func (h *notificationHarness) event(id int64) dbgen.NotificationEvent {
	h.t.Helper()
	event, err := h.store.Queries.GetNotificationEvent(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return event
}

func TestNotificationRetriesKeepKeyAndPayload(t *testing.T) {
	h := newNotificationHarness(t)
	h.now = at("2026-09-01", 6, 5)
	event := h.failure("2026-08")

	// Without Resend set up the event waits.
	h.step(at("2026-09-01", 6, 5))
	if len(h.fake.emails()) != 0 || h.event(event.ID).DeliveryState != deliveryPending {
		t.Fatal("sent without configuration")
	}

	// A server error leaves delivery uncertain, so the retry reuses the key.
	h.configure()
	h.fake.statuses = []int{http.StatusInternalServerError}
	next := h.step(at("2026-09-01", 6, 6))
	expectTime(t, next, at("2026-09-01", 6, 11))
	pending := h.event(event.ID)
	if pending.DeliveryState != deliveryPending || pending.LastErrorCode.String != "server_error" || pending.AttemptCount != 1 {
		t.Fatalf("event = %+v", pending)
	}
	h.step(at("2026-09-01", 6, 11))
	delivered := h.event(event.ID)
	if delivered.DeliveryState != deliveryDelivered || delivered.EmailID.String != "email-2" || delivered.LastErrorCode.Valid {
		t.Fatalf("event = %+v", delivered)
	}
	sent := h.fake.emails()
	if len(sent) != 2 || sent[0].key != event.IdempotencyKey || sent[1].key != event.IdempotencyKey || sent[0].body != sent[1].body {
		t.Fatalf("sent = %+v", sent)
	}
	var email integrations.Email
	if err := json.Unmarshal([]byte(sent[0].body), &email); err != nil {
		t.Fatal(err)
	}
	if email.From != "Budget <budget@example.com>" || len(email.To) != 1 || email.To[0] != "owner@example.com" ||
		!strings.Contains(email.Text, "Status: Import failed") || !strings.Contains(email.Text, "Trading 212 report for August 2026") ||
		!strings.Contains(email.Text, "https://budget.example.com/imports/") {
		t.Fatalf("email = %+v", email)
	}
	var attempts int64
	if err := h.store.DB.QueryRow("SELECT count(*) FROM notification_attempts WHERE event_id = ?", event.ID).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts = %d, %v", attempts, err)
	}
	// A delivered email is never sent again.
	h.step(at("2026-09-01", 8, 0))
	if len(h.fake.emails()) != 2 {
		t.Fatal("delivered email sent again")
	}
}

func TestNotificationDeliveryOutcomes(t *testing.T) {
	h := newNotificationHarness(t)
	h.configure()
	h.now = at("2026-09-01", 6, 0)

	// A rejected email fails at once and is not retried.
	rejected := h.failure("2026-06")
	h.fake.statuses = []int{http.StatusUnprocessableEntity}
	h.step(at("2026-09-01", 6, 0))
	if event := h.event(rejected.ID); event.DeliveryState != deliveryFailed || event.LastErrorCode.String != "rejected" {
		t.Fatalf("event = %+v", event)
	}

	// Uncertain attempts stop before Resend forgets the key, and the event
	// is marked uncertain instead of being sent blindly.
	uncertain := h.failure("2026-07")
	h.fake.statuses = make([]int, 100)
	for i := range h.fake.statuses {
		h.fake.statuses[i] = http.StatusBadGateway
	}
	now := at("2026-09-01", 6, 1)
	for range 60 {
		if h.event(uncertain.ID).DeliveryState != deliveryPending {
			break
		}
		now = h.step(now)
	}
	event := h.event(uncertain.ID)
	if event.DeliveryState != deliveryUncertain || event.LastErrorCode.String != "uncertain" {
		t.Fatalf("event = %+v", event)
	}
	first, _ := time.Parse(time.RFC3339, event.FirstAttemptAt.String)
	if last := h.now.Sub(first); last < integrations.IdempotencyWindow {
		t.Fatalf("stopped after %s", last)
	}
	count := len(h.fake.emails())
	h.step(h.now.Add(48 * time.Hour))
	if len(h.fake.emails()) != count {
		t.Fatal("uncertain email sent again")
	}
	for _, email := range h.fake.emails()[1:] {
		if email.key != uncertain.IdempotencyKey {
			t.Fatalf("key changed: %s", email.key)
		}
	}

	// An attempt interrupted by a restart counts as uncertain.
	interrupted := h.failure("2026-08")
	if _, err := h.store.DB.Exec(`UPDATE notification_events SET delivery_state = 'sending', attempt_count = 1,
		first_attempt_at = ? WHERE id = ?`, stamp(h.now).String, interrupted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.Exec(`INSERT INTO notification_attempts (event_id, attempt_number, started_at) VALUES (?, 1, ?)`,
		interrupted.ID, stamp(h.now).String); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := h.store.Queries.AbandonNotificationAttempts(ctx, stamp(h.now)); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Queries.ResumeSendingNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if uncertain, err := h.store.Queries.CountUncertainAttempts(ctx, interrupted.ID); err != nil || uncertain != 1 {
		t.Fatalf("uncertain attempts = %d, %v", uncertain, err)
	}
	if event := h.event(interrupted.ID); event.DeliveryState != deliveryPending {
		t.Fatalf("event = %+v", event)
	}
}
