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
	"budget-tracking/backend/internal/notify"
)

// Email delivery states.
const (
	deliveryPending   = "pending"
	deliveryDelivered = "delivered"
	deliveryFailed    = "failed"
	deliveryUncertain = "uncertain"
)

// Notifications emails recorded notification events through Resend. It
// runs apart from imports, so a failed email never affects one. Each event
// keeps its idempotency key for all retries, and retries stop before Resend
// forgets the key; after that an event whose delivery is unknown is marked
// uncertain and only sent again when the owner asks.
type Notifications struct {
	Store  *database.Store
	Client *integrations.Client
	Now    func() time.Time
	// Settings holds the Resend key and sender, which all users share. Nil
	// means Store.
	Settings *database.Store
	// Recipient replaces the configured recipient, so a user other than the
	// owner gets their own emails.
	Recipient string
}

func NewNotifications(store *database.Store, client *integrations.Client) *Notifications {
	return &Notifications{Store: store, Client: client, Now: time.Now}
}

// Run blocks until the context ends. It checks at least every minute, so
// events recorded by the app are sent promptly.
func (n *Notifications) Run(ctx context.Context) {
	q := n.Store.Queries
	if err := q.AbandonNotificationAttempts(ctx, stamp(n.Now())); err != nil {
		slog.Error("cannot recover notification attempts", "error", err)
	}
	if err := q.ResumeSendingNotifications(ctx); err != nil {
		slog.Error("cannot resume notifications", "error", err)
	}
	for {
		next, err := n.Step(ctx)
		wait := time.Minute
		if err != nil {
			slog.Error("notification delivery step failed", "error", err)
		} else {
			wait = min(max(next.Sub(n.Now()), time.Second), time.Minute)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type delivery struct {
	apiKey string
	config dbgen.NotificationConfig
}

// delivery returns the Resend settings when email is turned on. Without
// them events stay pending and are sent once email is set up.
func (n *Notifications) delivery(ctx context.Context) (delivery, bool, error) {
	q := n.Store.Queries
	if n.Settings != nil {
		q = n.Settings.Queries
	}
	row, err := q.GetIntegration(ctx, "resend")
	if errors.Is(err, sql.ErrNoRows) {
		return delivery{}, false, nil
	}
	if err != nil {
		return delivery{}, false, err
	}
	var stored struct {
		APIKey string `json:"apiKey"`
	}
	if json.Unmarshal([]byte(row.CredentialsJson), &stored) != nil || stored.APIKey == "" || row.Enabled != 1 {
		return delivery{}, false, nil
	}
	config, err := q.GetNotificationConfig(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return delivery{}, false, nil
	}
	if err != nil || config.Enabled != 1 {
		return delivery{}, false, err
	}
	if n.Recipient != "" {
		config.Recipient = n.Recipient
	}
	return delivery{apiKey: stored.APIKey, config: config}, true, nil
}

// Step sends the events that are due and returns when the next is due.
func (n *Notifications) Step(ctx context.Context) (time.Time, error) {
	settings, ready, err := n.delivery(ctx)
	if err != nil || !ready {
		return n.Now().Add(time.Minute), err
	}
	q := n.Store.Queries
	for {
		event, err := q.DueNotification(ctx, stamp(n.Now()).String)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return time.Time{}, err
		}
		if err := n.send(ctx, event, settings); err != nil {
			return time.Time{}, err
		}
	}
	next, err := q.NextNotificationAt(ctx)
	if err != nil || next == "" {
		return n.Now().Add(time.Minute), err
	}
	at, err := time.Parse(time.RFC3339, next)
	if err != nil {
		return n.Now().Add(time.Minute), nil
	}
	return at, nil
}

func (n *Notifications) finish(ctx context.Context, event dbgen.NotificationEvent, state string, next time.Time, emailID, code, detail string) error {
	return n.Store.Queries.FinishNotification(ctx, dbgen.FinishNotificationParams{
		DeliveryState: state, NextAttemptAt: stamp(next).String, EmailID: nullString(emailID),
		LastErrorCode: nullString(code), LastErrorDetail: nullString(detail), ID: event.ID,
	})
}

// send makes one delivery attempt. The rendered email is stored before the
// first attempt, so every retry sends the same request with the same key.
func (n *Notifications) send(ctx context.Context, event dbgen.NotificationEvent, settings delivery) error {
	q := n.Store.Queries
	now := n.Now()
	if event.FirstAttemptAt.Valid {
		first, _ := time.Parse(time.RFC3339, event.FirstAttemptAt.String)
		if now.Sub(first) >= integrations.IdempotencyWindow {
			uncertain, err := q.CountUncertainAttempts(ctx, event.ID)
			if err != nil {
				return err
			}
			slog.Warn("notification delivery stopped", "event", event.ID, "uncertain", uncertain > 0)
			if uncertain > 0 {
				return n.finish(ctx, event, deliveryUncertain, now, "", "uncertain",
					"The email may or may not have been sent, and Resend no longer protects a retry from a duplicate. Send it again only if it did not arrive.")
			}
			return n.finish(ctx, event, deliveryFailed, now, "", event.LastErrorCode.String,
				"Delivery kept failing for 24 hours. Check the Resend integration and send it again.")
		}
	}
	payload := notify.Parse(event.PayloadJson)
	stored := event.PayloadJson
	if payload.Email == nil {
		email := notify.Render(payload, settings.config)
		payload.Email = &email
		stored = notify.WithEmail(payload, email)
	}
	number, err := q.StartNotificationAttempt(ctx, dbgen.StartNotificationAttemptParams{
		StartedAt: stamp(now), PayloadJson: stored, ID: event.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	attemptID, err := q.CreateNotificationAttempt(ctx, dbgen.CreateNotificationAttemptParams{
		EventID: event.ID, AttemptNumber: number, StartedAt: stamp(now).String,
	})
	if err != nil {
		return err
	}
	emailID, sendErr := n.Client.SendEmail(ctx, settings.apiKey, event.IdempotencyKey, *payload.Email)
	finished := n.Now()
	outcome, code, detail := integrations.OutcomeDelivered, "", ""
	var failed *integrations.DeliveryError
	if errors.As(sendErr, &failed) {
		outcome, code, detail = failed.Outcome, failed.Code, failed.Detail
	} else if sendErr != nil {
		outcome, code, detail = integrations.OutcomeUncertain, "unknown", "The email may or may not have been sent"
	}
	if err := q.FinishNotificationAttempt(ctx, dbgen.FinishNotificationAttemptParams{
		FinishedAt: stamp(finished), Outcome: nullString(outcome), EmailID: nullString(emailID),
		ErrorCode: nullString(code), ErrorDetail: nullString(detail), ID: attemptID,
	}); err != nil {
		return err
	}
	switch outcome {
	case integrations.OutcomeDelivered:
		slog.Info("notification email sent", "event", event.ID)
		return n.finish(ctx, event, deliveryDelivered, finished, emailID, "", "")
	case integrations.OutcomePermanent:
		slog.Warn("notification email failed", "event", event.ID, "code", code)
		return n.finish(ctx, event, deliveryFailed, finished, "", code, detail)
	}
	slog.Warn("notification email will be retried", "event", event.ID, "code", code, "attempt", number)
	backoff := min(firstBackoff<<min(number-1, 8), maxRetryBackoff)
	return n.finish(ctx, event, deliveryPending, finished.Add(backoff), "", code, detail)
}
