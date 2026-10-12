package httpserver

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"budget-tracking/backend/internal/imports"
)

func TestSlovakBankImportAndReimport(t *testing.T) {
	data, err := os.ReadFile("testdata/slovak-bank-anonymized.csv")
	if err != nil {
		t.Fatal(err)
	}
	checkSlovakBankImport(t, data, 4, 1, -1030, 9324, "2026-09-01", "2026-09-04")
}

func checkSlovakBankImport(t *testing.T, data []byte, debits, credits, debitTotal, creditTotal int64, start, end string) {
	t.Helper()
	app := newTestApp(t, Options{})
	cookie := app.setup()
	account := app.account(cookie, "Bank CSV test")
	staged := app.imported(cookie, account, "document.csv", data)
	count := debits + credits
	if staged.State != stateReady || staged.RowCount != count || staged.ErrorCount != 0 ||
		staged.RowCounts[imports.RowValid] != count || staged.DuplicateCount != 0 || staged.ExcludedCount != 0 {
		t.Fatalf("state = %s, rows = %d, valid = %d, errors = %d, duplicates = %d, excluded = %d",
			staged.State, staged.RowCount, staged.RowCounts[imports.RowValid], staged.ErrorCount, staged.DuplicateCount, staged.ExcludedCount)
	}
	if staged.PeriodStart == nil || *staged.PeriodStart != start || staged.PeriodEnd == nil || *staged.PeriodEnd != end {
		t.Fatal("the mapped dates did not match the statement period")
	}
	if app.count("SELECT count(*) FROM transactions") != 0 {
		t.Fatal("staging must not write transactions")
	}
	committed := decode[importDetail](t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", staged.ID), nil, cookie))
	if committed.InsertedCount != count || committed.State != stateCommitted {
		t.Fatalf("committed %d rows, state = %s", committed.InsertedCount, committed.State)
	}
	if got := app.count("SELECT count(*) FROM transactions WHERE kind = 'expense' AND amount_minor < 0"); got != debits {
		t.Fatalf("debit count = %d, want %d", got, debits)
	}
	if got := app.count("SELECT count(*) FROM transactions WHERE kind = 'income' AND amount_minor > 0"); got != credits {
		t.Fatalf("credit count = %d, want %d", got, credits)
	}
	if got := app.count("SELECT sum(amount_minor) FROM transactions WHERE kind = 'expense'"); got != debitTotal {
		t.Fatalf("debit total = %d, want %d", got, debitTotal)
	}
	if got := app.count("SELECT sum(amount_minor) FROM transactions WHERE kind = 'income'"); got != creditTotal {
		t.Fatalf("credit total = %d, want %d", got, creditTotal)
	}
	rows := app.batchTransactions(cookie, staged.ID)
	if int64(len(rows)) != count {
		t.Fatalf("register contains %d rows, want %d", len(rows), count)
	}
	for _, row := range rows {
		if row.Currency != "EUR" || row.PayeeName != "" || row.CategoryName != "Uncategorized" || !row.Imported {
			t.Fatal("bank rows must keep EUR and avoid treating card details as payees or categories")
		}
		if row.Notes == "" {
			t.Fatal("a bank description must survive when recipient information is empty")
		}
		if original := app.detail(cookie, row.ID).Original; original == nil ||
			strings.ReplaceAll(original.Amount, ",", ".") != strings.TrimPrefix(row.Amount, "-") {
			t.Fatal("the original unsigned decimal-comma amount must be preserved")
		}
	}
	again := app.imported(cookie, account, "document-again.csv", data)
	if again.State != stateReady || again.DuplicateCount != count || again.ErrorCount != 0 || again.ConflictCount != 0 ||
		again.MappingVersion == nil || staged.MappingVersion == nil || *again.MappingVersion != *staged.MappingVersion {
		t.Fatalf("reimport: state = %s, duplicates = %d, errors = %d, conflicts = %d",
			again.State, again.DuplicateCount, again.ErrorCount, again.ConflictCount)
	}
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/commit", again.ID), nil, cookie)
	if got := app.count("SELECT count(*) FROM transactions"); got != count {
		t.Fatalf("reimport created extra transactions: %d, want %d", got, count)
	}
}
