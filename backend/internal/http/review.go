package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/jobs"
	"budget-tracking/backend/internal/notify"
	"budget-tracking/backend/internal/rates"
)

const (
	notificationPageSize = 50
	scheduledPageSize    = 24
)

// confirmBatch marks a committed batch as reviewed and dismisses its review
// notification. Its transactions and their revisions stay unchanged; the
// register derives their status from the batch. Categories Jev picked, as
// kept or corrected, become payee rules.
func confirmBatch(ctx context.Context, q txn, id int64, now time.Time) error {
	confirmed := nullString(auth.FormatTime(now))
	changed, err := q.ConfirmBatch(ctx, dbgen.ConfirmBatchParams{ConfirmedAt: confirmed, ID: id})
	if err != nil {
		return err
	}
	if changed == 0 {
		batch, err := q.GetImportBatch(ctx, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return &requestError{status: http.StatusNotFound, message: "Import not found"}
		case err != nil:
			return err
		case batch.ReviewStatus == "confirmed":
			return nil
		case batch.Status != "committed":
			return conflictError("Commit the import before confirming it")
		}
		return conflictError("This import has no new transactions to review")
	}
	if err := imports.LearnRules(ctx, q.Tx, id); err != nil {
		return err
	}
	return q.DismissBatchReview(ctx, dbgen.DismissBatchReviewParams{DismissedAt: confirmed, BatchID: sql.NullInt64{Int64: id, Valid: true}})
}

// cleanRates drops exchange rates that no pending import needs any more. It
// runs after a confirmation commits; a failure only delays the cleanup to the
// next daily rates run.
func (a *api) cleanRates(ctx context.Context) {
	if _, err := rates.Cleanup(ctx, a.store, a.now()); err != nil {
		slog.Error("rate cleanup after confirmation failed", "error", err)
	}
}

func (a *api) confirmImport(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	if err := a.inTx(r.Context(), func(q txn) error { return confirmBatch(r.Context(), q, id, a.now()) }); err != nil {
		writeFailure(w, err, "confirm import")
		return
	}
	a.cleanRates(r.Context())
	a.writeImport(w, r, id, http.StatusOK)
}

type scheduledJobView struct {
	ID            int64   `json:"id"`
	Period        string  `json:"period"`
	State         string  `json:"state"`
	ReportStatus  *string `json:"reportStatus"`
	BatchID       *int64  `json:"batchId"`
	RetryCount    int64   `json:"retryCount"`
	NextAttemptAt string  `json:"nextAttemptAt"`
	ErrorCode     *string `json:"errorCode"`
	ErrorDetail   *string `json:"errorDetail"`
	UpdatedAt     string  `json:"updatedAt"`
	CanRerun      bool    `json:"canRerun"`
}

type scheduledImportsView struct {
	Enabled        bool               `json:"enabled"`
	AccountID      *int64             `json:"accountId"`
	EarliestPeriod string             `json:"earliestPeriod"`
	Timezone       string             `json:"timezone"`
	NextRunAt      string             `json:"nextRunAt"`
	Jobs           []scheduledJobView `json:"jobs"`
}

// monthlySource returns the account and first month of automatic imports,
// and whether they are turned on.
func (a *api) monthlySource(ctx context.Context) (dbgen.Integration, bool, error) {
	row, credentials, found, err := a.loadIntegration(ctx, a.store.Queries, trading212)
	if err != nil || !found {
		return row, false, err
	}
	enabled := row.Enabled == 1 && credentials.APIKey != "" && credentials.APISecret != "" &&
		row.SourceAccountID.Valid && row.EarliestAutomaticPeriod.Valid
	return row, enabled, nil
}

func openJob(state string) bool {
	return state != "completed" && state != "failed" && state != "canceled"
}

func (a *api) listScheduledImports(w http.ResponseWriter, r *http.Request, _ *session) {
	row, enabled, err := a.monthlySource(r.Context())
	if err != nil {
		internalError(w, "load scheduled imports")
		return
	}
	listed, err := a.store.Queries.ListMonthlyJobs(r.Context(), scheduledPageSize)
	if err != nil {
		internalError(w, "list scheduled imports")
		return
	}
	view := scheduledImportsView{Enabled: enabled, EarliestPeriod: row.EarliestAutomaticPeriod.String,
		Timezone: rates.Timezone, NextRunAt: auth.FormatTime(jobs.NextDailyRun(a.now())), Jobs: []scheduledJobView{}}
	if row.SourceAccountID.Valid {
		view.AccountID = &row.SourceAccountID.Int64
	}
	for _, job := range listed {
		item := scheduledJobView{ID: job.ID, Period: job.PeriodKey, State: job.State, ReportStatus: nullText(job.ReportStatus),
			RetryCount: job.RetryCount, NextAttemptAt: job.NextAttemptAt, ErrorCode: nullText(job.LastErrorCode),
			ErrorDetail: nullText(job.LastErrorDetail), UpdatedAt: job.UpdatedAt}
		if job.BatchID.Valid {
			item.BatchID = &job.BatchID.Int64
		}
		item.CanRerun = enabled && !openJob(job.State) && job.SourceAccountID == row.SourceAccountID &&
			job.PeriodKey >= view.EarliestPeriod
		view.Jobs = append(view.Jobs, item)
	}
	writeJSON(w, http.StatusOK, view)
}

// rerunScheduledImport imports a month again from its start. Transactions
// already imported are skipped by their IDs, so a rerun only adds what is
// missing.
func (a *api) rerunScheduledImport(w http.ResponseWriter, r *http.Request, _ *session) {
	period := r.PathValue("period")
	row, enabled, err := a.monthlySource(r.Context())
	if err != nil {
		internalError(w, "load scheduled imports")
		return
	}
	if !enabled {
		writeError(w, http.StatusConflict, "Turn on automatic Trading 212 imports first")
		return
	}
	now := a.now()
	if _, _, err := jobs.MonthBounds(period); err != nil || period < row.EarliestAutomaticPeriod.String ||
		jobs.FirstRunAt(period).After(now) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("Only months from %s up to the current month can be imported",
			row.EarliestAutomaticPeriod.String))
		return
	}
	var started bool
	err = a.inTx(r.Context(), func(q txn) error {
		started, err = jobs.Rerun(r.Context(), q.Queries, row.SourceAccountID.Int64, period, now)
		return err
	})
	if err != nil {
		internalError(w, "rerun scheduled import")
		return
	}
	if !started {
		writeError(w, http.StatusConflict, "This month is still being imported")
		return
	}
	a.wakeMonthly()
	job, err := a.store.Queries.GetMonthlyJob(r.Context(), dbgen.GetMonthlyJobParams{SourceAccountID: row.SourceAccountID, PeriodKey: period})
	if err != nil {
		internalError(w, "load scheduled import")
		return
	}
	writeJSON(w, http.StatusAccepted, scheduledJobView{ID: job.ID, Period: job.PeriodKey, State: job.State,
		RetryCount: job.RetryCount, NextAttemptAt: job.NextAttemptAt, UpdatedAt: job.UpdatedAt})
}

// notificationView never includes the rendered email.
type notificationView struct {
	ID              int64   `json:"id"`
	Kind            string  `json:"kind"`
	Source          string  `json:"source"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason"`
	NewTransactions int64   `json:"newTransactions"`
	BatchID         *int64  `json:"batchId"`
	Link            string  `json:"link"`
	DeliveryState   string  `json:"deliveryState"`
	DeliveryError   *string `json:"deliveryError"`
	CreatedAt       string  `json:"createdAt"`
	CanDismiss      bool    `json:"canDismiss"`
	CanResend       bool    `json:"canResend"`
}

func newNotificationView(event dbgen.NotificationEvent) notificationView {
	payload := notify.Parse(event.PayloadJson)
	view := notificationView{ID: event.ID, Kind: event.Kind, Source: payload.Source, Status: payload.Status,
		Reason: payload.Reason, NewTransactions: payload.NewTransactions, Link: "/imports/",
		DeliveryState: event.DeliveryState, DeliveryError: nullText(event.LastErrorDetail), CreatedAt: event.CreatedAt,
		CanDismiss: event.Kind == notify.KindFailure,
		CanResend:  event.DeliveryState == "failed" || event.DeliveryState == "uncertain"}
	if event.BatchID.Valid {
		view.BatchID = &event.BatchID.Int64
		view.Link = fmt.Sprintf("/imports/?batch=%d", event.BatchID.Int64)
	}
	return view
}

func (a *api) listNotifications(w http.ResponseWriter, r *http.Request, _ *session) {
	events, err := a.store.Queries.ListActiveNotifications(r.Context(), notificationPageSize)
	if err != nil {
		internalError(w, "list notifications")
		return
	}
	views := make([]notificationView, 0, len(events))
	for _, event := range events {
		views = append(views, newNotificationView(event))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": views})
}

func (a *api) notificationEvent(w http.ResponseWriter, r *http.Request) (dbgen.NotificationEvent, bool) {
	id, ok := pathID(w, r, "Notification")
	if !ok {
		return dbgen.NotificationEvent{}, false
	}
	event, err := a.store.Queries.GetNotificationEvent(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Notification not found")
		return event, false
	}
	if err != nil {
		internalError(w, "load notification")
		return event, false
	}
	return event, true
}

// dismissNotification hides a failure alert. Review notifications stay until
// their import is confirmed.
func (a *api) dismissNotification(w http.ResponseWriter, r *http.Request, _ *session) {
	event, ok := a.notificationEvent(w, r)
	if !ok {
		return
	}
	if event.Kind != notify.KindFailure {
		writeError(w, http.StatusConflict, "Confirm the import to dismiss this notification")
		return
	}
	if _, err := a.store.Queries.DismissNotification(r.Context(), dbgen.DismissNotificationParams{
		DismissedAt: nullString(auth.FormatTime(a.now())), ID: event.ID}); err != nil {
		internalError(w, "dismiss notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resendNotification sends a failed or uncertain email again. It is always
// the owner's decision, so it uses a new idempotency key and renders the
// email again with the current settings.
func (a *api) resendNotification(w http.ResponseWriter, r *http.Request, _ *session) {
	event, ok := a.notificationEvent(w, r)
	if !ok {
		return
	}
	payload := notify.Parse(event.PayloadJson)
	payload.Email = nil
	changed, err := a.store.Queries.ResendNotification(r.Context(), dbgen.ResendNotificationParams{
		IdempotencyKey: notify.NewKey(event.Kind), PayloadJson: notify.Encode(payload),
		NextAttemptAt: auth.FormatTime(a.now()), ID: event.ID})
	if err != nil {
		internalError(w, "resend notification")
		return
	}
	if changed == 0 {
		writeError(w, http.StatusConflict, "Only a failed or uncertain email can be sent again")
		return
	}
	event, err = a.store.Queries.GetNotificationEvent(r.Context(), event.ID)
	if err != nil {
		internalError(w, "load notification")
		return
	}
	writeJSON(w, http.StatusOK, newNotificationView(event))
}
