package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
)

const (
	maxUploadBytes = 20 << 20
	// Uploads, validation, and commits of large files outlast the server's
	// default read and write timeouts, so those handlers extend them.
	uploadReadTimeout = 2 * time.Minute
	importWorkTimeout = 5 * time.Minute
	suggestTimeout    = 90 * time.Second
	importPageSize    = 50
	defaultRowPage    = 100
	maxRowPage        = 500
)

// Derived import states shown to the owner.
const (
	stateUploading       = "uploading"
	stateFailed          = "failed"
	stateNeedsMapping    = "needs_mapping"
	stateNeedsValidation = "needs_validation"
	stateNeedsAttention  = "needs_attention"
	stateReady           = "ready"
	stateCommitted       = "committed"
)

type importView struct {
	ID               int64                   `json:"id"`
	Account          accountRef              `json:"account"`
	Filename         string                  `json:"filename"`
	State            string                  `json:"state"`
	PeriodStart      *string                 `json:"periodStart"`
	PeriodEnd        *string                 `json:"periodEnd"`
	RowCount         int64                   `json:"rowCount"`
	InsertedCount    int64                   `json:"insertedCount"`
	ExcludedCount    int64                   `json:"excludedCount"`
	DuplicateCount   int64                   `json:"duplicateCount"`
	ErrorCount       int64                   `json:"errorCount"`
	ConflictCount    int64                   `json:"conflictCount"`
	ValidationErrors []imports.ListedError   `json:"validationErrors"`
	UnknownActions   []imports.UnknownAction `json:"unknownActions"`
	MappingVersion   *int64                  `json:"mappingVersion"`
	CreatedAt        string                  `json:"createdAt"`
	CommittedAt      *string                 `json:"committedAt"`
	ReviewStatus     string                  `json:"reviewStatus"`
	ConfirmedAt      *string                 `json:"confirmedAt"`
}

type importDetail struct {
	importView
	Header []string `json:"header"`
	// Examples holds each column's first non-empty value in the first rows.
	Examples   []string         `json:"examples"`
	Mapping    *imports.Mapping `json:"mapping"`
	RowCounts  map[string]int64 `json:"rowCounts"`
	CanSuggest bool             `json:"canSuggest"`
	// SuggestedCategoryCount is how many transactions have a category
	// TypeSafe Jev picked.
	SuggestedCategoryCount int64 `json:"suggestedCategoryCount"`
	// ScheduledPeriod is the month a scheduled Trading 212 import fetched.
	ScheduledPeriod *string `json:"scheduledPeriod"`
}

type stagingRowView struct {
	Row             int64           `json:"row"`
	Values          []string        `json:"values"`
	CorrectedValues []string        `json:"correctedValues"`
	Excluded        bool            `json:"excluded"`
	Status          string          `json:"status"`
	Errors          []string        `json:"errors"`
	Record          *imports.Record `json:"record"`
}

func importState(batch dbgen.ImportBatch) string {
	switch {
	case batch.Status == "committed":
		return stateCommitted
	case !batch.StagedAt.Valid && batch.Status == "failed":
		return stateFailed
	case !batch.StagedAt.Valid:
		return stateUploading
	case !batch.MappingID.Valid:
		return stateNeedsMapping
	case batch.Status == "failed":
		return stateNeedsAttention
	case batch.Status == "validating":
		return stateReady
	}
	return stateNeedsValidation
}

func decodeList[T any](text string) []T {
	items := []T{}
	_ = json.Unmarshal([]byte(text), &items)
	return items
}

func newImportView(batch dbgen.ImportBatch, account accountRef, version *int64) importView {
	return importView{
		ID: batch.ID, Account: account, Filename: batch.Filename, State: importState(batch),
		PeriodStart: nullText(batch.PeriodStart), PeriodEnd: nullText(batch.PeriodEnd),
		RowCount: batch.RowCount, InsertedCount: batch.InsertedCount, ExcludedCount: batch.ExcludedCount,
		DuplicateCount: batch.DuplicateCount, ErrorCount: batch.ErrorCount, ConflictCount: batch.ConflictCount,
		ValidationErrors: decodeList[imports.ListedError](batch.ValidationErrorsJson),
		UnknownActions:   decodeList[imports.UnknownAction](batch.UnknownActionsJson),
		MappingVersion:   version, CreatedAt: batch.CreatedAt, CommittedAt: nullText(batch.CommittedAt),
		ReviewStatus: batch.ReviewStatus, ConfirmedAt: nullText(batch.ConfirmedAt),
	}
}

// writeImportFailure reports pipeline errors, which carry messages written
// for the owner, and logs anything else by operation only.
func writeImportFailure(w http.ResponseWriter, err error, operation string) {
	var known *imports.Error
	var suggestion *integrations.SuggestError
	switch {
	case errors.As(err, &known):
		status := map[imports.ErrorKind]int{imports.Invalid: http.StatusUnprocessableEntity,
			imports.NotFound: http.StatusNotFound, imports.Busy: http.StatusConflict, imports.Conflict: http.StatusConflict}[known.Kind]
		writeError(w, status, known.Message)
	case errors.As(err, &suggestion):
		writeError(w, http.StatusBadGateway, suggestion.Message)
	default:
		writeFailure(w, err, operation)
	}
}

// extendDeadlines lets a long import step finish past the server timeouts.
func extendDeadlines(w http.ResponseWriter, read, write time.Duration) {
	controller := http.NewResponseController(w)
	if read > 0 {
		_ = controller.SetReadDeadline(time.Now().Add(read))
	}
	_ = controller.SetWriteDeadline(time.Now().Add(write))
}

func (a *api) typeSafeReady(ctx context.Context) (string, bool, error) {
	row, credentials, found, err := a.loadIntegration(ctx, a.main.Queries, typeSafe)
	if err != nil || !found {
		return "", false, err
	}
	return credentials.APIKey, row.Enabled == 1 && credentials.APIKey != "", nil
}

// typeSafeKey returns the saved TypeSafe key. The chat only needs the key,
// because a question is sent when the owner asks it.
func (a *api) typeSafeKey(ctx context.Context) (string, error) {
	_, credentials, found, err := a.loadIntegration(ctx, a.main.Queries, typeSafe)
	if err != nil || !found {
		return "", err
	}
	return credentials.APIKey, nil
}

func (a *api) importDetail(ctx context.Context, id int64) (importDetail, error) {
	batch, err := a.store.Queries.GetImportBatch(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return importDetail{}, &imports.Error{Kind: imports.NotFound, Message: "Import not found"}
	}
	if err != nil {
		return importDetail{}, err
	}
	account, err := a.store.Queries.GetAccount(ctx, batch.SourceAccountID)
	if err != nil {
		return importDetail{}, err
	}
	var version *int64
	if batch.MappingID.Valid {
		if mapping, err := a.store.Queries.GetMapping(ctx, batch.MappingID.Int64); err == nil {
			version = &mapping.Version
		}
	}
	detail := importDetail{
		importView: newImportView(batch, accountRef{ID: account.ID, Name: account.Name, Currency: account.Currency}, version),
		Header:     imports.Header(batch), RowCounts: map[string]int64{},
	}
	if detail.Header == nil {
		detail.Header = []string{}
	}
	records, err := imports.SampleRecords(ctx, a.store.Queries, id)
	if err != nil {
		return importDetail{}, err
	}
	detail.Examples = imports.Examples(detail.Header, records)
	if mapping, ok := imports.BatchMapping(batch); ok {
		detail.Mapping = &mapping
	}
	counts, err := a.store.Queries.CountStagingRows(ctx, id)
	if err != nil {
		return importDetail{}, err
	}
	for _, count := range counts {
		detail.RowCounts[count.Status] = count.RowCount
	}
	job, err := a.store.Queries.JobForBatch(ctx, sql.NullInt64{Int64: id, Valid: true})
	switch {
	case err == nil:
		detail.ScheduledPeriod = &job.PeriodKey
	case !errors.Is(err, sql.ErrNoRows):
		return importDetail{}, err
	}
	if detail.State == stateCommitted {
		if detail.SuggestedCategoryCount, err = imports.SuggestedCount(ctx, a.store.DB, id); err != nil {
			return importDetail{}, err
		}
	}
	state := detail.State
	if state != stateCommitted && state != stateFailed && state != stateUploading {
		if _, ready, err := a.typeSafeReady(ctx); err != nil {
			return importDetail{}, err
		} else {
			detail.CanSuggest = ready
		}
	}
	return detail, nil
}

func (a *api) writeImport(w http.ResponseWriter, r *http.Request, id int64, status int) {
	detail, err := a.importDetail(r.Context(), id)
	if err != nil {
		writeImportFailure(w, err, "load import")
		return
	}
	writeJSON(w, status, detail)
}

func validFilename(name string) bool {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// uploadImport streams a CSV body into staging. It is the one endpoint that
// accepts a body other than JSON. The file is never written to disk.
func (a *api) uploadImport(w http.ResponseWriter, r *http.Request, current *session) {
	media, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "text/csv" || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		writeError(w, http.StatusUnsupportedMediaType, "Upload a UTF-8 CSV file with Content-Type text/csv")
		return
	}
	query := r.URL.Query()
	fields := map[string]string{}
	accountID, err := strconv.ParseInt(query.Get("accountId"), 10, 64)
	if err != nil || accountID <= 0 {
		fields["accountId"] = "Choose an account"
	}
	filename := path.Base(strings.ReplaceAll(strings.TrimSpace(query.Get("filename")), "\\", "/"))
	if !validFilename(filename) || filename == "." || filename == "/" {
		fields["filename"] = "Give the file a name of at most 255 characters"
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	extendDeadlines(w, uploadReadTimeout, importWorkTimeout)
	body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
	id, err := a.imports.Stage(r.Context(), accountID, filename, body, a.location(current))
	if err != nil && id == 0 {
		var known *imports.Error
		if errors.As(err, &known) && known.Kind == imports.Invalid {
			writeFieldErrors(w, map[string]string{"accountId": known.Message})
			return
		}
		writeImportFailure(w, err, "stage import")
		return
	}
	if err != nil {
		writeImportFailure(w, err, "prepare import")
		return
	}
	a.writeImport(w, r, id, http.StatusCreated)
}

// importSorts are the import history's sort keys. States sort in workflow
// order, as importState derives them; uploads without a period sort last.
var importSorts = map[string]string{
	"filename": "b.filename COLLATE NOCASE",
	"account":  "a.name COLLATE NOCASE",
	"state": `CASE WHEN b.status = 'committed' THEN 6 WHEN b.staged_at IS NULL AND b.status = 'failed' THEN 0
    WHEN b.staged_at IS NULL THEN 1 WHEN b.mapping_id IS NULL THEN 2 WHEN b.status = 'failed' THEN 3
    WHEN b.status = 'validating' THEN 5 ELSE 4 END`,
	"period":         "b.period_start",
	"rowCount":       "b.row_count",
	"insertedCount":  "b.inserted_count",
	"duplicateCount": "b.duplicate_count",
	"problems":       "b.error_count + b.conflict_count",
	"createdAt":      "b.id",
}

func (a *api) listImports(w http.ResponseWriter, r *http.Request, _ *session) {
	values := r.URL.Query()
	page, sort, direction, fields := 1, "createdAt", "desc", map[string]string{}
	if value := values.Get("page"); value != "" {
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 || number > 1_000_000 {
			fields["page"] = "Choose a valid page"
		}
		page = number
	}
	parseSort(values, importSorts, &sort, &direction, fields)
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	ids, err := pageIDs(r.Context(), a.store.DB, `SELECT b.id FROM import_batches b JOIN accounts a ON a.id = b.source_account_id
ORDER BY `+importSorts[sort]+" "+direction+" NULLS LAST, b.id DESC LIMIT ? OFFSET ?", importPageSize, (page-1)*importPageSize)
	if err != nil {
		internalError(w, "list imports")
		return
	}
	rows, err := a.store.Queries.ListImportBatches(r.Context(), ids)
	if err != nil {
		internalError(w, "list imports")
		return
	}
	rows = inOrder(ids, rows, func(row dbgen.ListImportBatchesRow) int64 { return row.ID })
	total, err := a.store.Queries.CountImportBatches(r.Context())
	if err != nil {
		internalError(w, "count imports")
		return
	}
	views := make([]importView, 0, len(rows))
	for _, row := range rows {
		batch := dbgen.ImportBatch{ID: row.ID, SourceAccountID: row.SourceAccountID, MappingID: row.MappingID,
			MappingSnapshotJson: row.MappingSnapshotJson, Filename: row.Filename, PeriodStart: row.PeriodStart,
			PeriodEnd: row.PeriodEnd, Status: row.Status, ReviewStatus: row.ReviewStatus, RowCount: row.RowCount,
			InsertedCount: row.InsertedCount, ExcludedCount: row.ExcludedCount, DuplicateCount: row.DuplicateCount,
			ErrorCount: row.ErrorCount, ValidationErrorsJson: row.ValidationErrorsJson, CreatedAt: row.CreatedAt,
			CommittedAt: row.CommittedAt, ConfirmedAt: row.ConfirmedAt, StagedAt: row.StagedAt, ConflictCount: row.ConflictCount,
			UnknownActionsJson: row.UnknownActionsJson}
		view := newImportView(batch, accountRef{ID: row.SourceAccountID, Name: row.AccountName}, nil)
		// The list stays small; per-row details load with the batch.
		view.ValidationErrors, view.UnknownActions = []imports.ListedError{}, []imports.UnknownAction{}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": views, "total": total, "page": page, "pageSize": importPageSize})
}

func (a *api) getImport(w http.ResponseWriter, r *http.Request, _ *session) {
	if id, ok := pathID(w, r, "Import"); ok {
		a.writeImport(w, r, id, http.StatusOK)
	}
}

var rowStatuses = []string{"", imports.RowPending, imports.RowValid, imports.RowInvalid, imports.RowExcluded,
	imports.RowDuplicate, imports.RowConflict}

func (a *api) listImportRows(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	query := r.URL.Query()
	fields := map[string]string{}
	status := query.Get("status")
	if !contains(rowStatuses, status) {
		fields["status"] = "Choose a row status"
	}
	var after int64
	if value := query.Get("after"); value != "" {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number < 0 {
			fields["after"] = "Choose a valid row"
		}
		after = number
	}
	limit := defaultRowPage
	if value := query.Get("limit"); value != "" {
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 || number > maxRowPage {
			fields["limit"] = "Choose a page size from 1 to 500"
		}
		limit = number
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	if _, err := a.store.Queries.GetImportBatch(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Import not found")
		return
	} else if err != nil {
		internalError(w, "load import")
		return
	}
	// Fetch one extra row to tell whether another page exists.
	rows, err := a.store.Queries.ListStagingRowsByStatus(r.Context(), dbgen.ListStagingRowsByStatusParams{
		BatchID: id, Status: status, AfterRow: after, PageSize: int64(limit + 1)})
	if err != nil {
		internalError(w, "list import rows")
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	views := make([]stagingRowView, 0, len(rows))
	for _, row := range rows {
		view := stagingRowView{Row: row.RowNumber, Excluded: row.ExcludedByOwner == 1, Status: row.Status,
			Errors: decodeList[string](row.ValidationErrorsJson), Values: decodeList[string](row.RawRecordJson)}
		if row.CorrectedRecordJson.Valid {
			view.CorrectedValues = decodeList[string](row.CorrectedRecordJson.String)
		}
		if row.ParsedRecordJson.Valid {
			var record imports.Record
			if json.Unmarshal([]byte(row.ParsedRecordJson.String), &record) == nil {
				view.Record = &record
			}
		}
		views = append(views, view)
	}
	var next *int64
	if more {
		next = &rows[len(rows)-1].RowNumber
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": views, "nextAfter": next})
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (a *api) setImportMapping(w http.ResponseWriter, r *http.Request, current *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	var input struct {
		Mapping *imports.Mapping `json:"mapping"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Mapping == nil {
		writeFieldErrors(w, map[string]string{"mapping": "Choose how to read the columns"})
		return
	}
	extendDeadlines(w, 0, importWorkTimeout)
	if err := a.imports.SetMapping(r.Context(), id, *input.Mapping, a.location(current)); err != nil {
		writeImportFailure(w, err, "set import mapping")
		return
	}
	a.writeImport(w, r, id, http.StatusOK)
}

// suggestImportMapping asks TypeSafe Jev for a mapping from the header and
// at most three records. The suggestion is validated but not saved; the
// owner reviews it and saves it as a mapping.
func (a *api) suggestImportMapping(w http.ResponseWriter, r *http.Request, current *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	apiKey, ready, err := a.typeSafeReady(r.Context())
	if err != nil {
		internalError(w, "load typesafe integration")
		return
	}
	if !ready {
		writeError(w, http.StatusConflict, "Connect and enable TypeSafe in Integrations, or map the columns manually")
		return
	}
	header, sample, currency, err := a.imports.Sample(r.Context(), id)
	if err != nil {
		writeImportFailure(w, err, "sample import")
		return
	}
	extendDeadlines(w, 0, suggestTimeout+10*time.Second)
	ctx, cancel := context.WithTimeout(r.Context(), suggestTimeout)
	defer cancel()
	ask := func(ctx context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error) {
		return a.integrations.Ask(ctx, apiKey, state, questions)
	}
	mapping, notes, err := imports.Suggest(ctx, ask, header, sample, currency)
	if err != nil {
		writeImportFailure(w, err, "suggest import mapping")
		return
	}
	notes, err = imports.CheckSuggestion(&mapping, notes, header, sample, a.location(current))
	if err != nil {
		writeImportFailure(w, err, "check import mapping")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mapping": mapping, "notes": notes})
}

func (a *api) validateImport(w http.ResponseWriter, r *http.Request, current *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	extendDeadlines(w, 0, importWorkTimeout)
	if err := a.imports.Validate(r.Context(), id, a.location(current)); err != nil {
		writeImportFailure(w, err, "validate import")
		return
	}
	a.writeImport(w, r, id, http.StatusOK)
}

func (a *api) commitImport(w http.ResponseWriter, r *http.Request, current *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	extendDeadlines(w, 0, importWorkTimeout)
	if err := a.imports.CommitReviewed(r.Context(), id, a.location(current)); err != nil {
		writeImportFailure(w, err, "commit import")
		return
	}
	a.writeImport(w, r, id, http.StatusOK)
}

func (a *api) correctImportRow(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	row, err := strconv.ParseInt(r.PathValue("row"), 10, 64)
	if err != nil || row <= 0 {
		writeError(w, http.StatusNotFound, "Row not found")
		return
	}
	var input struct {
		Excluded bool     `json:"excluded"`
		Values   []string `json:"values"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	for _, value := range input.Values {
		if !utf8.ValidString(value) || len(value) > 4000 {
			writeFieldErrors(w, map[string]string{"values": "Values must be text of at most 4000 characters"})
			return
		}
	}
	if err := a.imports.CorrectRow(r.Context(), id, row, input.Excluded, input.Values); err != nil {
		writeImportFailure(w, err, "correct import row")
		return
	}
	a.writeImport(w, r, id, http.StatusOK)
}

func (a *api) deleteImport(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Import")
	if !ok {
		return
	}
	if err := a.imports.Delete(r.Context(), id); err != nil {
		writeImportFailure(w, err, "delete import")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
