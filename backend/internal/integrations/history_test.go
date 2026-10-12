package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFetchDividends(t *testing.T) {
	var paths []string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if user, secret, ok := r.BasicAuth(); !ok || user != "synthetic-key" || secret != syntheticSecret {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"items":[
				{"reference":"d-1","paidOn":"2026-03-02T10:00:00.000Z","type":"ORDINARY","amount":1.7,"currency":"EUR",
					"grossAmountPerShare":0.25,"quantity":8,"ticker":"AAPL_US_EQ","tickerCurrency":"USD",
					"instrument":{"currency":"USD","isin":"US0378331005","name":" Apple\tInc ","ticker":"AAPL_US_EQ"}},
				{"reference":"d-2","paidOn":"2026-03-01T08:00:00Z","type":"ORDINARY","amount":0.05,"currency":"EUR",
					"grossAmountPerShare":3.3,"quantity":1.5,"ticker":"VOD_EQ","instrument":{"currency":"GBX"}},
				{"reference":"d-3","paidOn":"2026-02-28T08:00:00Z","type":"ORDINARY","amount":-1.7,"currency":"EUR",
					"grossAmountPerShare":0.25,"quantity":8,"tickerCurrency":"USD"}],
				"nextPagePath":"/api/v0/equity/history/dividends?limit=50&cursor=abc"}`))
			return
		}
		w.Write([]byte(`{"items":[{"reference":"d-4","paidOn":"2026-01-15T08:00:00+01:00","type":"RETURN_OF_CAPITAL",
			"amount":0.3,"currency":"EUR","quantity":2,"instrument":{"currency":"USD"}}],"nextPagePath":null}`))
	})
	credentials := Trading212Credentials{Environment: "live", APIKey: "synthetic-key", APISecret: syntheticSecret}
	page, err := client.FetchDividends(context.Background(), credentials, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "/equity/history/dividends?limit=50&cursor=abc" || len(page.Items) != 3 {
		t.Fatalf("page %+v", page)
	}
	want := Dividend{Reference: "d-1", PaidAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), Type: "ORDINARY",
		Ticker: "AAPL_US_EQ", ISIN: "US0378331005", Name: "Apple Inc", Quantity: "8", PerShare: "0.25",
		Currency: "USD", Gross: 200, ReceivedCurrency: "EUR", Received: 170}
	if page.Items[0] != want {
		t.Errorf("first %+v", page.Items[0])
	}
	// 1.5 shares at 3.3 pence are 0.0495 pounds.
	if item := page.Items[1]; item.Currency != "GBP" || item.Gross != 5 || item.Ticker != "VOD_EQ" {
		t.Errorf("pence %+v", item)
	}
	// A reversal is credited negative, so its gross amount is too.
	if item := page.Items[2]; item.Currency != "USD" || item.Gross != -200 || item.Received != -170 {
		t.Errorf("reversal %+v", item)
	}
	page, err = client.FetchDividends(context.Background(), credentials, page.Next)
	if err != nil || page.Next != "" || len(page.Items) != 1 {
		t.Fatalf("second page %+v, %v", page, err)
	}
	// Without the amount per share the received amount stands in.
	if item := page.Items[0]; item.Currency != "EUR" || item.Gross != 30 || !item.PaidAt.Equal(time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("fallback %+v", item)
	}
	if !slices.Equal(paths, []string{"/live/equity/history/dividends?limit=50", "/live/equity/history/dividends?limit=50&cursor=abc"}) {
		t.Fatalf("paths %v", paths)
	}
}

func TestFetchTransactions(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/demo/equity/history/transactions?limit=50" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"items":[
			{"reference":"t-1","type":"INTEREST_ON_FREE_CASH","dateTime":"2026-04-01T00:10:00Z","amount":0.055,"currency":"EUR"},
			{"reference":"t-2","type":"DEPOSIT","dateTime":"2026-03-31T09:00:00Z","amount":100,"currency":"EUR"}],
			"nextPagePath":"/equity/history/transactions?limit=50&cursor=2"}`))
	})
	page, err := client.FetchTransactions(context.Background(), Trading212Credentials{Environment: "demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []CashTransaction{
		{Reference: "t-1", Type: "INTEREST_ON_FREE_CASH", At: time.Date(2026, 4, 1, 0, 10, 0, 0, time.UTC), Currency: "EUR", Amount: 6},
		{Reference: "t-2", Type: "DEPOSIT", At: time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC), Currency: "EUR", Amount: 10000},
	}
	if !slices.Equal(page.Items, want) || page.Next != "/equity/history/transactions?limit=50&cursor=2" {
		t.Fatalf("page %+v", page)
	}
}

func TestFetchHistoryFailures(t *testing.T) {
	for _, test := range []struct {
		name                   string
		status                 int
		body                   string
		permanent, rateLimited bool
		detail                 string
	}{
		{"no permission", 403, "", true, false, "Dividend history"},
		{"rate limited", 429, "", false, true, "rate limiting"},
		{"other endpoint", 200, `{"items":[],"nextPagePath":"/api/v0/equity/account/summary?limit=50"}`, false, false, "cannot read"},
		{"other host", 200, `{"items":[],"nextPagePath":"https://example.com/equity/history/dividends?limit=50"}`, false, false, "cannot read"},
		{"parent path", 200, `{"items":[],"nextPagePath":"/equity/history/dividends?cursor=/../../pies"}`, false, false, "cannot read"},
		{"no reference", 200, `{"items":[{"paidOn":"2026-03-02T10:00:00Z","amount":1,"currency":"EUR"}]}`, false, false, "cannot read"},
		{"bad time", 200, `{"items":[{"reference":"r","paidOn":"2 March","amount":1,"currency":"EUR"}]}`, false, false, "cannot read"},
		{"bad currency", 200, `{"items":[{"reference":"r","paidOn":"2026-03-02T10:00:00Z","amount":1,"currency":"euro"}]}`, false, false, "cannot read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			})
			_, err := client.FetchDividends(context.Background(), Trading212Credentials{APIKey: "k", APISecret: syntheticSecret}, "")
			var failed *FetchError
			if !errors.As(err, &failed) || failed.Permanent != test.permanent || failed.RateLimited != test.rateLimited ||
				!strings.Contains(failed.Detail, test.detail) {
				t.Fatalf("error %#v", err)
			}
		})
	}
}

func TestGrossMinorLimitsNumbers(t *testing.T) {
	for _, test := range []struct{ quantity, perShare string }{
		{"1e999", "1"}, {"1", strings.Repeat("1", 33)}, {"", "1"}, {"x", "1"},
	} {
		if _, _, ok := grossMinor(json.Number(test.quantity), json.Number(test.perShare), "USD"); ok {
			t.Errorf("%q x %q accepted", test.quantity, test.perShare)
		}
	}
	if currency, minor, ok := grossMinor("0.123456", "1.5e1", "USD"); !ok || currency != "USD" || minor != 185 {
		t.Errorf("%s %d %v", currency, minor, ok)
	}
}
