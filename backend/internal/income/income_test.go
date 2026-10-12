package income

import (
	"testing"
	"time"

	"budget-tracking/backend/internal/nbs"
)

func usd(period, value string) nbs.Rate {
	return nbs.Rate{Currency: "USD", Period: period, Rate: value}
}

func TestDayRate(t *testing.T) {
	table := NewTable([]nbs.Rate{usd("2026-01-05", "1.18"), usd("2025-12-31", "1.17"), usd("2026-01-02", "1.19")}, nil)
	for date, want := range map[string]rate{
		"2026-01-02": {value: "1.19", period: "2026-01-02"},
		// New Year's Day has no rate; the last one of December applies.
		"2026-01-01": {value: "1.17", period: "2025-12-31"},
		// The rate of Monday shows none was published over the weekend.
		"2026-01-04": {value: "1.19", period: "2026-01-02"},
		// Nothing after the last rate shows it is final yet.
		"2026-01-06": {value: "1.18", period: "2026-01-05", provisional: true},
	} {
		got, ok := table.day("USD", date)
		if !ok || got != want {
			t.Errorf("%s: %+v %v", date, got, ok)
		}
	}
	for _, date := range []string{"2025-12-30", "2026-01-16"} {
		if got, ok := table.day("USD", date); ok {
			t.Errorf("%s: %+v", date, got)
		}
	}
	if _, ok := table.day("JPY", "2026-01-02"); ok {
		t.Error("JPY found")
	}
	// A month read after it ended has every rate it will ever have.
	table = NewTable([]nbs.Rate{usd("2026-01-30", "1.18")}, []string{"2026-01"})
	if got, ok := table.day("USD", "2026-01-31"); !ok || got.provisional {
		t.Errorf("complete month: %+v %v", got, ok)
	}
}

func TestAverages(t *testing.T) {
	table := NewTable([]nbs.Rate{
		usd("2026-01-02", "1.25"), usd("2026-01-05", "1.20"), usd("2026-02-02", "1.1001"), usd("2026-02", "1.1000"),
	}, nil)
	// NBS's own average wins over the mean of the days, which may differ.
	if got, ok := table.month("USD", "2026-02"); !ok || got != (rate{value: "1.1000", period: "2026-02"}) {
		t.Errorf("official: %+v %v", got, ok)
	}
	if got, ok := table.month("USD", "2026-01"); !ok || got != (rate{value: "1.2250", period: "2026-01", provisional: true}) {
		t.Errorf("computed: %+v %v", got, ok)
	}
	if got, ok := table.year("USD", "2026"); !ok || got != (rate{value: "1.1834", period: "2026", provisional: true}) {
		t.Errorf("year: %+v %v", got, ok)
	}
	if _, ok := table.month("USD", "2026-03"); ok {
		t.Error("month without rates found")
	}
	got, ok := table.incomeMonths("USD", "2026", []string{"2026-01", "2026-02"})
	if !ok || got != (rate{value: "1.1625", period: "2026", provisional: true}) {
		t.Errorf("income months: %+v %v", got, ok)
	}
	if _, ok := table.incomeMonths("USD", "2026", []string{"2026-01", "2026-03"}); ok {
		t.Error("income months with a missing month found")
	}
}

func totalsOf(result Result, method string) Totals {
	for _, total := range result.Totals {
		if total.Method == method {
			return total
		}
	}
	return Totals{}
}

var slovakTax = map[string]int64{KindDividend: 7, KindInterest: 19, KindCashback: 19}

func TestCompute(t *testing.T) {
	table := NewTable([]nbs.Rate{
		usd("2026-01-02", "1.25"), usd("2026-01-05", "1.20"), usd("2026-02-02", "1.10"),
		usd("2026-01", "1.2000"), usd("2026-02", "1.1000"), usd("2026", "1.1500"),
	}, []string{"2026-01"})
	items := []Item{
		{ID: 1, Kind: KindDividend, Type: "ORDINARY", PaidOn: "2026-01-02", Currency: "USD", Amount: 1000},
		{ID: 2, Kind: KindInterest, Type: "INTEREST_ON_FREE_CASH", PaidOn: "2026-02-03", Currency: "USD", Amount: 500},
		{ID: 3, Kind: KindCashback, Type: "Spending cashback", PaidOn: "2026-03-01", Currency: "EUR", Amount: 27},
		{ID: 4, Kind: KindDividend, Type: "RETURN_OF_CAPITAL", PaidOn: "2026-03-10", Currency: "USD", Amount: 10000},
	}
	result, err := Compute("2026", items, table, slovakTax)
	if err != nil {
		t.Fatal(err)
	}
	if months := result.Months["USD"]; len(months) != 2 || months[0] != "2026-01" || months[1] != "2026-02" {
		t.Errorf("months: %v", result.Months)
	}
	for index, want := range []map[string]Conversion{
		{
			MethodMonthly:      {Rate: "1.2000", Period: "2026-01", EURCents: 833},
			MethodDaily:        {Rate: "1.25", Period: "2026-01-02", EURCents: 800},
			MethodAnnual:       {Rate: "1.1500", Period: "2026", EURCents: 870},
			MethodIncomeMonths: {Rate: "1.1500", Period: "2026", EURCents: 870},
		},
		{
			MethodMonthly:      {Rate: "1.1000", Period: "2026-02", EURCents: 455},
			MethodDaily:        {Rate: "1.10", Period: "2026-02-02", EURCents: 455, Provisional: true},
			MethodAnnual:       {Rate: "1.1500", Period: "2026", EURCents: 435},
			MethodIncomeMonths: {Rate: "1.1500", Period: "2026", EURCents: 435},
		},
		{
			MethodMonthly:      {Rate: "1", Period: "2026-03", EURCents: 27},
			MethodDaily:        {Rate: "1", Period: "2026-03-01", EURCents: 27},
			MethodAnnual:       {Rate: "1", Period: "2026", EURCents: 27},
			MethodIncomeMonths: {Rate: "1", Period: "2026", EURCents: 27},
		},
	} {
		for method, conversion := range want {
			if got := result.Conversions[index][method]; got != conversion {
				t.Errorf("item %d %s: %+v", index+1, method, got)
			}
		}
	}
	// A return of capital is shown but neither taxed nor counted as missing.
	if conversion := result.Conversions[3][MethodMonthly]; !conversion.Missing() {
		t.Errorf("return of capital: %+v", conversion)
	}
	for method, want := range map[string][3]int64{
		MethodMonthly: {1315, 149, 0}, MethodDaily: {1282, 147, 1}, MethodAnnual: {1332, 149, 0}, MethodIncomeMonths: {1332, 149, 0},
	} {
		total := totalsOf(result, method)
		if total.Base != want[0] || total.Tax != want[1] || total.Provisional != (want[2] == 1) || total.Missing != 0 {
			t.Errorf("%s: %+v", method, total)
		}
	}
	monthly := totalsOf(result, MethodMonthly)
	if monthly.Taxes[KindDividend] != 58 || monthly.Taxes[KindInterest] != 86 || monthly.Taxes[KindCashback] != 5 {
		t.Errorf("monthly taxes: %v", monthly.Taxes)
	}
	if result.Recommended != MethodDaily || result.Equal {
		t.Errorf("recommended %q equal %v", result.Recommended, result.Equal)
	}
}

func TestComputeRecommendsOnlyCompleteMethods(t *testing.T) {
	table := NewTable([]nbs.Rate{usd("2026", "1.1500")}, nil)
	items := []Item{{ID: 1, Kind: KindDividend, PaidOn: "2026-04-15", Currency: "USD", Amount: 1000}}
	result, err := Compute("2026", items, table, slovakTax)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{MethodMonthly, MethodDaily, MethodIncomeMonths} {
		if total := totalsOf(result, method); total.Missing != 1 || total.Base != 0 {
			t.Errorf("%s: %+v", method, total)
		}
	}
	if result.Recommended != MethodAnnual || result.Equal {
		t.Errorf("recommended %q equal %v", result.Recommended, result.Equal)
	}
	// Without any rate nothing can be recommended.
	result, err = Compute("2026", items, NewTable(nil, nil), slovakTax)
	if err != nil || result.Recommended != "" || result.Equal {
		t.Errorf("no rates: %q %v %v", result.Recommended, result.Equal, err)
	}
}

func TestComputeInEUROnly(t *testing.T) {
	items := []Item{
		{ID: 1, Kind: KindInterest, PaidOn: "2026-05-01", Currency: "EUR", Amount: 1234},
		{ID: 2, Kind: KindCashback, PaidOn: "2026-05-02", Currency: "EUR", Amount: -100},
	}
	result, err := Compute("2026", items, NewTable(nil, nil), slovakTax)
	if err != nil {
		t.Fatal(err)
	}
	// Every method gives the same tax, so the first in the act is chosen.
	if result.Recommended != MethodMonthly || !result.Equal {
		t.Errorf("recommended %q equal %v", result.Recommended, result.Equal)
	}
	total := totalsOf(result, MethodAnnual)
	// A reversed cashback leaves a negative base that owes nothing.
	if total.Bases[KindInterest] != 1234 || total.Bases[KindCashback] != -100 || total.Taxes[KindCashback] != 0 || total.Tax != 234 {
		t.Errorf("totals: %+v", total)
	}
}

func TestTaxRoundsHalfUp(t *testing.T) {
	for _, test := range []struct{ base, percent, want int64 }{
		{50, 1, 1}, {49, 1, 0}, {833, 7, 58}, {650, 7, 46}, {150, 19, 29}, {0, 19, 0}, {-100, 7, 0},
	} {
		if got := Tax(test.base, test.percent); got != test.want {
			t.Errorf("Tax(%d, %d) = %d, want %d", test.base, test.percent, got, test.want)
		}
	}
}

func TestParseCashback(t *testing.T) {
	report := "\xEF\xBB\xBFAction,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name\n" +
		"Card debit,2026-08-03 17:40:00,,card-1,-12.50,EUR,Shop\n" +
		"Spending cashback,2026-08-03 17:45:11,,cash-1,0.27,EUR,\n" +
		"Interest on cash,2026-08-04 00:00:00,,interest-1,0.05,EUR,\n" +
		"Spending cashback,2026-08-12 19:20:01.5,,cash-2,0.1234,USD,\n"
	credits, err := ParseCashback([]byte(report))
	if err != nil {
		t.Fatal(err)
	}
	if len(credits) != 2 {
		t.Fatalf("%+v", credits)
	}
	first, second := credits[0], credits[1]
	if first.ID != "cash-1" || first.Action != "Spending cashback" || first.Currency != "EUR" || first.Amount != 27 ||
		first.At.Format(time.RFC3339) != "2026-08-03T17:45:11Z" {
		t.Errorf("first: %+v", first)
	}
	if second.ID != "cash-2" || second.Currency != "USD" || second.Amount != 12 {
		t.Errorf("second: %+v", second)
	}
	for name, data := range map[string]string{
		"no total":     "Action,Time (UTC),ID,Currency (Total)\nSpending cashback,2026-08-03 17:45:11,c,EUR\n",
		"no id":        "Action,Time (UTC),ID,Total,Currency (Total)\nSpending cashback,2026-08-03 17:45:11,,0.27,EUR\n",
		"date only":    "Action,Time (UTC),ID,Total,Currency (Total)\nSpending cashback,2026-08-03,c,0.27,EUR\n",
		"bad amount":   "Action,Time (UTC),ID,Total,Currency (Total)\nSpending cashback,2026-08-03 17:45:11,c,n/a,EUR\n",
		"bad currency": "Action,Time (UTC),ID,Total,Currency (Total)\nSpending cashback,2026-08-03 17:45:11,c,0.27,euro\n",
	} {
		if _, err := ParseCashback([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
