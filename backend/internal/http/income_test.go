package httpserver

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
)

func (app *testApp) income(source, kind, sourceType, paidOn, currency string, amount int64) int64 {
	app.t.Helper()
	id, err := app.store.Queries.SaveIncome(context.Background(), dbgen.SaveIncomeParams{SourceID: source, Kind: kind,
		PaidAt: paidOn + "T10:00:00Z", PaidOn: paidOn, Currency: currency, AmountMinor: amount, SourceType: sourceType,
		ReceivedCurrency: "EUR", ReceivedMinor: amount, ImportedAt: paidOn + "T10:00:00Z"})
	if err != nil {
		app.t.Fatal(err)
	}
	return id
}

func TestIncome(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	cookie := app.setup()
	view := decode[incomeView](t, app.expect(http.StatusOK, "GET", "/api/investing/income", nil, cookie))
	if view.Configured || view.Year != 2026 || len(view.Years) != 1 || len(view.Items) != 0 || len(view.Totals) != 4 ||
		!view.Equal || view.TaxPercent["dividend"] != 7 {
		t.Fatalf("empty view %+v", view)
	}
	for _, year := range []string{"1999", "2027", "last"} {
		app.expect(http.StatusUnprocessableEntity, "GET", "/api/investing/income?year="+year, nil, cookie)
	}

	ctx, q := context.Background(), app.store.Queries
	dividend := app.income("dividend:d-1", "dividend", "ORDINARY", "2026-01-02", "USD", 1000)
	app.income("dividend:d-2", "dividend", "RETURN_OF_CAPITAL", "2026-01-02", "USD", 10000)
	app.income("cashback:c-1", "cashback", "Spending cashback", "2026-03-01", "EUR", 27)
	app.income("transaction:t-1", "interest", "INTEREST_ON_FREE_CASH", "2025-12-31", "EUR", 5)
	if err := q.SetIncomeInformative(ctx, dbgen.SetIncomeInformativeParams{InformativeEurCents: sql.NullInt64{Int64: 862, Valid: true},
		InformativeRate: nullString("1.16"), InformativeRateDate: nullString("2026-10-01"), ID: dividend}); err != nil {
		t.Fatal(err)
	}
	for period, rate := range map[string]string{"2025-12-31": "1.17", "2026-01-02": "1.25", "2026-01": "1.2000", "2026": "1.1500"} {
		if err := q.SaveNBSRate(ctx, dbgen.SaveNBSRateParams{Currency: "USD", Period: period, Rate: rate, FetchedAt: "2027-01-05T10:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.SaveNBSFile(ctx, dbgen.SaveNBSFileParams{Period: "2026-01", FetchedAt: "2026-02-01T10:00:00Z", Complete: 1}); err != nil {
		t.Fatal(err)
	}

	view = decode[incomeView](t, app.expect(http.StatusOK, "GET", "/api/investing/income?year=2026", nil, cookie))
	if len(view.Years) != 2 || view.Years[0] != 2026 || view.Years[1] != 2025 || len(view.Items) != 3 {
		t.Fatalf("view %+v", view)
	}
	first := view.Items[0]
	if first.ID != dividend || !first.Taxable || first.Currency != "USD" || first.AmountMinor != 1000 ||
		*first.InformativeEURCents != 862 || *first.InformativeRate != "1.16" {
		t.Fatalf("dividend %+v", first)
	}
	for method, want := range map[string]int64{"monthly": 833, "daily": 800, "annual": 870, "income_months": 833} {
		if got := first.Conversions[method]; got.EURCents == nil || *got.EURCents != want {
			t.Errorf("%s: %+v", method, got)
		}
	}
	if daily := first.Conversions["daily"]; *daily.Rate != "1.25" || *daily.Period != "2026-01-02" || daily.Provisional {
		t.Errorf("daily %+v", daily)
	}
	if returned := view.Items[1]; returned.Taxable || returned.InformativeEURCents != nil {
		t.Errorf("return of capital %+v", returned)
	}
	// 7 % of the dividend plus 19 % of 0.27 EUR of cashback.
	for _, total := range view.Totals {
		want := map[string]int64{"monthly": 63, "daily": 61, "annual": 66, "income_months": 63}[total.Method]
		if total.TaxCents != want || total.Missing != 0 || total.Taxes["cashback"] != 5 {
			t.Errorf("%s: %+v", total.Method, total)
		}
	}
	if view.Recommended == nil || *view.Recommended != "daily" || view.Equal || len(view.Months["USD"]) != 1 {
		t.Fatalf("recommended %v equal %v months %v", view.Recommended, view.Equal, view.Months)
	}

	export := app.expect(http.StatusOK, "GET", "/api/investing/income.csv?year=2026", nil, cookie)
	want := "Date,Type,Amount,Currency,Informative EUR,Monthly average EUR,Rate on the day EUR,Annual average EUR,Average of income months EUR\n" +
		"2026-01-02,Dividend,10.00,USD,8.62,8.33,8.00,8.70,8.33\n" +
		"2026-01-02,Return of capital,100.00,USD,,83.33,80.00,86.96,83.33\n" +
		"2026-03-01,Spending cashback,0.27,EUR,,0.27,0.27,0.27,0.27\n"
	if got := export.Body.String(); got != want || export.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		export.Header().Get("Content-Disposition") != `attachment; filename="investment-income-2026.csv"` {
		t.Fatalf("export %v\n%s", export.Header(), got)
	}
	app.expect(http.StatusUnprocessableEntity, "GET", "/api/investing/income.csv?year=2027", nil, cookie)
	if csvText("=HYPERLINK()") != "'=HYPERLINK()" || csvText("Spending cashback") != "Spending cashback" {
		t.Fatal("formula guard")
	}

	view = decode[incomeView](t, app.expect(http.StatusOK, "GET", "/api/investing/income?year=2025", nil, cookie))
	if view.Year != 2025 || len(view.Items) != 1 || view.Items[0].Kind != "interest" || *view.Recommended != "monthly" || !view.Equal {
		t.Fatalf("2025 %+v", view)
	}
}

func TestTaxSettings(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	cookie := app.setup()
	defaults := decode[map[string]int64](t, app.expect(http.StatusOK, "GET", "/api/settings/tax", nil, cookie))
	if defaults["dividend"] != 7 || defaults["interest"] != 19 || defaults["cashback"] != 19 {
		t.Fatalf("defaults %+v", defaults)
	}
	for _, body := range []map[string]any{{"dividend": 101, "interest": 19, "cashback": 19},
		{"dividend": -1, "interest": 19, "cashback": 19}, {"dividend": 10, "interest": 19}} {
		app.expect(http.StatusUnprocessableEntity, "PUT", "/api/settings/tax", body, cookie)
	}
	app.expect(http.StatusBadRequest, "PUT", "/api/settings/tax", map[string]any{"dividend": 10.5, "interest": 19, "cashback": 19}, cookie)
	saved := decode[map[string]int64](t, app.expect(http.StatusOK, "PUT", "/api/settings/tax",
		map[string]any{"dividend": 10, "interest": 0, "cashback": 25}, cookie))
	if saved["dividend"] != 10 || saved["interest"] != 0 || saved["cashback"] != 25 {
		t.Fatalf("saved %+v", saved)
	}

	app.income("dividend:d-1", "dividend", "ORDINARY", "2026-01-02", "EUR", 1000)
	app.income("transaction:t-1", "interest", "INTEREST_ON_FREE_CASH", "2026-02-02", "EUR", 500)
	app.income("cashback:c-1", "cashback", "Spending cashback", "2026-03-01", "EUR", 200)
	view := decode[incomeView](t, app.expect(http.StatusOK, "GET", "/api/investing/income?year=2026", nil, cookie))
	if view.TaxPercent["dividend"] != 10 || view.TaxPercent["cashback"] != 25 {
		t.Fatalf("tax percent %+v", view.TaxPercent)
	}
	for _, total := range view.Totals {
		if total.Taxes["dividend"] != 100 || total.Taxes["interest"] != 0 || total.Taxes["cashback"] != 50 || total.TaxCents != 150 {
			t.Fatalf("totals %+v", total)
		}
	}
}
