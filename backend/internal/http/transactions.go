package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/catalog"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

const (
	maxSplits    = 50
	maxBulkItems = 500
)

// requestError carries a response for a request that cannot be applied.
// Handlers return it from inside a database transaction to roll back.
type requestError struct {
	status  int
	message string
	fields  map[string]string
}

func (e *requestError) Error() string { return e.message }

func fieldError(fields map[string]string) error {
	return &requestError{status: http.StatusUnprocessableEntity, message: "Check the highlighted fields", fields: fields}
}

func conflictError(message string) error {
	return &requestError{status: http.StatusConflict, message: message}
}

var errStale = conflictError("This transaction changed since you opened it. Reload and try again.")

// writeFailure reports a requestError to the client and anything else as an
// internal error that logs only the operation name.
func writeFailure(w http.ResponseWriter, err error, operation string) {
	var request *requestError
	if errors.As(err, &request) {
		writeJSON(w, request.status, errorBody{Error: request.message, Fields: request.fields})
		return
	}
	internalError(w, operation)
}

// txn gives generated queries and raw SQL access to one database transaction.
type txn struct {
	*sql.Tx
	*dbgen.Queries
}

// inTx runs fn in one database transaction and rolls back on any error.
func (a *api) inTx(ctx context.Context, fn func(txn) error) error {
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(txn{tx, a.store.Queries.WithTx(tx)}); err != nil {
		return err
	}
	return tx.Commit()
}

type splitInput struct {
	CategoryID int64  `json:"categoryId"`
	Amount     string `json:"amount"`
	Notes      string `json:"notes"`
}

// transactionInput is the complete editable state of a transaction. Amounts
// are positive magnitudes; the kind decides the stored sign.
type transactionInput struct {
	Revision    int64        `json:"revision"`
	AccountID   int64        `json:"accountId"`
	ToAccountID int64        `json:"toAccountId"`
	Date        string       `json:"date"`
	Kind        string       `json:"kind"`
	Amount      string       `json:"amount"`
	Payee       string       `json:"payee"`
	CategoryID  *int64       `json:"categoryId"`
	RefundOf    *int64       `json:"refundOf"`
	Notes       string       `json:"notes"`
	Splits      []splitInput `json:"splits"`
	// ConfirmImport also confirms the import the transaction came from.
	ConfirmImport bool `json:"confirmImport"`
}

type split struct {
	categoryID int64
	magnitude  int64
	notes      string
}

type draft struct {
	account    dbgen.Account
	toAccount  dbgen.Account
	occurredAt string
	date       string
	kind       string
	magnitude  int64
	payee      string
	categoryID int64
	refundOf   *dbgen.Transaction
	notes      string
	splits     []split
}

// transactionContext describes the stored transaction being edited.
type transactionContext struct {
	existing    dbgen.Transaction
	counterpart *dbgen.Transaction
	refunds     []dbgen.Transaction
	// conversion is set for foreign transactions; their amount is locked.
	conversion *dbgen.TransactionConversion
}

func (a *api) location(current *session) *time.Location {
	location, err := time.LoadLocation(current.timezone)
	if err != nil {
		location, _ = time.LoadLocation(auth.DefaultTimezone)
	}
	return location
}

func validNotes(notes string) bool {
	return utf8.ValidString(notes) && utf8.RuneCountInString(notes) <= 1000
}

// usableAccount checks an account that receives a new or moved transaction.
// A moved transaction keeps its currency. New entries are EUR only, because
// foreign amounts need the saved rate that only imports record.
func usableAccount(ctx context.Context, q txn, id int64, currency string) (dbgen.Account, string, error) {
	account, err := q.GetAccount(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return account, "Choose an account", nil
	case err != nil:
		return account, "", err
	case account.Archived == 1:
		return account, "This account is archived", nil
	case currency == "" && account.Currency != "EUR":
		return account, "Manual entries go to EUR accounts; foreign transactions arrive through imports", nil
	case currency != "" && account.Currency != currency:
		return account, "Choose an account in " + currency, nil
	}
	return account, "", nil
}

// prepare validates input against the stored state and returns what to
// write. edit is nil when creating.
func (a *api) prepare(ctx context.Context, q txn, input transactionInput, current *session, edit *transactionContext) (draft, error) {
	fields := map[string]string{}
	var d draft
	var existing *dbgen.Transaction
	linked := false
	if edit != nil {
		existing = &edit.existing
		linked = edit.counterpart != nil
	}

	d.kind = input.Kind
	switch {
	case !ledger.ValidKind(d.kind):
		fields["kind"] = "Choose a transaction type"
	case linked && d.kind != ledger.KindTransfer:
		fields["kind"] = "Delete this transfer to record it as another type"
	case len(edit.refundsOrNil()) > 0 && d.kind != ledger.KindExpense:
		fields["kind"] = "Unlink this expense's refunds before changing its type"
	}

	if existing == nil || input.AccountID != existing.AccountID {
		currency := ""
		if existing != nil {
			currency = existing.Currency
		}
		account, message, err := usableAccount(ctx, q, input.AccountID, currency)
		if err != nil {
			return d, err
		}
		if message != "" {
			fields["accountId"] = message
		}
		d.account = account
	} else {
		account, err := q.GetAccount(ctx, existing.AccountID)
		if err != nil {
			return d, err
		}
		d.account = account
	}
	if linked && fields["accountId"] == "" && input.AccountID == edit.counterpart.AccountID {
		fields["accountId"] = "Choose an account other than the transfer's other side"
	}
	if existing == nil && d.kind == ledger.KindTransfer {
		account, message, err := usableAccount(ctx, q, input.ToAccountID, d.account.Currency)
		switch {
		case err != nil:
			return d, err
		case message != "":
			fields["toAccountId"] = message
		case input.ToAccountID == input.AccountID:
			fields["toAccountId"] = "Choose a different account"
		}
		d.toAccount = account
	}

	location := a.location(current)
	day, err := time.ParseInLocation(time.DateOnly, input.Date, location)
	if err != nil || day.Year() < 2000 || day.After(a.now().In(location).AddDate(1, 0, 0)) {
		fields["date"] = "Enter a date as YYYY-MM-DD"
	}
	d.date, d.occurredAt = input.Date, auth.FormatTime(day)
	if existing != nil && existing.LocalDate == input.Date {
		// Keep the precise imported time when the date is unchanged.
		d.occurredAt = existing.OccurredAt
	}

	currency := d.account.Currency
	if currency == "" {
		currency = "EUR"
	}
	money, err := ledger.ParseMoney(strings.TrimSpace(input.Amount), currency)
	if err != nil || money.Minor() <= 0 {
		fields["amount"] = "Enter a positive amount, such as 12.50"
	} else {
		d.magnitude = money.Minor()
	}
	if total := refundTotal(edit.refundsOrNil(), 0); d.magnitude > 0 && total > d.magnitude {
		fields["amount"] = "The amount cannot be less than its linked refunds"
	}
	if edit != nil && edit.conversion != nil && fields["amount"] == "" && fields["kind"] == "" {
		// The saved EUR amount is never recalculated, so the source amount
		// and its sign stay as imported.
		switch {
		case d.magnitude != max(existing.AmountMinor, -existing.AmountMinor):
			fields["amount"] = "This amount was converted from " + existing.Currency + " at a saved rate and cannot change"
		case ledger.SignedAmount(d.kind, d.magnitude, existing.AmountMinor < 0) != existing.AmountMinor:
			fields["kind"] = "This type would reverse the saved " + existing.Currency + " conversion"
		}
	}

	if payee := ledger.DisplayName(input.Payee); payee != "" {
		if !ledger.ValidName(payee) {
			fields["payee"] = "Enter a payee of up to 100 characters"
		}
		d.payee = payee
	}
	d.notes = strings.TrimSpace(input.Notes)
	if !validNotes(d.notes) {
		fields["notes"] = "Use at most 1000 characters"
	}

	if input.CategoryID != nil && d.kind != ledger.KindTransfer {
		found, err := catalog.Categories.Exists(ctx, q, *input.CategoryID)
		if err != nil {
			return d, err
		}
		if !found {
			fields["categoryId"] = "Choose a category"
		}
		d.categoryID = *input.CategoryID
	}

	if len(input.Splits) > 0 {
		if err := a.prepareSplits(ctx, q, input.Splits, currency, &d, fields); err != nil {
			return d, err
		}
	}

	if input.RefundOf != nil {
		if err := prepareRefund(ctx, q, *input.RefundOf, input.CategoryID, existing, &d, fields); err != nil {
			return d, err
		}
	}
	if len(fields) > 0 {
		return d, fieldError(fields)
	}
	return d, nil
}

func (e *transactionContext) refundsOrNil() []dbgen.Transaction {
	if e == nil {
		return nil
	}
	return e.refunds
}

func refundTotal(refunds []dbgen.Transaction, except int64) int64 {
	var total int64
	for _, refund := range refunds {
		if refund.ID != except {
			total += refund.AmountMinor
		}
	}
	return total
}

func (a *api) prepareSplits(ctx context.Context, q txn, inputs []splitInput, currency string, d *draft, fields map[string]string) error {
	switch {
	case d.kind != ledger.KindExpense && d.kind != ledger.KindIncome:
		fields["splits"] = "Only expenses and income can be split"
		return nil
	case len(inputs) < 2 || len(inputs) > maxSplits:
		fields["splits"] = "Split into 2 to 50 parts"
		return nil
	}
	var sum int64
	for index, input := range inputs {
		prefix := "splits." + strconv.Itoa(index) + "."
		part := split{categoryID: input.CategoryID, notes: strings.TrimSpace(input.Notes)}
		found, err := catalog.Categories.Exists(ctx, q, input.CategoryID)
		if err != nil {
			return err
		}
		if !found {
			fields[prefix+"categoryId"] = "Choose a category"
		}
		money, err := ledger.ParseMoney(strings.TrimSpace(input.Amount), currency)
		if err != nil || money.Minor() <= 0 {
			fields[prefix+"amount"] = "Enter a positive amount"
		} else {
			part.magnitude = money.Minor()
			sum += part.magnitude
		}
		if !validNotes(part.notes) {
			fields[prefix+"notes"] = "Use at most 1000 characters"
		}
		d.splits = append(d.splits, part)
	}
	if len(fields) == 0 && sum != d.magnitude {
		difference, _ := decimal(d.magnitude-sum, currency)
		fields["splits"] = "Split amounts must add up to the transaction amount (difference " + difference + ")"
	}
	return nil
}

// originalCategories lists the categories a linked refund may reduce and
// the default one, the category of the largest split.
func originalCategories(ctx context.Context, q txn, original dbgen.Transaction) ([]int64, int64, error) {
	splits, err := q.ListSplits(ctx, original.ID)
	if err != nil {
		return nil, 0, err
	}
	if len(splits) == 0 {
		if !original.CategoryID.Valid {
			id, err := catalog.Uncategorized(ctx, q)
			return []int64{id}, id, err
		}
		return []int64{original.CategoryID.Int64}, original.CategoryID.Int64, nil
	}
	categories := make([]int64, 0, len(splits))
	primary, largest := int64(0), int64(0)
	for _, part := range splits {
		categories = append(categories, part.CategoryID)
		if magnitude := -part.AmountMinor; magnitude > largest {
			primary, largest = part.CategoryID, magnitude
		}
	}
	return categories, primary, nil
}

// prepareRefund links a refund to an expense in the same currency. All
// refunds together cannot exceed the expense, and the refund reduces one of
// the expense's categories.
func prepareRefund(ctx context.Context, q txn, originalID int64, category *int64, existing *dbgen.Transaction, d *draft, fields map[string]string) error {
	if d.kind != ledger.KindRefund {
		fields["refundOf"] = "Only refunds can be linked to an expense"
		return nil
	}
	original, err := q.GetTransaction(ctx, originalID)
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err == nil && original.DeletedAt.Valid):
		fields["refundOf"] = "Choose an existing expense"
		return nil
	case err != nil:
		return err
	case original.Kind != ledger.KindExpense || (existing != nil && original.ID == existing.ID):
		fields["refundOf"] = "A refund can only be linked to an expense"
		return nil
	case d.account.Currency != "" && original.Currency != d.account.Currency:
		fields["refundOf"] = "The refund must use the expense's currency"
		return nil
	}
	refunds, err := q.ListRefunds(ctx, sql.NullInt64{Int64: original.ID, Valid: true})
	if err != nil {
		return err
	}
	var self int64
	if existing != nil {
		self = existing.ID
	}
	if refundTotal(refunds, self)+d.magnitude > -original.AmountMinor {
		fields["amount"] = "Refunds cannot exceed the original expense"
	}
	allowed, primary, err := originalCategories(ctx, q, original)
	if err != nil {
		return err
	}
	d.categoryID = primary
	if category != nil {
		if !slices.Contains(allowed, *category) {
			fields["categoryId"] = "A linked refund reduces a category of the original expense"
		}
		d.categoryID = *category
	}
	d.refundOf = &original
	return nil
}

// category decides the stored category: none for transfers and splits,
// the chosen one otherwise, and for new entries without a choice the payee's
// rule or Uncategorized.
func (d draft) category(ctx context.Context, q txn, payee sql.NullInt64, creating bool) (sql.NullInt64, error) {
	if d.kind == ledger.KindTransfer || len(d.splits) > 0 {
		return sql.NullInt64{}, nil
	}
	if d.categoryID != 0 {
		return sql.NullInt64{Int64: d.categoryID, Valid: true}, nil
	}
	var id int64
	var err error
	if creating {
		id, err = catalog.Categorize(ctx, q, payee, "")
	} else {
		id, err = catalog.Uncategorized(ctx, q)
	}
	return sql.NullInt64{Int64: id, Valid: err == nil}, err
}

func resolvePayee(ctx context.Context, q txn, name string) (sql.NullInt64, error) {
	if name == "" {
		return sql.NullInt64{}, nil
	}
	id, _, err := catalog.Payees.Resolve(ctx, q, name)
	return sql.NullInt64{Int64: id, Valid: err == nil}, err
}

func writeSplits(ctx context.Context, q txn, transaction dbgen.Transaction, splits []split) error {
	if err := q.DeleteSplits(ctx, transaction.ID); err != nil {
		return err
	}
	if len(splits) == 0 {
		return nil
	}
	parts := make([]int64, len(splits))
	for index, part := range splits {
		parts[index] = ledger.SignedAmount(transaction.Kind, part.magnitude, false)
	}
	cents, err := ledger.AllocateEURCents(transaction.AmountMinor, transaction.EurCents, parts)
	if err != nil {
		return fieldError(map[string]string{"splits": "Split amounts must add up to the transaction amount"})
	}
	for index, part := range splits {
		if err := q.CreateSplit(ctx, dbgen.CreateSplitParams{
			TransactionID: transaction.ID, Position: int64(index), CategoryID: part.categoryID,
			AmountMinor: parts[index], EurCents: cents[index], Notes: part.notes,
		}); err != nil {
			return err
		}
	}
	return nil
}

// cascadeRefunds keeps linked refunds on a category of their expense after
// the expense is recategorized or split.
func cascadeRefunds(ctx context.Context, q txn, original dbgen.Transaction) error {
	refunds, err := q.ListRefunds(ctx, sql.NullInt64{Int64: original.ID, Valid: true})
	if err != nil || len(refunds) == 0 {
		return err
	}
	allowed, primary, err := originalCategories(ctx, q, original)
	if err != nil {
		return err
	}
	for _, refund := range refunds {
		if !refund.CategoryID.Valid || !slices.Contains(allowed, refund.CategoryID.Int64) {
			if err := q.SetTransactionCategory(ctx, dbgen.SetTransactionCategoryParams{
				CategoryID: sql.NullInt64{Int64: primary, Valid: true}, ID: refund.ID,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *api) createTransaction(w http.ResponseWriter, r *http.Request, current *session) {
	var input transactionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	var view transactionView
	err := a.inTx(r.Context(), func(q txn) error {
		d, err := a.prepare(r.Context(), q, input, current, nil)
		if err != nil {
			return err
		}
		payee, err := resolvePayee(r.Context(), q, d.payee)
		if err != nil {
			return err
		}
		category, err := d.category(r.Context(), q, payee, true)
		if err != nil {
			return err
		}
		units, err := ledger.MinorUnits(d.account.Currency)
		if err != nil {
			return err
		}
		params := dbgen.CreateTransactionParams{
			AccountID: d.account.ID, OccurredAt: d.occurredAt, LocalDate: d.date, Kind: d.kind,
			AmountMinor: ledger.SignedAmount(d.kind, d.magnitude, true), Currency: d.account.Currency,
			MinorUnit: int64(units), EurCents: ledger.SignedAmount(d.kind, d.magnitude, true), PayeeID: payee,
			CategoryID: category, Notes: d.notes, ReviewStatus: "confirmed",
		}
		if d.refundOf != nil {
			params.RefundOfTransactionID = sql.NullInt64{Int64: d.refundOf.ID, Valid: true}
		}
		created, err := q.CreateTransaction(r.Context(), params)
		if err != nil {
			return err
		}
		if err := writeSplits(r.Context(), q, created, d.splits); err != nil {
			return err
		}
		if d.kind == ledger.KindTransfer {
			params.AccountID, params.AmountMinor, params.EurCents = d.toAccount.ID, d.magnitude, d.magnitude
			incoming, err := q.CreateTransaction(r.Context(), params)
			if err != nil {
				return err
			}
			transfer, err := q.CreateLinkedTransfer(r.Context())
			if err != nil {
				return err
			}
			for _, leg := range []dbgen.CreateTransferLegParams{
				{TransferID: transfer, TransactionID: created.ID, Direction: "outgoing"},
				{TransferID: transfer, TransactionID: incoming.ID, Direction: "incoming"},
			} {
				if err := q.CreateTransferLeg(r.Context(), leg); err != nil {
					return err
				}
			}
		}
		view, err = loadView(r.Context(), q, created.ID)
		return err
	})
	if err != nil {
		writeFailure(w, err, "create transaction")
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func transactionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "Transaction not found")
		return 0, false
	}
	return id, true
}

// loadContext loads a live transaction with its transfer counterpart and
// linked refunds.
func loadContext(ctx context.Context, q txn, id int64) (transactionContext, error) {
	var edit transactionContext
	existing, err := q.GetTransaction(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && existing.DeletedAt.Valid) {
		return edit, &requestError{status: http.StatusNotFound, message: "Transaction not found"}
	}
	if err != nil {
		return edit, err
	}
	edit.existing = existing
	leg, err := q.GetTransferCounterpart(ctx, id)
	switch {
	case err == nil:
		counterpart, err := q.GetTransaction(ctx, leg.TransactionID)
		if err != nil {
			return edit, err
		}
		edit.counterpart = &counterpart
	case !errors.Is(err, sql.ErrNoRows):
		return edit, err
	}
	edit.refunds, err = q.ListRefunds(ctx, sql.NullInt64{Int64: id, Valid: true})
	if err != nil {
		return edit, err
	}
	conversion, err := q.GetTransactionConversion(ctx, id)
	switch {
	case err == nil:
		edit.conversion = &conversion
	case !errors.Is(err, sql.ErrNoRows):
		return edit, err
	}
	return edit, nil
}

type ruleSuggestion struct {
	PayeeID      int64  `json:"payeeId"`
	PayeeName    string `json:"payeeName"`
	CategoryID   int64  `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	// OtherTransactions counts this payee's transactions in other categories,
	// which the owner may review and change by selecting them.
	OtherTransactions int64 `json:"otherTransactions"`
}

type updateResult struct {
	Transaction    transactionView `json:"transaction"`
	RuleSuggestion *ruleSuggestion `json:"ruleSuggestion"`
}

func (a *api) updateTransaction(w http.ResponseWriter, r *http.Request, current *session) {
	id, ok := transactionID(w, r)
	if !ok {
		return
	}
	var input transactionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	var result updateResult
	err := a.inTx(r.Context(), func(q txn) error {
		edit, err := loadContext(r.Context(), q, id)
		if err != nil {
			return err
		}
		if edit.existing.Revision != input.Revision {
			return errStale
		}
		if edit.existing.Kind != ledger.KindTransfer && input.Kind == ledger.KindTransfer && !edit.existing.ImportBatchID.Valid {
			return fieldError(map[string]string{"kind": "Create a new transfer between two accounts instead"})
		}
		d, err := a.prepare(r.Context(), q, input, current, &edit)
		if err != nil {
			return err
		}
		payee, err := resolvePayee(r.Context(), q, d.payee)
		if err != nil {
			return err
		}
		category, err := d.category(r.Context(), q, payee, false)
		if err != nil {
			return err
		}
		// A single imported transfer keeps its direction.
		amount := ledger.SignedAmount(d.kind, d.magnitude, edit.existing.AmountMinor < 0)
		eurCents := amount
		if edit.conversion != nil {
			eurCents = edit.existing.EurCents
		}
		params := dbgen.UpdateTransactionParams{
			AccountID: d.account.ID, OccurredAt: d.occurredAt, LocalDate: d.date, Kind: d.kind,
			AmountMinor: amount, Currency: edit.existing.Currency, MinorUnit: edit.existing.MinorUnit,
			EurCents: eurCents, PayeeID: payee, CategoryID: category, Notes: d.notes,
			ID: id, ExpectedRevision: input.Revision,
		}
		if d.refundOf != nil {
			params.RefundOfTransactionID = sql.NullInt64{Int64: d.refundOf.ID, Valid: true}
		}
		updated, err := q.UpdateTransaction(r.Context(), params)
		if errors.Is(err, sql.ErrNoRows) {
			return errStale
		}
		if err != nil {
			return err
		}
		if err := writeSplits(r.Context(), q, updated, d.splits); err != nil {
			return err
		}
		if other := edit.counterpart; other != nil {
			otherAmount := ledger.SignedAmount(ledger.KindTransfer, d.magnitude, other.AmountMinor < 0)
			if _, err := q.UpdateTransaction(r.Context(), dbgen.UpdateTransactionParams{
				AccountID: other.AccountID, OccurredAt: d.occurredAt, LocalDate: d.date, Kind: other.Kind,
				AmountMinor: otherAmount, Currency: other.Currency, MinorUnit: other.MinorUnit, EurCents: otherAmount,
				PayeeID: other.PayeeID, CategoryID: other.CategoryID, Notes: d.notes,
				ID: other.ID, ExpectedRevision: other.Revision,
			}); err != nil {
				return err
			}
		}
		if err := cascadeRefunds(r.Context(), q, updated); err != nil {
			return err
		}
		if input.ConfirmImport {
			if !edit.existing.ImportBatchID.Valid {
				return conflictError("This transaction was not imported")
			}
			if err := confirmBatch(r.Context(), q, edit.existing.ImportBatchID.Int64, a.now()); err != nil {
				return err
			}
		}
		if result.Transaction, err = loadView(r.Context(), q, id); err != nil {
			return err
		}
		result.RuleSuggestion, err = suggestRule(r.Context(), q, edit.existing, updated, d)
		return err
	})
	if err != nil {
		writeFailure(w, err, "update transaction")
		return
	}
	if input.ConfirmImport {
		a.cleanRates(r.Context())
	}
	writeJSON(w, http.StatusOK, result)
}

// suggestRule offers a future rule when the owner corrects the category of
// a payee's transaction and no rule already gives that category.
func suggestRule(ctx context.Context, db dbgen.DBTX, before, after dbgen.Transaction, d draft) (*ruleSuggestion, error) {
	if !after.PayeeID.Valid || !after.CategoryID.Valid || d.refundOf != nil || len(d.splits) > 0 ||
		(after.Kind != ledger.KindExpense && after.Kind != ledger.KindIncome && after.Kind != ledger.KindRefund) ||
		before.CategoryID == after.CategoryID {
		return nil, nil
	}
	ruled, found, err := catalog.RuleCategory(ctx, db, after.PayeeID.Int64)
	if err != nil || (found && ruled == after.CategoryID.Int64) {
		return nil, err
	}
	suggestion := ruleSuggestion{PayeeID: after.PayeeID.Int64, CategoryID: after.CategoryID.Int64}
	err = db.QueryRowContext(ctx, `SELECT payees.name, categories.name,
        (SELECT count(*) FROM transactions WHERE payee_id = payees.id AND deleted_at IS NULL AND id != ?3
            AND kind != 'transfer' AND (category_id IS NULL OR category_id != categories.id))
        FROM payees, categories WHERE payees.id = ?1 AND categories.id = ?2`,
		suggestion.PayeeID, suggestion.CategoryID, after.ID).Scan(&suggestion.PayeeName, &suggestion.CategoryName, &suggestion.OtherTransactions)
	return &suggestion, err
}

// deleteLive soft-deletes a transaction and its transfer counterpart.
func deleteLive(ctx context.Context, q txn, edit transactionContext, revision int64) error {
	if _, err := q.DeleteTransaction(ctx, dbgen.DeleteTransactionParams{ID: edit.existing.ID, ExpectedRevision: revision}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errStale
		}
		return err
	}
	if other := edit.counterpart; other != nil && !other.DeletedAt.Valid {
		if _, err := q.DeleteTransaction(ctx, dbgen.DeleteTransactionParams{ID: other.ID, ExpectedRevision: other.Revision}); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

var errActiveRefunds = conflictError("Delete or unlink the linked refunds first")

func (a *api) deleteTransaction(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := transactionID(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Include the transaction revision")
		return
	}
	err = a.inTx(r.Context(), func(q txn) error {
		edit, err := loadContext(r.Context(), q, id)
		if err != nil {
			return err
		}
		if len(edit.refunds) > 0 {
			return errActiveRefunds
		}
		return deleteLive(r.Context(), q, edit, revision)
	})
	if err != nil {
		writeFailure(w, err, "delete transaction")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type splitView struct {
	CategoryID   int64  `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	Amount       string `json:"amount"`
	EURCents     int64  `json:"eurCents"`
	Notes        string `json:"notes"`
}

type originalView struct {
	Amount           string  `json:"amount"`
	Currency         string  `json:"currency"`
	Timestamp        string  `json:"timestamp"`
	Action           string  `json:"action"`
	MerchantName     string  `json:"merchantName"`
	MerchantCategory string  `json:"merchantCategory"`
	Notes            string  `json:"notes"`
	FeeAmount        *string `json:"feeAmount"`
	FeeCurrency      *string `json:"feeCurrency"`
}

type conversionView struct {
	OriginalAmount   string `json:"originalAmount"`
	OriginalCurrency string `json:"originalCurrency"`
	EURCents         int64  `json:"eurCents"`
	Rate             string `json:"rate"`
	RateDate         string `json:"rateDate"`
	ProviderPolicy   string `json:"providerPolicy"`
	RoundingRule     string `json:"roundingRule"`
	ConvertedAt      string `json:"convertedAt"`
}

type transactionDetail struct {
	transactionView
	OccurredAt    string            `json:"occurredAt"`
	Splits        []splitView       `json:"splits"`
	Refunds       []transactionView `json:"refunds"`
	RefundedTotal string            `json:"refundedTotal"`
	RefundOfView  *transactionView  `json:"refundOfTransaction"`
	TransferPeer  *transactionView  `json:"transferCounterpart"`
	Original      *originalView     `json:"importedOriginal"`
	Conversion    *conversionView   `json:"conversion"`
	EditCount     int64             `json:"editCount"`
	ImportBatch   *importBatchRef   `json:"importBatch"`
}

// importBatchRef lets the editor offer to confirm the transaction's import.
type importBatchRef struct {
	ID           int64  `json:"id"`
	ReviewStatus string `json:"reviewStatus"`
}

func (a *api) getTransaction(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := transactionID(w, r)
	if !ok {
		return
	}
	var detail transactionDetail
	err := a.inTx(r.Context(), func(q txn) error {
		edit, err := loadContext(r.Context(), q, id)
		if err != nil {
			return err
		}
		if detail.transactionView, err = loadView(r.Context(), q, id); err != nil {
			return err
		}
		detail.OccurredAt = edit.existing.OccurredAt
		splits, err := q.ListSplits(r.Context(), id)
		if err != nil {
			return err
		}
		detail.Splits = make([]splitView, 0, len(splits))
		for _, part := range splits {
			amount, err := decimal(part.AmountMinor, edit.existing.Currency)
			if err != nil {
				return err
			}
			detail.Splits = append(detail.Splits, splitView{CategoryID: part.CategoryID, CategoryName: part.CategoryName,
				Amount: amount, EURCents: part.EurCents, Notes: part.Notes})
		}
		detail.Refunds = []transactionView{}
		for _, refund := range edit.refunds {
			view, err := loadView(r.Context(), q, refund.ID)
			if err != nil {
				return err
			}
			detail.Refunds = append(detail.Refunds, view)
		}
		if detail.RefundedTotal, err = decimal(refundTotal(edit.refunds, 0), edit.existing.Currency); err != nil {
			return err
		}
		if refundOf := edit.existing.RefundOfTransactionID; refundOf.Valid {
			view, err := loadView(r.Context(), q, refundOf.Int64)
			if err != nil {
				return err
			}
			detail.RefundOfView = &view
		}
		if edit.counterpart != nil {
			view, err := loadView(r.Context(), q, edit.counterpart.ID)
			if err != nil {
				return err
			}
			detail.TransferPeer = &view
		}
		original, err := q.GetImportedOriginal(r.Context(), id)
		switch {
		case err == nil:
			detail.Original = &originalView{Amount: original.AmountText, Currency: original.Currency,
				Timestamp: original.TimestampText, Action: original.Action, MerchantName: original.MerchantName,
				MerchantCategory: original.MerchantCategory, Notes: original.Notes,
				FeeAmount: nullText(original.FeeAmountText), FeeCurrency: nullText(original.FeeCurrency)}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if batchID := edit.existing.ImportBatchID; batchID.Valid {
			batch, err := q.GetImportBatch(r.Context(), batchID.Int64)
			if err != nil {
				return err
			}
			detail.ImportBatch = &importBatchRef{ID: batch.ID, ReviewStatus: batch.ReviewStatus}
		}
		if conversion := edit.conversion; conversion != nil {
			amount, err := decimal(conversion.OriginalAmountMinor, conversion.OriginalCurrency)
			if err != nil {
				return err
			}
			detail.Conversion = &conversionView{OriginalAmount: amount, OriginalCurrency: conversion.OriginalCurrency,
				EURCents: conversion.EurCents, Rate: conversion.Rate, RateDate: conversion.RateDate,
				ProviderPolicy: conversion.ProviderPolicy, RoundingRule: conversion.RoundingRule, ConvertedAt: conversion.ConvertedAt}
		}
		return q.QueryRowContext(r.Context(), "SELECT count(*) FROM transaction_edit_history WHERE transaction_id = ?", id).Scan(&detail.EditCount)
	})
	if err != nil {
		writeFailure(w, err, "load transaction")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func nullText(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

type bulkItem struct {
	ID       int64 `json:"id"`
	Revision int64 `json:"revision"`
}

// bulkInput changes only the fields that are present. Payee "" clears it.
type bulkInput struct {
	Items      []bulkItem `json:"items"`
	Action     string     `json:"action"`
	CategoryID *int64     `json:"categoryId"`
	Payee      *string    `json:"payee"`
	AccountID  *int64     `json:"accountId"`
	Reviewed   bool       `json:"reviewed"`
}

// bulkTransactions applies one change to the selected transactions in a
// single database transaction. Any stale or unsuitable row rejects the whole
// request so a selection is never half-applied.
func (a *api) bulkTransactions(w http.ResponseWriter, r *http.Request, _ *session) {
	var input bulkInput
	if !decodeJSON(w, r, &input) {
		return
	}
	switch {
	case len(input.Items) == 0 || len(input.Items) > maxBulkItems:
		writeFieldErrors(w, map[string]string{"items": "Select 1 to 500 transactions"})
		return
	case input.Action != "update" && input.Action != "delete":
		writeFieldErrors(w, map[string]string{"action": "Choose update or delete"})
		return
	case input.Action == "update" && input.CategoryID == nil && input.Payee == nil && input.AccountID == nil && !input.Reviewed:
		writeFieldErrors(w, map[string]string{"action": "Choose at least one change"})
		return
	}
	var changed int
	err := a.inTx(r.Context(), func(q txn) error {
		var err error
		if input.Action == "delete" {
			changed, err = bulkDelete(r.Context(), q, input.Items)
		} else {
			changed, err = bulkUpdate(r.Context(), q, input)
		}
		return err
	})
	if err != nil {
		writeFailure(w, err, "bulk edit transactions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"changed": changed})
}

func bulkDelete(ctx context.Context, q txn, items []bulkItem) (int, error) {
	deleted := map[int64]bool{}
	var originals []int64
	for _, item := range items {
		if deleted[item.ID] {
			continue
		}
		edit, err := loadContext(ctx, q, item.ID)
		if err != nil {
			return 0, err
		}
		if edit.existing.Revision != item.Revision {
			return 0, errStale
		}
		if err := deleteLive(ctx, q, edit, item.Revision); err != nil {
			return 0, err
		}
		deleted[item.ID] = true
		if edit.counterpart != nil {
			deleted[edit.counterpart.ID] = true
		}
		if len(edit.refunds) > 0 {
			originals = append(originals, item.ID)
		}
	}
	for _, id := range originals {
		count, err := q.CountActiveRefunds(ctx, sql.NullInt64{Int64: id, Valid: true})
		if err != nil {
			return 0, err
		}
		if count > 0 {
			return 0, errActiveRefunds
		}
	}
	return len(items), nil
}

func bulkUpdate(ctx context.Context, q txn, input bulkInput) (int, error) {
	var payee sql.NullInt64
	if input.Payee != nil {
		name := ledger.DisplayName(*input.Payee)
		if name != "" && !ledger.ValidName(name) {
			return 0, fieldError(map[string]string{"payee": "Enter a payee of up to 100 characters"})
		}
		var err error
		if payee, err = resolvePayee(ctx, q, name); err != nil {
			return 0, err
		}
	}
	if input.CategoryID != nil {
		found, err := catalog.Categories.Exists(ctx, q, *input.CategoryID)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, fieldError(map[string]string{"categoryId": "Choose a category"})
		}
	}
	for _, item := range input.Items {
		edit, err := loadContext(ctx, q, item.ID)
		if err != nil {
			return 0, err
		}
		existing := edit.existing
		if existing.Revision != item.Revision {
			return 0, errStale
		}
		params := dbgen.UpdateTransactionParams{
			AccountID: existing.AccountID, OccurredAt: existing.OccurredAt, LocalDate: existing.LocalDate,
			Kind: existing.Kind, AmountMinor: existing.AmountMinor, Currency: existing.Currency,
			MinorUnit: existing.MinorUnit, EurCents: existing.EurCents, PayeeID: existing.PayeeID,
			CategoryID: existing.CategoryID, RefundOfTransactionID: existing.RefundOfTransactionID,
			Notes: existing.Notes, ID: existing.ID, ExpectedRevision: existing.Revision,
		}
		if input.CategoryID != nil {
			splits, err := q.ListSplits(ctx, existing.ID)
			if err != nil {
				return 0, err
			}
			switch {
			case existing.Kind == ledger.KindTransfer:
				return 0, fieldError(map[string]string{"categoryId": "Transfers have no category; deselect them first"})
			case len(splits) > 0:
				return 0, fieldError(map[string]string{"categoryId": "Split transactions keep categories per part; edit them one by one"})
			case existing.RefundOfTransactionID.Valid:
				return 0, fieldError(map[string]string{"categoryId": "Linked refunds follow their expense's category; deselect them first"})
			}
			params.CategoryID = sql.NullInt64{Int64: *input.CategoryID, Valid: true}
		}
		if input.Payee != nil {
			params.PayeeID = payee
		}
		if input.AccountID != nil && *input.AccountID != existing.AccountID {
			_, message, err := usableAccount(ctx, q, *input.AccountID, existing.Currency)
			if err != nil {
				return 0, err
			}
			if message == "" && edit.counterpart != nil && edit.counterpart.AccountID == *input.AccountID {
				message = "A transfer cannot move to the account on its other side"
			}
			if message != "" {
				return 0, fieldError(map[string]string{"accountId": message})
			}
			params.AccountID = *input.AccountID
		}
		updated, err := q.UpdateTransaction(ctx, params)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errStale
		}
		if err != nil {
			return 0, err
		}
		if input.Reviewed && updated.ReviewStatus == "needs_review" {
			if _, err := q.SetTransactionReviewStatus(ctx, dbgen.SetTransactionReviewStatusParams{
				ReviewStatus: "confirmed", ID: updated.ID, ExpectedRevision: updated.Revision,
			}); err != nil {
				return 0, err
			}
		}
		if err := cascadeRefunds(ctx, q, updated); err != nil {
			return 0, err
		}
	}
	return len(input.Items), nil
}
