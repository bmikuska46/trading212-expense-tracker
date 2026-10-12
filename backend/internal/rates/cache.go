package rates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

// LocalDate is the calendar date of an instant in the schedule timezone.
func LocalDate(instant time.Time) string { return instant.In(Location).Format(time.DateOnly) }

func addDays(date string, days int) string {
	day, _ := time.Parse(time.DateOnly, date)
	return day.AddDate(0, 0, days).Format(time.DateOnly)
}

// confirmedThrough is the last local date a fetch at this instant proves
// its response covers. Before PublishHour the day's rate may still appear,
// so only the previous day is final.
func confirmedThrough(fetchedAt time.Time) string {
	through := LocalDate(fetchedAt)
	if fetchedAt.In(Location).Hour() < PublishHour {
		through = addDays(through, -1)
	}
	return through
}

// Save stores every quote with its publication date and the fetch time.
// Each quote is the latest publication for its currency, so it applies from
// its publication date through the date the fetch confirms. Dates before an
// earlier fetch's coverage stay unassigned; nothing is backfilled.
func Save(ctx context.Context, q *dbgen.Queries, quotes []Quote, fetchedAt time.Time) error {
	through := confirmedThrough(fetchedAt)
	fetched := fetchedAt.UTC().Format(time.RFC3339)
	for _, quote := range quotes {
		covered := max(quote.Date, through)
		err := q.SaveRate(ctx, dbgen.SaveRateParams{BaseCurrency: quote.Base, QuoteCurrency: quote.Quote,
			PublicationDate: quote.Date, Rate: quote.Rate, ProviderPolicy: ProviderPolicy, FetchedAt: fetched,
			ConfirmedThrough: sql.NullString{String: covered, Valid: true}})
		if err != nil {
			return err
		}
	}
	return nil
}

// Conversion records how a foreign amount became EUR. It is saved with the
// transaction and never recalculated.
type Conversion struct {
	Rate           string `json:"rate"`
	RateDate       string `json:"rateDate"`
	ProviderPolicy string `json:"providerPolicy"`
	RoundingRule   string `json:"roundingRule"`
	EURCents       int64  `json:"eurCents"`
}

// MissingRate explains why a date has no usable cached rate.
type MissingRate struct {
	Currency, Date string
	// Upcoming is set when the next daily fetch can still cover the date.
	Upcoming bool
}

func (e *MissingRate) Error() string {
	if e.Upcoming {
		return fmt.Sprintf("The %s rate for %s is not collected yet. Rates are saved daily at 18:00 Europe/Bratislava; validate again after the next update.", e.Currency, e.Date)
	}
	return fmt.Sprintf("No saved %s rate covers %s. Past rates are never downloaded, so exclude this row to import the rest.", e.Currency, e.Date)
}

// Convert divides a foreign amount by the rate for its local date: the
// latest publication on or before that date, provided a fetch confirmed no
// later publication applied to it. Future rates are never used.
func Convert(ctx context.Context, q *dbgen.Queries, amount ledger.Money, date string, now time.Time) (Conversion, error) {
	cached, err := q.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: amount.Currency(), LocalDate: date})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Conversion{}, err
	}
	covered := cached.PublicationDate
	if cached.ConfirmedThrough.Valid {
		covered = cached.ConfirmedThrough.String
	}
	if errors.Is(err, sql.ErrNoRows) || covered < date {
		// Any successful fetch from today on covers yesterday and later.
		return Conversion{}, &MissingRate{Currency: amount.Currency(), Date: date, Upcoming: date >= addDays(LocalDate(now), -1)}
	}
	rate, err := ledger.ParseRate(cached.Rate)
	if err != nil {
		return Conversion{}, err
	}
	cents, err := ledger.EURCents(amount, rate)
	if err != nil {
		return Conversion{}, err
	}
	return Conversion{Rate: cached.Rate, RateDate: cached.PublicationDate, ProviderPolicy: cached.ProviderPolicy,
		RoundingRule: ledger.RoundingRule, EURCents: cents}, nil
}

// Cleanup removes cached rates older than the previous local month unless
// an import awaiting confirmation covers their month. Each retained range
// also keeps its predecessor, the rate that applies on its first day. Saved
// transaction conversions are separate rows and never change.
func Cleanup(ctx context.Context, store *database.Store, now time.Time) (int, error) {
	local := now.In(Location)
	windowStart := time.Date(local.Year(), local.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := store.Queries.WithTx(tx)
	pending, err := q.PendingImportPeriods(ctx)
	if err != nil {
		return 0, err
	}
	type span struct{ start, end string }
	retained := []span{{start: windowStart}}
	for _, batch := range pending {
		if !batch.PeriodStart.Valid || !batch.PeriodEnd.Valid {
			// A staged batch without a validated period may need any rate.
			return 0, nil
		}
		start, end := monthSpan(batch.PeriodStart.String, batch.PeriodEnd.String)
		if start < windowStart {
			retained = append(retained, span{start, end})
		}
	}
	rows, err := q.ListRatesBefore(ctx, windowStart)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for first := 0; first < len(rows); {
		last := first
		for last < len(rows) && rows[last].BaseCurrency == rows[first].BaseCurrency &&
			rows[last].QuoteCurrency == rows[first].QuoteCurrency {
			last++
		}
		currency := rows[first:last]
		keep := make([]bool, len(currency))
		for _, kept := range retained {
			predecessor := -1
			for index, row := range currency {
				if row.PublicationDate <= kept.start {
					predecessor = index
				}
				if row.PublicationDate >= kept.start && (kept.end == "" || row.PublicationDate < kept.end) {
					keep[index] = true
				}
			}
			if predecessor >= 0 {
				keep[predecessor] = true
			}
		}
		for index, row := range currency {
			if keep[index] {
				continue
			}
			if err := q.DeleteRate(ctx, dbgen.DeleteRateParams{BaseCurrency: row.BaseCurrency, QuoteCurrency: row.QuoteCurrency, PublicationDate: row.PublicationDate}); err != nil {
				return 0, err
			}
			deleted++
		}
		first = last
	}
	return deleted, tx.Commit()
}

// monthSpan widens a half-open date period to whole months.
func monthSpan(start, end string) (string, string) {
	first, _ := time.Parse(time.DateOnly, start)
	last, _ := time.Parse(time.DateOnly, end)
	last = last.AddDate(0, 0, -1)
	from := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(last.Year(), last.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return from.Format(time.DateOnly), to.Format(time.DateOnly)
}
