package database

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"budget-tracking/backend/internal/database/dbgen"
)

const financialMarker = "SYNTHETIC-PRIVATE-MERCHANT-628e4b1f"
const credentialMarker = "SYNTHETIC-INTEGRATION-SECRET-f4d702a1"

func assertEncryptedFiles(t *testing.T, path string, required ...string) {
	t.Helper()
	for _, suffix := range required {
		info, err := os.Stat(path + suffix)
		if err != nil || info.Size() == 0 {
			t.Fatalf("expected nonempty %s sidecar: %v", suffix, err)
		}
	}
	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{financialMarker, credentialMarker, "imported_originals", "staging_rows"} {
			if bytes.Contains(content, []byte(marker)) {
				t.Fatalf("plaintext financial data or schema found in %s", filepath.Base(file))
			}
		}
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("database or sidecar has public permissions: %s", filepath.Base(file))
		}
	}
}

func TestFinancialDataAndFailedStagingRemainEncryptedAcrossRestart(t *testing.T) {
	store, path, keyFile := testStore(t)
	ctx := context.Background()
	account, batch := newAccountAndBatch(t, store)
	if err := store.Queries.SetIntegration(ctx, dbgen.SetIntegrationParams{
		Provider: "typesafe", CredentialsJson: `{"api_key":"` + credentialMarker + `"}`, Enabled: 1,
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal([]string{financialMarker, "-12345.67", "EUR", "synthetic notes"})
	if err != nil {
		t.Fatal(err)
	}
	for row := int64(1); row <= 200; row++ {
		if err := store.Queries.StageRow(ctx, dbgen.StageRowParams{
			BatchID: batch.ID, RowNumber: row, RawRecordJson: string(payload), Status: "invalid",
			ValidationErrorsJson: `["synthetic validation error"]`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(t, store, "UPDATE import_batches SET status = 'failed', row_count = 200, error_count = 200 WHERE id = ?", batch.ID)
	committedBatch, err := store.Queries.CreateImportBatch(ctx, dbgen.CreateImportBatchParams{SourceAccountID: account.ID, Filename: "synthetic-committed.csv"})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := store.Queries.CreateTransaction(ctx, importedTransaction(account.ID, committedBatch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Queries.CreateImportedOriginal(ctx, dbgen.CreateImportedOriginalParams{
		TransactionID: transaction.ID, AmountText: "-11.00", Currency: "USD", TimestampText: "2026-09-25 12:00:00",
		Action: "Card debit", MerchantName: financialMarker, Notes: financialMarker, RawRecordJson: string(payload),
	}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, store, "UPDATE import_batches SET status = 'committed', row_count = 1, inserted_count = 1 WHERE id = ?", committedBatch.ID)
	// Inspect an active WAL and its shared-memory index before checkpointing.
	assertEncryptedFiles(t, path, "-wal", "-shm")
	var tempStore int
	if err := store.DB.QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil || tempStore != 2 {
		t.Fatalf("temporary data must stay in memory: %d, %v", tempStore, err)
	}
	store.DB.Close()
	assertEncryptedFiles(t, path, "")
	reopened, err := Open(ctx, path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	failed, err := reopened.Queries.GetImportBatch(ctx, batch.ID)
	if err != nil || failed.Status != "failed" || failed.ErrorCount != 200 {
		t.Fatal("failed batch was not retained")
	}
	var failedTransactions int
	if err := reopened.DB.QueryRow("SELECT count(*) FROM transactions WHERE import_batch_id = ?", batch.ID).Scan(&failedTransactions); err != nil || failedTransactions != 0 {
		t.Fatal("failed staging unexpectedly created ledger transactions")
	}
	staged, err := reopened.Queries.ListStagingRows(ctx, dbgen.ListStagingRowsParams{BatchID: batch.ID, AfterRow: 0, PageSize: 25})
	if err != nil || len(staged) != 25 || staged[0].RawRecordJson != string(payload) || staged[0].Status != "invalid" {
		t.Fatal("failed staging was not retained or pagination was lost")
	}
	original, err := reopened.Queries.GetImportedOriginal(ctx, transaction.ID)
	if err != nil || original.MerchantName != financialMarker {
		t.Fatal("financial original was not retained")
	}
	var credentials string
	if err := reopened.DB.QueryRow("SELECT credentials_json FROM integrations WHERE provider = 'typesafe'").Scan(&credentials); err != nil || !strings.Contains(credentials, credentialMarker) {
		t.Fatal("encrypted credentials were not retained")
	}
	// Exercise SQLCipher rollback journal encryption as well as normal WAL operation.
	execSQL(t, reopened, "PRAGMA journal_mode = DELETE")
	execSQL(t, reopened, "PRAGMA cache_size = 10")
	tx, err := reopened.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE staging_rows SET status = 'pending' WHERE batch_id = ?", batch.ID); err != nil {
		t.Fatal(err)
	}
	assertEncryptedFiles(t, path, "-journal")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var invalidRows int
	if err := reopened.DB.QueryRow("SELECT count(*) FROM staging_rows WHERE status = 'invalid'").Scan(&invalidRows); err != nil || invalidRows != 200 {
		t.Fatal("journal rollback lost failed staging")
	}
}

func TestOrdinarySQLiteCannotReadFinancialDatabase(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("ordinary SQLite CLI is unavailable; encrypted driver/key checks still run")
	}
	// scripts/go.sh sets a loader path for the Go SQLCipher driver on Linux.
	// The system CLI must use its own ordinary SQLite library for this check.
	command := func(args ...string) *exec.Cmd {
		cmd := exec.Command(sqlite, args...)
		for _, variable := range os.Environ() {
			name, _, _ := strings.Cut(variable, "=")
			if name != "LD_LIBRARY_PATH" && name != "DYLD_LIBRARY_PATH" && name != "DYLD_FALLBACK_LIBRARY_PATH" {
				cmd.Env = append(cmd.Env, variable)
			}
		}
		return cmd
	}
	version, err := command(":memory:", "PRAGMA cipher_version;").Output()
	if err != nil || strings.TrimSpace(string(version)) != "" {
		t.Fatal("sqlite3 CLI must be ordinary SQLite for this check")
	}
	store, path, _ := testStore(t)
	execSQL(t, store, "INSERT INTO payees (name, normalized_name) VALUES (?, ?)", financialMarker, strings.ToLower(financialMarker))
	store.DB.Close()
	output, err := command("-readonly", path, "SELECT name FROM payees;").CombinedOutput()
	if err == nil || bytes.Contains(output, []byte(financialMarker)) || !bytes.Contains(output, []byte("not a database")) {
		t.Fatalf("ordinary SQLite did not reject encrypted financial data: %v", err)
	}
}

func TestConcurrentDatabaseStartupAndOwnerConstraint(t *testing.T) {
	store, path, keyFile := testStore(t)
	runtimeOnlyDatabase(t, store)
	store.DB.Close()
	ctx := context.Background()
	results := make(chan error, 4)
	for range 4 {
		go func() {
			reopened, err := Open(ctx, path, keyFile)
			if err == nil {
				err = reopened.DB.Close()
			}
			results <- err
		}()
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(ctx, path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	var count int
	if err := reopened.DB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != migrationCount(t) {
		t.Fatalf("concurrent migration history: %d, %v", count, err)
	}
	// The singleton constraint also applies independently of HTTP onboarding.
	// Separate stores hold separate connections, as separate processes would.
	stores := make([]*Store, 4)
	for index := range stores {
		if stores[index], err = Open(ctx, path, keyFile); err != nil {
			t.Fatal(err)
		}
		defer stores[index].DB.Close()
	}
	start := make(chan struct{})
	for _, concurrent := range stores {
		go func() {
			<-start
			_, err := concurrent.DB.Exec("INSERT INTO owner (id, email, password_hash) VALUES (1, 'synthetic@example.test', 'synthetic-hash')")
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for range stores {
		if err := <-results; err == nil {
			succeeded++
		}
	}
	var owners int
	if err := reopened.DB.QueryRow("SELECT count(*) FROM owner").Scan(&owners); err != nil || succeeded != 1 || owners != 1 {
		t.Fatalf("expected one owner insert, got %d inserts and %d owners", succeeded, owners)
	}
}
