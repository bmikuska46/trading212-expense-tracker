package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"budget-tracking/backend/internal/ledger"
)

type Trading212Credentials struct {
	Environment string
	APIKey      string
	APISecret   string
	// Sync is read by the background jobs; the checks ignore it.
	Sync Trading212Sync
}

// Trading212Sync selects what the background jobs read from Trading 212.
// Monthly card transaction imports are the integration's enabled flag.
// Pie snapshots belong to a portfolio snapshot, so Pies needs Portfolio.
type Trading212Sync struct {
	Portfolio bool `json:"portfolio"`
	Pies      bool `json:"pies"`
	Dividends bool `json:"dividends"`
	Interest  bool `json:"interest"`
	Cashback  bool `json:"cashback"`
}

// AllTrading212Sync is the default: everything is read.
var AllTrading212Sync = Trading212Sync{Portfolio: true, Pies: true, Dividends: true, Interest: true, Cashback: true}

// Trading212SyncOrAll returns the stored selection, or everything when a key
// was saved before the selection existed.
func Trading212SyncOrAll(stored *Trading212Sync) Trading212Sync {
	if stored == nil {
		return AllTrading212Sync
	}
	sync := *stored
	sync.Pies = sync.Pies && sync.Portfolio
	return sync
}

// Trading212Needs reports whether a check item is a permission that the
// selected parts use. Account data is always needed.
func Trading212Needs(key string, monthly bool, sync Trading212Sync) bool {
	switch key {
	case "history_transactions":
		return sync.Interest
	case "history_reports":
		return monthly || sync.Cashback
	case "history_dividends":
		return sync.Dividends
	}
	return true
}

// Trading212Account identifies the source account behind a key.
type Trading212Account struct {
	ID       string
	Currency string
}

func (c *Client) trading212Base(environment string) string {
	if environment == "demo" {
		return c.Trading212Demo
	}
	return c.Trading212Live
}

// CheckTrading212 verifies the key and secret, reads the source account, and
// checks the history permissions that monthly report imports and investment
// income need.
func (c *Client) CheckTrading212(ctx context.Context, credentials Trading212Credentials) (Check, *Trading212Account) {
	base := strings.TrimRight(c.trading212Base(credentials.Environment), "/")
	authorize := func(request *http.Request) { request.SetBasicAuth(credentials.APIKey, credentials.APISecret) }
	summary, err := c.get(ctx, base+"/equity/account/summary", authorize)
	if err != nil {
		return failed("Could not reach Trading 212. Check the network connection and try again."), nil
	}
	switch summary.status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return failed("Trading 212 rejected the API key or secret. Check both values and the selected environment."), nil
	case http.StatusForbidden:
		return failed("The API key lacks the Account data permission, which is needed to identify the source account.",
			Item{Key: "account", Label: "Account data", State: StateMissing}), nil
	case http.StatusTooManyRequests:
		return failed("Trading 212 is rate limiting requests. Wait a few seconds and try again."), nil
	default:
		return failed("Trading 212 returned an unexpected response. Try again later."), nil
	}
	var account struct {
		ID       json.Number `json:"id"`
		Currency string      `json:"currency"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(summary.body)))
	decoder.UseNumber()
	if err := decoder.Decode(&account); err != nil || account.ID.String() == "" {
		return failed("Trading 212 returned an account summary this app cannot read."), nil
	}
	if _, err := ledger.MinorUnits(account.Currency); err != nil {
		return failed("Trading 212 reported an unsupported account currency."), nil
	}
	items := []Item{{Key: "account", Label: "Account data", State: StateOK}}
	items = append(items, c.trading212Permission(ctx, base+"/equity/history/transactions?limit=1", authorize,
		"history_transactions", "Transaction history"))
	items = append(items, c.trading212Permission(ctx, base+"/equity/history/exports", authorize,
		"history_reports", "History reports"))
	items = append(items, c.trading212Permission(ctx, base+"/equity/history/dividends?limit=1", authorize,
		"history_dividends", "Dividend history"))
	return summarize("Connected to Trading 212", items), &Trading212Account{ID: account.ID.String(), Currency: account.Currency}
}

func (c *Client) trading212Permission(ctx context.Context, url string, authorize func(*http.Request), key, label string) Item {
	item := Item{Key: key, Label: label}
	result, err := c.get(ctx, url, authorize)
	switch {
	case err != nil:
		item.State, item.Detail = StateUnverified, "Could not reach Trading 212"
	case result.status == http.StatusOK:
		item.State = StateOK
	case result.status == http.StatusForbidden:
		item.State, item.Detail = StateMissing, "Enable this permission for the API key"
	case result.status == http.StatusTooManyRequests:
		item.State, item.Detail = StateUnverified, "Rate limited; check again in a minute"
	default:
		item.State, item.Detail = StateUnverified, "Unexpected response"
	}
	return item
}
