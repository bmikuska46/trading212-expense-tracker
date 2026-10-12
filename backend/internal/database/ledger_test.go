package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

func testStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	directory := t.TempDir()
	keyFile := filepath.Join(directory, "key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "data", "budget.db")
	store, err := Open(context.Background(), path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	return store, path, keyFile
}

func execSQL(t *testing.T, store *Store, statement string, args ...any) {
	t.Helper()
	if _, err := store.DB.Exec(statement, args...); err != nil {
		t.Fatalf("fixture or assertion SQL failed: %v", err)
	}
}

func newAccountAndBatch(t *testing.T, store *Store) (dbgen.Account, dbgen.ImportBatch) {
	t.Helper()
	ctx := context.Background()
	account, err := store.Queries.CreateAccount(ctx, dbgen.CreateAccountParams{Name: "Synthetic source", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := store.Queries.CreateImportBatch(ctx, dbgen.CreateImportBatchParams{
		SourceAccountID: account.ID, Filename: "synthetic.csv",
		PeriodStart: sql.NullString{String: "2026-09-01", Valid: true},
		PeriodEnd:   sql.NullString{String: "2026-10-01", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return account, batch
}

func importedTransaction(accountID, batchID int64) dbgen.CreateTransactionParams {
	return dbgen.CreateTransactionParams{
		AccountID: accountID, SourceAccountID: sql.NullInt64{Int64: accountID, Valid: true},
		ExternalID:    sql.NullString{String: "synthetic-external-id", Valid: true},
		ImportBatchID: sql.NullInt64{Int64: batchID, Valid: true},
		OccurredAt:    "2026-09-25T12:00:00Z", LocalDate: "2026-09-25", Kind: "expense",
		AmountMinor: -1100, Currency: "USD", MinorUnit: 2, EurCents: -1000,
		Notes: "Synthetic source notes", ReviewStatus: "needs_review",
	}
}

func TestImportedRecordsPreserveOriginalsAndConversionsAfterEdits(t *testing.T) {
	store, _, _ := testStore(t)
	account, batch := newAccountAndBatch(t, store)
	ctx := context.Background()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queries := store.Queries.WithTx(tx)
	transaction, err := queries.CreateTransaction(ctx, importedTransaction(account.ID, batch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateImportedOriginal(ctx, dbgen.CreateImportedOriginalParams{
		TransactionID: transaction.ID, AmountText: "-11.00", Currency: "USD",
		TimestampText: "2026-09-25 12:00:00", Action: "Card debit", MerchantName: "Synthetic shop",
		MerchantCategory: "Groceries", Notes: "Synthetic source notes",
		FeeAmountText: sql.NullString{String: "0.05", Valid: true}, FeeCurrency: sql.NullString{String: "EUR", Valid: true},
		RawRecordJson: `["Card debit","2026-09-25 12:00:00","Synthetic source notes","synthetic-external-id","-11.00","USD","0.05","EUR","Synthetic shop","Groceries"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateTransactionConversion(ctx, dbgen.CreateTransactionConversionParams{
		TransactionID: transaction.ID, OriginalAmountMinor: -1100, OriginalCurrency: "USD", OriginalMinorUnit: 2,
		EurCents: -1000, Rate: "1.100000", RateDate: "2026-09-25", ProviderPolicy: "frankfurter-default",
		RoundingRule: ledger.RoundingRule,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	original, err := store.Queries.GetImportedOriginal(ctx, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	conversion, err := store.Queries.GetTransactionConversion(ctx, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	update := dbgen.UpdateTransactionParams{
		AccountID: account.ID, OccurredAt: transaction.OccurredAt, LocalDate: transaction.LocalDate,
		Kind: "expense", AmountMinor: -550, Currency: "USD", MinorUnit: 2, EurCents: -500,
		Notes: "Owner corrected notes", ID: transaction.ID, ExpectedRevision: 0,
	}
	// Saved conversions are permanent, so their amounts cannot be edited.
	if _, err := store.Queries.UpdateTransaction(ctx, update); err == nil {
		t.Fatal("converted amount edit accepted")
	}
	update.AmountMinor, update.EurCents = transaction.AmountMinor, transaction.EurCents
	edited, err := store.Queries.UpdateTransaction(ctx, update)
	if err != nil || edited.Revision != 1 || edited.Notes != "Owner corrected notes" {
		t.Fatalf("owner edit: %v, %v", edited, err)
	}
	if _, err := store.Queries.UpdateTransaction(ctx, update); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale edit must fail: %v", err)
	}
	afterOriginal, err := store.Queries.GetImportedOriginal(ctx, transaction.ID)
	if err != nil || original != afterOriginal {
		t.Fatal("owner edit changed imported originals")
	}
	afterConversion, err := store.Queries.GetTransactionConversion(ctx, transaction.ID)
	if err != nil || conversion != afterConversion {
		t.Fatal("owner edit changed saved conversion")
	}
	history, err := store.Queries.ListTransactionEdits(ctx, transaction.ID)
	if err != nil || len(history) != 1 || history[0].Revision != 0 {
		t.Fatalf("edit history: %v, %v", history, err)
	}
	var snapshot struct {
		AmountMinor int64  `json:"amount_minor"`
		Notes       string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(history[0].SnapshotJson), &snapshot); err != nil || snapshot.AmountMinor != -1100 || snapshot.Notes != transaction.Notes || !strings.Contains(history[0].SnapshotJson, `"deleted_at":null`) {
		t.Fatal("edit history lost previous financial values")
	}
	for _, statement := range []string{
		"UPDATE imported_originals SET notes = 'replaced' WHERE transaction_id = ?",
		"DELETE FROM imported_originals WHERE transaction_id = ?",
		"UPDATE transaction_conversions SET rate = '9' WHERE transaction_id = ?",
		"DELETE FROM transaction_conversions WHERE transaction_id = ?",
		"UPDATE transactions SET external_id = 'replaced' WHERE id = ?",
		"INSERT OR REPLACE INTO imported_originals SELECT * FROM imported_originals WHERE transaction_id = ?",
		"INSERT OR REPLACE INTO transaction_conversions SELECT * FROM transaction_conversions WHERE transaction_id = ?",
		"UPDATE transaction_edit_history SET snapshot_json = '{}' WHERE transaction_id = ?",
		"DELETE FROM transaction_edit_history WHERE transaction_id = ?",
	} {
		if _, err := store.DB.Exec(statement, transaction.ID); err == nil {
			t.Errorf("immutable source accepted mutation: %s", statement)
		}
	}
	// Rate cache deletion cannot cascade into saved transaction conversion details.
	execSQL(t, store, `INSERT INTO exchange_rates (quote_currency, publication_date, rate, provider_policy, fetched_at) VALUES ('USD', '2026-09-25', '1.100000', 'frankfurter-default', '2026-09-25T16:00:00Z')`)
	execSQL(t, store, "DELETE FROM exchange_rates")
	afterConversion, err = store.Queries.GetTransactionConversion(ctx, transaction.ID)
	if err != nil || conversion != afterConversion {
		t.Fatal("cache cleanup changed saved conversion")
	}
}

func TestSourceIDUniquenessAndFailedPromotionRollback(t *testing.T) {
	store, _, _ := testStore(t)
	account, batch := newAccountAndBatch(t, store)
	ctx := context.Background()
	params := importedTransaction(account.ID, batch.ID)
	first, err := store.Queries.CreateTransaction(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries.CreateTransaction(ctx, params); err == nil {
		t.Fatal("duplicate source ID accepted")
	}
	other, otherBatch := newAccountAndBatch(t, store)
	if _, err := store.Queries.CreateTransaction(ctx, importedTransaction(other.ID, otherBatch.ID)); err != nil {
		t.Fatalf("same external ID in a different source account rejected: %v", err)
	}
	// Several manual records may have no source ID.
	manual := params
	manual.SourceAccountID, manual.ImportBatchID, manual.ExternalID = sql.NullInt64{}, sql.NullInt64{}, sql.NullString{}
	for range 2 {
		if _, err := store.Queries.CreateTransaction(ctx, manual); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO payees (name, normalized_name) VALUES ('Rollback payee', 'rollback payee')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries.WithTx(tx).CreateTransaction(ctx, params); err == nil {
		t.Fatal("duplicate during promotion accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var payees int
	if err := store.DB.QueryRow("SELECT count(*) FROM payees").Scan(&payees); err != nil || payees != 0 {
		t.Fatal("failed promotion left a related payee")
	}
	actual, err := store.Queries.FindTransactionBySourceID(ctx, dbgen.FindTransactionBySourceIDParams{SourceAccountID: params.SourceAccountID, ExternalID: params.ExternalID})
	if err != nil || actual != first {
		t.Fatal("duplicate attempt changed the original transaction")
	}
}

func TestSchemaConstraintsAndIndexes(t *testing.T) {
	store, _, _ := testStore(t)
	account, batch := newAccountAndBatch(t, store)
	execSQL(t, store, "INSERT INTO owner (id, email, password_hash) VALUES (1, 'synthetic@example.test', 'synthetic-password-hash')")
	for _, statement := range []string{
		"INSERT INTO owner (id, email, password_hash) VALUES (2, 'second@example.test', 'hash')",
		"INSERT INTO owner (id, email, password_hash) VALUES (1, 'second@example.test', 'hash')",
		"INSERT INTO accounts (name, currency, opening_balance_minor) VALUES ('Nonzero', 'EUR', 1)",
		"INSERT INTO accounts (name, currency, integration_provider) VALUES ('Missing ID', 'EUR', 'typesafe')",
		"INSERT INTO categorization_rules (category_id) VALUES (1)",
		"INSERT INTO transaction_splits (transaction_id, position, category_id, amount_minor, eur_cents) VALUES (999, 0, 1, 1, 1)",
	} {
		if _, err := store.DB.Exec(statement); err == nil {
			t.Errorf("invalid relationship accepted: %s", statement)
		}
	}
	ctx := context.Background()
	params := importedTransaction(account.ID, batch.ID)
	for _, scenario := range []string{"missing source", "missing ID", "empty ID", "missing batch", "invalid kind", "invalid status", "missing account"} {
		invalid := params
		switch scenario {
		case "missing source":
			invalid.SourceAccountID = sql.NullInt64{}
		case "missing ID":
			invalid.ExternalID = sql.NullString{}
		case "empty ID":
			invalid.ExternalID = sql.NullString{String: " ", Valid: true}
		case "missing batch":
			invalid.ImportBatchID = sql.NullInt64{}
		case "invalid kind":
			invalid.Kind = "cashback"
		case "invalid status":
			invalid.ReviewStatus = "invalid"
		case "missing account":
			invalid.AccountID = 999
		}
		if _, err := store.Queries.CreateTransaction(ctx, invalid); err == nil {
			t.Errorf("accepted %s", scenario)
		}
	}
	for _, rate := range []string{"0", "0.000", "-1", "1e2", ".5", "1.", "1.2.3", ".", "", "NaN"} {
		if _, err := store.DB.Exec(`INSERT INTO exchange_rates (quote_currency, publication_date, rate, provider_policy, fetched_at) VALUES ('USD', '2026-09-25', ?, 'default', '2026-09-25T16:00:00Z')`, rate); err == nil {
			t.Errorf("invalid exact rate accepted: %q", rate)
		}
	}
	for _, index := range []string{"transactions_date", "transactions_account_date", "transactions_payee_date", "transactions_category_date", "transactions_batch", "transactions_review_date"} {
		var count int
		if err := store.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %s: %v", index, err)
		}
	}
	var integrity string
	if err := store.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("database integrity: %s, %v", integrity, err)
	}
	rows, err := store.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("foreign key validation failed")
	}
}
