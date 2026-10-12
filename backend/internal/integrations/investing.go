package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/ledger"
)

// Trading 212 allows one pie detail request per five seconds. Requests are
// spaced a little wider to stay clear of the limit.
const PieNameInterval = 6 * time.Second

// AccountSummary is the account's value in minor units of its currency.
type AccountSummary struct {
	AccountID        string
	Currency         string
	TotalValue       int64
	InvestmentsValue int64
	CostBasis        int64
	Unrealized       int64
	Realized         int64
	CashAvailable    int64
	CashInPies       int64
	CashReserved     int64
}

// PieSummary is one pie's result in minor units of the account currency.
type PieSummary struct {
	ID                  int64
	Invested            int64
	Value               int64
	Result              int64
	Cash                int64
	DividendsGained     int64
	DividendsReinvested int64
	DividendsInCash     int64
}

// FetchError describes a failed portfolio request. Messages are written
// here and never copy response bodies.
type FetchError struct {
	Detail string
	// Permanent failures need the user to change the key before a retry helps.
	Permanent bool
	// RateLimited failures succeed after waiting a minute.
	RateLimited bool
}

func (e *FetchError) Error() string { return e.Detail }

func fetchFailure(status int, permission, action string) *FetchError {
	switch status {
	case http.StatusUnauthorized:
		return &FetchError{Permanent: true, Detail: "Trading 212 rejected the API key or secret. Update them in Integrations."}
	case http.StatusForbidden:
		return &FetchError{Permanent: true,
			Detail: fmt.Sprintf("The API key lacks the %s permission. Enable it in Trading 212 and save a new key in Integrations.", permission)}
	case http.StatusTooManyRequests:
		return &FetchError{RateLimited: true, Detail: fmt.Sprintf("Trading 212 is rate limiting requests to %s.", action)}
	}
	return &FetchError{Detail: fmt.Sprintf("Trading 212 returned an unexpected response while trying to %s.", action)}
}

func unreachable(action string) *FetchError {
	return &FetchError{Detail: fmt.Sprintf("Could not reach Trading 212 to %s.", action)}
}

func decodeNumbers(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	return decoder.Decode(target)
}

// minorAmounts rounds each reported decimal to minor units. A missing value
// counts as zero.
func minorAmounts(currency string, values ...json.Number) ([]int64, error) {
	amounts := make([]int64, len(values))
	for i, value := range values {
		if value == "" {
			continue
		}
		amount, err := ledger.RoundMoney(value.String(), currency)
		if err != nil {
			return nil, err
		}
		amounts[i] = amount.Minor()
	}
	return amounts, nil
}

// FetchAccountSummary reads the account's value, cost basis, and cash.
func (c *Client) FetchAccountSummary(ctx context.Context, credentials Trading212Credentials) (AccountSummary, error) {
	const action = "read the account summary"
	base, authorize := c.trading212Auth(credentials)
	result, err := c.get(ctx, base+"/equity/account/summary", authorize)
	if err != nil {
		return AccountSummary{}, unreachable(action)
	}
	if result.status != http.StatusOK {
		return AccountSummary{}, fetchFailure(result.status, "Account data", action)
	}
	var summary struct {
		ID       json.Number `json:"id"`
		Currency string      `json:"currency"`
		Total    json.Number `json:"totalValue"`
		Cash     struct {
			Available json.Number `json:"availableToTrade"`
			InPies    json.Number `json:"inPies"`
			Reserved  json.Number `json:"reservedForOrders"`
		} `json:"cash"`
		Investments struct {
			Value      json.Number `json:"currentValue"`
			Cost       json.Number `json:"totalCost"`
			Unrealized json.Number `json:"unrealizedProfitLoss"`
			Realized   json.Number `json:"realizedProfitLoss"`
		} `json:"investments"`
	}
	unreadable := &FetchError{Detail: "Trading 212 returned an account summary this app cannot read."}
	if err := decodeNumbers(result.body, &summary); err != nil || summary.ID.String() == "" || summary.Total == "" {
		return AccountSummary{}, unreadable
	}
	amounts, err := minorAmounts(summary.Currency, summary.Total, summary.Investments.Value, summary.Investments.Cost,
		summary.Investments.Unrealized, summary.Investments.Realized, summary.Cash.Available, summary.Cash.InPies, summary.Cash.Reserved)
	if err != nil {
		return AccountSummary{}, unreadable
	}
	return AccountSummary{AccountID: summary.ID.String(), Currency: summary.Currency,
		TotalValue: amounts[0], InvestmentsValue: amounts[1], CostBasis: amounts[2], Unrealized: amounts[3],
		Realized: amounts[4], CashAvailable: amounts[5], CashInPies: amounts[6], CashReserved: amounts[7]}, nil
}

// FetchPies lists every pie with its result. Values are in the account
// currency, which the pie list does not repeat.
func (c *Client) FetchPies(ctx context.Context, credentials Trading212Credentials, currency string) ([]PieSummary, error) {
	const action = "list pies"
	base, authorize := c.trading212Auth(credentials)
	result, err := c.get(ctx, base+"/equity/pies", authorize)
	if err != nil {
		return nil, unreachable(action)
	}
	if result.status != http.StatusOK {
		return nil, fetchFailure(result.status, "Pies read", action)
	}
	var listed []struct {
		ID       json.Number `json:"id"`
		Cash     json.Number `json:"cash"`
		Dividend struct {
			Gained     json.Number `json:"gained"`
			Reinvested json.Number `json:"reinvested"`
			InCash     json.Number `json:"inCash"`
		} `json:"dividendDetails"`
		Result struct {
			Invested json.Number `json:"priceAvgInvestedValue"`
			Value    json.Number `json:"priceAvgValue"`
			Result   json.Number `json:"priceAvgResult"`
		} `json:"result"`
	}
	unreadable := &FetchError{Detail: "Trading 212 returned a pie list this app cannot read."}
	if err := decodeNumbers(result.body, &listed); err != nil {
		return nil, unreadable
	}
	pies := make([]PieSummary, 0, len(listed))
	for _, item := range listed {
		id, err := item.ID.Int64()
		if err != nil || id <= 0 {
			return nil, unreadable
		}
		amounts, err := minorAmounts(currency, item.Result.Invested, item.Result.Value, item.Result.Result, item.Cash,
			item.Dividend.Gained, item.Dividend.Reinvested, item.Dividend.InCash)
		if err != nil {
			return nil, unreadable
		}
		pies = append(pies, PieSummary{ID: id, Invested: amounts[0], Value: amounts[1], Result: amounts[2], Cash: amounts[3],
			DividendsGained: amounts[4], DividendsReinvested: amounts[5], DividendsInCash: amounts[6]})
	}
	return pies, nil
}

// FetchPieName returns the name a pie was given in the app. Only the
// per-pie endpoint carries it, and it is rate limited to PieNameInterval.
func (c *Client) FetchPieName(ctx context.Context, credentials Trading212Credentials, id int64) (string, error) {
	const action = "read a pie name"
	base, authorize := c.trading212Auth(credentials)
	result, err := c.get(ctx, base+"/equity/pies/"+strconv.FormatInt(id, 10), authorize)
	if err != nil {
		return "", unreachable(action)
	}
	if result.status != http.StatusOK {
		return "", fetchFailure(result.status, "Pies read", action)
	}
	var detail struct {
		Settings struct {
			Name string `json:"name"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(result.body, &detail); err != nil {
		return "", &FetchError{Detail: "Trading 212 returned pie details this app cannot read."}
	}
	return ledger.DisplayName(detail.Settings.Name), nil
}
