package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/rates"
)

// countingTransport replaces the default transport so any outbound request,
// including one a rate client would make, is counted and refused.
type countingTransport struct{ requests atomic.Int64 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.requests.Add(1)
	return nil, errors.New("outbound requests are not allowed in this test")
}

func refuseOutbound(t *testing.T) *countingTransport {
	t.Helper()
	transport := &countingTransport{}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	return transport
}

func localTime(date string, hour, minute int) time.Time {
	day, _ := time.Parse(time.DateOnly, date)
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, rates.Location)
}

func (app *testApp) saveRate(fetchedAt time.Time, date, currency, rate string) {
	app.t.Helper()
	quote := rates.Quote{Date: date, Base: "EUR", Quote: currency, Rate: rate}
	if err := rates.Save(context.Background(), app.store.Queries, []rates.Quote{quote}, fetchedAt); err != nil {
		app.t.Fatal(err)
	}
}

func (app *testApp) foreignAccount(cookie *http.Cookie, name, currency string) int64 {
	app.t.Helper()
	return decode[accountView](app.t, app.expect(http.StatusCreated, "POST", "/api/accounts",
		map[string]string{"name": name, "currency": currency}, cookie)).ID
}

func (app *testApp) stagedRows(cookie *http.Cookie, batch int64, status string) []stagingRowView {
	app.t.Helper()
	return decode[struct {
		Rows []stagingRowView `json:"rows"`
	}](app.t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/imports/%d/rows?status=%s", batch, status), nil, cookie)).Rows
}

const header = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n"

// usdFile has a weekday row, a Friday-night UTC row that is Saturday in
// Bratislava, a July row older than every cached rate, and a row from the
// current day before its rate is published.
const usdFile = header +
	"Card debit,2026-08-03 09:14:22,,usd-1,-12.34,USD,Example Diner,RESTAURANTS\n" +
	"Card debit,2026-08-07 23:30:00,,usd-2,-100.00,USD,Sample Store,RETAIL_STORES\n" +
	"Card debit,2026-07-20 10:00:00,,usd-3,-5.00,USD,Sample Kiosk,MISCELLANEOUS\n" +
	"Card debit,2026-08-10 08:00:00,,usd-4,-10.00,USD,Example Cafe,RESTAURANTS\n"

func TestForeignImportConvertsAtSavedRates(t *testing.T) {
	now := localTime("2026-08-10", 12, 0)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	outbound := refuseOutbound(t)
	cookie := app.setup()
	dollars := app.foreignAccount(cookie, "Dollars", "USD")
	euros := app.account(cookie, "Euros")
	ctx := context.Background()

	app.saveRate(localTime("2026-08-03", 18, 5), "2026-08-03", "USD", "1.1")
	app.saveRate(localTime("2026-08-07", 18, 5), "2026-08-07", "USD", "1.0842")
	// The Sunday fetch returns Friday's publication again.
	app.saveRate(localTime("2026-08-09", 18, 5), "2026-08-07", "USD", "1.0842")

	staged := app.imported(cookie, dollars, "usd.csv", []byte(usdFile))
	if staged.State != stateNeedsAttention || staged.ErrorCount != 2 || staged.RowCounts[imports.RowValid] != 2 {
		t.Fatalf("staged: %+v", staged)
	}
	invalid := app.stagedRows(cookie, staged.ID, "invalid")
	if len(invalid) != 2 || invalid[0].Row != 3 || invalid[1].Row != 4 ||
		!strings.Contains(invalid[0].Errors[0], "No saved USD rate covers 2026-07-20") ||
		!strings.Contains(invalid[1].Errors[0], "not collected yet") {
		t.Fatalf("missing rates: %+v", invalid)
	}
	valid := app.stagedRows(cookie, staged.ID, "valid")
	if len(valid) != 2 || valid[0].Record.Conversion == nil || valid[0].Record.Conversion.EURCents != -1122 ||
		valid[1].Record.Conversion.RateDate != "2026-08-07" || valid[1].Record.Conversion.EURCents != -9223 {
		t.Fatalf("staged conversions: %+v", valid)
	}

	// The July row is excluded; the day's row waits for the 18:00 fetch.
	app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/imports/%d/rows/3", staged.ID), map[string]any{"excluded": true}, cookie)
	waiting := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/validate", staged.ID), nil, cookie))
	if waiting.State != stateNeedsAttention || waiting.ErrorCount != 1 {
		t.Fatalf("before the evening fetch: %+v", waiting)
	}
	now = localTime("2026-08-10", 18, 30)
	app.saveRate(now, "2026-08-10", "USD", "1.25")
	ready := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/validate", staged.ID), nil, cookie))
	if ready.State != stateReady || ready.RowCounts[imports.RowValid] != 3 {
		t.Fatalf("after the evening fetch: %+v", ready)
	}
	app.commitUnreviewed(cookie, staged.ID)
	if count := outbound.requests.Load(); count != 0 {
		t.Fatalf("validation made %d outbound requests", count)
	}

	rows := app.batchTransactions(cookie, staged.ID)
	diner, store, cafe := byNotesOrPayee(rows, "Example Diner"), byNotesOrPayee(rows, "Sample Store"), byNotesOrPayee(rows, "Example Cafe")
	if len(rows) != 3 || diner.Currency != "USD" || diner.Amount != "-12.34" || diner.EURCents != -1122 ||
		store.Date != "2026-08-08" || store.EURCents != -9223 || cafe.EURCents != -800 {
		t.Fatalf("converted rows: %+v", rows)
	}
	conversion := app.detail(cookie, store.ID).Conversion
	if conversion == nil || conversion.OriginalAmount != "-100.00" || conversion.OriginalCurrency != "USD" || conversion.Rate != "1.0842" ||
		conversion.RateDate != "2026-08-07" || conversion.ProviderPolicy != rates.ProviderPolicy ||
		conversion.RoundingRule != ledger.RoundingRule || conversion.EURCents != -9223 {
		t.Fatalf("saved conversion: %+v", conversion)
	}

	// The converted amount is fixed; other fields stay editable.
	target := fmt.Sprintf("/api/transactions/%d", diner.ID)
	for name, changes := range map[string]map[string]any{
		"amount":  {"amount": "20.00"},
		"sign":    {"kind": "income"},
		"account": {"accountId": euros},
	} {
		if response := app.request("PATCH", target, editInput(diner, changes), cookie); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s change: %d %s", name, response.Code, response.Body)
		}
	}
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", target, editInput(diner, map[string]any{"notes": "client lunch"}), cookie)).Transaction
	if edited.EURCents != -1122 || edited.Amount != "-12.34" || edited.Notes != "client lunch" {
		t.Fatalf("edited: %+v", edited)
	}
	// Manual entries stay in EUR.
	if response := app.request("POST", "/api/transactions", map[string]any{"accountId": dollars, "date": "2026-08-10",
		"kind": "expense", "amount": "5"}, cookie); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("manual USD entry: %d", response.Code)
	}

	// Two months later the batch is confirmed. Its rates stay while another
	// August import awaits its commit, and go once that import is removed.
	totals := app.stats(cookie, "")
	now = localTime("2026-10-05", 19, 0)
	app.saveRate(now, "2026-10-05", "USD", "1.2")
	cookie = sessionCookie(t, app.expect(http.StatusOK, "POST", "/api/session",
		map[string]string{"email": ownerEmail, "password": ownerPassword}, nil))
	if deleted, err := rates.Cleanup(ctx, app.store, now); err != nil || deleted != 0 {
		t.Fatalf("cleanup before confirmation: %d, %v", deleted, err)
	}
	pending := app.imported(cookie, dollars, "late.csv", []byte(header+"Card debit,2026-08-03 12:00:00,,usd-5,-3.00,USD,Example Diner,RESTAURANTS\n"))
	if pending.State != stateReady {
		t.Fatalf("pending import: %+v", pending)
	}
	confirmed := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/confirm", staged.ID), nil, cookie))
	if confirmed.ReviewStatus != "confirmed" || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed: %+v", confirmed)
	}
	if kept := app.count("SELECT count(*) FROM exchange_rates WHERE quote_currency = 'USD'"); kept != 4 {
		t.Fatalf("confirmation cleanup kept %d USD rates, want all four", kept)
	}
	app.expect(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/imports/%d", pending.ID), nil, cookie)
	if deleted, err := rates.Cleanup(ctx, app.store, now); err != nil || deleted != 2 {
		t.Fatalf("cleanup after confirmation: %d, %v", deleted, err)
	}
	if kept := app.count("SELECT count(*) FROM exchange_rates WHERE quote_currency = 'USD'"); kept != 2 {
		t.Fatalf("kept %d USD rates, want the predecessor and October", kept)
	}
	if after := app.stats(cookie, ""); !reflect.DeepEqual(after, totals) {
		t.Fatalf("totals changed: %+v, was %+v", after, totals)
	}
	if again := app.detail(cookie, store.ID); again.EURCents != -9223 || again.Conversion == nil || *again.Conversion != *conversion {
		t.Fatalf("conversion changed after cleanup: %+v", again.Conversion)
	}

	status := decode[ratesStatus](t, app.expect(http.StatusOK, "GET", "/api/rates", nil, cookie))
	if status.Currencies != 1 || status.LatestPublication == nil || *status.LatestPublication != "2026-10-05" ||
		status.NextRunAt != "2026-10-06T16:00:00Z" || status.LastAttempt != nil || status.Endpoint != rates.Endpoint {
		t.Fatalf("rates status: %+v", status)
	}
	if count := outbound.requests.Load(); count != 0 {
		t.Fatalf("the API made %d outbound requests", count)
	}
}

func TestImportRowMustMatchAccountCurrency(t *testing.T) {
	app := newTestApp(t, Options{Now: func() time.Time { return localTime("2026-08-10", 19, 0) }})
	cookie := app.setup()
	euros := app.account(cookie, "Euros")
	app.saveRate(localTime("2026-08-03", 18, 5), "2026-08-03", "USD", "1.1")
	staged := app.imported(cookie, euros, "mixed.csv", []byte(header+
		"Card debit,2026-08-03 09:14:22,,mix-1,-12.34,USD,Example Diner,RESTAURANTS\n"+
		"Card debit,2026-08-03 10:00:00,,mix-2,-3.00,EUR,Sample Kiosk,MISCELLANEOUS\n"))
	invalid := app.stagedRows(cookie, staged.ID, "invalid")
	if staged.ErrorCount != 1 || len(invalid) != 1 ||
		!strings.Contains(invalid[0].Errors[0], "This row is in USD, but the account is in EUR") {
		t.Fatalf("mismatch: %+v %+v", staged, invalid)
	}
}
