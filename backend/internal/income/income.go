// Package income converts dividends, interest, and cashback to EUR for a
// Slovak tax return, with each method § 31 ods. 2 písm. a) of Act 595/2003
// allows a taxpayer who does not keep accounts, and estimates the tax so the
// cheapest method can be chosen.
package income

import (
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/nbs"
)

// Income kinds.
const (
	KindDividend = "dividend"
	KindInterest = "interest"
	KindCashback = "cashback"
)

// Kinds lists the kinds in display order.
var Kinds = []string{KindDividend, KindInterest, KindCashback}

// Conversion methods of § 31 ods. 2 písm. a), in the order of the act.
const (
	// MethodMonthly is the average rate of the month the income was paid
	// (bod 1).
	MethodMonthly = "monthly"
	// MethodDaily is the rate valid on the day it was received (bod 2).
	MethodDaily = "daily"
	// MethodAnnual is the annual average rate of the tax year (bod 3).
	MethodAnnual = "annual"
	// MethodIncomeMonths is the average of the monthly averages of the
	// months in which income was received (bod 4).
	MethodIncomeMonths = "income_months"
)

// Methods lists the methods in the order of the act, which also breaks ties.
var Methods = []string{MethodMonthly, MethodDaily, MethodAnnual, MethodIncomeMonths}

// dailyGap is the longest wait for a daily rate. NBS publishes on every
// TARGET business day, so a longer gap means the rates are missing.
const dailyGap = 10

// Item is one income record to convert. Amount is in minor units of
// Currency.
type Item struct {
	ID       int64
	Kind     string
	Type     string
	PaidOn   string
	Currency string
	Amount   int64
}

// Taxable reports whether an item counts toward the tax. A return of
// capital pays back part of the investment and is not income.
func (i Item) Taxable() bool {
	return !strings.Contains(i.Type, "RETURN_OF_CAPITAL")
}

// Conversion is one item in EUR under one method. A missing rate leaves Rate
// empty. Provisional rates are averages NBS has not published yet, or a
// day's rate that may still be published, so the value can change.
type Conversion struct {
	Rate        string
	Period      string
	EURCents    int64
	Provisional bool
}

// Missing reports that no rate was found.
func (c Conversion) Missing() bool { return c.Rate == "" }

// Table holds the NBS rates of one tax year and the month before it.
type Table struct {
	daily    map[string][]nbs.Rate
	monthly  map[string]string
	annual   map[string]string
	complete map[string]bool
}

// NewTable indexes rates by currency. complete lists the month periods
// (YYYY-MM) whose files were read after the month ended.
func NewTable(rates []nbs.Rate, complete []string) *Table {
	table := &Table{daily: map[string][]nbs.Rate{}, monthly: map[string]string{}, annual: map[string]string{},
		complete: map[string]bool{}}
	for _, rate := range rates {
		switch len(rate.Period) {
		case len(time.DateOnly):
			table.daily[rate.Currency] = append(table.daily[rate.Currency], rate)
		case len("2006-01"):
			table.monthly[rate.Currency+" "+rate.Period] = rate.Rate
		case len("2006"):
			table.annual[rate.Currency+" "+rate.Period] = rate.Rate
		}
	}
	for _, days := range table.daily {
		slices.SortFunc(days, func(a, b nbs.Rate) int { return strings.Compare(a.Period, b.Period) })
	}
	for _, period := range complete {
		table.complete[period] = true
	}
	return table
}

type rate struct {
	value       string
	period      string
	provisional bool
}

// day returns the rate valid on a day: its own, or the last one published
// before it within dailyGap days. A day without its own rate is provisional
// until a later rate or the finished month shows none will follow.
func (t *Table) day(currency, date string) (rate, bool) {
	days := t.daily[currency]
	index, found := slices.BinarySearchFunc(days, date, func(rate nbs.Rate, date string) int { return strings.Compare(rate.Period, date) })
	if found {
		return rate{value: days[index].Rate, period: date}, true
	}
	if index == 0 {
		return rate{}, false
	}
	previous := days[index-1]
	day, _ := time.Parse(time.DateOnly, date)
	published, _ := time.Parse(time.DateOnly, previous.Period)
	if day.Sub(published) > dailyGap*24*time.Hour {
		return rate{}, false
	}
	final := index < len(days) || t.complete[date[:7]]
	return rate{value: previous.Rate, period: previous.Period, provisional: !final}, true
}

// average is the published average of a month or year, or the mean of its
// daily rates so far, which is provisional.
func (t *Table) average(currency, period string, published map[string]string) (rate, bool) {
	if value, ok := published[currency+" "+period]; ok {
		return rate{value: value, period: period}, true
	}
	var values []string
	for _, day := range t.daily[currency] {
		if strings.HasPrefix(day.Period, period+"-") {
			values = append(values, day.Rate)
		}
	}
	value, err := nbs.Average(values)
	if err != nil {
		return rate{}, false
	}
	return rate{value: value, period: period, provisional: true}, true
}

func (t *Table) month(currency, month string) (rate, bool) {
	return t.average(currency, month, t.monthly)
}

func (t *Table) year(currency, year string) (rate, bool) {
	return t.average(currency, year, t.annual)
}

// incomeMonths averages the monthly averages of the given months.
func (t *Table) incomeMonths(currency, year string, months []string) (rate, bool) {
	values := make([]string, 0, len(months))
	provisional := false
	for _, month := range months {
		monthly, ok := t.month(currency, month)
		if !ok {
			return rate{}, false
		}
		values = append(values, monthly.value)
		provisional = provisional || monthly.provisional
	}
	value, err := nbs.Average(values)
	if err != nil {
		return rate{}, false
	}
	return rate{value: value, period: year, provisional: provisional}, true
}

// Totals sums one method over a year. Bases and taxes are EUR cents per
// kind; Missing counts taxable items without a rate, which leave the totals
// incomplete.
type Totals struct {
	Method      string
	Bases       map[string]int64
	Taxes       map[string]int64
	Base        int64
	Tax         int64
	Missing     int
	Provisional bool
}

// Result converts every item of a year with each method.
type Result struct {
	Year string
	// Conversions holds one conversion per method for each item, in the
	// order of the items.
	Conversions []map[string]Conversion
	Totals      []Totals
	// Recommended is the complete method with the lowest tax, or empty when
	// every method misses a rate. Equal reports that all complete methods
	// give the same tax.
	Recommended string
	Equal       bool
	// Months lists, per currency, the months in which taxable income was
	// received, which method 4 averages.
	Months map[string][]string
}

// Compute converts the items of one tax year, all paid in that year, and
// taxes each kind at its percentage.
func Compute(year string, items []Item, table *Table, taxPercent map[string]int64) (Result, error) {
	result := Result{Year: year, Conversions: make([]map[string]Conversion, len(items)), Months: map[string][]string{}}
	for _, item := range items {
		if item.Currency == "EUR" || !item.Taxable() {
			continue
		}
		month := item.PaidOn[:7]
		if !slices.Contains(result.Months[item.Currency], month) {
			result.Months[item.Currency] = append(result.Months[item.Currency], month)
		}
	}
	for _, months := range result.Months {
		slices.Sort(months)
	}
	totals := make(map[string]*Totals, len(Methods))
	for _, method := range Methods {
		totals[method] = &Totals{Method: method, Bases: map[string]int64{}, Taxes: map[string]int64{}}
	}
	for index, item := range items {
		amount, err := ledger.NewMoney(item.Amount, item.Currency)
		if err != nil {
			return Result{}, fmt.Errorf("income %d: %w", item.ID, err)
		}
		conversions := make(map[string]Conversion, len(Methods))
		for _, method := range Methods {
			found, ok := rate{value: "1", period: year}, true
			if item.Currency != "EUR" {
				switch method {
				case MethodDaily:
					found, ok = table.day(item.Currency, item.PaidOn)
				case MethodMonthly:
					found, ok = table.month(item.Currency, item.PaidOn[:7])
				case MethodAnnual:
					found, ok = table.year(item.Currency, year)
				case MethodIncomeMonths:
					found, ok = table.incomeMonths(item.Currency, year, result.Months[item.Currency])
				}
			} else if method == MethodDaily {
				found.period = item.PaidOn
			} else if method == MethodMonthly {
				found.period = item.PaidOn[:7]
			}
			conversion := Conversion{}
			if ok {
				parsed, err := ledger.ParseRate(found.value)
				if err != nil {
					return Result{}, fmt.Errorf("rate for %s: %w", item.Currency, err)
				}
				cents, err := ledger.EURCents(amount, parsed)
				if err != nil {
					return Result{}, err
				}
				conversion = Conversion{Rate: found.value, Period: found.period, EURCents: cents, Provisional: found.provisional}
			}
			conversions[method] = conversion
			if !item.Taxable() {
				continue
			}
			total := totals[method]
			if conversion.Missing() {
				total.Missing++
				continue
			}
			total.Bases[item.Kind] += conversion.EURCents
			total.Provisional = total.Provisional || conversion.Provisional
		}
		result.Conversions[index] = conversions
	}
	for _, method := range Methods {
		total := totals[method]
		for _, kind := range Kinds {
			base := total.Bases[kind]
			total.Taxes[kind] = Tax(base, taxPercent[kind])
			total.Base += base
			total.Tax += total.Taxes[kind]
		}
		result.Totals = append(result.Totals, *total)
	}
	result.Recommended, result.Equal = recommend(result.Totals)
	return result, nil
}

// Tax applies a percentage to a base in cents and rounds to cents, half up
// as § 47 requires. A negative base owes nothing.
func Tax(base, percent int64) int64 {
	if base <= 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(base), big.NewInt(percent))
	cents, remainder := new(big.Int).QuoRem(product, big.NewInt(100), new(big.Int))
	if remainder.Int64()*2 >= 100 {
		cents.Add(cents, big.NewInt(1))
	}
	return cents.Int64()
}

// recommend picks the complete method with the lowest tax, then the lowest
// base, then the first in the act. Equal needs at least two complete
// methods.
func recommend(totals []Totals) (string, bool) {
	best, complete := -1, 0
	equal := true
	for index, total := range totals {
		if total.Missing > 0 {
			continue
		}
		complete++
		if best < 0 {
			best = index
			continue
		}
		current := totals[best]
		if total.Tax != current.Tax || total.Base != current.Base {
			equal = false
		}
		if total.Tax < current.Tax || (total.Tax == current.Tax && total.Base < current.Base) {
			best = index
		}
	}
	if best < 0 {
		return "", false
	}
	return totals[best].Method, equal && complete > 1
}
