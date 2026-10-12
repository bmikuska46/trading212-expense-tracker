package httpserver

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

type transactionView struct {
	ID                  int64  `json:"id"`
	AccountID           int64  `json:"accountId"`
	AccountName         string `json:"accountName"`
	Date                string `json:"date"`
	Kind                string `json:"kind"`
	Amount              string `json:"amount"`
	Currency            string `json:"currency"`
	EURCents            int64  `json:"eurCents"`
	Notes               string `json:"notes"`
	ReviewStatus        string `json:"reviewStatus"`
	Revision            int64  `json:"revision"`
	PayeeID             *int64 `json:"payeeId"`
	PayeeName           string `json:"payeeName"`
	CategoryID          *int64 `json:"categoryId"`
	CategoryName        string `json:"categoryName"`
	SplitCount          int64  `json:"splitCount"`
	RefundOf            *int64 `json:"refundOf"`
	TransferAccountName string `json:"transferAccountName"`
	Imported            bool   `json:"imported"`
}

// registerFilter holds validated register filters. Every value reaches SQL
// as a bound parameter; sort columns come from a fixed allowlist.
type registerFilter struct {
	search                                  string
	from, to                                string
	accountID, categoryID, payeeID, batchID int64
	kind, review                            string
	sort, direction                         string
	page, pageSize                          int
}

var sortColumns = map[string]string{
	"date":     "t.local_date",
	"amount":   "t.eur_cents",
	"payee":    "p.name COLLATE NOCASE",
	"category": "c.name COLLATE NOCASE",
	"account":  "a.name COLLATE NOCASE",
}

func parseRegisterFilter(values url.Values) (registerFilter, map[string]string) {
	filter := registerFilter{sort: "date", direction: "desc", page: 1, pageSize: defaultPageSize}
	fields := map[string]string{}
	filter.search = strings.TrimSpace(values.Get("q"))
	if len([]rune(filter.search)) > 100 {
		fields["q"] = "Search for at most 100 characters"
	}
	for key, target := range map[string]*string{"from": &filter.from, "to": &filter.to} {
		if value := values.Get(key); value != "" {
			if _, err := time.Parse(time.DateOnly, value); err != nil {
				fields[key] = "Use a date as YYYY-MM-DD"
			}
			*target = value
		}
	}
	for key, target := range map[string]*int64{"account": &filter.accountID, "category": &filter.categoryID, "payee": &filter.payeeID, "batch": &filter.batchID} {
		if value := values.Get(key); value != "" {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				fields[key] = "Choose a valid entry"
			}
			*target = id
		}
	}
	if filter.kind = values.Get("kind"); filter.kind != "" && !ledger.ValidKind(filter.kind) {
		fields["kind"] = "Choose a transaction type"
	}
	if filter.review = values.Get("review"); filter.review != "" &&
		filter.review != "needs_review" && filter.review != "confirmed" && filter.review != "source_conflict" {
		fields["review"] = "Choose a review status"
	}
	parseSort(values, sortColumns, &filter.sort, &filter.direction, fields)
	for key, target := range map[string]*int{"page": &filter.page, "pageSize": &filter.pageSize} {
		if value := values.Get(key); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || (key == "pageSize" && number > maxPageSize) || (key == "page" && number > 1_000_000) {
				fields[key] = "Choose a page size from 1 to 200"
				if key == "page" {
					fields[key] = "Choose a valid page"
				}
			}
			*target = number
		}
	}
	return filter, fields
}

// parseSort reads the sort key and direction over the given defaults. Keys
// must be in columns, an allowlist of SQL expressions, so the chosen
// expression is safe to place in ORDER BY.
func parseSort(values url.Values, columns map[string]string, sort, direction *string, fields map[string]string) {
	if value := values.Get("sort"); value != "" {
		if _, ok := columns[value]; !ok {
			fields["sort"] = "Choose a sortable column"
		}
		*sort = value
	}
	if value := values.Get("dir"); value != "" {
		if value != "asc" && value != "desc" {
			fields["dir"] = "Sort ascending or descending"
		}
		*direction = value
	}
}

// pageIDs runs a query that picks one sorted page of IDs.
func pageIDs(ctx context.Context, db dbgen.DBTX, query string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// inOrder arranges rows loaded by ID in the order pageIDs chose.
func inOrder[T any](ids []int64, rows []T, id func(T) int64) []T {
	byID := make(map[int64]T, len(rows))
	for _, row := range rows {
		byID[id(row)] = row
	}
	ordered := make([]T, 0, len(rows))
	for _, key := range ids {
		if row, ok := byID[key]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered
}

const registerJoins = `
JOIN accounts a ON a.id = t.account_id
LEFT JOIN payees p ON p.id = t.payee_id
LEFT JOIN categories c ON c.id = t.category_id`

// where builds the filter clause. Search matches notes, payee and category
// names, and the exact amount when the text is a plain decimal.
func (f registerFilter) where() (string, []any) {
	clauses := []string{"t.deleted_at IS NULL"}
	var args []any
	if f.search != "" {
		search := "(instr(lower(t.notes), ?) > 0 OR instr(p.normalized_name, ?) > 0 OR instr(c.normalized_name, ?) > 0"
		normalized := ledger.NormalizeName(f.search)
		args = append(args, strings.ToLower(f.search), normalized, normalized)
		if money, err := ledger.ParseMoney(strings.TrimPrefix(f.search, "-"), "EUR"); err == nil && money.Minor() > 0 {
			search += " OR abs(t.eur_cents) = ?"
			args = append(args, money.Minor())
		}
		clauses = append(clauses, search+")")
	}
	if f.from != "" {
		clauses, args = append(clauses, "t.local_date >= ?"), append(args, f.from)
	}
	if f.to != "" {
		clauses, args = append(clauses, "t.local_date <= ?"), append(args, f.to)
	}
	if f.accountID != 0 {
		clauses, args = append(clauses, "t.account_id = ?"), append(args, f.accountID)
	}
	if f.payeeID != 0 {
		clauses, args = append(clauses, "t.payee_id = ?"), append(args, f.payeeID)
	}
	if f.categoryID != 0 {
		clauses = append(clauses, "(t.category_id = ? OR EXISTS (SELECT 1 FROM transaction_splits s WHERE s.transaction_id = t.id AND s.category_id = ?))")
		args = append(args, f.categoryID, f.categoryID)
	}
	if f.batchID != 0 {
		clauses, args = append(clauses, "t.import_batch_id = ?"), append(args, f.batchID)
	}
	if f.kind != "" {
		clauses, args = append(clauses, "t.kind = ?"), append(args, f.kind)
	}
	if f.review != "" {
		clauses, args = append(clauses, effectiveReview+" = ?"), append(args, f.review)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (f registerFilter) orderBy() string {
	direction := " DESC"
	if f.direction == "asc" {
		direction = " ASC"
	}
	order := sortColumns[f.sort] + direction
	if f.sort != "date" {
		order += ", t.local_date" + direction
	}
	return " ORDER BY " + order + ", t.id" + direction
}

// effectiveReview is a transaction's review status. Confirming an import
// changes only the batch, so its transactions keep their revisions and edit
// history and read as confirmed through it.
const effectiveReview = `CASE WHEN t.review_status = 'needs_review' AND EXISTS (SELECT 1 FROM import_batches b
    WHERE b.id = t.import_batch_id AND b.review_status = 'confirmed') THEN 'confirmed' ELSE t.review_status END`

const viewColumns = `SELECT t.id, t.account_id, a.name, t.local_date, t.kind, t.amount_minor, t.currency, t.eur_cents,
    t.notes, ` + effectiveReview + `, t.revision, t.payee_id, coalesce(p.name, ''), t.category_id, coalesce(c.name, ''),
    (SELECT count(*) FROM transaction_splits s WHERE s.transaction_id = t.id), t.refund_of_transaction_id,
    coalesce((SELECT oa.name FROM transfer_legs own
        JOIN transfer_legs other ON other.transfer_id = own.transfer_id AND other.transaction_id != own.transaction_id
        JOIN transactions ot ON ot.id = other.transaction_id JOIN accounts oa ON oa.id = ot.account_id
        WHERE own.transaction_id = t.id), ''),
    t.import_batch_id IS NOT NULL
FROM transactions t` + registerJoins

func scanViews(rows *sql.Rows) ([]transactionView, error) {
	defer rows.Close()
	views := []transactionView{}
	for rows.Next() {
		var view transactionView
		var minor int64
		var payee, category, refund sql.NullInt64
		if err := rows.Scan(&view.ID, &view.AccountID, &view.AccountName, &view.Date, &view.Kind, &minor,
			&view.Currency, &view.EURCents, &view.Notes, &view.ReviewStatus, &view.Revision, &payee,
			&view.PayeeName, &category, &view.CategoryName, &view.SplitCount, &refund,
			&view.TransferAccountName, &view.Imported); err != nil {
			return nil, err
		}
		amount, err := decimal(minor, view.Currency)
		if err != nil {
			return nil, err
		}
		view.Amount = amount
		view.PayeeID, view.CategoryID, view.RefundOf = nullID(payee), nullID(category), nullID(refund)
		views = append(views, view)
	}
	return views, rows.Err()
}

func nullID(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func loadView(ctx context.Context, db dbgen.DBTX, id int64) (transactionView, error) {
	rows, err := db.QueryContext(ctx, viewColumns+" WHERE t.id = ?", id)
	if err != nil {
		return transactionView{}, err
	}
	views, err := scanViews(rows)
	if err != nil {
		return transactionView{}, err
	}
	if len(views) == 0 {
		return transactionView{}, sql.ErrNoRows
	}
	return views[0], nil
}

type registerPage struct {
	Transactions []transactionView `json:"transactions"`
	Total        int64             `json:"total"`
	Page         int               `json:"page"`
	PageSize     int               `json:"pageSize"`
}

// listTransactions returns one page of the register. The page's IDs are
// chosen first so joins and subqueries run only for the rows returned.
func (a *api) listTransactions(w http.ResponseWriter, r *http.Request, _ *session) {
	filter, fields := parseRegisterFilter(r.URL.Query())
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	where, args := filter.where()
	var total int64
	if err := a.store.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM transactions t"+registerJoins+where, args...).Scan(&total); err != nil {
		internalError(w, "count transactions")
		return
	}
	order := filter.orderBy()
	pageArgs := append(append([]any{}, args...), filter.pageSize, (filter.page-1)*filter.pageSize)
	rows, err := a.store.DB.QueryContext(r.Context(), viewColumns+" WHERE t.id IN (SELECT t.id FROM transactions t"+
		registerJoins+where+order+" LIMIT ? OFFSET ?)"+order, pageArgs...)
	if err != nil {
		internalError(w, "list transactions")
		return
	}
	views, err := scanViews(rows)
	if err != nil {
		internalError(w, "list transactions")
		return
	}
	writeJSON(w, http.StatusOK, registerPage{Transactions: views, Total: total, Page: filter.page, PageSize: filter.pageSize})
}

type categoryTotal struct {
	CategoryID   *int64 `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	// SpendingEURCents is expenses minus linked and unlinked refunds.
	SpendingEURCents int64 `json:"spendingEurCents"`
	IncomeEURCents   int64 `json:"incomeEurCents"`
}

type statisticsView struct {
	IncomeEURCents   int64           `json:"incomeEurCents"`
	ExpenseEURCents  int64           `json:"expenseEurCents"`
	RefundEURCents   int64           `json:"refundEurCents"`
	SpendingEURCents int64           `json:"spendingEurCents"`
	NetEURCents      int64           `json:"netEurCents"`
	TransferCount    int64           `json:"transferCount"`
	Categories       []categoryTotal `json:"categories"`
}

// statistics totals income and spending for the register filters.
func (a *api) statistics(w http.ResponseWriter, r *http.Request, _ *session) {
	filter, fields := parseRegisterFilter(r.URL.Query())
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	stats, err := a.computeStatistics(r.Context(), filter)
	if err != nil {
		internalError(w, "total transactions")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// computeStatistics totals income and spending for register filters. The
// register summary and the dashboard share it, so both always agree.
// Transfers move money between accounts and never count as income or
// spending. Refunds reduce the category they carry, which for linked refunds
// is always a category of the original expense.
func (a *api) computeStatistics(ctx context.Context, filter registerFilter) (statisticsView, error) {
	where, args := filter.where()
	var stats statisticsView
	err := a.store.DB.QueryRowContext(ctx, `SELECT
        coalesce(sum(CASE WHEN t.kind = 'income' THEN t.eur_cents END), 0),
        coalesce(sum(CASE WHEN t.kind = 'expense' THEN -t.eur_cents END), 0),
        coalesce(sum(CASE WHEN t.kind = 'refund' THEN t.eur_cents END), 0),
        count(CASE WHEN t.kind = 'transfer' THEN 1 END)
    FROM transactions t`+registerJoins+where, args...).Scan(&stats.IncomeEURCents, &stats.ExpenseEURCents, &stats.RefundEURCents, &stats.TransferCount)
	if err != nil {
		return stats, err
	}
	stats.SpendingEURCents = stats.ExpenseEURCents - stats.RefundEURCents
	stats.NetEURCents = stats.IncomeEURCents - stats.SpendingEURCents
	rows, err := a.store.DB.QueryContext(ctx, `WITH matched AS (
        SELECT t.id, t.kind, t.eur_cents, t.category_id FROM transactions t`+registerJoins+where+` AND t.kind != 'transfer'
    ), lines AS (
        SELECT matched.kind, coalesce(s.category_id, matched.category_id) AS category_id,
            coalesce(s.eur_cents, matched.eur_cents) AS eur_cents
        FROM matched LEFT JOIN transaction_splits s ON s.transaction_id = matched.id
    )
    SELECT lines.category_id, coalesce(categories.name, ''),
        sum(CASE WHEN lines.kind IN ('expense', 'refund') THEN -lines.eur_cents ELSE 0 END) AS spending,
        sum(CASE WHEN lines.kind = 'income' THEN lines.eur_cents ELSE 0 END)
    FROM lines LEFT JOIN categories ON categories.id = lines.category_id
    GROUP BY lines.category_id ORDER BY spending DESC, categories.name COLLATE NOCASE`, args...)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	stats.Categories = []categoryTotal{}
	for rows.Next() {
		var total categoryTotal
		var id sql.NullInt64
		if err := rows.Scan(&id, &total.CategoryName, &total.SpendingEURCents, &total.IncomeEURCents); err != nil {
			return stats, err
		}
		total.CategoryID = nullID(id)
		stats.Categories = append(stats.Categories, total)
	}
	return stats, rows.Err()
}
