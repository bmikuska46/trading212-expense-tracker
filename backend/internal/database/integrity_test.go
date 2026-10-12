package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

func TestNotificationEventUpgradePreservesDeliveryHistory(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	runtimeOnlyDatabase(t, store)
	files := migrationFiles(t)
	for name := range files {
		if name > "000003_~" {
			delete(files, name)
		}
	}
	if err := applyMigrations(ctx, store.DB, files); err != nil {
		t.Fatal(err)
	}
	// The generated queries target the latest schema, so the old schema is
	// filled with plain SQL.
	account, err := store.Queries.CreateAccount(ctx, dbgen.CreateAccountParams{Name: "Synthetic source", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	batch := dbgen.ImportBatch{ID: 5}
	execSQL(t, store, `INSERT INTO import_batches (id, source_account_id, filename) VALUES (5, ?, 'synthetic.csv')`, account.ID)
	execSQL(t, store, `INSERT INTO notification_events (id, batch_id, kind, payload_json, idempotency_key, delivery_state, attempt_count, next_attempt_at, email_id)
		VALUES (7, ?, 'import_review', '{"status":"needs_review"}', 'synthetic-key', 'delivered', 1, '2026-10-01T06:00:00Z', 'synthetic-email')`, batch.ID)
	execSQL(t, store, `INSERT INTO notification_attempts (event_id, attempt_number, started_at, outcome, email_id)
		VALUES (7, 1, '2026-10-01T06:00:00Z', 'delivered', 'synthetic-email')`)
	if err := applyMigrations(ctx, store.DB, migrationFiles(t)); err != nil {
		t.Fatal(err)
	}
	var batchID, attemptEvent int64
	var jobID sql.NullInt64
	var emailID string
	if err := store.DB.QueryRow("SELECT batch_id, job_id, email_id FROM notification_events WHERE id = 7").Scan(&batchID, &jobID, &emailID); err != nil ||
		batchID != batch.ID || jobID.Valid || emailID != "synthetic-email" {
		t.Fatalf("upgrade lost notification event: %v", err)
	}
	if err := store.DB.QueryRow("SELECT event_id FROM notification_attempts WHERE attempt_number = 1").Scan(&attemptEvent); err != nil || attemptEvent != 7 {
		t.Fatalf("upgrade lost notification attempt: %v", err)
	}
	var attemptsSQL string
	if err := store.DB.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'notification_attempts'").Scan(&attemptsSQL); err != nil ||
		!strings.Contains(attemptsSQL, `REFERENCES "notification_events"`) {
		t.Fatalf("attempts must reference the rebuilt events table: %s", attemptsSQL)
	}
	rows, err := store.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("upgrade left foreign key violations")
	}
	rows.Close()

	execSQL(t, store, `INSERT INTO scheduled_jobs (id, kind, source_account_id, period_key, period_start_utc, period_end_utc, next_attempt_at)
		VALUES (3, 'monthly_import', ?, '2026-09', '2026-08-31T22:00:00Z', '2026-09-30T22:00:00Z', '2026-10-01T04:00:00Z')`, account.ID)
	// A job can fail before it creates a batch, and alerts once per failure.
	execSQL(t, store, `INSERT INTO notification_events (job_id, kind, payload_json, idempotency_key, next_attempt_at)
		VALUES (3, 'import_failure', '{}', 'synthetic-job-failure', '2026-10-01T04:00:00Z')`)
	for _, statement := range []string{
		`INSERT INTO notification_events (job_id, kind, payload_json, idempotency_key, next_attempt_at) VALUES (3, 'import_failure', '{}', 'repeat', 'x')`,
		`INSERT INTO notification_events (batch_id, kind, payload_json, idempotency_key, next_attempt_at) VALUES (?, 'import_review', '{}', 'repeat', 'x')`,
		`INSERT INTO notification_events (job_id, kind, payload_json, idempotency_key, next_attempt_at) VALUES (3, 'import_review', '{}', 'review-without-batch', 'x')`,
		`INSERT INTO notification_events (kind, payload_json, idempotency_key, next_attempt_at) VALUES ('import_failure', '{}', 'no-subject', 'x')`,
		`INSERT INTO notification_events (job_id, kind, payload_json, idempotency_key, next_attempt_at) VALUES (99, 'import_failure', '{}', 'missing-job', 'x')`,
	} {
		var args []any
		if strings.Contains(statement, "?") {
			args = append(args, batch.ID)
		}
		if _, err := store.DB.Exec(statement, args...); err == nil {
			t.Errorf("invalid notification event accepted: %s", statement)
		}
	}
}

func TestConvertedAmountsAreLockedAndDeletesAreSoft(t *testing.T) {
	store, _, _ := testStore(t)
	account, batch := newAccountAndBatch(t, store)
	ctx := context.Background()
	transaction, err := store.Queries.CreateTransaction(ctx, importedTransaction(account.ID, batch.ID))
	if err != nil {
		t.Fatal(err)
	}
	conversion := dbgen.CreateTransactionConversionParams{
		TransactionID: transaction.ID, OriginalAmountMinor: -1100, OriginalCurrency: "USD", OriginalMinorUnit: 2,
		EurCents: -999, Rate: "1.100000", RateDate: "2026-09-25", ProviderPolicy: "frankfurter-default",
		RoundingRule: ledger.RoundingRule,
	}
	if err := store.Queries.CreateTransactionConversion(ctx, conversion); err == nil {
		t.Fatal("conversion that disagrees with the transaction accepted")
	}
	conversion.EurCents = -1000
	if err := store.Queries.CreateTransactionConversion(ctx, conversion); err != nil {
		t.Fatal(err)
	}
	update := dbgen.UpdateTransactionParams{
		AccountID: account.ID, OccurredAt: transaction.OccurredAt, LocalDate: transaction.LocalDate,
		Kind: "expense", AmountMinor: transaction.AmountMinor, Currency: "USD", MinorUnit: 2, EurCents: -1001,
		Notes: transaction.Notes, ID: transaction.ID, ExpectedRevision: 0,
	}
	if _, err := store.Queries.UpdateTransaction(ctx, update); err == nil {
		t.Fatal("converted EUR amount edit accepted")
	}
	var edits int
	if err := store.DB.QueryRow("SELECT count(*) FROM transaction_edit_history WHERE transaction_id = ?", transaction.ID).Scan(&edits); err != nil || edits != 0 {
		t.Fatal("rejected edit left history")
	}
	if _, err := store.DB.Exec("DELETE FROM transactions WHERE id = ?", transaction.ID); err == nil {
		t.Fatal("hard delete accepted")
	}
	deleted, err := store.Queries.DeleteTransaction(ctx, dbgen.DeleteTransactionParams{ID: transaction.ID, ExpectedRevision: 0})
	if err != nil || !deleted.DeletedAt.Valid || deleted.Revision != 1 {
		t.Fatalf("soft delete: %v", err)
	}
	update.EurCents, update.ExpectedRevision = transaction.EurCents, 1
	if _, err := store.Queries.UpdateTransaction(ctx, update); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted transaction edit must fail: %v", err)
	}
	// A reimport still sees the deleted source ID and must not recreate it.
	if _, err := store.Queries.CreateTransaction(ctx, importedTransaction(account.ID, batch.ID)); err == nil {
		t.Fatal("deleted source ID reimported")
	}
	found, err := store.Queries.FindTransactionBySourceID(ctx, dbgen.FindTransactionBySourceIDParams{
		SourceAccountID: transaction.SourceAccountID, ExternalID: transaction.ExternalID,
	})
	if err != nil || !found.DeletedAt.Valid {
		t.Fatalf("deleted source record lookup: %v", err)
	}
}
