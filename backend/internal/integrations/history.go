package integrations

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"time"

	"budget-tracking/backend/internal/ledger"
)

const (
	// Dividend and transaction history allow 20 requests a minute. Pages are
	// spaced a little wider to stay clear of the limit.
	HistoryPageInterval = 3500 * time.Millisecond
	historyPageSize     = "50"
)

// Dividend is one paid dividend. The gross amount is in the currency the
// instrument pays in, before withholding tax; the received amount is what
// was credited in the account currency.
type Dividend struct {
	Reference string
	PaidAt    time.Time
	Type      string
	Ticker    string
	ISIN      string
	Name      string
	// Quantity and PerShare are the decimals Trading 212 reports.
	Quantity         string
	PerShare         string
	Currency         string
	Gross            int64
	ReceivedCurrency string
	Received         int64
}

// DividendPage is one page of dividends, newest first. Next is empty on the
// last page.
type DividendPage struct {
	Items []Dividend
	Next  string
}

// CashTransaction is one movement of account cash, such as a deposit or
// interest on free cash.
type CashTransaction struct {
	Reference string
	Type      string
	At        time.Time
	Currency  string
	Amount    int64
}

// TransactionPage is one page of cash transactions, newest first.
type TransactionPage struct {
	Items []CashTransaction
	Next  string
}

// historyPage requests the first page of an endpoint, or the page a previous
// response pointed to. The next path must stay on the same endpoint, so a
// response cannot send the credentials anywhere else.
func (c *Client) historyPage(ctx context.Context, credentials Trading212Credentials, endpoint, next, permission, action string) ([]byte, error) {
	base, authorize := c.trading212Auth(credentials)
	path := endpoint + "?limit=" + historyPageSize
	if next != "" {
		path = next
	}
	result, err := c.get(ctx, base+path, authorize)
	if err != nil {
		return nil, unreachable(action)
	}
	if result.status != http.StatusOK {
		return nil, fetchFailure(result.status, permission, action)
	}
	return result.body, nil
}

// nextPage turns a nextPagePath into a path below the API base, or reports
// that it leaves the endpoint.
func nextPage(value *string, endpoint string) (string, bool) {
	if value == nil || *value == "" {
		return "", true
	}
	path := strings.TrimPrefix(*value, "/api/v0")
	if !strings.HasPrefix(path, endpoint+"?") || strings.ContainsAny(path, "#\\") || strings.Contains(path, "..") {
		return "", false
	}
	return path, true
}

// exactNumber reads a JSON number of at most 32 characters as an exact
// fraction. Exponents are limited so a hostile value cannot be slow to
// expand.
func exactNumber(value json.Number) (*big.Rat, bool) {
	text := value.String()
	if text == "" || len(text) > 32 {
		return nil, false
	}
	if _, exponent, scientific := strings.Cut(strings.ToLower(text), "e"); scientific && len(strings.TrimLeft(exponent, "+-0")) > 2 {
		return nil, false
	}
	return new(big.Rat).SetString(text)
}

// grossMinor multiplies the shares by the gross amount per share and rounds
// the product once to minor units. Prices in pence (GBX) become pounds.
func grossMinor(quantity, perShare json.Number, currency string) (string, int64, bool) {
	shares, ok := exactNumber(quantity)
	if !ok {
		return "", 0, false
	}
	price, ok := exactNumber(perShare)
	if !ok {
		return "", 0, false
	}
	product := new(big.Rat).Mul(shares, price)
	if currency == "GBX" {
		currency = "GBP"
		product.Quo(product, big.NewRat(100, 1))
	}
	// The product of two such decimals, even in pence, has fewer places.
	amount, err := ledger.RoundMoney(product.FloatString(80), currency)
	if err != nil {
		return "", 0, false
	}
	return currency, amount.Minor(), true
}

// FetchDividends reads one page of paid dividends. When the gross amount
// cannot be worked out, the received amount stands in for it.
func (c *Client) FetchDividends(ctx context.Context, credentials Trading212Credentials, next string) (DividendPage, error) {
	const endpoint, action = "/equity/history/dividends", "read the dividend history"
	body, err := c.historyPage(ctx, credentials, endpoint, next, "Dividend history", action)
	if err != nil {
		return DividendPage{}, err
	}
	var page struct {
		Items []struct {
			Reference      string      `json:"reference"`
			PaidOn         string      `json:"paidOn"`
			Type           string      `json:"type"`
			Amount         json.Number `json:"amount"`
			Currency       string      `json:"currency"`
			PerShare       json.Number `json:"grossAmountPerShare"`
			Quantity       json.Number `json:"quantity"`
			Ticker         string      `json:"ticker"`
			TickerCurrency string      `json:"tickerCurrency"`
			Instrument     struct {
				Currency string `json:"currency"`
				ISIN     string `json:"isin"`
				Name     string `json:"name"`
				Ticker   string `json:"ticker"`
			} `json:"instrument"`
		} `json:"items"`
		NextPagePath *string `json:"nextPagePath"`
	}
	unreadable := &FetchError{Detail: "Trading 212 returned a dividend history this app cannot read."}
	if err := decodeNumbers(body, &page); err != nil {
		return DividendPage{}, unreadable
	}
	path, ok := nextPage(page.NextPagePath, endpoint)
	if !ok {
		return DividendPage{}, unreadable
	}
	dividends := make([]Dividend, 0, len(page.Items))
	for _, item := range page.Items {
		paidAt, err := time.Parse(time.RFC3339Nano, item.PaidOn)
		if err != nil || item.Reference == "" || item.Amount == "" {
			return DividendPage{}, unreadable
		}
		received, err := ledger.RoundMoney(item.Amount.String(), item.Currency)
		if err != nil {
			return DividendPage{}, unreadable
		}
		dividend := Dividend{Reference: item.Reference, PaidAt: paidAt.UTC(), Type: item.Type,
			Ticker: item.Instrument.Ticker, ISIN: item.Instrument.ISIN, Name: ledger.DisplayName(item.Instrument.Name),
			Quantity: item.Quantity.String(), PerShare: item.PerShare.String(),
			Currency: item.Currency, Gross: received.Minor(), ReceivedCurrency: item.Currency, Received: received.Minor()}
		if dividend.Ticker == "" {
			dividend.Ticker = item.Ticker
		}
		currency := item.Instrument.Currency
		if currency == "" {
			currency = item.TickerCurrency
		}
		if issued, gross, ok := grossMinor(item.Quantity, item.PerShare, currency); ok {
			// A reversed dividend is credited as a negative amount.
			if received.Minor() < 0 {
				gross = -gross
			}
			dividend.Currency, dividend.Gross = issued, gross
		}
		dividends = append(dividends, dividend)
	}
	return DividendPage{Items: dividends, Next: path}, nil
}

// FetchTransactions reads one page of cash transactions in the currency
// each was made in.
func (c *Client) FetchTransactions(ctx context.Context, credentials Trading212Credentials, next string) (TransactionPage, error) {
	const endpoint, action = "/equity/history/transactions", "read the transaction history"
	body, err := c.historyPage(ctx, credentials, endpoint, next, "Transaction history", action)
	if err != nil {
		return TransactionPage{}, err
	}
	var page struct {
		Items []struct {
			Reference string      `json:"reference"`
			Type      string      `json:"type"`
			DateTime  string      `json:"dateTime"`
			Amount    json.Number `json:"amount"`
			Currency  string      `json:"currency"`
		} `json:"items"`
		NextPagePath *string `json:"nextPagePath"`
	}
	unreadable := &FetchError{Detail: "Trading 212 returned a transaction history this app cannot read."}
	if err := decodeNumbers(body, &page); err != nil {
		return TransactionPage{}, unreadable
	}
	path, ok := nextPage(page.NextPagePath, endpoint)
	if !ok {
		return TransactionPage{}, unreadable
	}
	transactions := make([]CashTransaction, 0, len(page.Items))
	for _, item := range page.Items {
		at, err := time.Parse(time.RFC3339Nano, item.DateTime)
		if err != nil || item.Reference == "" || item.Amount == "" {
			return TransactionPage{}, unreadable
		}
		amount, err := ledger.RoundMoney(item.Amount.String(), item.Currency)
		if err != nil {
			return TransactionPage{}, unreadable
		}
		transactions = append(transactions, CashTransaction{Reference: item.Reference, Type: item.Type, At: at.UTC(),
			Currency: item.Currency, Amount: amount.Minor()})
	}
	return TransactionPage{Items: transactions, Next: path}, nil
}
