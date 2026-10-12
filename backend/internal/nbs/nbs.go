// Package nbs downloads the exchange rates Národná banka Slovenska publishes:
// the daily ECB reference rates of a month with the month's average, and the
// annual average of a finished year. Slovak tax returns convert foreign
// income with these rates.
package nbs

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/ledger"
)

const (
	BaseURL = "https://nbs.sk/export/sk"
	// RequestInterval spaces requests to the NBS site.
	RequestInterval = time.Second

	requestTimeout  = 20 * time.Second
	maxResponseSize = 1 << 20
	// AverageDecimals is the precision of the published monthly and annual
	// averages, which round the mean of the daily rates half up.
	AverageDecimals = 4
)

// Rate is foreign currency units per EUR as an exact plain decimal. Period is
// a day (YYYY-MM-DD), a month (YYYY-MM), or a year (YYYY).
type Rate struct {
	Currency string
	Period   string
	Rate     string
}

// FetchError describes a failed download. Messages are written here and
// never copy the response body.
type FetchError struct{ Detail string }

func (e *FetchError) Error() string { return e.Detail }

// Client holds the endpoint so tests can point it at a local server.
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		BaseURL: BaseURL,
	}
}

// Month downloads a month's daily rates and the average NBS publishes with
// them. The average of an unfinished month covers the days so far.
func (c *Client) Month(ctx context.Context, year int, month time.Month) ([]Rate, []Rate, error) {
	data, err := c.download(ctx, fmt.Sprintf("%s/exchange-rate-month/%04d-%02d/csv", strings.TrimRight(c.BaseURL, "/"), year, month))
	if err != nil {
		return nil, nil, err
	}
	return ParseMonth(data, year, month)
}

// Year downloads a year's file and returns its annual average, which NBS
// adds once the year has ended.
func (c *Client) Year(ctx context.Context, year int) ([]Rate, error) {
	data, err := c.download(ctx, fmt.Sprintf("%s/exchange-rate-cumulative/%04d/csv", strings.TrimRight(c.BaseURL, "/"), year))
	if err != nil {
		return nil, err
	}
	return ParseYear(data, year)
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &FetchError{Detail: "The NBS rate address is invalid."}
	}
	request.Header.Set("Accept", "text/csv")
	request.Header.Set("User-Agent", "budget-tracking")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, &FetchError{Detail: "Could not reach Národná banka Slovenska for exchange rates."}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &FetchError{Detail: fmt.Sprintf("Národná banka Slovenska answered the rate request with HTTP %d.", response.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return nil, &FetchError{Detail: "The NBS rate download was interrupted."}
	}
	if len(data) > maxResponseSize {
		return nil, &FetchError{Detail: "The NBS rate file is too large."}
	}
	return data, nil
}

var errFormat = &FetchError{Detail: "Národná banka Slovenska returned a rate file this app cannot read."}

// table reads a semicolon separated file and checks that every column after
// the first is a currency code. A file without content has no rows.
func table(data []byte, first string) ([]string, [][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, nil
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = ';'
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) == 0 {
		return nil, nil, errFormat
	}
	header := records[0]
	if len(header) < 2 || strings.TrimSpace(header[0]) != first {
		return nil, nil, errFormat
	}
	for _, code := range header[1:] {
		if !currencyCode(strings.TrimSpace(code)) {
			return nil, nil, errFormat
		}
	}
	return header, records[1:], nil
}

// values reads one row of rates. Empty cells are skipped; any other value
// that is not a positive decimal rejects the file.
func values(header, record []string, period string) ([]Rate, error) {
	rates := make([]Rate, 0, len(header)-1)
	for index := 1; index < len(header) && index < len(record); index++ {
		text := strings.TrimSpace(record[index])
		if text == "" {
			continue
		}
		rate, err := Decimal(text)
		if err != nil {
			return nil, errFormat
		}
		rates = append(rates, Rate{Currency: strings.TrimSpace(header[index]), Period: period, Rate: rate})
	}
	return rates, nil
}

// ParseMonth reads a monthly file into daily rates and the month's average.
// A file with a day outside the month, or a value that is not a rate, is
// rejected whole, so a format change never stores partial data.
func ParseMonth(data []byte, year int, month time.Month) ([]Rate, []Rate, error) {
	header, records, err := table(data, "Dátum")
	if err != nil {
		return nil, nil, err
	}
	period := fmt.Sprintf("%04d-%02d", year, month)
	var daily, average []Rate
	for _, record := range records {
		label := strings.TrimSpace(record[0])
		switch {
		case label == "":
		case strings.HasPrefix(label, "Priemer"):
			if average, err = values(header, record, period); err != nil {
				return nil, nil, err
			}
		case label[0] >= '0' && label[0] <= '9':
			day, err := ParseDate(label)
			if err != nil || day.Year() != year || day.Month() != month {
				return nil, nil, errFormat
			}
			rates, err := values(header, record, day.Format(time.DateOnly))
			if err != nil {
				return nil, nil, err
			}
			daily = append(daily, rates...)
		}
	}
	return daily, average, nil
}

// ParseYear returns the annual average of a cumulative year file, from its
// "Priemer/YYYY" row. Unfinished years have no such row and no rates.
func ParseYear(data []byte, year int) ([]Rate, error) {
	header, records, err := table(data, "Obdobie")
	if err != nil {
		return nil, err
	}
	label := "Priemer/" + strconv.Itoa(year)
	for _, record := range records {
		if strings.TrimSpace(record[0]) == label {
			return values(header, record, strconv.Itoa(year))
		}
	}
	return nil, nil
}

// ParseDate reads the "2. 1. 2026" dates of the monthly files.
func ParseDate(text string) (time.Time, error) {
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("expected a day, month, and year")
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return time.Time{}, errors.New("expected a day, month, and year")
		}
		numbers[index] = number
	}
	day := time.Date(numbers[2], time.Month(numbers[1]), numbers[0], 0, 0, 0, 0, time.UTC)
	if day.Day() != numbers[0] || int(day.Month()) != numbers[1] {
		return time.Time{}, errors.New("not a calendar date")
	}
	return day, nil
}

// Decimal turns a published value such as "19 593,06" into a plain decimal.
// Thousands may be separated by spaces, and the decimal mark is a comma.
func Decimal(text string) (string, error) {
	plain := strings.Map(func(r rune) rune {
		switch r {
		case ' ', ' ', ' ':
			return -1
		case ',':
			return '.'
		}
		return r
	}, text)
	if _, err := ledger.ParseRate(plain); err != nil {
		return "", err
	}
	return plain, nil
}

// Average is the mean of plain decimal rates rounded half up to
// AverageDecimals, the way NBS computes its monthly and annual averages.
func Average(rates []string) (string, error) {
	if len(rates) == 0 {
		return "", errors.New("no rates to average")
	}
	sum := new(big.Rat)
	for _, rate := range rates {
		if _, err := ledger.ParseRate(rate); err != nil {
			return "", err
		}
		value, ok := new(big.Rat).SetString(rate)
		if !ok {
			return "", errors.New("invalid rate")
		}
		sum.Add(sum, value)
	}
	sum.Quo(sum, new(big.Rat).SetInt64(int64(len(rates))))
	// Rates are positive, so FloatString's half away from zero is half up.
	return sum.FloatString(AverageDecimals), nil
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
