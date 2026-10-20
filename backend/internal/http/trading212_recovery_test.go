package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/rates"
)

func TestTrading212RevalidationRepairsLegacyMapping(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	file := header +
		"Spending cashback,2026-08-03 09:00:00,,cashback,0.10,EUR,,\n" +
		"Deposit,2026-08-03 10:00:00,,deposit,20.00,EUR,,\n" +
		"Card debit,2026-08-03 11:00:00,,debit,-2.00,EUR,Store,RETAIL_STORES\n" +
		"Card credit,2026-08-03 12:00:00,,credit,1.00,EUR,Store,RETAIL_STORES\n"
	staged := app.imported(cookie, account, "legacy.csv", []byte(file))
	legacy := *staged.Mapping
	legacy.Actions = []imports.ActionRule{{Value: "  CARD   debit ", Kind: "transfer"}}
	legacy.PayeeColumn = nil
	if _, err := app.store.DB.Exec("UPDATE import_batches SET mapping_snapshot_json = ? WHERE id = ?", legacy.Canonical(), staged.ID); err != nil {
		t.Fatal(err)
	}
	ready := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/validate", staged.ID), nil, cookie))
	if ready.ErrorCount != 0 || ready.ExcludedCount != 1 || ready.RowCounts[imports.RowValid] != 3 {
		t.Fatalf("legacy mapping must recover all known actions: errors=%d excluded=%d valid=%d unknown=%+v", ready.ErrorCount, ready.ExcludedCount, ready.RowCounts[imports.RowValid], ready.UnknownActions)
	}
	if ready.Mapping.PayeeColumn != nil || ready.Mapping.Actions[0] != legacy.Actions[0] {
		t.Fatalf("explicit choices changed: %+v", ready.Mapping)
	}
	rows := app.stagedRows(cookie, staged.ID, "valid")
	if rows[0].Record.Kind != "transfer" || rows[1].Record.Kind != "transfer" || rows[2].Record.Kind != "refund" {
		t.Fatalf("completed action kinds: %+v", rows)
	}
	version := *ready.MappingVersion
	again := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/validate", staged.ID), nil, cookie))
	if *again.MappingVersion != version {
		t.Fatal("revalidation created another identical mapping version")
	}
	batch, err := app.store.Queries.GetImportBatch(context.Background(), staged.ID)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := app.store.Queries.LatestMapping(context.Background(), dbgen.LatestMappingParams{Source: imports.Source(account), FormatFingerprint: batch.FormatFingerprint.String})
	if err != nil || !strings.Contains(cached.MappingJson, "Spending cashback") {
		t.Fatalf("recovered mapping was not cached: %s %v", cached.MappingJson, err)
	}
}

func TestTrading212RoundedZeroConversionRemainsEditable(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	app.saveRate(localTime("2026-08-03", 19, 0), "2026-08-03", "HUF", "400")
	staged := app.imported(cookie, account, "small.csv", []byte(header+
		"Card debit,2026-08-03 10:00:00,,small-huf,-1.00,HUF,Store,RETAIL_STORES\n"))
	if staged.ErrorCount != 0 {
		t.Fatalf("small conversion invalid: %+v", staged)
	}
	app.commit(cookie, staged.ID)
	transaction := app.batchTransactions(cookie, staged.ID)[0]
	if transaction.Amount != "0.00" || transaction.Currency != "EUR" {
		t.Fatalf("rounded amount: %+v", transaction)
	}
	target := fmt.Sprintf("/api/transactions/%d", transaction.ID)
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", target,
		editInput(transaction, map[string]any{"notes": "reviewed small payment"}), cookie)).Transaction
	if edited.Amount != "0.00" || edited.Notes != "reviewed small payment" {
		t.Fatalf("rounded payment metadata edit: %+v", edited)
	}
	response := app.request("PATCH", target, editInput(edited, map[string]any{"kind": "income"}), cookie)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "HUF") {
		t.Fatalf("rounded payment reversed source sign: %d %s", response.Code, response.Body)
	}
	app.expect(http.StatusUnprocessableEntity, "POST", "/api/transactions",
		editInput(edited, map[string]any{"amount": "0.00"}), cookie)
}

func TestTrading212MixedImportConvertsToAccountEUR(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("date") != "2026-08-08" || r.URL.Query().Get("base") != "EUR" || r.URL.Query().Get("quotes") != "USD" {
			t.Errorf("historical rate request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"date":"2026-08-07","base":"EUR","quote":"USD","rate":1.25}]`)
	}))
	defer server.Close()
	app := newTestApp(t, Options{Now: func() time.Time { return localTime("2026-10-20", 19, 0) }})
	pipeline := imports.New(app.store)
	pipeline.Now = app.options.Now
	pipeline.Rates = &rates.Client{URL: server.URL, HTTP: server.Client()}
	app.options.Imports = pipeline
	var err error
	app.handler, err = New(app.store, filepath.Join(app.directory, "web"), app.options)
	if err != nil {
		t.Fatal(err)
	}
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	var file strings.Builder
	file.WriteString(header)
	for index := range 46 {
		action, amount, currency := "Spending cashback", "0.10", "USD"
		switch {
		case index >= 45:
			action, amount, currency = "Card credit", "5.00", "EUR"
		case index >= 27:
			action, amount, currency = "Card debit", "-1.00", "EUR"
		case index >= 23:
			action, amount = "Card debit", "-12.50"
		case index >= 21:
			action, amount, currency = "Deposit", "100.00", "EUR"
		}
		fmt.Fprintf(&file, "%s,2026-08-08 10:00:00,,mixed-%d,%s,%s,Store,RETAIL_STORES\n", action, index, amount, currency)
	}
	staged := app.imported(cookie, account, "mixed.csv", []byte(file.String()))
	if staged.ErrorCount != 0 || staged.ExcludedCount != 21 || staged.RowCounts[imports.RowValid] != 25 || len(staged.UnknownActions) != 0 {
		t.Fatalf("mixed file counts: %+v", staged)
	}
	if requests.Load() != 1 {
		t.Fatalf("historical requests for one date and currency: %d", requests.Load())
	}
	excluded := app.stagedRows(cookie, staged.ID, "excluded")
	if len(excluded) != 21 || len(excluded[0].Errors) != 0 {
		t.Fatalf("cashback exclusions: %+v", excluded)
	}
	valid := app.stagedRows(cookie, staged.ID, "valid")
	if record := valid[2].Record; record.Currency != "USD" || record.AmountMinor != -1250 || record.Conversion == nil || record.Conversion.EURCents != -1000 || record.Conversion.RateDate != "2026-08-07" {
		t.Fatalf("source record and historical conversion: %+v", record)
	}
	committed := app.commit(cookie, staged.ID)
	if committed.InsertedCount != 25 || requests.Load() != 1 {
		t.Fatalf("commit downloaded rates or lost rows: inserted=%d requests=%d", committed.InsertedCount, requests.Load())
	}
	foreign, err := app.store.Queries.FindTransactionBySourceID(context.Background(), dbgen.FindTransactionBySourceIDParams{SourceAccountID: nullInt(account), ExternalID: nullString("mixed-23")})
	if err != nil {
		t.Fatal(err)
	}
	detail := app.detail(cookie, foreign.ID)
	if detail.Currency != "EUR" || detail.Amount != "-10.00" || detail.EURCents != -1000 || detail.Original == nil || detail.Original.Amount != "-12.50" || detail.Original.Currency != "USD" || detail.Conversion == nil || detail.Conversion.OriginalCurrency != "USD" || detail.Conversion.OriginalAmount != "-12.50" || detail.Conversion.Rate != "1.25" {
		t.Fatalf("EUR ledger with USD source metadata: %+v", detail)
	}
	target := fmt.Sprintf("/api/transactions/%d", detail.ID)
	response := app.request("PATCH", target, editInput(detail.transactionView, map[string]any{"amount": "9.00"}), cookie)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "USD") {
		t.Fatalf("converted amount lock must name source currency: %d %s", response.Code, response.Body)
	}
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", target, editInput(detail.transactionView, map[string]any{"notes": "reviewed"}), cookie)).Transaction
	if edited.Amount != "-10.00" || edited.Currency != "EUR" || edited.Notes != "reviewed" {
		t.Fatalf("converted edit: %+v", edited)
	}
	accounts := decode[struct {
		Accounts []accountView `json:"accounts"`
	}](t, app.expect(http.StatusOK, "GET", "/api/accounts", nil, cookie)).Accounts
	if len(accounts) != 1 || accounts[0].Balance != "147.00" || accounts[0].BalanceEURCents != 14700 {
		t.Fatalf("EUR account balance: %+v", accounts)
	}
	if _, err := app.store.DB.Exec("DELETE FROM exchange_rates"); err != nil {
		t.Fatal(err)
	}
	again := app.imported(cookie, account, "mixed-again.csv", []byte(file.String()))
	if again.ErrorCount != 0 || again.DuplicateCount != 25 || again.ExcludedCount != 21 || requests.Load() != 1 {
		t.Fatalf("reimport must use source IDs without rates: %+v requests=%d", again, requests.Load())
	}
	if result := app.commit(cookie, again.ID); result.InsertedCount != 0 || requests.Load() != 1 {
		t.Fatalf("duplicate commit: %+v requests=%d", result, requests.Load())
	}
}

func TestTrading212HistoricalRateFailureCanBeRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "private provider response")
			return
		}
		fmt.Fprint(w, `[{"date":"2026-08-03","base":"EUR","quote":"USD","rate":1.25}]`)
	}))
	defer server.Close()
	app := newTestApp(t, Options{})
	pipeline := imports.New(app.store)
	pipeline.Rates = &rates.Client{URL: server.URL, HTTP: server.Client()}
	app.options.Imports = pipeline
	var err error
	app.handler, err = New(app.store, filepath.Join(app.directory, "web"), app.options)
	if err != nil {
		t.Fatal(err)
	}
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	file := header +
		"Card debit,2026-08-03 10:00:00,,retry-1,-12.50,USD,Store,RETAIL_STORES\n" +
		"Card debit,2026-08-03 11:00:00,,retry-2,-12.50,USD,Store,RETAIL_STORES\n"
	staged := app.imported(cookie, account, "retry.csv", []byte(file))
	invalid := app.stagedRows(cookie, staged.ID, "invalid")
	if staged.ErrorCount != 2 || requests.Load() != 1 || len(invalid) != 2 || !strings.Contains(invalid[0].Errors[0], "Validate again to retry") || strings.Contains(invalid[0].Errors[0], "private provider") {
		t.Fatalf("safe failure shared by matching rows: %+v %+v requests=%d", staged, invalid, requests.Load())
	}
	ready := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/validate", staged.ID), nil, cookie))
	if ready.ErrorCount != 0 || ready.RowCounts[imports.RowValid] != 2 || requests.Load() != 2 {
		t.Fatalf("retry: %+v requests=%d", ready, requests.Load())
	}
	if result := app.commit(cookie, staged.ID); result.InsertedCount != 2 || requests.Load() != 2 {
		t.Fatalf("commit after retry: %+v requests=%d", result, requests.Load())
	}
}

func TestEURConversionIntegrityRejectsMismatchedLedgerAmounts(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Euros")
	row := app.transaction(cookie, map[string]any{"accountId": account, "date": "2026-08-03", "kind": "expense", "amount": "10.00"})
	for _, cents := range []int64{-999, -1001} {
		err := app.store.Queries.CreateTransactionConversion(context.Background(), dbgen.CreateTransactionConversionParams{
			TransactionID: row.ID, OriginalAmountMinor: -1250, OriginalCurrency: "USD", OriginalMinorUnit: 2,
			EurCents: cents, Rate: "1.25", RateDate: "2026-08-03", ProviderPolicy: rates.HistoricalProviderPolicy, RoundingRule: "half-away-from-zero",
		})
		if err == nil || !strings.Contains(err.Error(), "conversion details must match") {
			t.Fatalf("mismatched conversion accepted for %d: %v", cents, err)
		}
	}
}
