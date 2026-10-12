package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/trading212-anonymized.csv")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (app *testApp) upload(cookie *http.Cookie, accountID int64, filename string, body io.Reader) *httptest.ResponseRecorder {
	app.t.Helper()
	target := fmt.Sprintf("/api/imports?accountId=%d&filename=%s", accountID, url.QueryEscape(filename))
	request := httptest.NewRequest("POST", target, body)
	request.Header.Set("Content-Type", "text/csv")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, request)
	return response
}

func (app *testApp) imported(cookie *http.Cookie, accountID int64, filename string, body []byte) importDetail {
	app.t.Helper()
	response := app.upload(cookie, accountID, filename, bytes.NewReader(body))
	if response.Code != http.StatusCreated {
		app.t.Fatalf("upload %s: %d %s", filename, response.Code, response.Body)
	}
	return decode[importDetail](app.t, response)
}

func (app *testApp) count(query string, args ...any) int64 {
	app.t.Helper()
	var count int64
	if err := app.store.DB.QueryRow(query, args...).Scan(&count); err != nil {
		app.t.Fatal(err)
	}
	return count
}

func (app *testApp) batchTransactions(cookie *http.Cookie, batch int64) []transactionView {
	app.t.Helper()
	return decode[registerPage](app.t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/transactions?batch=%d&pageSize=200", batch), nil, cookie)).Transactions
}

func byNotesOrPayee(transactions []transactionView, text string) transactionView {
	for _, transaction := range transactions {
		if transaction.PayeeName == text || transaction.Notes == text {
			return transaction
		}
	}
	return transactionView{}
}

func TestTrading212ImportAndReimport(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	uncategorized := app.count("SELECT count(*) FROM categories")

	staged := app.imported(cookie, account, "trading212-august.csv", fixture(t))
	if staged.State != stateReady || staged.RowCount != 8 || staged.ExcludedCount != 2 || staged.ErrorCount != 0 ||
		staged.DuplicateCount != 0 || staged.MappingVersion == nil || *staged.MappingVersion != 1 || staged.RowCounts[imports.RowValid] != 6 {
		t.Fatalf("staged fixture: %+v", staged)
	}
	if *staged.PeriodStart != "2026-08-02" || *staged.PeriodEnd != "2026-08-13" {
		t.Fatalf("period: %s to %s", *staged.PeriodStart, *staged.PeriodEnd)
	}
	if count := app.count("SELECT count(*) FROM transactions"); count != 0 {
		t.Fatal("staging must not write the ledger")
	}

	committed := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie))
	if committed.State != stateCommitted || committed.InsertedCount != 6 || committed.CommittedAt == nil ||
		committed.ReviewStatus != "confirmed" || committed.ConfirmedAt == nil {
		t.Fatalf("committed: %+v", committed)
	}
	if events := app.count("SELECT count(*) FROM notification_events"); events != 0 {
		t.Fatalf("manual import created %d review notifications", events)
	}
	rows := app.batchTransactions(cookie, staged.ID)
	if len(rows) != 6 {
		t.Fatalf("ledger rows: %d", len(rows))
	}
	bistro := byNotesOrPayee(rows, "Example Bistro")
	if bistro.Amount != "-12.40" || bistro.Kind != "expense" || bistro.CategoryName != "Restaurants" || bistro.Currency != "EUR" ||
		bistro.ReviewStatus != "confirmed" || !bistro.Imported {
		t.Fatalf("card debit: %+v", bistro)
	}
	if downtown := byNotesOrPayee(rows, "Example Bistro, Downtown"); downtown.CategoryID == nil || *downtown.CategoryID != *bistro.CategoryID {
		t.Fatalf("quoted merchant with a comma: %+v", downtown)
	}
	if market := byNotesOrPayee(rows, "Example Market"); market.CategoryName != "Retail stores" {
		t.Fatalf("market: %+v", market)
	}
	kiosk := byNotesOrPayee(rows, "Sample Kiosk")
	if kiosk.Notes != "Line one\nline two" || kiosk.Amount != "-8.10" || kiosk.CategoryName != "Miscellaneous" {
		t.Fatalf("embedded newline row: %+v", kiosk)
	}
	if original := app.detail(cookie, kiosk.ID).Original; original == nil || *original.FeeAmount != "0.02" || *original.FeeCurrency != "EUR" ||
		original.Amount != "-8.10" {
		t.Fatalf("conversion fee must be kept as source metadata without changing the total: %+v", original)
	}
	deposit := byNotesOrPayee(rows, "Bank transfer")
	if deposit.Kind != "transfer" || deposit.Amount != "500.00" || deposit.CategoryID != nil {
		t.Fatalf("deposit: %+v", deposit)
	}
	for _, row := range rows {
		if row.Kind == "income" || row.Amount == "0.27" || row.Amount == "0.12" {
			t.Fatalf("cashback imported: %+v", row)
		}
	}
	if payees := app.count("SELECT count(*) FROM payees"); payees != 4 {
		t.Fatalf("payees: %d", payees)
	}
	if categories := app.count("SELECT count(*) FROM categories"); categories != uncategorized+3 {
		t.Fatalf("categories: %d", categories)
	}

	history := decode[struct {
		Imports []importView `json:"imports"`
		Total   int64        `json:"total"`
	}](t, app.expect(http.StatusOK, "GET", "/api/imports", nil, cookie))
	if history.Total != 1 || history.Imports[0].Filename != "trading212-august.csv" || history.Imports[0].InsertedCount != 6 ||
		history.Imports[0].Account.Name != "Trading 212" {
		t.Fatalf("history: %+v", history)
	}

	// The owner edits a row, then the same file is imported again.
	edited := decode[updateResult](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", bistro.ID),
		editInput(bistro, map[string]any{"payee": "Owner Lunch Spot", "notes": "team lunch"}), cookie)).Transaction
	again := app.imported(cookie, account, "trading212-august.csv", fixture(t))
	if again.State != stateReady || again.DuplicateCount != 6 || again.ExcludedCount != 2 || again.ConflictCount != 0 ||
		*again.MappingVersion != 1 {
		t.Fatalf("reimport: %+v", again)
	}
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", again.ID), nil, cookie)
	if count := app.count("SELECT count(*) FROM transactions"); count != 6 {
		t.Fatalf("reimport created duplicates: %d", count)
	}
	if kept := app.detail(cookie, bistro.ID); kept.PayeeName != "Owner Lunch Spot" || kept.Notes != "team lunch" || kept.Revision != edited.Revision {
		t.Fatalf("reimport lost an edit: %+v", kept)
	}

	// History lists the newest upload first and sorts on request.
	order := func(query string) []int64 {
		ids := []int64{}
		for _, batch := range decode[struct {
			Imports []importView `json:"imports"`
		}](t, app.expect(http.StatusOK, "GET", "/api/imports"+query, nil, cookie)).Imports {
			ids = append(ids, batch.ID)
		}
		return ids
	}
	if ids := order(""); len(ids) != 2 || ids[0] != again.ID {
		t.Fatalf("default history order: %v", ids)
	}
	if ids := order("?sort=createdAt&dir=asc"); ids[1] != again.ID {
		t.Fatalf("oldest first: %v", ids)
	}
	if ids := order("?sort=duplicateCount&dir=desc"); ids[0] != again.ID {
		t.Fatalf("most duplicates first: %v", ids)
	}
	if ids := order("?sort=insertedCount&dir=desc"); ids[1] != again.ID {
		t.Fatalf("most added first: %v", ids)
	}
	app.expect(http.StatusUnprocessableEntity, "GET", "/api/imports?sort=notes", nil, cookie)
}

func TestReimportFlagsConflictingSourceValues(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	first := app.imported(cookie, account, "august.csv", fixture(t))
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", first.ID), nil, cookie)
	market := byNotesOrPayee(app.batchTransactions(cookie, first.ID), "Example Market")

	changed := bytes.Replace(fixture(t), []byte("-54.99"), []byte("-55.99"), 1)
	second := app.imported(cookie, account, "august-corrected.csv", changed)
	if second.State != stateReady || second.ConflictCount != 1 || second.DuplicateCount != 5 {
		t.Fatalf("conflict validation: %+v", second)
	}
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", second.ID), nil, cookie)
	flagged := app.detail(cookie, market.ID)
	if flagged.ReviewStatus != "source_conflict" || flagged.Amount != "-54.99" {
		t.Fatalf("conflict must flag without overwriting: %+v", flagged)
	}
	if conflicts := app.count("SELECT count(*) FROM source_conflicts WHERE transaction_id = ?", market.ID); conflicts != 1 {
		t.Fatalf("source conflicts: %d", conflicts)
	}
	third := app.imported(cookie, account, "august-corrected.csv", changed)
	if third.ConflictCount != 0 || third.DuplicateCount != 6 {
		t.Fatalf("a recorded conflict must not be flagged twice: %+v", third)
	}
}

const unknownActionFile = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n" +
	"Card debit,2026-08-02 09:14:22,,anon-1,-12.40,EUR,Example Bistro,RESTAURANTS\n" +
	"Card credit,2026-08-04 10:00:00,,anon-2,5.00,EUR,Example Market,RETAIL_STORES\n" +
	"Card debit,2026-13-40 10:00:00,,anon-3,-3.00,EUR,Sample Kiosk,MISCELLANEOUS\n" +
	"Card debit,2026-08-06 10:00:00,,anon-4,-7.00,USD,Sample Shop,MISCELLANEOUS\n"

func TestInvalidImportWritesNothingUntilCorrected(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	categories := app.count("SELECT count(*) FROM categories")

	staged := app.imported(cookie, account, "unknown.csv", []byte(unknownActionFile))
	if staged.State != stateNeedsAttention || staged.ErrorCount != 3 || len(staged.UnknownActions) != 1 ||
		staged.UnknownActions[0].Value != "Card credit" {
		t.Fatalf("invalid file: %+v", staged)
	}
	if response := app.request("POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("commit with errors: %d", response.Code)
	}
	if app.count("SELECT count(*) FROM transactions")+app.count("SELECT count(*) FROM payees") != 0 ||
		app.count("SELECT count(*) FROM categories") != categories {
		t.Fatal("an invalid import wrote ledger rows or entities")
	}

	invalid := decode[struct {
		Rows []stagingRowView `json:"rows"`
	}](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/imports/%d/rows?status=invalid", staged.ID), nil, cookie)).Rows
	if len(invalid) != 3 || invalid[0].Row != 2 || len(invalid[1].Errors) == 0 {
		t.Fatalf("invalid rows: %+v", invalid)
	}

	mapping := *staged.Mapping
	mapping.Actions = append(mapping.Actions, imports.ActionRule{Value: "Card credit", Kind: "refund"})
	remapped := decode[importDetail](t, app.expect(http.StatusOK, "PUT", fmt.Sprintf("/api/imports/%d/mapping", staged.ID),
		map[string]any{"mapping": mapping}, cookie))
	if remapped.ErrorCount != 2 || *remapped.MappingVersion != 2 {
		t.Fatalf("remapped: %+v", remapped)
	}
	corrected := append([]string{}, invalid[1].Values...)
	corrected[1] = "2026-08-05 10:00:00"
	pending := decode[importDetail](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/imports/%d/rows/3", staged.ID),
		map[string]any{"excluded": false, "values": corrected}, cookie))
	if pending.State != stateNeedsValidation {
		t.Fatalf("a correction must require validation: %s", pending.State)
	}
	if response := app.request("POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("commit before validation: %d", response.Code)
	}
	app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/imports/%d/rows/4", staged.ID), map[string]any{"excluded": true}, cookie)
	ready := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/validate", staged.ID), nil, cookie))
	if ready.State != stateReady || ready.ExcludedCount != 1 || ready.RowCounts[imports.RowValid] != 3 {
		t.Fatalf("after corrections: %+v", ready)
	}
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie)
	rows := app.batchTransactions(cookie, staged.ID)
	refund := byNotesOrPayee(rows, "Example Market")
	if len(rows) != 3 || refund.Kind != "refund" || refund.Amount != "5.00" || byNotesOrPayee(rows, "Sample Kiosk").Date != "2026-08-05" {
		t.Fatalf("corrected import: %+v", rows)
	}
	var raw string
	if err := app.store.DB.QueryRow("SELECT raw_record_json FROM imported_originals WHERE transaction_id = ?", byNotesOrPayee(rows, "Sample Kiosk").ID).Scan(&raw); err != nil ||
		!strings.Contains(raw, "2026-13-40") || !strings.Contains(raw, "correctedValues") {
		t.Fatalf("the original and corrected values must both be kept: %s %v", raw, err)
	}
}

// bankFile is an unfamiliar format: semicolons, decimal commas, day-first
// dates, and separate debit and credit columns. Rows four and five carry
// values that must never reach TypeSafe.
const bankFile = "Datum;Popis;Výdaj;Príjem;Mena\n" +
	"02.08.2026;Example Bistro;12,40;;EUR\n" +
	"03.08.2026;Salary Example;;1 500,00;EUR\n" +
	"04.08.2026;Example Market;54,99;;EUR\n" +
	"05.08.2026;Sample Kiosk;8,10;;EUR\n" +
	"06.08.2026;MARKER-ROW-FIVE;3,00;;EUR\n" +
	"06.08.2026;MARKER-ROW-FIVE;3,00;;EUR\n"

func bankAnswers() map[string]string {
	return map[string]string{
		"date": "column 1", "amount_mode": imports.AmountDebitCredit, "debit": "column 3", "credit": "column 4",
		"currency": "column 5", "payee": "column 2",
		"date_format": imports.DateDMY, "decimal": "comma", "thousands": "space",
	}
}

// fakeJev answers each question with its configured option, or with "none"
// when the question offers it, at full probability.
type fakeJev struct {
	mu       sync.Mutex
	bodies   [][]byte
	choices  map[string]string
	failures atomic.Int32
}

type jevRequest struct {
	State     json.RawMessage `json:"state"`
	Model     string          `json:"model"`
	Questions map[string]struct {
		Type     string         `json:"type"`
		Criteria map[string]any `json:"criteria"`
	} `json:"questions"`
}

func (f *fakeJev) answer(choices map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.choices = choices
}

func newFakeJev(t *testing.T) (*fakeJev, *integrations.Client) {
	fake := &fakeJev{choices: bankAnswers()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/typesafe/models":
			w.Write([]byte(`{"models":[{"name":"` + integrations.JevModel + `"}]}`))
		case "/typesafe/systemone":
			body, _ := io.ReadAll(r.Body)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.bodies = append(fake.bodies, body)
			if fake.failures.Add(-1) >= 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var request jevRequest
			if json.Unmarshal(body, &request) != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			answers := map[string]integrations.Answer{}
			for id, question := range request.Questions {
				if question.Type == integrations.QuestionNoul {
					answers[id] = integrations.Answer{Type: question.Type, Noul: 0.1}
					continue
				}
				picked, ok := fake.choices[id]
				if _, offered := question.Criteria["none"]; !ok && offered {
					picked = "none"
				}
				answers[id] = integrations.Answer{Type: question.Type, Choice: picked,
					Probabilities: map[string]float64{picked: 1}, Confidence: 0.95}
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "jev-1", "answers": answers})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client := integrations.NewClient()
	client.TypeSafe = server.URL + "/typesafe"
	return fake, client
}

const dayOrderNote = "The sample does not show whether dates are day-first or month-first. Check the date order."

func TestUnfamiliarFormatSendsAtMostThreeRecords(t *testing.T) {
	fake, client := newFakeJev(t)
	app := newTestApp(t, Options{Integrations: client})
	cookie := app.setup()
	account := app.account(cookie, "Bank")

	staged := app.imported(cookie, account, "bank.csv", []byte(bankFile))
	if staged.State != stateNeedsMapping || staged.CanSuggest || len(staged.Header) != 5 {
		t.Fatalf("unfamiliar format: %+v", staged)
	}
	suggest := fmt.Sprintf("/api/imports/%d/suggest-mapping", staged.ID)
	if response := app.request("POST", suggest, nil, cookie); response.Code != http.StatusConflict || len(fake.bodies) != 0 {
		t.Fatalf("suggestion without TypeSafe: %d", response.Code)
	}
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie)

	fake.failures.Store(1)
	suggestion := decode[struct {
		Mapping imports.Mapping `json:"mapping"`
		Notes   []string        `json:"notes"`
	}](t, app.expect(http.StatusOK, "POST", suggest, nil, cookie))
	if len(fake.bodies) != 3 || !bytes.Equal(fake.bodies[0], fake.bodies[1]) {
		t.Fatalf("a retry must resend the same request: %d requests", len(fake.bodies))
	}
	for index, body := range fake.bodies {
		var request jevRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.Model != integrations.JevModel || bytes.Contains(body, []byte("MARKER-ROW-FIVE")) || bytes.Contains(body, []byte("Sample Kiosk")) {
			t.Fatalf("outbound request exceeded the sample: %s", body)
		}
		if index < 2 {
			var state struct {
				Columns []struct {
					Name   string   `json:"name"`
					Values []string `json:"values"`
				} `json:"columns"`
			}
			if err := json.Unmarshal(request.State, &state); err != nil {
				t.Fatal(err)
			}
			if len(state.Columns) != 5 || state.Columns[3].Name != "Príjem" {
				t.Fatalf("columns: %s", request.State)
			}
			for _, column := range state.Columns {
				if len(column.Values) != 3 {
					t.Fatalf("every column must hold the three sample records: %s", request.State)
				}
			}
			continue
		}
		var state struct {
			Rows []map[string]string `json:"rows"`
		}
		if err := json.Unmarshal(request.State, &state); err != nil || len(state.Rows) != 3 || state.Rows[1]["money in"] != "1 500,00" {
			t.Fatalf("values request: %s", request.State)
		}
	}
	if !slices.Equal(suggestion.Notes, []string{dayOrderNote}) || suggestion.Mapping.Date.Format != imports.DateDMY ||
		suggestion.Mapping.Amount.ThousandsSeparator != " " || len(suggestion.Mapping.Ambiguities) != 0 {
		t.Fatalf("suggestion: %+v", suggestion)
	}

	unreadableAnswers := bankAnswers()
	unreadableAnswers["currency"] = "column 2"
	fake.answer(unreadableAnswers)
	unreadable := decode[struct {
		Notes []string `json:"notes"`
	}](t, app.expect(http.StatusOK, "POST", suggest, nil, cookie))
	if len(unreadable.Notes) != 4 || !strings.HasPrefix(unreadable.Notes[1], "Sample row 1:") {
		t.Fatalf("sample rows the suggestion cannot read must come back as notes: %+v", unreadable)
	}
	sharedAnswers := bankAnswers()
	sharedAnswers["debit"] = "column 1"
	fake.answer(sharedAnswers)
	if rejected := app.request("POST", suggest, nil, cookie); rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a mapping that does not fit the file must be rejected: %d %s", rejected.Code, rejected.Body)
	}
	if count := app.count("SELECT count(*) FROM csv_mappings"); count != 0 {
		t.Fatal("a suggestion must not be saved until the owner applies it")
	}

	ready := decode[importDetail](t, app.expect(http.StatusOK, "PUT", fmt.Sprintf("/api/imports/%d/mapping", staged.ID),
		map[string]any{"mapping": suggestion.Mapping}, cookie))
	if ready.State != stateReady || ready.RowCounts[imports.RowValid] != 6 {
		t.Fatalf("suggested mapping: %+v", ready)
	}
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie)
	rows := app.batchTransactions(cookie, staged.ID)
	salary := byNotesOrPayee(rows, "Salary Example")
	if len(rows) != 6 || salary.Amount != "1500.00" || salary.Kind != "income" || salary.ReviewStatus != "confirmed" || byNotesOrPayee(rows, "Example Bistro").Amount != "-12.40" {
		t.Fatalf("bank import: %+v", rows)
	}

	// The cached mapping applies to the next file of this format, and rows
	// without an ID column deduplicate by content and occurrence.
	again := app.imported(cookie, account, "bank-again.csv", []byte(bankFile+"07.08.2026;New Row;1,00;;EUR\n"))
	if again.State != stateReady || again.DuplicateCount != 6 || again.RowCounts[imports.RowValid] != 1 {
		t.Fatalf("cached mapping reimport: %+v", again)
	}
}

// headerlessFile has no header row, like some bank exports: the first line
// is already a transaction and no column names the currency.
const headerlessFile = "Owner,2026-09-01,Example Shop,MISCELLANEOUS,-9.84,OUT\n" +
	"Owner,2026-09-02,Example Cafe,RESTAURANTS,-6.70,OUT\n" +
	"Owner,2026-09-03,Employer Example,SALARY,1500.00,IN\n" +
	"Owner,2026-09-04,MARKER-ROW-FOUR,MISCELLANEOUS,-1.00,OUT\n"

func TestFileWithoutHeaderKeepsEveryRow(t *testing.T) {
	fake, client := newFakeJev(t)
	fake.answer(map[string]string{
		"date": "column 2", "amount_mode": imports.AmountSingle, "amount": "column 5", "payee": "column 3", "action": "column 6",
		"action 1": "expense", "action 2": "income",
		"date_format": imports.DateISO, "sign": imports.NegativeIsOutflow, "decimal": "point", "thousands": "none",
	})
	app := newTestApp(t, Options{Integrations: client})
	cookie := app.setup()
	account := app.account(cookie, "Card")
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie)

	staged := app.imported(cookie, account, "converted.csv", []byte(headerlessFile))
	if staged.State != stateNeedsMapping || !staged.CanSuggest || staged.RowCount != 4 ||
		!slices.Equal(staged.Header, []string{"Column 1", "Column 2", "Column 3", "Column 4", "Column 5", "Column 6"}) {
		t.Fatalf("headerless file: %+v", staged)
	}
	if want := []string{"Owner", "2026-09-01", "Example Shop", "MISCELLANEOUS", "-9.84", "OUT"}; !slices.Equal(staged.Examples, want) {
		t.Fatalf("examples come from the first line: %q", staged.Examples)
	}
	suggestion := decode[struct {
		Mapping imports.Mapping `json:"mapping"`
		Notes   []string        `json:"notes"`
	}](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/suggest-mapping", staged.ID), nil, cookie))
	if len(suggestion.Notes) != 0 || suggestion.Mapping.Currency.Fixed == nil || *suggestion.Mapping.Currency.Fixed != "EUR" ||
		!slices.Equal(suggestion.Mapping.Actions, []imports.ActionRule{{Value: "OUT", Kind: "expense"}, {Value: "IN", Kind: "income"}}) {
		t.Fatalf("suggestion: %+v", suggestion)
	}
	var request jevRequest
	if err := json.Unmarshal(fake.bodies[0], &request); err != nil {
		t.Fatal(err)
	}
	var state struct {
		Columns []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"columns"`
	}
	if err := json.Unmarshal(request.State, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Columns) != 6 || state.Columns[0].Name != "Column 1" || len(state.Columns[2].Values) != 3 ||
		state.Columns[2].Values[0] != "Example Shop" || bytes.Contains(fake.bodies[0], []byte("MARKER-ROW-FOUR")) {
		t.Fatalf("the sample must hold the first three records: %s", request.State)
	}

	ready := decode[importDetail](t, app.expect(http.StatusOK, "PUT", fmt.Sprintf("/api/imports/%d/mapping", staged.ID),
		map[string]any{"mapping": suggestion.Mapping}, cookie))
	if ready.State != stateReady || ready.RowCounts[imports.RowValid] != 4 {
		t.Fatalf("headerless mapping: %+v", ready)
	}
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie)
	rows := app.batchTransactions(cookie, staged.ID)
	if len(rows) != 4 || byNotesOrPayee(rows, "Example Shop").Amount != "-9.84" || byNotesOrPayee(rows, "Employer Example").Kind != "income" {
		t.Fatalf("the first line must be imported as a transaction: %+v", rows)
	}
}

type failingReader struct {
	data []byte
	read bool
}

func (r *failingReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, errors.New("connection reset")
	}
	r.read = true
	return copy(buffer, r.data), nil
}

func TestImportRecoversFromInterruptions(t *testing.T) {
	var interrupt atomic.Bool
	options := Options{ImportCommitHook: func(inserted int) error {
		if inserted == 3 && interrupt.Load() {
			return errors.New("simulated crash")
		}
		return nil
	}}
	app := newTestApp(t, options)
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")

	// A connection lost mid-upload fails the batch and removes its rows.
	lost := app.upload(cookie, account, "lost.csv", &failingReader{data: fixture(t)[:400]})
	failed := decode[importDetail](t, lost)
	if lost.Code != http.StatusCreated || failed.State != stateFailed || failed.RowCount != 0 ||
		app.count("SELECT count(*) FROM staging_rows WHERE batch_id = ?", failed.ID) != 0 {
		t.Fatalf("interrupted upload: %d %+v", lost.Code, failed)
	}
	malformed := app.imported(cookie, account, "malformed.csv", []byte("Action,ID\n\"unterminated,1\n"))
	if malformed.State != stateFailed || len(malformed.ValidationErrors) != 1 || !strings.Contains(malformed.ValidationErrors[0].Message, "Line") {
		t.Fatalf("malformed file: %+v", malformed)
	}

	// A process stopped while streaming leaves a batch without staged_at;
	// startup recovery fails it and deletes the partial rows.
	ctx := context.Background()
	batch, err := app.store.Queries.CreateImportBatch(ctx, dbgen.CreateImportBatchParams{SourceAccountID: account, Filename: "crash.csv"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Queries.StageRow(ctx, dbgen.StageRowParams{BatchID: batch.ID, RowNumber: 1, RawRecordJson: `["x"]`,
		Status: imports.RowPending, ValidationErrorsJson: "[]"}); err != nil {
		t.Fatal(err)
	}
	app.restart()
	if recovered, err := imports.Recover(ctx, app.store); err != nil || recovered != 1 {
		t.Fatalf("recover: %d %v", recovered, err)
	}
	crashed := decode[importDetail](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/imports/%d", batch.ID), nil, cookie))
	if crashed.State != stateFailed || app.count("SELECT count(*) FROM staging_rows WHERE batch_id = ?", batch.ID) != 0 {
		t.Fatalf("recovered batch: %+v", crashed)
	}
	app.expect(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/imports/%d", batch.ID), nil, cookie)

	// A failure during the final commit rolls back every row and entity.
	staged := app.imported(cookie, account, "august.csv", fixture(t))
	interrupt.Store(true)
	commit := fmt.Sprintf("/api/imports/%d/commit", staged.ID)
	if response := app.request("POST", commit, nil, cookie); response.Code != http.StatusInternalServerError {
		t.Fatalf("interrupted commit: %d", response.Code)
	}
	if app.count("SELECT count(*) FROM transactions")+app.count("SELECT count(*) FROM payees")+app.count("SELECT count(*) FROM imported_originals") != 0 {
		t.Fatal("an interrupted commit left partial ledger rows")
	}
	if after := decode[importDetail](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/imports/%d", staged.ID), nil, cookie)); after.State != stateReady || after.ReviewStatus != "not_required" || after.ConfirmedAt != nil {
		t.Fatalf("the batch must stay ready to retry: %s", after.State)
	}
	interrupt.Store(false)
	app.restart()
	if done := decode[importDetail](t, app.expect(http.StatusOK, "POST", commit, nil, cookie)); done.InsertedCount != 6 || done.ReviewStatus != "confirmed" || done.ConfirmedAt == nil {
		t.Fatalf("retried commit: %+v", done)
	}
	if response := app.request("DELETE", fmt.Sprintf("/api/imports/%d", staged.ID), nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("deleting a committed import: %d", response.Code)
	}
}

func TestLargeImportStreamsInChunks(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	var file bytes.Buffer
	file.WriteString("Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n")
	for index := range 1234 {
		fmt.Fprintf(&file, "Card debit,2026-08-%02d 10:00:00,,anon-%d,-%d.%02d,EUR,Store %d,RETAIL_STORES\n", index%28+1, index, index%50+1, index%100, index%40)
	}
	staged := app.imported(cookie, account, "large.csv", file.Bytes())
	if staged.State != stateReady || staged.RowCount != 1234 || staged.RowCounts[imports.RowValid] != 1234 {
		t.Fatalf("large file: %+v", staged)
	}
	page := decode[struct {
		Rows      []stagingRowView `json:"rows"`
		NextAfter *int64           `json:"nextAfter"`
	}](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/imports/%d/rows?after=500&limit=200", staged.ID), nil, cookie))
	if len(page.Rows) != 200 || page.Rows[0].Row != 501 || page.NextAfter == nil || *page.NextAfter != 700 {
		t.Fatalf("row page: %d %v", len(page.Rows), page.NextAfter)
	}
	done := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie))
	if done.InsertedCount != 1234 || app.count("SELECT count(*) FROM payees") != 40 {
		t.Fatalf("large commit: %+v", done)
	}
}

func TestImportUploadValidation(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Trading 212")
	request := httptest.NewRequest("POST", fmt.Sprintf("/api/imports?accountId=%d&filename=a.csv", account), strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON upload: %d", response.Code)
	}
	if response := app.upload(cookie, 999, "a.csv", strings.NewReader(unknownActionFile)); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing account: %d", response.Code)
	}
	if count := app.count("SELECT count(*) FROM import_batches"); count != 0 {
		t.Fatalf("rejected uploads created batches: %d", count)
	}
	if detail := app.imported(cookie, account, `C:\Users\owner\report.csv`, []byte(unknownActionFile)); detail.Filename != "report.csv" {
		t.Fatalf("filename: %s", detail.Filename)
	}
}
