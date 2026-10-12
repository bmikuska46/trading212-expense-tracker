// Package rates downloads the latest exchange rates with EUR and every
// account currency as the base, keeps them in the encrypted cache, and
// converts foreign amounts to EUR. It never requests historical dates or date
// ranges.
package rates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	// Embed the zone database so the schedule does not depend on host tzdata.
	_ "time/tzdata"

	"budget-tracking/backend/internal/ledger"
)

const (
	// Endpoint returns the latest published rate of every currency against
	// the base query parameter, blended across Frankfurter's default providers.
	Endpoint = "https://api.frankfurter.dev/v2/rates"
	// Base is the currency the ledger converts to.
	Base           = "EUR"
	ProviderPolicy = "frankfurter-v2-latest-default-providers"
	Timezone       = "Europe/Bratislava"
	// PublishHour is the local hour of the daily fetch. By then the day's
	// publications are out, so a fetch at or after it confirms the whole day.
	PublishHour = 18

	requestTimeout  = 20 * time.Second
	maxResponseSize = 1 << 20
)

// Location is the timezone of the daily schedule.
var Location = mustLocation(Timezone)

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

// Bases lists the currencies to fetch rates against: EUR, then the other
// account currencies in their given order.
func Bases(accountCurrencies []string) []string {
	bases := []string{Base}
	for _, code := range accountCurrencies {
		if code != Base {
			bases = append(bases, code)
		}
	}
	return bases
}

// Quote is one response record. Rate is quote-currency units per one unit
// of Base, as an exact plain decimal.
type Quote struct {
	Date  string
	Base  string
	Quote string
	Rate  string
}

// FetchError describes a failed download. Messages are written here and
// never copy the response body.
type FetchError struct {
	Code   string
	Detail string
}

func (e *FetchError) Error() string { return e.Detail }

// Client holds the endpoint so tests can point it at a local server.
type Client struct {
	HTTP *http.Client
	URL  string
}

func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		URL: Endpoint,
	}
}

// Latest downloads the current rates against a base currency. It is the
// only request this package makes; there are no date parameters.
func (c *Client) Latest(ctx context.Context, base string) ([]Quote, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"?base="+url.QueryEscape(base), nil)
	if err != nil {
		return nil, &FetchError{Code: "unreachable", Detail: "The rate service address is invalid"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "budget-tracking")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, &FetchError{Code: "unreachable", Detail: "The rate service could not be reached"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &FetchError{Code: "http_status", Detail: fmt.Sprintf("The rate service answered with HTTP %d", response.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return nil, &FetchError{Code: "unreachable", Detail: "The rate response was interrupted"}
	}
	if len(body) > maxResponseSize {
		return nil, &FetchError{Code: "invalid_response", Detail: "The rate response is too large"}
	}
	return Parse(body, base)
}

// Parse reads a response for a base. A record with an unexpected base, date,
// or rate rejects the whole response, so a format change never stores
// partial data.
func Parse(body []byte, base string) ([]Quote, error) {
	invalid := func(detail string) error { return &FetchError{Code: "invalid_response", Detail: detail} }
	var records []struct {
		Date  string      `json:"date"`
		Base  string      `json:"base"`
		Quote string      `json:"quote"`
		Rate  json.Number `json:"rate"`
	}
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, invalid("The rate response is not a list of rates")
	}
	quotes := make([]Quote, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if record.Base != base {
			return nil, invalid("The rate response is not based on " + base)
		}
		if _, err := time.Parse(time.DateOnly, record.Date); err != nil {
			return nil, invalid("The rate response has an invalid publication date")
		}
		rate, err := plainDecimal(record.Rate.String())
		if err != nil {
			return nil, invalid("The rate response has an invalid rate")
		}
		if !currencyCode(record.Quote) {
			// The cache only holds three-letter codes; other quotes cannot
			// match a transaction currency.
			continue
		}
		if seen[record.Quote] {
			return nil, invalid("The rate response repeats a currency")
		}
		seen[record.Quote] = true
		quotes = append(quotes, Quote{Date: record.Date, Base: record.Base, Quote: record.Quote, Rate: rate})
	}
	if len(quotes) == 0 {
		return nil, invalid("The rate response has no rates")
	}
	return quotes, nil
}

func currencyCode(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, letter := range value {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

// plainDecimal returns a positive JSON number as a plain decimal. Plain
// input is kept verbatim; an exponent moves the decimal point exactly.
func plainDecimal(number string) (string, error) {
	invalid := errors.New("invalid rate")
	result := number
	if index := strings.IndexAny(number, "eE"); index >= 0 {
		exponent, err := strconv.Atoi(number[index+1:])
		if err != nil || exponent < -64 || exponent > 64 {
			return "", invalid
		}
		whole, fraction, _ := strings.Cut(number[:index], ".")
		digits := whole + fraction
		point := len(whole) + exponent
		if point <= 0 {
			digits, point = strings.Repeat("0", 1-point)+digits, 1
		}
		if point > len(digits) {
			digits += strings.Repeat("0", point-len(digits))
		}
		whole, fraction = strings.TrimLeft(digits[:point], "0"), digits[point:]
		if whole == "" {
			whole = "0"
		}
		result = whole
		if fraction != "" {
			result += "." + fraction
		}
	}
	if _, err := ledger.ParseRate(result); err != nil {
		return "", invalid
	}
	return result, nil
}
