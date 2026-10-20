package rates

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

// SaveHistorical records coverage only through the requested date. A rate
// returned for a weekend cannot establish coverage through today's fetch.
func SaveHistorical(ctx context.Context, q *dbgen.Queries, quote Quote, date string, fetchedAt time.Time) error {
	if _, err := time.Parse(time.DateOnly, date); err != nil || quote.Date > date || quote.Base != Base || date > LocalDate(fetchedAt) {
		return &FetchError{Code: "invalid_response", Detail: "The historical rate does not cover the requested date"}
	}
	if _, err := time.Parse(time.DateOnly, quote.Date); err != nil || !currencyCode(quote.Quote) || quote.Quote == Base {
		return &FetchError{Code: "invalid_response", Detail: "The historical rate has an invalid currency or publication date"}
	}
	if _, err := ledger.ParseRate(quote.Rate); err != nil {
		return &FetchError{Code: "invalid_response", Detail: "The historical rate has an invalid rate"}
	}
	covered := date
	if date > confirmedThrough(fetchedAt) && quote.Date < date {
		covered = confirmedThrough(fetchedAt)
	}
	return q.SaveRate(ctx, dbgen.SaveRateParams{
		BaseCurrency: Base, QuoteCurrency: quote.Quote, PublicationDate: quote.Date,
		Rate: quote.Rate, ProviderPolicy: HistoricalProviderPolicy,
		FetchedAt:        fetchedAt.UTC().Format(time.RFC3339),
		ConfirmedThrough: sql.NullString{String: covered, Valid: true},
	})
}

// EnsureHistorical fills a missing date from Frankfurter before validation
// opens a database transaction. Already covered dates need no request.
func EnsureHistorical(ctx context.Context, q *dbgen.Queries, client *Client, date, currency string, now time.Time) error {
	if _, err := time.Parse(time.DateOnly, date); err != nil || date > LocalDate(now) || !currencyCode(currency) || currency == Base {
		return &FetchError{Code: "invalid_request", Detail: "Choose a valid past or current rate date and foreign currency"}
	}
	cached, err := q.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: currency, LocalDate: date})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && cached.ConfirmedThrough.Valid && cached.ConfirmedThrough.String >= date {
		return nil
	}
	if err == nil && cached.PublicationDate == date {
		return nil
	}
	if client == nil {
		return &FetchError{Code: "unreachable", Detail: "The historical rate service is unavailable"}
	}
	quote, err := client.At(ctx, date, currency)
	if err != nil {
		return err
	}
	return SaveHistorical(ctx, q, quote, date, now)
}
