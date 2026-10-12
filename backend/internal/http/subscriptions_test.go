package httpserver

import (
	"net/http"
	"testing"
	"time"
)

func TestSubscriptionsFindMonthlyRepeats(t *testing.T) {
	app := newTestApp(t, Options{Now: func() time.Time { return localTime("2026-09-30", 12, 0) }})
	if response := app.request("GET", "/api/subscriptions", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous subscriptions: %d", response.Code)
	}
	cookie := app.setup()
	checking := app.account(cookie, "Checking")
	pay := func(payee, kind, amount string, dates ...string) {
		for _, date := range dates {
			app.transaction(cookie, map[string]any{"accountId": checking, "date": date, "kind": kind, "amount": amount, "payee": payee})
		}
	}
	// Weekends move charges by up to four days, and a price change continues
	// the same subscription once the new price repeats.
	pay("Netflix", "expense", "12.99", "2026-05-15", "2026-06-17", "2026-07-14")
	pay("Netflix", "expense", "15.99", "2026-08-15", "2026-09-13")
	// One payee can bill two subscriptions.
	pay("Apple", "expense", "2.99", "2026-07-01", "2026-08-01", "2026-09-01")
	pay("Apple", "expense", "10.99", "2026-08-20", "2026-09-19")
	// The 31st is due on the last day of shorter months; the gym then stopped.
	pay("Gym", "expense", "30", "2026-01-31", "2026-02-28", "2026-03-31")
	// Not subscriptions: a repeat two weeks or two months apart, a charge six
	// days late, a single payment, changing amounts, and income.
	pay("Grocer", "expense", "45.10", "2026-06-01", "2026-06-15", "2026-08-01")
	pay("Grocer", "expense", "52.30", "2026-07-01")
	pay("Phone", "expense", "20", "2026-06-10", "2026-07-16")
	pay("Spotify", "expense", "9.99", "2026-09-05")
	pay("Employer", "income", "2500", "2026-07-01", "2026-08-01", "2026-09-01")

	view := decode[subscriptionsView](t, app.expect(http.StatusOK, "GET", "/api/subscriptions", nil, cookie))
	if view.Today != "2026-09-30" || view.MonthlyEURCents != 1599+1099+299 || view.YearlyEURCents != 12*(1599+1099+299) {
		t.Fatalf("totals: %+v", view)
	}
	type summary struct {
		payee, amount, since, lastPaid, nextDue string
		months                                  int
		paid                                    int64
		active                                  bool
	}
	want := []summary{
		{"Netflix", "15.99", "2026-05-15", "2026-09-13", "2026-10-15", 5, 3*1299 + 2*1599, true},
		{"Apple", "10.99", "2026-08-20", "2026-09-19", "2026-10-20", 2, 2 * 1099, true},
		{"Apple", "2.99", "2026-07-01", "2026-09-01", "2026-10-01", 3, 3 * 299, true},
		{"Gym", "30.00", "2026-01-31", "2026-03-31", "2026-04-30", 3, 9000, false},
	}
	if len(view.Subscriptions) != len(want) {
		t.Fatalf("subscriptions: %+v", view.Subscriptions)
	}
	for index, item := range view.Subscriptions {
		got := summary{item.PayeeName, item.Amount, item.Since, item.LastPaid, item.NextDue, item.Months, item.PaidEURCents, item.Active}
		if got != want[index] || item.Currency != "EUR" || item.Account.ID != checking || item.CategoryName != "Uncategorized" {
			t.Fatalf("subscription %d: got %+v, want %+v", index, item, want[index])
		}
	}
	if previous := view.Subscriptions[0].PreviousAmount; previous == nil || *previous != "12.99" {
		t.Fatalf("Netflix previous price: %v", previous)
	}
	if view.Subscriptions[1].PreviousAmount != nil {
		t.Fatalf("Apple has no price change: %+v", view.Subscriptions[1])
	}
}

func TestAddMonthsKeepsTheDay(t *testing.T) {
	start := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	for months, want := range []string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31"} {
		if got := addMonths(start, months).Format(time.DateOnly); got != want {
			t.Fatalf("%d months: got %s, want %s", months, got, want)
		}
	}
	if got := addMonths(time.Date(2027, time.November, 30, 0, 0, 0, 0, time.UTC), 3).Format(time.DateOnly); got != "2028-02-29" {
		t.Fatalf("leap year: %s", got)
	}
}
