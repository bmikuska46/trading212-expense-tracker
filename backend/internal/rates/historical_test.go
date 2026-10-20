package rates

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHistoricalRateCachesOnlyRequestedCoverage(t *testing.T) {
	store := testStore(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("base") != "EUR" || r.URL.Query().Get("quotes") != "USD" || r.URL.Query().Get("date") != "2026-09-13" {
			t.Errorf("historical request: %s", r.URL)
		}
		fmt.Fprint(w, `[{"date":"2026-09-11","base":"EUR","quote":"USD","rate":1.1}]`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), URL: server.URL}
	ctx, now := context.Background(), local("2026-09-20", 19)
	for range 2 {
		if err := EnsureHistorical(ctx, store.Queries, client, "2026-09-13", "USD", now); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatalf("historical requests = %d, want 1", requests)
	}
	conversion, err := Convert(ctx, store.Queries, usd(t, "-11.00"), "2026-09-13", now)
	if err != nil || conversion.EURCents != -1000 || conversion.RateDate != "2026-09-11" || conversion.ProviderPolicy != HistoricalProviderPolicy {
		t.Fatalf("historical conversion = %+v, %v", conversion, err)
	}
	var missing *MissingRate
	if _, err := Convert(ctx, store.Queries, usd(t, "-11.00"), "2026-09-14", now); !errors.As(err, &missing) {
		t.Fatalf("historical fetch incorrectly covered next day: %v", err)
	}
}

func TestHistoricalRateRejectsUnrelatedResponses(t *testing.T) {
	for name, response := range map[string]string{
		"future publication": `[{"date":"2026-09-14","base":"EUR","quote":"USD","rate":1.1}]`,
		"wrong currency":     `[{"date":"2026-09-11","base":"EUR","quote":"GBP","rate":1.1}]`,
		"wrong base":         `[{"date":"2026-09-11","base":"GBP","quote":"USD","rate":1.1}]`,
		"invalid rate":       `[{"date":"2026-09-11","base":"EUR","quote":"USD","rate":0}]`,
		"missing rate":       `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			client := &Client{HTTP: server.Client(), URL: server.URL}
			if err := EnsureHistorical(context.Background(), store.Queries, client, "2026-09-13", "USD", local("2026-09-20", 19)); err == nil {
				t.Fatal("invalid historical response accepted")
			}
			var count int
			if err := store.DB.QueryRow("SELECT count(*) FROM exchange_rates").Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid response wrote %d rates: %v", count, err)
			}
		})
	}
}

func TestHistoricalRateFailureCanBeRetried(t *testing.T) {
	store := testStore(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `[{"date":"2026-09-11","base":"EUR","quote":"USD","rate":1.1}]`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), URL: server.URL}
	ctx, now := context.Background(), local("2026-09-20", 19)
	if err := EnsureHistorical(ctx, store.Queries, client, "2026-09-13", "USD", now); err == nil {
		t.Fatal("service failure accepted")
	}
	if err := EnsureHistorical(ctx, store.Queries, client, "2026-09-13", "USD", now); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("retry requests = %d, want 2", requests)
	}
}

func TestHistoricalRateDoesNotFinalizeTodayBeforePublication(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	quote := Quote{Date: "2026-09-14", Base: "EUR", Quote: "USD", Rate: "1.1"}
	if err := SaveHistorical(ctx, store.Queries, quote, "2026-09-15", local("2026-09-15", 9)); err != nil {
		t.Fatal(err)
	}
	var missing *MissingRate
	if _, err := Convert(ctx, store.Queries, usd(t, "-11.00"), "2026-09-15", local("2026-09-15", 9)); !errors.As(err, &missing) || !missing.Upcoming {
		t.Fatalf("today used yesterday's rate before publication: %v", err)
	}
	quote.Date, quote.Rate = "2026-09-15", "1.25"
	if err := SaveHistorical(ctx, store.Queries, quote, "2026-09-15", local("2026-09-15", 19)); err != nil {
		t.Fatal(err)
	}
	conversion, err := Convert(ctx, store.Queries, usd(t, "-12.50"), "2026-09-15", local("2026-09-15", 19))
	if err != nil || conversion.EURCents != -1000 || conversion.RateDate != "2026-09-15" {
		t.Fatalf("today's published conversion: %+v %v", conversion, err)
	}
}
