package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// CheckResend verifies the key and, when the key may read domains, that the
// sender's domain is verified. Sending-only keys cannot read domains.
func (c *Client) CheckResend(ctx context.Context, apiKey, senderDomain string) Check {
	result, err := c.get(ctx, strings.TrimRight(c.Resend, "/")+"/domains?limit=100", bearer(apiKey))
	if err != nil {
		return failed("Could not reach Resend. Check the network connection and try again.")
	}
	item := Item{Key: "sender_domain", Label: "Sender domain " + senderDomain}
	switch result.status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		var problem struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(result.body, &problem)
		if result.status == http.StatusUnauthorized && problem.Name == "restricted_api_key" {
			item.State, item.Detail = StateUnverified, "The key can only send email, so domain verification cannot be checked"
			return summarize("Connected to Resend", []Item{item})
		}
		return failed("Resend rejected the API key.")
	case http.StatusTooManyRequests:
		return failed("Resend is rate limiting requests. Try again shortly.")
	default:
		return failed("Resend returned an unexpected response. Try again later.")
	}
	var domains struct {
		Data []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(result.body, &domains); err != nil {
		return failed("Resend returned a domain list this app cannot read.")
	}
	item.State, item.Detail = StateMissing, "Add and verify this domain in Resend"
	if domains.HasMore {
		item.State, item.Detail = StateUnverified, "The domain was not in the first 100 domains"
	}
	for _, domain := range domains.Data {
		if strings.EqualFold(domain.Name, senderDomain) {
			if domain.Status == "verified" {
				item.State, item.Detail = StateOK, ""
			} else {
				item.State, item.Detail = StateMissing, "The domain is not verified yet"
			}
			break
		}
	}
	return summarize("Connected to Resend", []Item{item})
}
