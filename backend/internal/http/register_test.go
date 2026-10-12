package httpserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
)

// expect sends a request and fails the test unless the status matches.
func (app *testApp) expect(status int, method, target string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	app.t.Helper()
	response := app.request(method, target, body, cookie)
	if response.Code != status {
		app.t.Fatalf("%s %s: got %d, want %d: %s", method, target, response.Code, status, response.Body)
	}
	return response
}

func (app *testApp) account(cookie *http.Cookie, name string) int64 {
	app.t.Helper()
	return decode[accountView](app.t, app.expect(http.StatusCreated, "POST", "/api/accounts", map[string]string{"name": name, "currency": "EUR"}, cookie)).ID
}

func (app *testApp) category(cookie *http.Cookie, name string) int64 {
	app.t.Helper()
	response := app.request("POST", "/api/categories", map[string]string{"name": name}, cookie)
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		app.t.Fatalf("category %q: %d %s", name, response.Code, response.Body)
	}
	return decode[entityView](app.t, response).ID
}

func (app *testApp) transaction(cookie *http.Cookie, entry map[string]any) transactionView {
	app.t.Helper()
	return decode[transactionView](app.t, app.expect(http.StatusCreated, "POST", "/api/transactions", entry, cookie))
}

func (app *testApp) detail(cookie *http.Cookie, id int64) transactionDetail {
	app.t.Helper()
	return decode[transactionDetail](app.t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/transactions/%d", id), nil, cookie))
}

func (app *testApp) stats(cookie *http.Cookie, query string) statisticsView {
	app.t.Helper()
	return decode[statisticsView](app.t, app.expect(http.StatusOK, "GET", "/api/statistics"+query, nil, cookie))
}

func categorySpending(stats statisticsView, id int64) int64 {
	for _, total := range stats.Categories {
		if total.CategoryID != nil && *total.CategoryID == id {
			return total.SpendingEURCents
		}
	}
	return 0
}

// edit resubmits a transaction's complete state with the given changes.
func editInput(view transactionView, changes map[string]any) map[string]any {
	amount := view.Amount
	if amount[0] == '-' {
		amount = amount[1:]
	}
	input := map[string]any{
		"revision": view.Revision, "accountId": view.AccountID, "date": view.Date, "kind": view.Kind,
		"amount": amount, "payee": view.PayeeName, "categoryId": view.CategoryID, "refundOf": view.RefundOf, "notes": view.Notes,
	}
	for key, value := range changes {
		input[key] = value
	}
	return input
}

func TestTransfersRefundsAndSplits(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	checking, savings := app.account(cookie, "Checking"), app.account(cookie, "Savings")
	groceries, household, food := app.category(cookie, "Groceries"), app.category(cookie, "Household"), app.category(cookie, "Food")

	expense := app.transaction(cookie, map[string]any{"accountId": checking, "date": "2026-09-01", "kind": "expense", "amount": "100", "payee": "Grocer", "categoryId": groceries})
	app.transaction(cookie, map[string]any{"accountId": checking, "date": "2026-09-02", "kind": "income", "amount": "1000", "payee": "Employer"})
	transfer := app.transaction(cookie, map[string]any{"accountId": checking, "toAccountId": savings, "date": "2026-09-03", "kind": "transfer", "amount": "200"})
	if transfer.Amount != "-200.00" || transfer.TransferAccountName != "Savings" || transfer.CategoryID != nil {
		t.Fatalf("outgoing transfer leg: %+v", transfer)
	}
	stats := app.stats(cookie, "")
	if stats.IncomeEURCents != 100000 || stats.ExpenseEURCents != 10000 || stats.TransferCount != 2 || stats.NetEURCents != 90000 {
		t.Fatalf("transfers must not count as income or spending: %+v", stats)
	}

	// A linked refund reduces the original expense's category, whatever
	// category the request names.
	refund := app.transaction(cookie, map[string]any{"accountId": checking, "date": "2026-09-05", "kind": "refund", "amount": "30", "refundOf": expense.ID})
	if refund.CategoryID == nil || *refund.CategoryID != groceries || refund.Amount != "30.00" {
		t.Fatalf("linked refund category: %+v", refund)
	}
	for _, invalid := range []map[string]any{
		{"accountId": checking, "date": "2026-09-05", "kind": "refund", "amount": "80", "refundOf": expense.ID},
		{"accountId": checking, "date": "2026-09-05", "kind": "refund", "amount": "5", "refundOf": transfer.ID},
		{"accountId": checking, "date": "2026-09-05", "kind": "refund", "amount": "5", "refundOf": expense.ID, "categoryId": household},
		{"accountId": checking, "date": "2026-09-05", "kind": "expense", "amount": "5", "refundOf": expense.ID},
	} {
		app.expect(http.StatusUnprocessableEntity, "POST", "/api/transactions", invalid, cookie)
	}
	stats = app.stats(cookie, "")
	if stats.RefundEURCents != 3000 || stats.SpendingEURCents != 7000 || categorySpending(stats, groceries) != 7000 {
		t.Fatalf("refund totals: %+v", stats)
	}

	// Split amounts must equal the parent amount.
	mismatch := map[string]any{"accountId": checking, "date": "2026-09-06", "kind": "expense", "amount": "50", "payee": "Market",
		"splits": []map[string]any{{"categoryId": groceries, "amount": "20"}, {"categoryId": household, "amount": "20"}}}
	if fields := decode[errorBody](t, app.expect(http.StatusUnprocessableEntity, "POST", "/api/transactions", mismatch, cookie)).Fields; fields["splits"] == "" {
		t.Fatalf("split mismatch fields: %v", fields)
	}
	mismatch["splits"] = []map[string]any{{"categoryId": groceries, "amount": "20"}, {"categoryId": household, "amount": "30"}}
	split := app.transaction(cookie, mismatch)
	if split.SplitCount != 2 || split.CategoryID != nil {
		t.Fatalf("split parent: %+v", split)
	}
	if detail := app.detail(cookie, split.ID); len(detail.Splits) != 2 || detail.Splits[0].EURCents+detail.Splits[1].EURCents != -5000 {
		t.Fatalf("split parts: %+v", detail.Splits)
	}
	app.expect(http.StatusUnprocessableEntity, "POST", "/api/transactions", map[string]any{"accountId": checking, "toAccountId": savings, "date": "2026-09-06", "kind": "transfer", "amount": "50",
		"splits": []map[string]any{{"categoryId": groceries, "amount": "20"}, {"categoryId": household, "amount": "30"}}}, cookie)
	stats = app.stats(cookie, "")
	if categorySpending(stats, groceries) != 9000 || categorySpending(stats, household) != 3000 || stats.SpendingEURCents != 12000 {
		t.Fatalf("split statistics: %+v", stats)
	}
	if filtered := decode[registerPage](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/transactions?category=%d", household), nil, cookie)); filtered.Total != 1 {
		t.Fatalf("category filter must match split parts: %+v", filtered)
	}

	// Recategorizing the expense moves its linked refund along.
	updated := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", expense.ID), editInput(expense, map[string]any{"categoryId": food}), cookie))
	if *updated.Transaction.CategoryID != food || updated.Transaction.Revision != expense.Revision+1 {
		t.Fatalf("recategorized expense: %+v", updated.Transaction)
	}
	if detail := app.detail(cookie, refund.ID); *detail.CategoryID != food || detail.RefundOfView == nil || detail.RefundOfView.ID != expense.ID {
		t.Fatalf("refund did not follow the expense: %+v", detail)
	}
	if detail := app.detail(cookie, expense.ID); len(detail.Refunds) != 1 || detail.RefundedTotal != "30.00" {
		t.Fatalf("expense refunds: %+v", detail)
	}
	app.expect(http.StatusUnprocessableEntity, "PATCH", fmt.Sprintf("/api/transactions/%d", expense.ID), editInput(updated.Transaction, map[string]any{"amount": "20"}), cookie)
	app.expect(http.StatusUnprocessableEntity, "PATCH", fmt.Sprintf("/api/transactions/%d", expense.ID), editInput(updated.Transaction, map[string]any{"kind": "income"}), cookie)
	app.expect(http.StatusConflict, "DELETE", fmt.Sprintf("/api/transactions/%d?revision=%d", expense.ID, updated.Transaction.Revision), nil, cookie)
	app.expect(http.StatusConflict, "PATCH", fmt.Sprintf("/api/transactions/%d", expense.ID), editInput(expense, map[string]any{"notes": "stale"}), cookie)

	// Editing one transfer leg updates both; deleting removes both.
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", transfer.ID), editInput(transfer, map[string]any{"amount": "250", "notes": "Top up"}), cookie)).Transaction
	peer := app.detail(cookie, transfer.ID).TransferPeer
	if edited.Amount != "-250.00" || peer == nil || peer.Amount != "250.00" || peer.Notes != "Top up" || peer.AccountID != savings {
		t.Fatalf("transfer legs after edit: %+v %+v", edited, peer)
	}
	app.expect(http.StatusUnprocessableEntity, "PATCH", fmt.Sprintf("/api/transactions/%d", transfer.ID), editInput(edited, map[string]any{"kind": "expense"}), cookie)
	app.expect(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/transactions/%d?revision=%d", transfer.ID, edited.Revision), nil, cookie)
	if stats := app.stats(cookie, ""); stats.TransferCount != 0 {
		t.Fatalf("transfer legs remain: %+v", stats)
	}
	accounts := decode[map[string][]accountView](t, app.request("GET", "/api/accounts", nil, cookie))["accounts"]
	for _, account := range accounts {
		if account.ID == savings && account.TransactionCount != 0 {
			t.Fatalf("deleted incoming leg still counted: %+v", account)
		}
	}
}

func TestEquivalentNamesCreateEntitiesOnce(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	wallet := app.account(cookie, "Wallet")
	retail := app.expect(http.StatusCreated, "POST", "/api/categories", map[string]string{"name": "RETAIL_STORES"}, cookie)
	again := app.expect(http.StatusOK, "POST", "/api/categories", map[string]string{"name": "  retail stores "}, cookie)
	if decode[entityView](t, retail).ID != decode[entityView](t, again).ID {
		t.Fatal("equivalent category names created two categories")
	}
	first := app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-01", "kind": "expense", "amount": "1", "payee": "Tesco Stores"})
	second := app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-02", "kind": "expense", "amount": "2", "payee": "  TESCO   stores"})
	if first.PayeeID == nil || second.PayeeID == nil || *first.PayeeID != *second.PayeeID || second.PayeeName != "Tesco Stores" {
		t.Fatalf("equivalent payees: %+v %+v", first, second)
	}

	// Renaming keeps the old name as an alias.
	retailID := decode[entityView](t, retail).ID
	app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/categories/%d", retailID), map[string]string{"name": "Shops"}, cookie)
	if id := app.category(cookie, "Retail_Stores"); id != retailID {
		t.Fatalf("old name resolved to %d, want %d", id, retailID)
	}
	food := app.category(cookie, "Food")
	app.expect(http.StatusUnprocessableEntity, "PATCH", fmt.Sprintf("/api/categories/%d", food), map[string]string{"name": "shops"}, cookie)
	app.expect(http.StatusUnprocessableEntity, "PATCH", fmt.Sprintf("/api/categories/%d", food), map[string]string{"name": "retail stores"}, cookie)

	// Merging repoints transactions and rules and keeps the source name.
	express := app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-03", "kind": "expense", "amount": "3", "payee": "Tesco Express", "categoryId": food})
	app.expect(http.StatusOK, "POST", "/api/rules", map[string]any{"payeeId": *express.PayeeID, "categoryId": food}, cookie)
	app.expect(http.StatusNoContent, "POST", fmt.Sprintf("/api/payees/%d/merge", *express.PayeeID), map[string]int64{"targetId": *first.PayeeID}, cookie)
	if detail := app.detail(cookie, express.ID); *detail.PayeeID != *first.PayeeID || detail.Revision != express.Revision+1 {
		t.Fatalf("merged payee transaction: %+v", detail)
	}
	rules := decode[map[string][]ruleView](t, app.expect(http.StatusOK, "GET", "/api/rules", nil, cookie))["rules"]
	if len(rules) != 1 || rules[0].PayeeID != *first.PayeeID {
		t.Fatalf("rules after merge: %+v", rules)
	}
	later := app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-04", "kind": "expense", "amount": "4", "payee": "tesco express"})
	if *later.PayeeID != *first.PayeeID || *later.CategoryID != food {
		t.Fatalf("merged name must resolve to the target and its rule: %+v", later)
	}
	app.expect(http.StatusNoContent, "POST", fmt.Sprintf("/api/categories/%d/merge", food), map[string]int64{"targetId": retailID}, cookie)
	if detail := app.detail(cookie, later.ID); *detail.CategoryID != retailID {
		t.Fatalf("category merge: %+v", detail)
	}
	if id := app.category(cookie, "FOOD"); id != retailID {
		t.Fatal("merged category name must resolve to the target")
	}
	payees := decode[map[string]any](t, app.expect(http.StatusOK, "GET", "/api/payees?q=tesco", nil, cookie))
	if payees["total"].(float64) != 1 {
		t.Fatalf("payees: %v", payees)
	}

	var uncategorized int64
	for _, category := range decode[map[string][]entityView](t, app.expect(http.StatusOK, "GET", "/api/categories", nil, cookie))["categories"] {
		if category.System {
			uncategorized = category.ID
		}
	}
	app.expect(http.StatusConflict, "PATCH", fmt.Sprintf("/api/categories/%d", uncategorized), map[string]string{"name": "Other"}, cookie)
	app.expect(http.StatusConflict, "POST", fmt.Sprintf("/api/categories/%d/merge", uncategorized), map[string]int64{"targetId": retailID}, cookie)
	app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/categories/%d", uncategorized), map[string]string{"name": "UNCATEGORIZED"}, cookie)
}

func TestRulesApplyOnlyToFutureAndSelectedTransactions(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	wallet, other := app.account(cookie, "Wallet"), app.account(cookie, "Other")
	coffee := app.category(cookie, "Coffee")
	var cafes []transactionView
	for day := 1; day <= 3; day++ {
		cafes = append(cafes, app.transaction(cookie, map[string]any{"accountId": wallet, "date": fmt.Sprintf("2026-09-0%d", day), "kind": "expense", "amount": "3.20", "payee": "Cafe"}))
	}
	if cafes[0].CategoryName != "Uncategorized" {
		t.Fatalf("fallback category: %+v", cafes[0])
	}
	result := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", cafes[0].ID), editInput(cafes[0], map[string]any{"categoryId": coffee}), cookie))
	suggestion := result.RuleSuggestion
	if suggestion == nil || suggestion.PayeeName != "Cafe" || suggestion.CategoryID != coffee || suggestion.OtherTransactions != 2 {
		t.Fatalf("rule suggestion after correction: %+v", suggestion)
	}
	app.expect(http.StatusOK, "POST", "/api/rules", map[string]any{"payeeId": suggestion.PayeeID, "categoryId": coffee}, cookie)
	for _, cafe := range cafes[1:] {
		if detail := app.detail(cookie, cafe.ID); detail.CategoryName != "Uncategorized" || detail.Revision != cafe.Revision {
			t.Fatalf("a new rule changed an existing transaction: %+v", detail)
		}
	}
	if future := app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-04", "kind": "expense", "amount": "3.20", "payee": "CAFE"}); future.CategoryName != "Coffee" {
		t.Fatalf("rule not applied to a future transaction: %+v", future)
	}
	// A rule can name a category that does not exist yet.
	app.expect(http.StatusOK, "POST", "/api/rules", map[string]any{"payee": "Bakery", "category": "Bread & pastry"}, cookie)
	if bakery := app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-04", "kind": "expense", "amount": "2", "payee": "bakery"}); bakery.CategoryName != "Bread & pastry" {
		t.Fatalf("explicit rule category: %+v", bakery)
	}

	// The owner applies the change to existing transactions by selecting them.
	transfer := app.transaction(cookie, map[string]any{"accountId": wallet, "toAccountId": other, "date": "2026-09-05", "kind": "transfer", "amount": "10"})
	items := []bulkItem{{cafes[1].ID, cafes[1].Revision}, {cafes[2].ID, cafes[2].Revision}}
	app.expect(http.StatusUnprocessableEntity, "POST", "/api/transactions/bulk", map[string]any{"action": "update", "categoryId": coffee, "items": append(items, bulkItem{transfer.ID, transfer.Revision})}, cookie)
	if detail := app.detail(cookie, cafes[1].ID); detail.CategoryName != "Uncategorized" {
		t.Fatal("a rejected bulk edit was partially applied")
	}
	app.expect(http.StatusConflict, "POST", "/api/transactions/bulk", map[string]any{"action": "update", "categoryId": coffee, "items": []bulkItem{{cafes[0].ID, cafes[0].Revision}}}, cookie)
	changed := decode[map[string]int](t, app.expect(http.StatusOK, "POST", "/api/transactions/bulk", map[string]any{"action": "update", "categoryId": coffee, "payee": "Corner Cafe", "items": items}, cookie))
	if changed["changed"] != 2 {
		t.Fatalf("bulk result: %v", changed)
	}
	page := decode[registerPage](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/transactions?category=%d&sort=date&dir=asc", coffee), nil, cookie))
	if page.Total != 4 || page.Transactions[1].PayeeName != "Corner Cafe" {
		t.Fatalf("bulk-edited register: %+v", page)
	}
	deleted := decode[map[string]int](t, app.expect(http.StatusOK, "POST", "/api/transactions/bulk", map[string]any{"action": "delete", "items": []bulkItem{{transfer.ID, transfer.Revision}}}, cookie))
	if deleted["changed"] != 1 || app.stats(cookie, "").TransferCount != 0 {
		t.Fatalf("bulk delete of a transfer leg must remove both legs: %v", deleted)
	}
}

// importedFixture stores one imported transaction with its original record,
// as the CSV pipeline does.
func importedFixture(t *testing.T, app *testApp, accountID int64) int64 {
	t.Helper()
	ctx := context.Background()
	batch, err := app.store.Queries.CreateImportBatch(ctx, dbgen.CreateImportBatchParams{SourceAccountID: accountID, Filename: "fixture.csv"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := app.store.Queries.CreateTransaction(ctx, dbgen.CreateTransactionParams{
		AccountID: accountID, SourceAccountID: nullInt(accountID), ExternalID: nullString("ext-1"), ImportBatchID: nullInt(batch.ID),
		OccurredAt: "2026-09-10T08:30:00Z", LocalDate: "2026-09-10", Kind: "expense", AmountMinor: -1999, Currency: "EUR",
		MinorUnit: 2, EurCents: -1999, CategoryID: nullInt(1), ReviewStatus: "needs_review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Queries.CreateImportedOriginal(ctx, dbgen.CreateImportedOriginalParams{
		TransactionID: created.ID, AmountText: "-19.99", Currency: "EUR", TimestampText: "2026-09-10 08:30:00+00:00",
		Action: "Card debit", MerchantName: "Example Store", MerchantCategory: "RETAIL_STORES", RawRecordJson: `["Card debit"]`,
	}); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func TestEditsKeepImportedOriginals(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	wallet, other := app.account(cookie, "Wallet"), app.account(cookie, "Other")
	id := importedFixture(t, app, wallet)
	view := app.detail(cookie, id)
	if !view.Imported || view.OccurredAt != "2026-09-10T08:30:00Z" {
		t.Fatalf("imported view: %+v", view)
	}
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", id),
		editInput(view.transactionView, map[string]any{"amount": "25", "payee": "Owner Name", "notes": "fixed"}), cookie)).Transaction
	if edited.Amount != "-25.00" || edited.PayeeName != "Owner Name" {
		t.Fatalf("edited import: %+v", edited)
	}
	if detail := app.detail(cookie, id); detail.OccurredAt != "2026-09-10T08:30:00Z" {
		t.Fatal("an unchanged date must keep the imported time")
	}
	app.expect(http.StatusOK, "POST", "/api/transactions/bulk", map[string]any{"action": "update", "accountId": other, "reviewed": true,
		"items": []bulkItem{{id, edited.Revision}}}, cookie)
	detail := app.detail(cookie, id)
	original := detail.Original
	if original == nil || original.Amount != "-19.99" || original.MerchantName != "Example Store" || original.Action != "Card debit" ||
		detail.AccountID != other || detail.ReviewStatus != "confirmed" || detail.EditCount != 3 {
		t.Fatalf("imported original after edits: %+v %+v", detail, original)
	}
	app.expect(http.StatusOK, "POST", "/api/transactions/bulk", map[string]any{"action": "delete", "items": []bulkItem{{id, detail.Revision}}}, cookie)
	if detail := app.request("GET", fmt.Sprintf("/api/transactions/%d", id), nil, cookie); detail.Code != http.StatusNotFound {
		t.Fatalf("deleted transaction: %d", detail.Code)
	}
	var amount string
	if err := app.store.DB.QueryRow("SELECT amount_text FROM imported_originals WHERE transaction_id = ?", id).Scan(&amount); err != nil || amount != "-19.99" {
		t.Fatalf("original lost after delete: %q %v", amount, err)
	}
}

func TestRegisterPaginatesLargeLedgerOnServer(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts 100,000 transactions")
	}
	app := newTestApp(t, Options{})
	cookie := app.setup()
	accounts := []int64{app.account(cookie, "A"), app.account(cookie, "B"), app.account(cookie, "C")}
	categories := make([]int64, 10)
	for index := range categories {
		categories[index] = app.category(cookie, fmt.Sprintf("Category %d", index))
	}
	const count = 100_000
	tx, err := app.store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	payees := make([]int64, 50)
	for index := range payees {
		if err := tx.QueryRow("INSERT INTO payees (name, normalized_name) VALUES (?, ?) RETURNING id",
			fmt.Sprintf("Payee %02d", index), fmt.Sprintf("payee %02d", index)).Scan(&payees[index]); err != nil {
			t.Fatal(err)
		}
	}
	statement, err := tx.Prepare(`INSERT INTO transactions (account_id, occurred_at, local_date, kind, amount_minor, currency,
        minor_unit, eur_cents, payee_id, category_id, notes) VALUES (?, ?, ?, ?, ?, 'EUR', 2, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := range count {
		day := start.AddDate(0, 0, index%1000)
		amount := -int64(index%9973 + 1)
		kind := "expense"
		if index%10 == 0 {
			kind, amount = "income", -amount
		}
		if _, err := statement.Exec(accounts[index%3], day.Format(time.RFC3339), day.Format(time.DateOnly), kind, amount, amount,
			payees[index%50], categories[index%10], fmt.Sprintf("note %d", index)); err != nil {
			t.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	check := func(query string, wantTotal int64) registerPage {
		t.Helper()
		began := time.Now()
		response := app.expect(http.StatusOK, "GET", "/api/transactions?"+query, nil, cookie)
		elapsed := time.Since(began)
		page := decode[registerPage](t, response)
		if wantTotal >= 0 && page.Total != wantTotal {
			t.Fatalf("%s: total %d, want %d", query, page.Total, wantTotal)
		}
		if len(page.Transactions) > page.PageSize || response.Body.Len() > 256<<10 {
			t.Fatalf("%s returned %d rows in %d bytes", query, len(page.Transactions), response.Body.Len())
		}
		t.Logf("%-70s %6d rows of %6d in %v", query, len(page.Transactions), page.Total, elapsed)
		if elapsed > 5*time.Second {
			t.Fatalf("%s took %v", query, elapsed)
		}
		return page
	}
	var want int64
	countWhere := func(where string, args ...any) int64 {
		if err := app.store.DB.QueryRow("SELECT count(*) FROM transactions WHERE "+where, args...).Scan(&want); err != nil {
			t.Fatal(err)
		}
		return want
	}
	first := check("", count)
	if len(first.Transactions) != defaultPageSize || first.Transactions[0].Date != "2025-09-26" {
		t.Fatalf("first page: %d rows, newest %s", len(first.Transactions), first.Transactions[0].Date)
	}
	if deep := check("page=1999&pageSize=50", count); len(deep.Transactions) != 50 {
		t.Fatalf("deep page: %d", len(deep.Transactions))
	}
	if last := check("page=500&pageSize=200", count); len(last.Transactions) != 200 {
		t.Fatalf("last page: %d", len(last.Transactions))
	}
	filtered := check(fmt.Sprintf("account=%d&category=%d&from=2024-01-01&to=2024-06-30&pageSize=200", accounts[1], categories[4]),
		countWhere("account_id = ? AND category_id = ? AND local_date BETWEEN '2024-01-01' AND '2024-06-30'", accounts[1], categories[4]))
	for _, row := range filtered.Transactions {
		if row.AccountID != accounts[1] || *row.CategoryID != categories[4] || row.Date < "2024-01-01" || row.Date > "2024-06-30" {
			t.Fatalf("row outside filter: %+v", row)
		}
	}
	check(fmt.Sprintf("payee=%d&kind=income", payees[10]), countWhere("payee_id = ? AND kind = 'income'", payees[10]))
	check("q=payee+07", countWhere("payee_id = ?", payees[7]))
	check("q=note+4242", countWhere("instr(notes, 'note 4242') > 0"))
	sorted := check("sort=amount&dir=asc&pageSize=5", count)
	if sorted.Transactions[0].Amount != "-99.73" {
		t.Fatalf("amount sort: %+v", sorted.Transactions[0])
	}
	check("sort=payee&dir=desc&page=100", count)
	for _, invalid := range []string{"pageSize=100000", "sort=notes", "from=2024-13-01", "account=abc", "dir=sideways"} {
		app.expect(http.StatusUnprocessableEntity, "GET", "/api/transactions?"+invalid, nil, cookie)
	}
	stats := app.stats(cookie, fmt.Sprintf("?account=%d", accounts[0]))
	var income, expense int64
	app.store.DB.QueryRow("SELECT sum(CASE WHEN kind = 'income' THEN eur_cents END), -sum(CASE WHEN kind = 'expense' THEN eur_cents END) FROM transactions WHERE account_id = ?", accounts[0]).Scan(&income, &expense)
	if stats.IncomeEURCents != income || stats.ExpenseEURCents != expense {
		t.Fatalf("filtered statistics: %+v, want %d %d", stats, income, expense)
	}

	// The dashboard returns compact aggregates, however large the ledger.
	for _, account := range []string{"", fmt.Sprint(accounts[2])} {
		query := "?month=2024-03&account=" + account
		if account == "" {
			query = "?month=2025-06"
		}
		began := time.Now()
		response := app.expect(http.StatusOK, "GET", "/api/dashboard"+query, nil, cookie)
		elapsed := time.Since(began)
		view := decode[dashboardView](t, response)
		monthly := app.stats(cookie, fmt.Sprintf("?from=%s&to=%s&account=%s", view.From, view.To, account))
		if view.IncomeEURCents != monthly.IncomeEURCents || view.SpendingEURCents != monthly.SpendingEURCents || view.SpendingEURCents == 0 {
			t.Fatalf("dashboard %s: %+v, register %+v", query, view, monthly)
		}
		t.Logf("%-70s %6d bytes in %v", "dashboard"+query, response.Body.Len(), elapsed)
		if response.Body.Len() > 16<<10 || elapsed > 5*time.Second {
			t.Fatalf("dashboard %s returned %d bytes in %v", query, response.Body.Len(), elapsed)
		}
	}
}

func nullInt(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func TestPayeeListSorts(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	wallet := app.account(cookie, "Wallet")
	for _, payee := range []string{"Bakery", "Cinema", "Cinema", "Cinema", "Arcade", "Arcade"} {
		app.transaction(cookie, map[string]any{"accountId": wallet, "date": "2026-09-01", "kind": "expense", "amount": "1", "payee": payee})
	}
	app.expect(http.StatusOK, "POST", "/api/rules", map[string]any{"payee": "Bakery", "category": "Food"}, cookie)
	app.expect(http.StatusOK, "POST", "/api/rules", map[string]any{"payee": "Cinema", "category": "Fun"}, cookie)

	names := func(query string) []string {
		page := decode[struct {
			Payees []entityView `json:"payees"`
		}](t, app.expect(http.StatusOK, "GET", "/api/payees"+query, nil, cookie))
		names := []string{}
		for _, payee := range page.Payees {
			names = append(names, payee.Name)
		}
		return names
	}
	for query, want := range map[string]string{
		"":                                     "Arcade Bakery Cinema",
		"?sort=name&dir=desc":                  "Cinema Bakery Arcade",
		"?sort=transactionCount&dir=desc":      "Cinema Arcade Bakery",
		"?sort=transactionCount&dir=asc":       "Bakery Arcade Cinema",
		"?sort=rule&dir=asc":                   "Bakery Cinema Arcade",
		"?sort=rule&dir=desc":                  "Cinema Bakery Arcade",
		"?sort=name&dir=asc&pageSize=1&page=2": "Bakery",
	} {
		if got := strings.Join(names(query), " "); got != want {
			t.Errorf("%q sorted %s, want %s", query, got, want)
		}
	}
	app.expect(http.StatusUnprocessableEntity, "GET", "/api/payees?sort=date", nil, cookie)
	app.expect(http.StatusUnprocessableEntity, "GET", "/api/payees?dir=up", nil, cookie)
}
