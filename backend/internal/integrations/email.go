package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Resend keeps an idempotency key for 24 hours. Retries after that could
// send a second email, so they stop a little earlier.
const IdempotencyWindow = 23 * time.Hour

// Email is the exact request body sent to Resend. It is stored before the
// first attempt so every retry sends the same payload with the same key.
type Email struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

// Delivery outcomes.
const (
	OutcomeDelivered = "delivered"
	OutcomeTransient = "transient_failure"
	OutcomePermanent = "permanent_failure"
	// OutcomeUncertain means Resend may have accepted the email.
	OutcomeUncertain = "uncertain"
)

// DeliveryError describes a failed send. Messages are written here and never
// copy response bodies.
type DeliveryError struct {
	Outcome string
	Code    string
	Detail  string
}

func (e *DeliveryError) Error() string { return e.Detail }

// SendEmail sends one email with an idempotency key and returns its ID.
func (c *Client) SendEmail(ctx context.Context, apiKey, idempotencyKey string, email Email) (string, error) {
	body, _ := json.Marshal(email)
	authorize := func(request *http.Request) {
		bearer(apiKey)(request)
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	result, err := c.post(ctx, strings.TrimRight(c.Resend, "/")+"/emails", body, authorize)
	if err != nil {
		return "", &DeliveryError{Outcome: OutcomeUncertain, Code: "unreachable",
			Detail: "The connection to Resend failed; the email may or may not have been accepted"}
	}
	var reply struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(result.body, &reply)
	switch {
	case result.status == http.StatusOK:
		return reply.ID, nil
	case result.status == http.StatusUnauthorized || result.status == http.StatusForbidden:
		return "", &DeliveryError{Outcome: OutcomePermanent, Code: "unauthorized",
			Detail: "Resend rejected the API key or the sender. Check the Resend integration."}
	case result.status == http.StatusConflict && reply.Name == "concurrent_idempotent_requests":
		return "", &DeliveryError{Outcome: OutcomeTransient, Code: "concurrent",
			Detail: "Resend is still processing an earlier attempt of this email"}
	case result.status == http.StatusConflict:
		return "", &DeliveryError{Outcome: OutcomePermanent, Code: "idempotency_conflict",
			Detail: "Resend already used this idempotency key for a different email"}
	case result.status == http.StatusBadRequest || result.status == http.StatusUnprocessableEntity:
		return "", &DeliveryError{Outcome: OutcomePermanent, Code: "rejected",
			Detail: "Resend rejected the email. Check the sender domain and recipient."}
	case result.status == http.StatusTooManyRequests:
		return "", &DeliveryError{Outcome: OutcomeTransient, Code: "rate_limited", Detail: "Resend is rate limiting requests"}
	case result.status >= 500:
		return "", &DeliveryError{Outcome: OutcomeUncertain, Code: "server_error",
			Detail: "Resend returned a server error; the email may or may not have been accepted"}
	}
	return "", &DeliveryError{Outcome: OutcomeTransient, Code: "unexpected", Detail: "Resend returned an unexpected response"}
}
