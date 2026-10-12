package httpserver

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/income"
	"budget-tracking/backend/internal/jobs"
	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/nbs"
	"budget-tracking/backend/internal/rates"
)

// firstIncomeYear is the earliest tax year the income page accepts.
const firstIncomeYear = 2000

type incomeConversionView struct {
	Rate        *string `json:"rate"`
	Period      *string `json:"period"`
	EURCents    *int64  `json:"eurCents"`
	Provisional bool    `json:"provisional"`
}

type incomeItemView struct {
	ID               int64   `json:"id"`
	Kind             string  `json:"kind"`
	Type             string  `json:"type"`
	PaidAt           string  `json:"paidAt"`
	PaidOn           string  `json:"paidOn"`
	Currency         string  `json:"currency"`
	AmountMinor      int64   `json:"amountMinor"`
	ReceivedCurrency string  `json:"receivedCurrency"`
	ReceivedMinor    int64   `json:"receivedMinor"`
	Ticker           *string `json:"ticker"`
	ISIN             *string `json:"isin"`
	Name             *string `json:"name"`
	Quantity         *string `json:"quantity"`
	PerShare         *string `json:"perShare"`
	Taxable          bool    `json:"taxable"`
	// The informative value uses the rate cached when the item was imported
	// and is not a tax figure.
	InformativeEURCents *int64                          `json:"informativeEurCents"`
	InformativeRate     *string                         `json:"informativeRate"`
	InformativeRateDate *string                         `json:"informativeRateDate"`
	Conversions         map[string]incomeConversionView `json:"conversions"`
}

type incomeTotalsView struct {
	Method      string           `json:"method"`
	Bases       map[string]int64 `json:"bases"`
	Taxes       map[string]int64 `json:"taxes"`
	BaseCents   int64            `json:"baseCents"`
	TaxCents    int64            `json:"taxCents"`
	Missing     int              `json:"missing"`
	Provisional bool             `json:"provisional"`
}

type incomeSyncView struct {
	StartedOn         *string `json:"startedOn"`
	DividendsSyncedAt *string `json:"dividendsSyncedAt"`
	InterestSyncedAt  *string `json:"interestSyncedAt"`
	HistoryAttemptAt  *string `json:"historyAttemptAt"`
	HistoryError      *string `json:"historyError"`
	CashbackThrough   *string `json:"cashbackThrough"`
	CashbackAttemptAt *string `json:"cashbackAttemptAt"`
	CashbackError     *string `json:"cashbackError"`
	CashbackPending   bool    `json:"cashbackPending"`
	RatesCheckedAt    *string `json:"ratesCheckedAt"`
	RatesError        *string `json:"ratesError"`
}

type incomeView struct {
	Configured  bool                `json:"configured"`
	Year        int                 `json:"year"`
	Years       []int               `json:"years"`
	Sync        incomeSyncView      `json:"sync"`
	TaxPercent  map[string]int64    `json:"taxPercent"`
	Items       []incomeItemView    `json:"items"`
	Totals      []incomeTotalsView  `json:"totals"`
	Recommended *string             `json:"recommended"`
	Equal       bool                `json:"equal"`
	Months      map[string][]string `json:"months"`
}

// getIncome lists the dividends, interest, and cashback of a tax year with
// each conversion method and the tax each would give. The year defaults to
// the current one in Bratislava.
func (a *api) getIncome(w http.ResponseWriter, r *http.Request, _ *session) {
	if view, ok := a.loadIncome(w, r); ok {
		writeJSON(w, http.StatusOK, view)
	}
}

// loadIncome builds the income view of the requested year, or writes the
// error and reports false.
func (a *api) loadIncome(w http.ResponseWriter, r *http.Request) (incomeView, bool) {
	current := a.now().In(rates.Location).Year()
	year := current
	if value := r.URL.Query().Get("year"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < firstIncomeYear || parsed > current {
			writeFieldErrors(w, map[string]string{"year": "Choose a year from " + strconv.Itoa(firstIncomeYear) + " to " + strconv.Itoa(current)})
			return incomeView{}, false
		}
		year = parsed
	}
	ctx, q := r.Context(), a.store.Queries
	_, _, configured, err := jobs.Trading212Credentials(ctx, q)
	if err != nil {
		internalError(w, "load integration")
		return incomeView{}, false
	}
	state, err := q.GetIncomeState(ctx)
	if err != nil {
		internalError(w, "load income state")
		return incomeView{}, false
	}
	known, err := q.IncomeYears(ctx)
	if err != nil {
		internalError(w, "load income years")
		return incomeView{}, false
	}
	years := []int{current}
	for _, value := range known {
		if parsed, err := strconv.Atoi(value); err == nil && !slices.Contains(years, parsed) {
			years = append(years, parsed)
		}
	}
	slices.SortFunc(years, func(a, b int) int { return b - a })

	label := strconv.Itoa(year)
	rows, err := q.ListIncome(ctx, dbgen.ListIncomeParams{Since: label + "-01-01", Until: strconv.Itoa(year+1) + "-01-01"})
	if err != nil {
		internalError(w, "load income")
		return incomeView{}, false
	}
	// The daily rate of the 1st of January may be the last one of December.
	saved, err := q.ListNBSRates(ctx, dbgen.ListNBSRatesParams{Since: strconv.Itoa(year-1) + "-12", Until: strconv.Itoa(year + 1)})
	if err != nil {
		internalError(w, "load NBS rates")
		return incomeView{}, false
	}
	files, err := q.ListNBSFiles(ctx)
	if err != nil {
		internalError(w, "load NBS files")
		return incomeView{}, false
	}
	table := make([]nbs.Rate, len(saved))
	for index, row := range saved {
		table[index] = nbs.Rate{Currency: row.Currency, Period: row.Period, Rate: row.Rate}
	}
	var complete []string
	for _, file := range files {
		if file.Complete == 1 {
			complete = append(complete, file.Period)
		}
	}
	items := make([]income.Item, len(rows))
	for index, row := range rows {
		items[index] = income.Item{ID: row.ID, Kind: row.Kind, Type: row.SourceType, PaidOn: row.PaidOn,
			Currency: row.Currency, Amount: row.AmountMinor}
	}
	percents, err := q.GetTaxPercents(ctx)
	if err != nil {
		internalError(w, "load tax settings")
		return incomeView{}, false
	}
	taxPercent := taxPercentMap(percents)
	result, err := income.Compute(label, items, income.NewTable(table, complete), taxPercent)
	if err != nil {
		internalError(w, "convert income")
		return incomeView{}, false
	}

	view := incomeView{Configured: configured, Year: year, Years: years, TaxPercent: taxPercent,
		Items: make([]incomeItemView, len(rows)), Equal: result.Equal, Months: result.Months,
		Sync: incomeSyncView{StartedOn: nullText(state.StartedOn), DividendsSyncedAt: nullText(state.DividendsSyncedAt),
			InterestSyncedAt: nullText(state.InterestSyncedAt), HistoryAttemptAt: nullText(state.HistoryAttemptAt),
			HistoryError: nullText(state.HistoryError), CashbackThrough: nullText(state.CashbackThrough),
			CashbackAttemptAt: nullText(state.CashbackAttemptAt), CashbackError: nullText(state.CashbackError),
			CashbackPending: state.ReportFrom.Valid, RatesCheckedAt: nullText(state.RatesCheckedAt),
			RatesError: nullText(state.RatesError)}}
	if result.Recommended != "" {
		view.Recommended = &result.Recommended
	}
	for index, row := range rows {
		item := incomeItemView{ID: row.ID, Kind: row.Kind, Type: row.SourceType, PaidAt: row.PaidAt, PaidOn: row.PaidOn,
			Currency: row.Currency, AmountMinor: row.AmountMinor, ReceivedCurrency: row.ReceivedCurrency,
			ReceivedMinor: row.ReceivedMinor, Ticker: nullText(row.Ticker), ISIN: nullText(row.Isin), Name: nullText(row.Name),
			Quantity: nullText(row.Quantity), PerShare: nullText(row.PerShare), Taxable: items[index].Taxable(),
			InformativeRate: nullText(row.InformativeRate), InformativeRateDate: nullText(row.InformativeRateDate),
			Conversions: make(map[string]incomeConversionView, len(income.Methods))}
		if row.InformativeEurCents.Valid {
			item.InformativeEURCents = &row.InformativeEurCents.Int64
		}
		for method, conversion := range result.Conversions[index] {
			converted := incomeConversionView{Provisional: conversion.Provisional}
			if !conversion.Missing() {
				converted.Rate, converted.Period, converted.EURCents = &conversion.Rate, &conversion.Period, &conversion.EURCents
			}
			item.Conversions[method] = converted
		}
		view.Items[index] = item
	}
	for _, total := range result.Totals {
		view.Totals = append(view.Totals, incomeTotalsView{Method: total.Method, Bases: total.Bases, Taxes: total.Taxes,
			BaseCents: total.Base, TaxCents: total.Tax, Missing: total.Missing, Provisional: total.Provisional})
	}
	return view, true
}

// incomeMethodNames heads the method columns of the CSV export.
var incomeMethodNames = map[string]string{income.MethodMonthly: "Monthly average", income.MethodDaily: "Rate on the day",
	income.MethodAnnual: "Annual average", income.MethodIncomeMonths: "Average of income months"}

var tickerSuffix = regexp.MustCompile(`_[A-Z]{2}_EQ$|_EQ$`)

// incomeTypeLabel names a payment, with the ticker of a dividend.
func incomeTypeLabel(item incomeItemView) string {
	label := item.Type
	switch {
	case item.Kind == income.KindDividend && item.Type == "ORDINARY":
		label = "Dividend"
	case item.Type == "INTEREST_ON_FREE_CASH":
		label = "Interest on cash"
	case item.Type == "LENDING_INTEREST":
		label = "Share lending interest"
	case item.Type == strings.ToUpper(item.Type):
		words := strings.ReplaceAll(strings.ToLower(item.Type), "_", " ")
		label = strings.ToUpper(words[:1]) + words[1:]
	}
	if item.Kind == income.KindDividend && item.Ticker != nil {
		label += " " + tickerSuffix.ReplaceAllString(*item.Ticker, "")
	}
	return label
}

// csvText keeps a spreadsheet from reading text as a formula.
func csvText(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

// exportIncome downloads the payments of a tax year as CSV: each amount in
// the currency it was issued in, the informative EUR value, and the EUR
// value under each conversion method. Values without a rate are empty.
func (a *api) exportIncome(w http.ResponseWriter, r *http.Request, _ *session) {
	view, ok := a.loadIncome(w, r)
	if !ok {
		return
	}
	// The view already converted every amount, so the currencies are known.
	decimal := func(minor int64, currency string) string {
		money, _ := ledger.NewMoney(minor, currency)
		text, _ := money.Decimal()
		return text
	}
	cents := func(value *int64) string {
		if value == nil {
			return ""
		}
		return decimal(*value, "EUR")
	}
	var buffer bytes.Buffer
	out := csv.NewWriter(&buffer)
	header := []string{"Date", "Type", "Amount", "Currency", "Informative EUR"}
	for _, method := range income.Methods {
		header = append(header, incomeMethodNames[method]+" EUR")
	}
	_ = out.Write(header)
	for _, item := range view.Items {
		row := []string{item.PaidOn, csvText(incomeTypeLabel(item)), decimal(item.AmountMinor, item.Currency), item.Currency,
			cents(item.InformativeEURCents)}
		for _, method := range income.Methods {
			row = append(row, cents(item.Conversions[method].EURCents))
		}
		_ = out.Write(row)
	}
	out.Flush()
	if err := out.Error(); err != nil {
		internalError(w, "write income CSV")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="investment-income-%d.csv"`, view.Year))
	_, _ = w.Write(buffer.Bytes())
}
