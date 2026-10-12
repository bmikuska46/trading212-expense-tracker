// Package notify records in-app notifications and renders their emails.
// Events are written in the same database transaction as the change they
// report; a separate worker delivers the emails.
package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
)

const (
	KindReview  = "import_review"
	KindFailure = "import_failure"
)

// Payload holds the facts an email reports. It never includes merchant
// names, balances, or amounts. Email is filled before the first delivery
// attempt so retries send exactly the same request.
type Payload struct {
	Kind            string              `json:"kind"`
	BatchID         int64               `json:"batchId,omitempty"`
	JobID           int64               `json:"jobId,omitempty"`
	Source          string              `json:"source"`
	Status          string              `json:"status"`
	NewTransactions int64               `json:"newTransactions,omitempty"`
	Reason          string              `json:"reason,omitempty"`
	Email           *integrations.Email `json:"email,omitempty"`
}

// NewKey returns a fresh idempotency key for one delivery.
func NewKey(kind string) string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	return "budget-" + strings.ReplaceAll(kind, "_", "-") + "-" + hex.EncodeToString(buffer)
}

func stamp(instant time.Time) string { return instant.UTC().Format(time.RFC3339) }

// Parse reads a stored payload.
func Parse(text string) Payload {
	var payload Payload
	_ = json.Unmarshal([]byte(text), &payload)
	return payload
}

// Encode stores a payload.
func Encode(payload Payload) string {
	data, _ := json.Marshal(payload)
	return string(data)
}

// ReportSource names a scheduled Trading 212 report for its month.
func ReportSource(period string) string {
	month, err := time.Parse("2006-01", period)
	if err != nil {
		return "Trading 212 monthly report"
	}
	return "Trading 212 report for " + month.Format("January 2006")
}

// BatchSource describes an import without its filename, which the owner chose
// and may be personal.
func BatchSource(ctx context.Context, q *dbgen.Queries, batch dbgen.ImportBatch) (string, error) {
	job, err := q.JobForBatch(ctx, sql.NullInt64{Int64: batch.ID, Valid: true})
	switch {
	case err == nil:
		return ReportSource(job.PeriodKey), nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	return fmt.Sprintf("CSV import #%d", batch.ID), nil
}

// CreateReview records the review notification for a committed batch. The
// caller runs it inside the commit transaction.
func CreateReview(ctx context.Context, q *dbgen.Queries, batch dbgen.ImportBatch, inserted int64, now time.Time) error {
	source, err := BatchSource(ctx, q, batch)
	if err != nil {
		return err
	}
	payload := Payload{Kind: KindReview, BatchID: batch.ID, Source: source, Status: "Needs review", NewTransactions: inserted}
	_, err = q.CreateNotificationEvent(ctx, dbgen.CreateNotificationEventParams{
		BatchID: sql.NullInt64{Int64: batch.ID, Valid: true}, Kind: KindReview, PayloadJson: Encode(payload),
		IdempotencyKey: NewKey(KindReview), NextAttemptAt: stamp(now),
	})
	return err
}

// ReportJobFailure records a failed scheduled import. While an earlier
// failure of the same job is unresolved, the notification only shows the new
// cause and no second email is sent. Once the owner dismissed it, or the job
// completed, a new failure alerts again.
func ReportJobFailure(ctx context.Context, q *dbgen.Queries, job dbgen.ScheduledJob, reason string, now time.Time) error {
	payload := Payload{Kind: KindFailure, JobID: job.ID, BatchID: job.BatchID.Int64, Source: ReportSource(job.PeriodKey),
		Status: "Import failed", Reason: reason}
	existing, err := q.GetJobFailure(ctx, sql.NullInt64{Int64: job.ID, Valid: true})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = q.CreateNotificationEvent(ctx, dbgen.CreateNotificationEventParams{
			BatchID: job.BatchID, JobID: sql.NullInt64{Int64: job.ID, Valid: true}, Kind: KindFailure,
			PayloadJson: Encode(payload), IdempotencyKey: NewKey(KindFailure), NextAttemptAt: stamp(now),
		})
		return err
	case err != nil:
		return err
	case !existing.DismissedAt.Valid || existing.DeliveryState == "sending":
		// A pending retry must send exactly the stored email.
		payload.Email = Parse(existing.PayloadJson).Email
		return q.UpdateJobFailure(ctx, dbgen.UpdateJobFailureParams{BatchID: job.BatchID, PayloadJson: Encode(payload), ID: existing.ID})
	}
	return q.RearmJobFailure(ctx, dbgen.RearmJobFailureParams{BatchID: job.BatchID, PayloadJson: Encode(payload),
		IdempotencyKey: NewKey(KindFailure), NextAttemptAt: stamp(now), ID: existing.ID})
}

// Link is the app page the notification points to.
func Link(applicationURL string, payload Payload) string {
	if payload.BatchID != 0 {
		return fmt.Sprintf("%s/imports/?batch=%d", strings.TrimRight(applicationURL, "/"), payload.BatchID)
	}
	return strings.TrimRight(applicationURL, "/") + "/imports/"
}

// Render builds the email for a payload.
func Render(payload Payload, config dbgen.NotificationConfig) integrations.Email {
	link := Link(config.ApplicationUrl, payload)
	var subject, intro, action string
	lines := []string{"Status: " + payload.Status, "Import: " + payload.Source}
	if payload.Kind == KindReview {
		subject = "Budget: an import needs review"
		intro = "New transactions were imported and are already in your ledger. Review them and confirm the import."
		lines = append(lines, fmt.Sprintf("New transactions: %d", payload.NewTransactions))
		action = "Review the import"
	} else {
		subject = "Budget: an import failed"
		intro = "A scheduled import could not finish. Nothing more is sent about this failure until it is resolved."
		if payload.Reason != "" {
			lines = append(lines, "Cause: "+payload.Reason)
		}
		action = "Open imports"
	}
	text := intro + "\n\n" + strings.Join(lines, "\n") + "\n\n" + action + ": " + link + "\n"
	var body strings.Builder
	body.WriteString("<p>" + html.EscapeString(intro) + "</p><ul>")
	for _, line := range lines {
		body.WriteString("<li>" + html.EscapeString(line) + "</li>")
	}
	body.WriteString(fmt.Sprintf(`</ul><p><a href="%s">%s</a></p>`, html.EscapeString(link), html.EscapeString(action)))
	return integrations.Email{From: config.Sender, To: []string{config.Recipient}, Subject: subject, Text: text, HTML: body.String()}
}

// WithEmail stores the rendered email in the payload.
func WithEmail(payload Payload, email integrations.Email) string {
	payload.Email = &email
	return Encode(payload)
}
