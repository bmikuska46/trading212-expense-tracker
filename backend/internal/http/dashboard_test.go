package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"budget-tracking/backend/internal/rates"
)

func (app *testApp) dashboard(cookie *http.Cookie, query string) dashboardView {
	app.t.Helper()
	return decode[dashboardView](app.t, app.expect(http.StatusOK, "GET", "/api/dashboard"+query, nil, cookie))
}

// sameTotals fails unless the dashboard totals equal the register summary.
func sameTotals(t *testing.T, label string, view dashboardView, stats statisticsView) {
	t.Helper()
	if view.IncomeEURCents != stats.IncomeEURCents || view.ExpenseEURCents != stats.ExpenseEURCents ||
		view.RefundEURCents != stats.RefundEURCents || view.SpendingEURCents != stats.SpendingEURCents ||
		view.NetEURCents != stats.NetEURCents || view.TransferCount != stats.TransferCount {
		t.Fatalf("%s: dashboard %+v, register %+v", label, view, stats)
	}
	var categories int64
	for _, line := range view.Categories {
		categories += line.SpendingEURCents
	}
	if categories != view.SpendingEURCents {
		t.Fatalf("%s: categories add up to %d, spending is %d", label, categories, view.SpendingEURCents)
	}
}

func TestDashboardReconcilesWithRegister(t *testing.T) {
	app := newTestApp(t, Options{Now: func() time.Time { return localTime("2026-09-30", 23, 30) }})
	if response := app.request("GET", "/api/dashboard", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard: %d", response.Code)
	}
	cookie := app.setup()
	checking, savings := app.account(cookie, "Checking"), app.account(cookie, "Savings")
	entry := func(account int64, date, kind, amount string, extra map[string]any) transactionView {
		input := map[string]any{"accountId": account, "date": date, "kind": kind, "amount": amount}
		for key, value := range extra {
			input[key] = value
		}
		return app.transaction(cookie, input)
	}

	// Ten categories with growing spending, so the smallest fold into Other.
	categories := make([]int64, 10)
	for index := range categories {
		categories[index] = app.category(cookie, fmt.Sprintf("Category %d", index))
		entry(checking, "2026-09-10", "expense", fmt.Sprintf("%d", 10*(index+1)), map[string]any{"categoryId": categories[index]})
	}
	groceries, gifts := app.category(cookie, "Groceries"), app.category(cookie, "Gifts")
	expense := entry(checking, "2026-09-02", "expense", "120", map[string]any{"payee": "Grocer", "categoryId": groceries})
	entry(checking, "2026-09-05", "refund", "20", map[string]any{"refundOf": expense.ID})
	// An unlinked refund larger than the category's spending this month.
	entry(checking, "2026-09-06", "refund", "15", map[string]any{"categoryId": gifts})
	entry(checking, "2026-09-07", "expense", "90", map[string]any{"splits": []map[string]any{
		{"categoryId": groceries, "amount": "60"}, {"categoryId": categories[0], "amount": "30"}}})
	entry(checking, "2026-09-01", "income", "2500", map[string]any{"payee": "Employer"})
	entry(checking, "2026-09-15", "transfer", "400", map[string]any{"toAccountId": savings})
	entry(savings, "2026-08-20", "income", "7.5", nil)
	entry(checking, "2026-08-03", "expense", "45.10", map[string]any{"categoryId": groceries})
	entry(checking, "2025-10-31", "income", "100", nil)
	entry(checking, "2025-09-30", "income", "999", nil)
	entry(checking, "2026-10-01", "expense", "1", nil)

	view := app.dashboard(cookie, "")
	if view.Month != "2026-09" || view.From != "2026-09-01" || view.To != "2026-09-30" || view.AccountID != nil {
		t.Fatalf("default month in the owner's timezone: %+v", view)
	}
	stats := app.stats(cookie, "?from=2026-09-01&to=2026-09-30")
	sameTotals(t, "September", view, stats)
	if view.IncomeEURCents != 250000 || view.TransferCount != 2 || view.RefundEURCents != 3500 ||
		view.SpendingEURCents != 55000+12000+9000-3500 {
		t.Fatalf("transfers excluded and refunds applied: %+v", view)
	}
	if len(view.Categories) != dashboardCategories+1 || !view.Categories[dashboardCategories].Other ||
		view.Categories[0].CategoryName != "Groceries" || view.Categories[0].SpendingEURCents != 16000 {
		t.Fatalf("category breakdown: %+v", view.Categories)
	}

	// Every trend month matches the register summary for that month.
	if len(view.Trend) != dashboardMonths || view.Trend[0].Month != "2025-10" || view.Trend[11].Month != "2026-09" {
		t.Fatalf("trend months: %+v", view.Trend)
	}
	for _, month := range view.Trend {
		first, _ := time.Parse("2006-01", month.Month)
		monthly := app.stats(cookie, fmt.Sprintf("?from=%s&to=%s", first.Format(time.DateOnly), first.AddDate(0, 1, -1).Format(time.DateOnly)))
		if month.IncomeEURCents != monthly.IncomeEURCents || month.SpendingEURCents != monthly.SpendingEURCents ||
			month.NetEURCents != monthly.NetEURCents {
			t.Fatalf("trend %s: %+v, register %+v", month.Month, month, monthly)
		}
	}
	if view.Trend[0].IncomeEURCents != 10000 || view.Trend[10].SpendingEURCents != 4510 {
		t.Fatalf("trend values: %+v", view.Trend)
	}

	// Balances run to the end of the month and keep the source currency.
	balances := map[int64]dashboardAccount{}
	for _, account := range view.Accounts {
		balances[account.ID] = account
	}
	if saved := balances[savings]; saved.Balance != "407.50" || saved.BalanceEURCents != 40750 || saved.Change != "400.00" ||
		saved.Currency != "EUR" || saved.TransactionCount != 1 {
		t.Fatalf("savings balance: %+v", saved)
	}
	if current := balances[checking]; current.Change != "1375.00" || current.Balance != "2428.90" {
		t.Fatalf("checking balance: %+v", current)
	}

	// The account filter narrows totals, trend and balances alike.
	filtered := app.dashboard(cookie, fmt.Sprintf("?month=2026-08&account=%d", savings))
	sameTotals(t, "savings in August", filtered, app.stats(cookie, fmt.Sprintf("?from=2026-08-01&to=2026-08-31&account=%d", savings)))
	if filtered.IncomeEURCents != 750 || len(filtered.Accounts) != 1 || filtered.Accounts[0].Balance != "7.50" ||
		filtered.AccountID == nil || *filtered.AccountID != savings || filtered.Trend[11].Month != "2026-08" {
		t.Fatalf("filtered dashboard: %+v", filtered)
	}

	for _, invalid := range []string{"?month=2026-13", "?month=26-09", "?account=abc", "?account=0", "?account=9999"} {
		app.expect(http.StatusUnprocessableEntity, "GET", "/api/dashboard"+invalid, nil, cookie)
	}
}

func TestDashboardSurvivesRepeatImportsAndRateCleanup(t *testing.T) {
	now := localTime("2026-08-14", 12, 0)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	refuseOutbound(t)
	cookie := app.setup()
	trading, dollars := app.account(cookie, "Trading 212"), app.foreignAccount(cookie, "Dollars", "USD")
	app.saveRate(localTime("2026-08-03", 18, 5), "2026-08-03", "USD", "1.1")
	app.saveRate(localTime("2026-08-07", 18, 5), "2026-08-07", "USD", "1.0842")
	app.saveRate(localTime("2026-08-09", 18, 5), "2026-08-07", "USD", "1.0842")
	usd := []byte(header +
		"Card debit,2026-08-03 09:14:22,,usd-1,-12.34,USD,Example Diner,RESTAURANTS\n" +
		"Card debit,2026-08-07 23:30:00,,usd-2,-100.00,USD,Sample Store,RETAIL_STORES\n")
	euros := app.commitUnreviewed(cookie, app.imported(cookie, trading, "august.csv", fixture(t)).ID)
	foreign := app.commitUnreviewed(cookie, app.imported(cookie, dollars, "usd.csv", usd).ID)

	august := app.dashboard(cookie, "?month=2026-08")
	sameTotals(t, "August", august, app.stats(cookie, "?from=2026-08-01&to=2026-08-31"))
	// Cashback rows never enter the ledger, and deposits and withdrawals are
	// transfers, so neither changes income or spending.
	if august.IncomeEURCents != 0 || august.TransferCount != 2 || august.SpendingEURCents != 1240+5499+810+2350+1122+9223 {
		t.Fatalf("imported totals: %+v", august)
	}
	if august.Review.ImportCount != 2 || len(august.Review.Imports) != 2 || august.Review.Imports[0].ID != euros.ID ||
		august.Review.Imports[1].Account.Name != "Dollars" || august.Review.NeedsReviewCount != 8 {
		t.Fatalf("imports awaiting review: %+v", august.Review)
	}
	var dollarBalance dashboardAccount
	for _, account := range august.Accounts {
		if account.ID == dollars {
			dollarBalance = account
		}
	}
	if dollarBalance.Currency != "USD" || dollarBalance.Balance != "-112.34" || dollarBalance.BalanceEURCents != -10345 {
		t.Fatalf("foreign account keeps its currency: %+v", dollarBalance)
	}
	scoped := app.dashboard(cookie, fmt.Sprintf("?month=2026-08&account=%d", dollars))
	if scoped.Review.ImportCount != 1 || scoped.Review.Imports[0].ID != foreign.ID || scoped.Review.NeedsReviewCount != 2 {
		t.Fatalf("review scoped to the account: %+v", scoped.Review)
	}

	// Importing the same files again adds nothing.
	for account, body := range map[int64][]byte{trading: fixture(t), dollars: usd} {
		if again := app.commit(cookie, app.imported(cookie, account, "again.csv", body).ID); again.InsertedCount != 0 {
			t.Fatalf("repeat import inserted rows: %+v", again)
		}
	}
	if again := app.dashboard(cookie, "?month=2026-08"); !reflect.DeepEqual(again, august) {
		t.Fatalf("repeat imports changed the dashboard: %+v, was %+v", again, august)
	}

	// After confirmation, cleanup removes rates nothing pending needs, and
	// the saved conversions keep every total.
	for _, batch := range []int64{euros.ID, foreign.ID} {
		app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/confirm", batch), nil, cookie)
	}
	now = localTime("2026-10-05", 19, 0)
	app.saveRate(now, "2026-10-05", "USD", "1.2")
	cookie = sessionCookie(t, app.expect(http.StatusOK, "POST", "/api/session",
		map[string]string{"email": ownerEmail, "password": ownerPassword}, nil))
	if deleted, err := rates.Cleanup(context.Background(), app.store, now); err != nil || deleted == 0 {
		t.Fatalf("cleanup: %d, %v", deleted, err)
	}
	after := app.dashboard(cookie, "?month=2026-08")
	if after.Review.ImportCount != 0 || after.Review.NeedsReviewCount != 0 {
		t.Fatalf("confirmed imports still await review: %+v", after.Review)
	}
	after.Review, august.Review = dashboardReview{}, dashboardReview{}
	if !reflect.DeepEqual(after, august) {
		t.Fatalf("rate cleanup changed the dashboard: %+v, was %+v", after, august)
	}
}
