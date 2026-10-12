package rates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

func testStore(t *testing.T) *database.Store {
	t.Helper()
	directory := t.TempDir()
	keyFile := filepath.Join(directory, "key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(context.Background(), filepath.Join(directory, "budget.db"), keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	return store
}

func local(date string, hour int) time.Time {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		panic(err)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, Location)
}

func usd(t *testing.T, value string) ledger.Money {
	t.Helper()
	amount, err := ledger.ParseMoney(value, "USD")
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func save(t *testing.T, store *database.Store, fetchedAt time.Time, quotes ...Quote) {
	t.Helper()
	if err := Save(context.Background(), store.Queries, quotes, fetchedAt); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	body := `[
		{"date":"2026-09-11","base":"EUR","quote":"EUR","rate":1},
		{"date":"2026-09-11","base":"EUR","quote":"USD","rate":1.0842},
		{"date":"2026-09-11","base":"EUR","quote":"JPY","rate":1.625e2},
		{"date":"2026-09-10","base":"EUR","quote":"XYZ","rate":15E-4},
		{"date":"2026-09-11","base":"EUR","quote":"usdt","rate":1.08}
	]`
	quotes, err := Parse([]byte(body), "EUR")
	if err != nil {
		t.Fatal(err)
	}
	want := []Quote{
		{Date: "2026-09-11", Base: "EUR", Quote: "EUR", Rate: "1"},
		{Date: "2026-09-11", Base: "EUR", Quote: "USD", Rate: "1.0842"},
		{Date: "2026-09-11", Base: "EUR", Quote: "JPY", Rate: "162.5"},
		{Date: "2026-09-10", Base: "EUR", Quote: "XYZ", Rate: "0.0015"},
	}
	if len(quotes) != len(want) {
		t.Fatalf("quotes = %+v", quotes)
	}
	for index := range want {
		if quotes[index] != want[index] {
			t.Fatalf("quote %d = %+v, want %+v", index, quotes[index], want[index])
		}
	}

	for name, body := range map[string]string{
		"not a list":     `{"rates":{}}`,
		"empty":          `[]`,
		"other base":     `[{"date":"2026-09-11","base":"USD","quote":"EUR","rate":0.92}]`,
		"bad date":       `[{"date":"11.09.2026","base":"EUR","quote":"USD","rate":1.08}]`,
		"negative rate":  `[{"date":"2026-09-11","base":"EUR","quote":"USD","rate":-1.08}]`,
		"repeated quote": `[{"date":"2026-09-11","base":"EUR","quote":"USD","rate":1.08},{"date":"2026-09-10","base":"EUR","quote":"USD","rate":1.07}]`,
	} {
		var failed *FetchError
		if _, err := Parse([]byte(body), "EUR"); !errors.As(err, &failed) || failed.Code != "invalid_response" {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestPlainDecimal(t *testing.T) {
	for input, want := range map[string]string{
		"1.0842":    "1.0842",
		"25":        "25",
		"1.5e3":     "1500",
		"1.625E+2":  "162.5",
		"12e-1":     "1.2",
		"3.5e-4":    "0.00035",
		"0.0012e2":  "0.12",
		"7.25e0":    "7.25",
		"4096.5e-3": "4.0965",
	} {
		got, err := plainDecimal(input)
		if err != nil || got != want {
			t.Errorf("plainDecimal(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "1e999", "1ex", ""} {
		if got, err := plainDecimal(input); err == nil {
			t.Errorf("plainDecimal(%q) = %q, want an error", input, got)
		}
	}
}

func TestSaveKeepsPublicationDates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// A Saturday morning fetch returns Friday's publication.
	fetchedAt := local("2026-09-12", 10)
	save(t, store, fetchedAt, Quote{Date: "2026-09-11", Base: "EUR", Quote: "USD", Rate: "1.0842"})
	row, err := store.Queries.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: "USD", LocalDate: "2026-09-30"})
	if err != nil {
		t.Fatal(err)
	}
	if row.PublicationDate != "2026-09-11" || row.FetchedAt != fetchedAt.UTC().Format(time.RFC3339) ||
		row.BaseCurrency != "EUR" || row.Rate != "1.0842" || row.ProviderPolicy != ProviderPolicy {
		t.Fatalf("row = %+v", row)
	}
	// Before 18:00 only the previous day is final.
	if row.ConfirmedThrough.String != "2026-09-11" {
		t.Fatalf("confirmed through %q", row.ConfirmedThrough.String)
	}
	// A Sunday evening fetch still returns Friday's publication and extends it.
	save(t, store, local("2026-09-13", 18), Quote{Date: "2026-09-11", Base: "EUR", Quote: "USD", Rate: "1.0842"})
	// An earlier fetch replayed later never shrinks the coverage.
	save(t, store, local("2026-09-12", 9), Quote{Date: "2026-09-11", Base: "EUR", Quote: "USD", Rate: "1.0842"})
	row, err = store.Queries.RateAtOrBefore(ctx, dbgen.RateAtOrBeforeParams{QuoteCurrency: "USD", LocalDate: "2026-09-30"})
	if err != nil {
		t.Fatal(err)
	}
	if row.ConfirmedThrough.String != "2026-09-13" {
		t.Fatalf("confirmed through %q", row.ConfirmedThrough.String)
	}
}

func TestConvertDividesAndRoundsOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fetchedAt := local("2026-09-10", 19)
	save(t, store, fetchedAt,
		Quote{Date: "2026-09-10", Base: "EUR", Quote: "USD", Rate: "1.0842"},
		Quote{Date: "2026-09-10", Base: "EUR", Quote: "JPY", Rate: "162.5"},
		Quote{Date: "2026-09-10", Base: "EUR", Quote: "CHF", Rate: "4"})
	for _, test := range []struct {
		amount   string
		currency string
		cents    int64
	}{
		{"100.00", "USD", 9223},
		{"-100.00", "USD", -9223},
		{"1000", "JPY", 615},
		{"-1000", "JPY", -615},
		// 0.02 / 4 is exactly half a cent; ties round away from zero.
		{"0.02", "CHF", 1},
		{"-0.02", "CHF", -1},
		{"0.06", "CHF", 2},
		{"-0.06", "CHF", -2},
		{"0.01", "CHF", 0},
		{"-0.01", "CHF", 0},
		{"0.03", "CHF", 1},
		{"-0.03", "CHF", -1},
	} {
		amount, err := ledger.ParseMoney(test.amount, test.currency)
		if err != nil {
			t.Fatal(err)
		}
		conversion, err := Convert(ctx, store.Queries, amount, "2026-09-10", fetchedAt)
		if err != nil {
			t.Fatalf("%s %s: %v", test.amount, test.currency, err)
		}
		if conversion.EURCents != test.cents || conversion.RateDate != "2026-09-10" ||
			conversion.ProviderPolicy != ProviderPolicy || conversion.RoundingRule != ledger.RoundingRule {
			t.Errorf("%s %s = %+v, want %d cents", test.amount, test.currency, conversion, test.cents)
		}
	}
}

func TestConvertFallsBackWithoutFutureRates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	quote := func(date, rate string) Quote { return Quote{Date: date, Base: "EUR", Quote: "USD", Rate: rate} }
	// Friday's rate, then fetches through the weekend that return it again.
	save(t, store, local("2026-09-11", 18), quote("2026-09-11", "2"))
	save(t, store, local("2026-09-12", 18), quote("2026-09-11", "2"))
	save(t, store, local("2026-09-13", 18), quote("2026-09-11", "2"))
	// Monday's fetch before 18:00 does not cover Monday yet.
	save(t, store, local("2026-09-14", 10), quote("2026-09-11", "2"))
	// Monday evening's fetch was missed; Tuesday publishes a new rate.
	save(t, store, local("2026-09-15", 19), quote("2026-09-15", "4"))
	now := local("2026-09-15", 19)

	for _, date := range []string{"2026-09-11", "2026-09-12", "2026-09-13"} {
		conversion, err := Convert(ctx, store.Queries, usd(t, "10.00"), date, now)
		if err != nil {
			t.Fatalf("%s: %v", date, err)
		}
		if conversion.RateDate != "2026-09-11" || conversion.Rate != "2" || conversion.EURCents != 500 {
			t.Fatalf("%s: %+v", date, conversion)
		}
	}
	// Monday was never confirmed. Tuesday's later rate must not fill it.
	_, err := Convert(ctx, store.Queries, usd(t, "10.00"), "2026-09-14", now)
	var missing *MissingRate
	if !errors.As(err, &missing) || missing.Currency != "USD" || missing.Date != "2026-09-14" {
		t.Fatalf("Monday error = %v", err)
	}
	if !missing.Upcoming {
		t.Fatal("yesterday's rate can still arrive with today's fetch")
	}
	conversion, err := Convert(ctx, store.Queries, usd(t, "10.00"), "2026-09-15", now)
	if err != nil || conversion.RateDate != "2026-09-15" || conversion.EURCents != 250 {
		t.Fatalf("Tuesday = %+v, %v", conversion, err)
	}
	// Wednesday has no confirmed rate yet.
	if _, err := Convert(ctx, store.Queries, usd(t, "10.00"), "2026-09-16", now); !errors.As(err, &missing) {
		t.Fatalf("Wednesday error = %v", err)
	}
	// Before the first cached publication nothing applies, and a week later
	// the gap is reported as history that is never downloaded.
	_, err = Convert(ctx, store.Queries, usd(t, "10.00"), "2026-09-01", local("2026-09-22", 12))
	if !errors.As(err, &missing) || missing.Upcoming || !strings.Contains(missing.Error(), "never downloaded") {
		t.Fatalf("history error = %v", err)
	}
	_, err = Convert(ctx, store.Queries, usd(t, "10.00"), "2026-09-14", local("2026-09-22", 12))
	if !errors.As(err, &missing) || missing.Upcoming {
		t.Fatalf("missed day error = %v", err)
	}
}

func publications(t *testing.T, store *database.Store) []string {
	t.Helper()
	rows, err := store.Queries.ListRatesBefore(context.Background(), "9999-12-31")
	if err != nil {
		t.Fatal(err)
	}
	dates := []string{}
	for _, row := range rows {
		dates = append(dates, row.QuoteCurrency+" "+row.PublicationDate)
	}
	return dates
}

func TestCleanupKeepsPendingMonthsAndPredecessors(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, date := range []string{"2026-05-15", "2026-05-29", "2026-06-03", "2026-06-15", "2026-06-30",
		"2026-07-01", "2026-07-15", "2026-07-31", "2026-08-03", "2026-09-10"} {
		save(t, store, local(date, 19), Quote{Date: date, Base: "EUR", Quote: "USD", Rate: "1.1"})
	}
	save(t, store, local("2026-07-20", 19), Quote{Date: "2026-07-20", Base: "EUR", Quote: "GBP", Rate: "0.85"})
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO accounts (id, name, currency) VALUES (1, 'Dollars', 'USD')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO import_batches (id, source_account_id, filename, staged_at, status)
		VALUES (1, 1, 'staged.csv', '2026-09-01T10:00:00Z', 'staging')`); err != nil {
		t.Fatal(err)
	}
	now := local("2026-09-30", 19)

	// A staged batch without a validated period may need any month.
	if deleted, err := Cleanup(ctx, store, now); err != nil || deleted != 0 {
		t.Fatalf("deleted %d, %v", deleted, err)
	}
	// A committed June import awaiting review keeps June, plus May 29, the
	// rate that applies on June 1.
	if _, err := store.DB.ExecContext(ctx, `UPDATE import_batches SET period_start = '2026-06-10', period_end = '2026-06-21',
		status = 'committed', review_status = 'needs_review' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if deleted, err := Cleanup(ctx, store, now); err != nil || deleted != 3 {
		t.Fatalf("deleted %d, %v", deleted, err)
	}
	want := "GBP 2026-07-20,USD 2026-05-29,USD 2026-06-03,USD 2026-06-15,USD 2026-06-30,USD 2026-07-31,USD 2026-08-03,USD 2026-09-10"
	if got := strings.Join(publications(t, store), ","); got != want {
		t.Fatalf("kept %s", got)
	}
	// Once confirmed, only the current and previous month stay, with the
	// predecessor of August 1 for each currency.
	if _, err := store.DB.ExecContext(ctx, `UPDATE import_batches SET review_status = 'confirmed' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if deleted, err := Cleanup(ctx, store, now); err != nil || deleted != 4 {
		t.Fatalf("deleted %d, %v", deleted, err)
	}
	want = "GBP 2026-07-20,USD 2026-07-31,USD 2026-08-03,USD 2026-09-10"
	if got := strings.Join(publications(t, store), ","); got != want {
		t.Fatalf("kept %s", got)
	}
}
