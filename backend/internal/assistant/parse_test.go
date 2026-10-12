package assistant

import (
	"testing"
	"time"
)

// today is a Sunday.
var today = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

func TestResolvePeriod(t *testing.T) {
	cases := []struct {
		key, text, from, to string
	}{
		{"today", "", "2026-09-20", "2026-09-20"},
		{"yesterday", "", "2026-09-19", "2026-09-19"},
		{"this_week", "", "2026-09-14", "2026-09-20"},
		{"last_week", "", "2026-09-07", "2026-09-13"},
		{"this_month", "", "2026-09-01", "2026-09-30"},
		{"last_month", "", "2026-08-01", "2026-08-31"},
		{"this_quarter", "", "2026-07-01", "2026-09-30"},
		{"last_quarter", "", "2026-04-01", "2026-06-30"},
		{"this_year", "", "2026-01-01", "2026-12-31"},
		{"last_year", "", "2025-01-01", "2025-12-31"},
		{"last_7_days", "", "2026-09-14", "2026-09-20"},
		{"last_n", "in the last 2 weeks", "2026-09-07", "2026-09-20"},
		{"last_n", "over the past three months", "2026-06-21", "2026-09-20"},
		{"named_month", "in March", "2026-03-01", "2026-03-31"},
		{"named_month", "in December", "2025-12-01", "2025-12-31"},
		{"named_month", "in March 2024", "2024-03-01", "2024-03-31"},
		{"named_year", "in 2024", "2024-01-01", "2024-12-31"},
		{"specific_day", "on 3 March", "2026-03-03", "2026-03-03"},
		{"specific_day", "on 2025-03-03", "2025-03-03", "2025-03-03"},
		{"specific_day", "on 3.3.2025", "2025-03-03", "2025-03-03"},
		{"date_range", "from January to March", "2026-01-01", "2026-03-31"},
		{"since", "since 2024", "2024-01-01", "2026-09-20"},
		// A loose key still finds the date in the text.
		{"named_month", "in 2023", "2023-01-01", "2023-12-31"},
	}
	for _, item := range cases {
		period, ok := resolvePeriod(item.key, item.text, today)
		if !ok || period.From != item.from || period.To != item.to {
			t.Errorf("%s %q: got %+v %v, want %s to %s", item.key, item.text, period, ok, item.from, item.to)
		}
	}
	if period, ok := resolvePeriod("all_time", "", today); !ok || !period.allTime() {
		t.Errorf("all time: %+v", period)
	}
	if _, ok := resolvePeriod("named_month", "no date here", today); ok {
		t.Error("a named month needs a month in the text")
	}
}

func TestLimitBoundsAndCurrency(t *testing.T) {
	limits := map[string]int{
		"top 5 payees":                          5,
		"my three biggest purchases":            3,
		"show 20 transactions":                  20,
		"the last 10 payments":                  10,
		"spending in the last 30 days":          0,
		"what did I spend in the last 3 months": 0,
		"who is my biggest payee":               0,
	}
	for question, want := range limits {
		got, ok := limitOf(question)
		if got != want || ok != (want != 0) {
			t.Errorf("limit of %q: got %d %v, want %d", question, got, ok, want)
		}
	}

	bounds := []struct {
		question  string
		low, high int64
	}{
		{"transactions over 100 EUR", 10000, 0},
		{"purchases under €20.50", 0, 2050},
		{"between 10 and 50 euros", 1000, 5000},
		{"payments above 1.5k", 150000, 0},
		{"more than 1,200.00", 120000, 0},
		{"spending since 2024", 0, 0},
		{"spending over 2025", 0, 0},
	}
	for _, item := range bounds {
		low, high, _ := boundsOf(item.question)
		if low != item.low || high != item.high {
			t.Errorf("bounds of %q: got %d to %d, want %d to %d", item.question, low, high, item.low, item.high)
		}
	}

	currencies := map[string]string{
		"What is the USD exchange rate?":    "USD",
		"how many czech crowns for a euro?": "CZK",
		"the dollar rate":                   "USD",
		"rate of £":                         "GBP",
		"How much did I spend?":             "",
		"and the rest of it":                "",
	}
	for question, want := range currencies {
		if got := currencyOf(question); got != want {
			t.Errorf("currency of %q: got %q, want %q", question, got, want)
		}
	}

	if search, rest := quoted(`show "coffee beans" this month`); search != "coffee beans" || rest == "" {
		t.Errorf("quoted: %q %q", search, rest)
	}
}

func TestCleanQuestionAndFormat(t *testing.T) {
	if _, ok := CleanQuestion("  "); ok {
		t.Error("a blank question is not valid")
	}
	if question, ok := CleanQuestion("  How much?  "); !ok || question != "How much?" {
		t.Errorf("clean: %q %v", question, ok)
	}
	if money(123456, "EUR") != "€1,234.56" || money(-5, "EUR") != "-€0.05" || money(123456, "USD") != "1,234.56 USD" {
		t.Errorf("money: %s %s %s", money(123456, "EUR"), money(-5, "EUR"), money(123456, "USD"))
	}
}
