package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"budget-tracking/backend/internal/catalog"
	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/notify"
	"budget-tracking/backend/internal/rates"
)

const (
	// chunkSize bounds the records read into memory and written per
	// staging or validation transaction.
	chunkSize = 500
	maxRows   = 200_000
	// maxListedErrors caps the validation errors kept in import history;
	// every invalid row keeps its own errors in staging.
	maxListedErrors = 200
	maxUnknown      = 50
	// SampleSize is the most records ever sent to TypeSafe for one file.
	SampleSize = 3
)

// Staging row statuses.
const (
	RowPending   = "pending"
	RowValid     = "valid"
	RowInvalid   = "invalid"
	RowExcluded  = "excluded"
	RowDuplicate = "duplicate"
	RowConflict  = "conflict"
)

// ErrorKind classifies errors a caller reports to the owner.
type ErrorKind int

const (
	Invalid ErrorKind = iota
	NotFound
	Busy
	Conflict
)

// Error carries a message written by this package. It never includes row
// values, so callers can show it and log it.
type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string { return e.Message }

func userError(message string) error { return &Error{Kind: Invalid, Message: message} }

var (
	errBusy     = &Error{Kind: Busy, Message: "Another import step is running. Try again when it finishes."}
	errNotFound = &Error{Kind: NotFound, Message: "Import not found"}
	errChanged  = &Error{Kind: Conflict, Message: "The ledger changed since validation. Review the refreshed rows and commit again."}
)

// ListedError is one entry of a batch's validation errors. Row 0 is a
// problem with the whole file.
type ListedError struct {
	Row     int64  `json:"row"`
	Message string `json:"message"`
}

type UnknownAction struct {
	Value string `json:"value"`
	Rows  int64  `json:"rows"`
}

// Pipeline stages, validates, and commits CSV imports. One step runs at a
// time so validation and commit always see a consistent batch.
type Pipeline struct {
	store *database.Store
	mu    sync.Mutex
	// CommitHook runs after each ledger row is inserted during commit. Tests
	// use it to interrupt the final transaction.
	CommitHook func(inserted int) error
	// Now dates missing-rate messages.
	Now func() time.Time
	// TypeSafe suggests categories for new payees at commit when the owner
	// turned that on. Nil never asks.
	TypeSafe *integrations.Client
	// Settings holds the TypeSafe key, which all users share. Nil means the
	// pipeline's own store.
	Settings *database.Store
}

func New(store *database.Store) *Pipeline { return &Pipeline{store: store, Now: time.Now} }

type txn struct {
	*sql.Tx
	*dbgen.Queries
}

func (p *Pipeline) inTx(ctx context.Context, fn func(txn) error) error {
	tx, err := p.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(txn{tx, p.store.Queries.WithTx(tx)}); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Pipeline) lock() error {
	if !p.mu.TryLock() {
		return errBusy
	}
	return nil
}

// Source identifies where a format was seen, so each account keeps its own
// cached mappings.
func Source(accountID int64) string { return fmt.Sprintf("account:%d", accountID) }

func encode(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// UsableAccount reports why an account cannot receive imports.
func UsableAccount(ctx context.Context, queries *dbgen.Queries, id int64) (dbgen.Account, error) {
	account, err := queries.GetAccount(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return account, userError("Choose an account")
	case err != nil:
		return account, err
	case account.Archived == 1:
		return account, userError("This account is archived")
	}
	return account, nil
}

// Stage streams the CSV into encrypted staging rows in bounded chunks. The
// file itself is never written to disk. A read or parse failure marks the
// batch failed and removes its rows. When a cached or built-in mapping fits
// the format, the batch is validated right away.
func (p *Pipeline) Stage(ctx context.Context, accountID int64, filename string, body io.Reader, location *time.Location) (int64, error) {
	if err := p.lock(); err != nil {
		return 0, err
	}
	defer p.mu.Unlock()
	if _, err := UsableAccount(ctx, p.store.Queries, accountID); err != nil {
		return 0, err
	}
	batch, err := p.store.Queries.CreateImportBatch(ctx, dbgen.CreateImportBatchParams{SourceAccountID: accountID, Filename: filename})
	if err != nil {
		return 0, err
	}
	count, err := p.stream(ctx, batch.ID, body)
	if err != nil {
		message := "The upload was interrupted. Upload the file again."
		var known *Error
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &known):
			message = known.Message
		case errors.As(err, &tooLarge):
			message = fmt.Sprintf("The file is larger than %d MiB", tooLarge.Limit>>20)
		}
		if cleanup := p.failStaging(batch.ID, message); cleanup != nil {
			return batch.ID, cleanup
		}
		return batch.ID, nil
	}
	if err := p.store.Queries.FinishStaging(ctx, dbgen.FinishStagingParams{RowCount: count, ID: batch.ID}); err != nil {
		return batch.ID, err
	}
	batch, err = p.store.Queries.GetImportBatch(ctx, batch.ID)
	if err != nil {
		return batch.ID, err
	}
	mapping, found, err := p.knownMapping(ctx, batch)
	if err != nil || !found {
		return batch.ID, err
	}
	if err := p.saveMapping(ctx, batch, mapping); err != nil {
		return batch.ID, err
	}
	return batch.ID, p.validate(ctx, batch.ID, location)
}

func (p *Pipeline) stream(ctx context.Context, batchID int64, body io.Reader) (int64, error) {
	reader, err := NewReader(body)
	if err != nil {
		return 0, err
	}
	err = p.store.Queries.SetBatchFormat(ctx, dbgen.SetBatchFormatParams{
		HeaderJson:        sql.NullString{String: encode(reader.Header()), Valid: true},
		FormatFingerprint: sql.NullString{String: reader.Fingerprint(), Valid: true}, ID: batchID,
	})
	if err != nil {
		return 0, err
	}
	var count int64
	chunk := make([][]string, 0, chunkSize)
	flush := func() error {
		err := p.inTx(ctx, func(q txn) error {
			for index, record := range chunk {
				err := q.StageRow(ctx, dbgen.StageRowParams{BatchID: batchID, RowNumber: count + int64(index) + 1,
					RawRecordJson: encode(record), Status: RowPending, ValidationErrorsJson: "[]"})
				if err != nil {
					return err
				}
			}
			return nil
		})
		count += int64(len(chunk))
		chunk = chunk[:0]
		return err
	}
	for {
		record, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, err
		}
		if count+int64(len(chunk)) >= maxRows {
			return count, userError(fmt.Sprintf("The file has more than %d records", maxRows))
		}
		chunk = append(chunk, record)
		if len(chunk) == chunkSize {
			if err := flush(); err != nil {
				return count, err
			}
		}
	}
	if err := flush(); err != nil {
		return count, err
	}
	if count == 0 {
		return 0, userError("The file has a header but no records")
	}
	return count, nil
}

// failStaging runs even when the request was canceled, so an interrupted
// upload never leaves partial rows behind.
func (p *Pipeline) failStaging(batchID int64, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.inTx(ctx, func(q txn) error {
		if _, err := q.GetImportBatch(ctx, batchID); errors.Is(err, sql.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if err := q.DeleteStagingRows(ctx, batchID); err != nil {
			return err
		}
		return q.FailStaging(ctx, dbgen.FailStagingParams{ValidationErrorsJson: encode([]ListedError{{Message: message}}), ID: batchID})
	})
}

// Recover runs at startup. A batch still streaming when the process stopped
// can never finish, so it is marked failed and its partial rows are removed.
// Commits need no recovery: they run in one database transaction.
func Recover(ctx context.Context, store *database.Store) (int, error) {
	var recovered []int64
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := store.Queries.WithTx(tx)
	recovered, err = q.RecoverInterruptedStaging(ctx, encode([]ListedError{{Message: "The upload was interrupted by a restart. Upload the file again."}}))
	if err != nil {
		return 0, err
	}
	for _, id := range recovered {
		if err := q.DeleteStagingRows(ctx, id); err != nil {
			return 0, err
		}
	}
	return len(recovered), tx.Commit()
}

// Header returns the batch's column names.
func Header(batch dbgen.ImportBatch) []string {
	var header []string
	if batch.HeaderJson.Valid {
		_ = json.Unmarshal([]byte(batch.HeaderJson.String), &header)
	}
	return header
}

// BatchMapping returns the mapping snapshot the batch uses, if any.
func BatchMapping(batch dbgen.ImportBatch) (Mapping, bool) {
	if !batch.MappingSnapshotJson.Valid {
		return Mapping{}, false
	}
	mapping, err := ParseMapping([]byte(batch.MappingSnapshotJson.String))
	return mapping, err == nil
}

// knownMapping returns the latest cached mapping for this source and
// format, or a built-in mapping for a verified export layout.
func (p *Pipeline) knownMapping(ctx context.Context, batch dbgen.ImportBatch) (Mapping, bool, error) {
	cached, err := p.store.Queries.LatestMapping(ctx, dbgen.LatestMappingParams{
		Source: Source(batch.SourceAccountID), FormatFingerprint: batch.FormatFingerprint.String})
	switch {
	case err == nil:
		mapping, err := ParseMapping([]byte(cached.MappingJson))
		if err == nil && len(mapping.Validate(Header(batch))) == 0 {
			return mapping, true, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return Mapping{}, false, err
	}
	mapping, found := Trading212(Header(batch))
	if !found {
		mapping, found = SlovakBank(Header(batch))
	}
	return mapping, found, nil
}

// saveMapping caches the mapping as a new version for the source and format
// unless it equals the latest one, and snapshots it on the batch.
func (p *Pipeline) saveMapping(ctx context.Context, batch dbgen.ImportBatch, mapping Mapping) error {
	canonical := mapping.Canonical()
	return p.inTx(ctx, func(q txn) error {
		key := dbgen.LatestMappingParams{Source: Source(batch.SourceAccountID), FormatFingerprint: batch.FormatFingerprint.String}
		latest, err := q.LatestMapping(ctx, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err != nil || latest.MappingJson != canonical {
			latest, err = q.CreateMapping(ctx, dbgen.CreateMappingParams{Source: key.Source,
				FormatFingerprint: key.FormatFingerprint, Version: latest.Version + 1, MappingJson: canonical})
			if err != nil {
				return err
			}
		}
		return q.SetBatchMapping(ctx, dbgen.SetBatchMappingParams{MappingID: sql.NullInt64{Int64: latest.ID, Valid: true},
			MappingSnapshotJson: sql.NullString{String: canonical, Valid: true}, ID: batch.ID})
	})
}

func (p *Pipeline) editableBatch(ctx context.Context, id int64) (dbgen.ImportBatch, error) {
	batch, err := p.store.Queries.GetImportBatch(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return batch, errNotFound
	case err != nil:
		return batch, err
	case batch.Status == "committed":
		return batch, &Error{Kind: Conflict, Message: "This import is already committed"}
	case !batch.StagedAt.Valid:
		return batch, &Error{Kind: Conflict, Message: "This upload did not finish. Upload the file again."}
	}
	return batch, nil
}

// SetMapping validates the mapping against the header, caches it, and
// validates the whole batch with it.
func (p *Pipeline) SetMapping(ctx context.Context, batchID int64, mapping Mapping, location *time.Location) error {
	if err := p.lock(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	batch, err := p.editableBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if problems := mapping.Validate(Header(batch)); len(problems) > 0 {
		return userError(strings.Join(problems, ". "))
	}
	if err := p.saveMapping(ctx, batch, mapping); err != nil {
		return err
	}
	return p.validate(ctx, batchID, location)
}

// Sample returns the header, at most SampleSize records, and the account
// currency, the only data ever sent to TypeSafe for a file.
func (p *Pipeline) Sample(ctx context.Context, batchID int64) ([]string, [][]string, string, error) {
	batch, err := p.editableBatch(ctx, batchID)
	if err != nil {
		return nil, nil, "", err
	}
	account, err := p.store.Queries.GetAccount(ctx, batch.SourceAccountID)
	if err != nil {
		return nil, nil, "", err
	}
	records, err := SampleRecords(ctx, p.store.Queries, batchID)
	if err != nil {
		return nil, nil, "", err
	}
	return Header(batch), records, account.Currency, nil
}

// SampleRecords returns the file values of the first SampleSize staged rows.
func SampleRecords(ctx context.Context, queries *dbgen.Queries, batchID int64) ([][]string, error) {
	rows, err := queries.ListStagingRows(ctx, dbgen.ListStagingRowsParams{BatchID: batchID, AfterRow: 0, PageSize: SampleSize})
	if err != nil {
		return nil, err
	}
	records := make([][]string, 0, len(rows))
	for _, row := range rows {
		var values []string
		if err := json.Unmarshal([]byte(row.RawRecordJson), &values); err != nil {
			return nil, err
		}
		records = append(records, values)
	}
	return records, nil
}

// Examples returns each column's first non-empty value in the records, so
// the owner can recognize a column by what it holds rather than its name.
func Examples(header []string, records [][]string) []string {
	examples := make([]string, len(header))
	for index := range examples {
		for _, values := range records {
			if index < len(values) && strings.TrimSpace(values[index]) != "" {
				examples[index] = strings.TrimSpace(values[index])
				break
			}
		}
	}
	return examples
}

// CheckSuggestion validates a suggested mapping against the header and
// applies it to the sample. A mapping that does not fit the file is
// rejected. The suggestion's notes and sample rows the mapping cannot read
// come back as notes for the owner, because a suggestion only fills the form:
// nothing is saved until the owner saves the mapping, and every row is
// validated then. Unknown actions are left for the owner to map.
func CheckSuggestion(mapping *Mapping, notes []string, header []string, sample [][]string, location *time.Location) ([]string, error) {
	if problems := mapping.Validate(header); len(problems) > 0 {
		return nil, userError("The suggested mapping was rejected: " + strings.Join(problems, ". "))
	}
	for index, values := range sample {
		result := mapping.Interpret(values, location)
		for _, problem := range result.Errors {
			if result.Unknown == "" || !strings.HasPrefix(problem, "The action") {
				notes = append(notes, fmt.Sprintf("Sample row %d: %s", index+1, problem))
			}
		}
	}
	return nonNil(notes), nil
}

// CorrectRow excludes a row or replaces its values. The batch must be
// validated again before it can commit.
func (p *Pipeline) CorrectRow(ctx context.Context, batchID, rowNumber int64, excluded bool, values []string) error {
	if err := p.lock(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	batch, err := p.editableBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if values != nil && len(values) != len(Header(batch)) {
		return userError(fmt.Sprintf("Enter %d values, one per column", len(Header(batch))))
	}
	return p.inTx(ctx, func(q txn) error {
		if _, err := q.GetStagingRow(ctx, dbgen.GetStagingRowParams{BatchID: batchID, RowNumber: rowNumber}); errors.Is(err, sql.ErrNoRows) {
			return &Error{Kind: NotFound, Message: "Row not found"}
		} else if err != nil {
			return err
		}
		corrected := sql.NullString{}
		if values != nil {
			corrected = sql.NullString{String: encode(values), Valid: true}
		}
		err := q.CorrectStagingRow(ctx, dbgen.CorrectStagingRowParams{ExcludedByOwner: boolInt(excluded),
			CorrectedRecordJson: corrected, BatchID: batchID, RowNumber: rowNumber})
		if err != nil {
			return err
		}
		return q.InvalidateValidation(ctx, batchID)
	})
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// Delete removes an uncommitted batch and its staging rows.
func (p *Pipeline) Delete(ctx context.Context, batchID int64) error {
	if err := p.lock(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	return p.inTx(ctx, func(q txn) error {
		if _, err := q.GetImportBatch(ctx, batchID); errors.Is(err, sql.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if err := q.DeleteStagingRows(ctx, batchID); err != nil {
			return err
		}
		// A scheduled import keeps its job, which the owner can run again.
		if err := q.DetachJobBatch(ctx, sql.NullInt64{Int64: batchID, Valid: true}); err != nil {
			return err
		}
		if err := q.DetachFailureBatch(ctx, sql.NullInt64{Int64: batchID, Valid: true}); err != nil {
			return err
		}
		deleted, err := q.DeleteImportBatch(ctx, batchID)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return &Error{Kind: Conflict, Message: "Only imports that were not committed can be removed"}
		}
		return nil
	})
}

// Validate checks every staged row with the batch mapping.
func (p *Pipeline) Validate(ctx context.Context, batchID int64, location *time.Location) error {
	if err := p.lock(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	if _, err := p.editableBatch(ctx, batchID); err != nil {
		return err
	}
	return p.validate(ctx, batchID, location)
}

type summary struct {
	counts       map[string]int64
	errors       []ListedError
	unknown      []UnknownAction
	first, last  string
	errorRows    int64
	unknownIndex map[string]int
}

func (s *summary) add(row int64, o outcome) {
	s.counts[o.status]++
	if o.status == RowInvalid {
		s.errorRows++
		for _, message := range o.errors {
			if len(s.errors) < maxListedErrors {
				s.errors = append(s.errors, ListedError{Row: row, Message: message})
			}
		}
	}
	if o.unknown != "" {
		if index, ok := s.unknownIndex[normalizeAction(o.unknown)]; ok {
			s.unknown[index].Rows++
		} else if len(s.unknown) < maxUnknown {
			s.unknownIndex[normalizeAction(o.unknown)] = len(s.unknown)
			s.unknown = append(s.unknown, UnknownAction{Value: o.unknown, Rows: 1})
		}
	}
	if o.record != nil && o.record.Date != "" && o.status != RowInvalid {
		if s.first == "" || o.record.Date < s.first {
			s.first = o.record.Date
		}
		if o.record.Date > s.last {
			s.last = o.record.Date
		}
	}
}

// validate classifies rows in chunks, each in a short transaction so other
// requests are not blocked by a large file.
func (p *Pipeline) validate(ctx context.Context, batchID int64, location *time.Location) error {
	batch, err := p.store.Queries.GetImportBatch(ctx, batchID)
	if err != nil {
		return err
	}
	mapping, ok := BatchMapping(batch)
	if !ok {
		return &Error{Kind: Conflict, Message: "Choose a mapping before validating"}
	}
	pass, err := p.newRun(ctx, batch, &mapping, location)
	if err != nil {
		return err
	}
	result := summary{counts: map[string]int64{}, unknownIndex: map[string]int{}}
	var after int64
	for {
		var page []dbgen.StagingRow
		err := p.inTx(ctx, func(q txn) error {
			page, err = q.ListStagingRows(ctx, dbgen.ListStagingRowsParams{BatchID: batchID, AfterRow: after, PageSize: chunkSize})
			if err != nil {
				return err
			}
			for _, row := range page {
				o, err := classify(ctx, q, pass, row)
				if err != nil {
					return err
				}
				result.add(row.RowNumber, o)
				if err := o.store(ctx, q, row); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(page) < chunkSize {
			break
		}
		after = page[len(page)-1].RowNumber
	}
	status := "validating"
	if result.errorRows > 0 {
		status = "failed"
	}
	params := dbgen.SetValidationResultParams{Status: status, ExcludedCount: result.counts[RowExcluded],
		DuplicateCount: result.counts[RowDuplicate], ErrorCount: result.errorRows, ConflictCount: result.counts[RowConflict],
		ValidationErrorsJson: encode(nonNil(result.errors)), UnknownActionsJson: encode(nonNil(result.unknown)), ID: batchID}
	if result.first != "" {
		end, _ := time.Parse(time.DateOnly, result.last)
		params.PeriodStart = sql.NullString{String: result.first, Valid: true}
		params.PeriodEnd = sql.NullString{String: end.AddDate(0, 0, 1).Format(time.DateOnly), Valid: true}
	}
	return p.store.Queries.SetValidationResult(ctx, params)
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// outcome is the classification of one staged row.
type outcome struct {
	status     string
	record     *Record
	errors     []string
	unknown    string
	externalID string
	existing   int64 // ledger transaction for a conflict
}

func (o outcome) store(ctx context.Context, q txn, row dbgen.StagingRow) error {
	parsed := sql.NullString{}
	if o.record != nil {
		parsed = sql.NullString{String: encode(o.record), Valid: true}
	}
	return q.UpdateStagingRow(ctx, dbgen.UpdateStagingRowParams{ParsedRecordJson: parsed, Status: o.status,
		ValidationErrorsJson: encode(nonNil(o.errors)), ExternalID: sql.NullString{String: o.externalID, Valid: o.externalID != ""},
		BatchID: row.BatchID, RowNumber: row.RowNumber})
}

// RowValues returns the values validation uses: the owner's correction when
// present, otherwise the uploaded record.
func RowValues(raw string, corrected sql.NullString) ([]string, error) {
	source := raw
	if corrected.Valid {
		source = corrected.String
	}
	var values []string
	err := json.Unmarshal([]byte(source), &values)
	return values, err
}

// run holds what every row of one validation or commit pass needs.
type run struct {
	batch    dbgen.ImportBatch
	mapping  *Mapping
	header   []string
	location *time.Location
	currency string // the account currency
	now      time.Time
}

func (p *Pipeline) newRun(ctx context.Context, batch dbgen.ImportBatch, mapping *Mapping, location *time.Location) (run, error) {
	account, err := p.store.Queries.GetAccount(ctx, batch.SourceAccountID)
	if err != nil {
		return run{}, err
	}
	return run{batch: batch, mapping: mapping, header: Header(batch), location: location, currency: account.Currency, now: p.Now()}, nil
}

// classify interprets a row and compares it with earlier rows of the file
// and with the ledger. Only rows that are new to the ledger are valid, and
// only valid rows need an exchange rate.
func classify(ctx context.Context, q txn, r run, row dbgen.StagingRow) (outcome, error) {
	batch, mapping, header := r.batch, r.mapping, r.header
	if row.ExcludedByOwner == 1 {
		return outcome{status: RowExcluded}, nil
	}
	values, err := RowValues(row.RawRecordJson, row.CorrectedRecordJson)
	if err != nil {
		return outcome{}, err
	}
	if len(values) != len(header) {
		return outcome{status: RowInvalid, errors: []string{fmt.Sprintf("This row has %d values; the header has %d columns", len(values), len(header))}}, nil
	}
	result := mapping.Interpret(values, r.location)
	if len(result.Errors) == 0 && result.Record.Currency != r.currency {
		result.Errors = []string{fmt.Sprintf("This row is in %s, but the account is in %s. Upload it to an account in %[1]s.",
			result.Record.Currency, r.currency)}
	}
	o := outcome{record: &result.Record, errors: result.Errors, unknown: result.Unknown}
	switch {
	case result.Record.Kind == KindSkip:
		o.status = RowExcluded
		return o, nil
	case len(result.Errors) > 0:
		o.status = RowInvalid
		return o, nil
	}
	if mapping.ExternalIDColumn == nil {
		// Earlier identical rows share the base; "$" sorts right after "#".
		base := generatedID(values)
		seen, err := q.CountEarlierIDs(ctx, dbgen.CountEarlierIDsParams{BatchID: row.BatchID,
			Low: sql.NullString{String: base + "#", Valid: true}, High: sql.NullString{String: base + "$", Valid: true}, BeforeRow: row.RowNumber})
		if err != nil {
			return o, err
		}
		result.Record.ExternalID = fmt.Sprintf("%s#%d", base, seen)
	}
	o.externalID = result.Record.ExternalID

	earlier, err := q.EarlierStagedID(ctx, dbgen.EarlierStagedIDParams{BatchID: row.BatchID,
		ExternalID: sql.NullString{String: o.externalID, Valid: true}, BeforeRow: row.RowNumber})
	switch {
	case err == nil:
		previous, err := RowValues(earlier.RawRecordJson, earlier.CorrectedRecordJson)
		if err != nil {
			return o, err
		}
		if slices.Equal(previous, values) {
			o.status = RowDuplicate
			return o, nil
		}
		o.status, o.externalID = RowInvalid, ""
		o.errors = []string{fmt.Sprintf("Row %d has the same ID with different values", earlier.RowNumber)}
		return o, nil
	case !errors.Is(err, sql.ErrNoRows):
		return o, err
	}

	existing, err := q.FindTransactionBySourceID(ctx, dbgen.FindTransactionBySourceIDParams{
		SourceAccountID: sql.NullInt64{Int64: batch.SourceAccountID, Valid: true}, ExternalID: sql.NullString{String: o.externalID, Valid: true}})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return convert(ctx, q, r, o)
	case err != nil:
		return o, err
	case existing.DeletedAt.Valid:
		// The owner deleted it; a reimport never brings it back.
		o.status = RowDuplicate
		return o, nil
	}
	original, err := q.GetImportedOriginal(ctx, existing.ID)
	if errors.Is(err, sql.ErrNoRows) {
		o.status = RowDuplicate
		return o, nil
	}
	if err != nil {
		return o, err
	}
	if sameSource(original, result.Record) {
		o.status = RowDuplicate
		return o, nil
	}
	seen, err := q.SourceConflictSeen(ctx, dbgen.SourceConflictSeenParams{TransactionID: existing.ID, IncomingRecordJson: encode(result.Record)})
	if err != nil {
		return o, err
	}
	if seen {
		o.status = RowDuplicate
		return o, nil
	}
	o.status, o.existing = RowConflict, existing.ID
	return o, nil
}

// convert marks a new row valid once its EUR amount is known. Foreign rows
// need a cached rate for their local date; nothing is downloaded here.
func convert(ctx context.Context, q txn, r run, o outcome) (outcome, error) {
	o.status = RowValid
	if o.record.Currency == "EUR" {
		return o, nil
	}
	amount, err := ledger.NewMoney(o.record.AmountMinor, o.record.Currency)
	if err != nil {
		return o, err
	}
	conversion, err := rates.Convert(ctx, q.Queries, amount, o.record.Date, r.now)
	var missing *rates.MissingRate
	switch {
	case errors.As(err, &missing):
		o.status, o.externalID, o.errors = RowInvalid, "", []string{missing.Error()}
		return o, nil
	case err != nil:
		return o, err
	}
	o.record.Conversion = &conversion
	return o, nil
}

// sameSource compares the source values of a reappearing ID. Owner edits
// live on the transaction, so they never cause a conflict.
func sameSource(original dbgen.ImportedOriginal, record Record) bool {
	sameAmount := original.AmountText == record.AmountText
	if original.AmountMinor.Valid {
		sameAmount = original.AmountMinor.Int64 == record.AmountMinor
	}
	sameTime := original.TimestampText == record.TimestampText
	if original.OccurredAt.Valid {
		sameTime = original.OccurredAt.String == record.OccurredAt
	}
	return sameAmount && sameTime && original.Currency == record.Currency &&
		normalizeAction(original.Action) == normalizeAction(record.Action) &&
		original.MerchantName == record.Payee && original.MerchantCategory == record.Category && original.Notes == record.Notes
}

// commitMode is who looked at a batch before it is committed, which decides
// whether the batch still waits for the owner's confirmation.
type commitMode int

const (
	// commitForReview leaves new transactions for review with a notification.
	commitForReview commitMode = iota
	// commitReviewed confirms the batch the owner reviewed in the app, unless
	// Jev picked categories, which the owner then checks.
	commitReviewed
	// commitConfirmed confirms an automatic Trading 212 sync right away.
	commitConfirmed
)

// Commit promotes every valid row to the ledger in one transaction, with
// the payees and categories they need. Rows are classified again inside the
// transaction; if any result differs from validation, nothing is written.
// New transactions wait for review.
func (p *Pipeline) Commit(ctx context.Context, batchID int64, location *time.Location) error {
	return p.commit(ctx, batchID, location, commitForReview)
}

// CommitReviewed imports rows the owner has reviewed and confirms the batch
// in the same transaction, without creating a review notification. When Jev
// picked categories, the batch waits for the owner to check them instead.
func (p *Pipeline) CommitReviewed(ctx context.Context, batchID int64, location *time.Location) error {
	return p.commit(ctx, batchID, location, commitReviewed)
}

// CommitConfirmed imports an automatic sync and confirms the batch in the
// same transaction, so its transactions need no review.
func (p *Pipeline) CommitConfirmed(ctx context.Context, batchID int64, location *time.Location) error {
	return p.commit(ctx, batchID, location, commitConfirmed)
}

func (p *Pipeline) commit(ctx context.Context, batchID int64, location *time.Location, mode commitMode) error {
	if err := p.lock(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	batch, err := p.editableBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if batch.Status != "validating" {
		return &Error{Kind: Conflict, Message: "Validate the import and resolve every invalid row before committing"}
	}
	if _, err := UsableAccount(ctx, p.store.Queries, batch.SourceAccountID); err != nil {
		return err
	}
	mapping, ok := BatchMapping(batch)
	if !ok {
		return &Error{Kind: Conflict, Message: "Choose a mapping before committing"}
	}
	pass, err := p.newRun(ctx, batch, &mapping, location)
	if err != nil {
		return err
	}
	picks := p.suggestCategories(ctx, batchID)
	err = p.inTx(ctx, func(q txn) error {
		inserted, suggested := 0, 0
		var after int64
		for {
			page, err := q.ListStagingRows(ctx, dbgen.ListStagingRowsParams{BatchID: batchID, AfterRow: after, PageSize: chunkSize})
			if err != nil {
				return err
			}
			for _, row := range page {
				o, err := classify(ctx, q, pass, row)
				if err != nil {
					return err
				}
				// A valid row must also keep the values and conversion the
				// owner reviewed.
				if o.status != row.Status || (o.status == RowValid && encode(o.record) != row.ParsedRecordJson.String) {
					return errChanged
				}
				switch o.status {
				case RowValid:
					picked, err := insert(ctx, q, batch, pass.header, row, o.record, picks)
					if err != nil {
						return err
					}
					inserted++
					if picked {
						suggested++
					}
					if p.CommitHook != nil {
						if err := p.CommitHook(inserted); err != nil {
							return err
						}
					}
				case RowConflict:
					err := q.CreateSourceConflict(ctx, dbgen.CreateSourceConflictParams{TransactionID: o.existing,
						BatchID: batchID, RowNumber: row.RowNumber, IncomingRecordJson: encode(o.record)})
					if err != nil {
						return err
					}
					if err := q.FlagSourceConflict(ctx, o.existing); err != nil {
						return err
					}
				}
			}
			if len(page) < chunkSize {
				break
			}
			after = page[len(page)-1].RowNumber
		}
		if err := q.CommitBatch(ctx, dbgen.CommitBatchParams{InsertedCount: int64(inserted), ID: batchID}); err != nil {
			return err
		}
		if confirm := mode == commitConfirmed || (mode == commitReviewed && suggested == 0); confirm && inserted > 0 {
			if _, err := q.ConfirmBatch(ctx, dbgen.ConfirmBatchParams{
				ConfirmedAt: sql.NullString{String: p.Now().UTC().Format(time.RFC3339), Valid: true}, ID: batchID,
			}); err != nil {
				return err
			}
		}
		return finishCommit(ctx, q, batch, int64(inserted), p.Now(), mode == commitForReview)
	})
	if errors.Is(err, errChanged) {
		if refresh := p.validate(ctx, batchID, location); refresh != nil {
			return refresh
		}
	}
	return err
}

// finishCommit records the review notification in the commit transaction
// and completes a scheduled job waiting for this batch, dismissing its
// failure notification.
func finishCommit(ctx context.Context, q txn, batch dbgen.ImportBatch, inserted int64, now time.Time, forReview bool) error {
	if inserted > 0 && forReview {
		if err := notify.CreateReview(ctx, q.Queries, batch, inserted, now); err != nil {
			return err
		}
	}
	jobs, err := q.CompleteJobsForBatch(ctx, sql.NullInt64{Int64: batch.ID, Valid: true})
	if err != nil {
		return err
	}
	dismissed := sql.NullString{String: now.UTC().Format(time.RFC3339), Valid: true}
	for _, job := range jobs {
		err := q.DismissJobFailure(ctx, dbgen.DismissJobFailureParams{DismissedAt: dismissed, JobID: sql.NullInt64{Int64: job, Valid: true}})
		if err != nil {
			return err
		}
	}
	return nil
}

// insert writes one ledger transaction and reports whether its category is
// Jev's pick.
func insert(ctx context.Context, q txn, batch dbgen.ImportBatch, header []string, row dbgen.StagingRow, record *Record, picks map[string]pick) (bool, error) {
	var payee, category sql.NullInt64
	var hint *pick
	// Names without letters or digits, such as "-", leave the payee empty.
	if name := ledger.DisplayName(record.Payee); ledger.ValidName(name) {
		id, _, err := catalog.Payees.Resolve(ctx, q, name)
		if err != nil {
			return false, err
		}
		payee = sql.NullInt64{Int64: id, Valid: true}
	}
	if record.Kind != ledger.KindTransfer {
		id, err := catalog.Categorize(ctx, q, payee, record.Category)
		if err != nil {
			return false, err
		}
		if id, hint, err = applyPick(ctx, q, picks, record, payee, id); err != nil {
			return false, err
		}
		category = sql.NullInt64{Int64: id, Valid: true}
	}
	minorUnit, err := ledger.MinorUnits(record.Currency)
	if err != nil {
		return false, err
	}
	eurCents := record.AmountMinor
	if record.Currency != "EUR" {
		if record.Conversion == nil {
			return false, errors.New("a foreign row reached commit without a conversion")
		}
		eurCents = record.Conversion.EURCents
	}
	created, err := q.CreateTransaction(ctx, dbgen.CreateTransactionParams{
		AccountID: batch.SourceAccountID, SourceAccountID: sql.NullInt64{Int64: batch.SourceAccountID, Valid: true},
		ExternalID: sql.NullString{String: record.ExternalID, Valid: true}, ImportBatchID: sql.NullInt64{Int64: batch.ID, Valid: true},
		OccurredAt: record.OccurredAt, LocalDate: record.Date, Kind: record.Kind, AmountMinor: record.AmountMinor,
		Currency: record.Currency, MinorUnit: int64(minorUnit), EurCents: eurCents,
		PayeeID: payee, CategoryID: category, Notes: record.Notes, ReviewStatus: "needs_review",
	})
	if err != nil {
		return false, err
	}
	if hint != nil {
		if err := recordSuggestion(ctx, q, created.ID, hint); err != nil {
			return false, err
		}
	}
	if conversion := record.Conversion; conversion != nil {
		err := q.CreateTransactionConversion(ctx, dbgen.CreateTransactionConversionParams{TransactionID: created.ID,
			OriginalAmountMinor: record.AmountMinor, OriginalCurrency: record.Currency, OriginalMinorUnit: int64(minorUnit),
			EurCents: conversion.EURCents, Rate: conversion.Rate, RateDate: conversion.RateDate,
			ProviderPolicy: conversion.ProviderPolicy, RoundingRule: conversion.RoundingRule})
		if err != nil {
			return false, err
		}
	}
	raw := map[string]any{"header": header, "values": json.RawMessage(row.RawRecordJson)}
	if row.CorrectedRecordJson.Valid {
		raw["correctedValues"] = json.RawMessage(row.CorrectedRecordJson.String)
	}
	return hint != nil, q.RecordImportedOriginal(ctx, dbgen.RecordImportedOriginalParams{
		TransactionID: created.ID, AmountText: record.AmountText, Currency: record.Currency,
		TimestampText: record.TimestampText, Action: record.Action, MerchantName: record.Payee,
		MerchantCategory: record.Category, Notes: record.Notes,
		FeeAmountText: sql.NullString{String: record.FeeAmount, Valid: record.FeeAmount != ""},
		FeeCurrency:   sql.NullString{String: record.FeeCurrency, Valid: record.FeeAmount != ""},
		RawRecordJson: encode(raw), AmountMinor: sql.NullInt64{Int64: record.AmountMinor, Valid: true},
		OccurredAt: sql.NullString{String: record.OccurredAt, Valid: true},
	})
}
