package assistant

import (
	"database/sql"
	"net/url"
	"strconv"
	"strings"
)

// filter narrows the ledger lines a question is about.
type filter struct {
	from, to                     string
	payees, categories, accounts []int64
	search                       string
	minCents, maxCents           int64
	kinds                        []string
}

func ids(items []Named) []int64 {
	result := make([]int64, len(items))
	for i, item := range items {
		result[i] = item.ID
	}
	return result
}

// kindsFor lists the transaction kinds behind a direction. Spending is
// expenses less refunds; transfers are never income or spending.
func kindsFor(direction string) []string {
	switch direction {
	case income:
		return []string{"income"}
	case spending:
		return []string{"expense", "refund"}
	}
	return []string{"expense", "refund", "income"}
}

func (r *run) filterOf(in Interpretation, period Period, direction string) filter {
	return filter{
		from: period.From, to: period.To,
		payees: ids(in.Payees), categories: ids(in.Categories), accounts: ids(in.Accounts),
		search: in.Search, minCents: in.MinCents, maxCents: in.MaxCents, kinds: kindsFor(direction),
	}
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

// lines is the shared ledger view: one row per transaction, or per split
// line of a split transaction, with the split's category and payee. A
// linked refund without a payee takes the payee of the purchase. Every
// answer reads it, so totals match the dashboard and the register.
func (f filter) lines() (string, []any) {
	var where []string
	var args []any
	where = append(where, "t.deleted_at IS NULL")
	if f.from != "" {
		where, args = append(where, "t.local_date >= ?"), append(args, f.from)
	}
	if f.to != "" {
		where, args = append(where, "t.local_date <= ?"), append(args, f.to)
	}
	if len(f.accounts) > 0 {
		where = append(where, "t.account_id IN ("+placeholders(len(f.accounts))+")")
		for _, id := range f.accounts {
			args = append(args, id)
		}
	}
	if len(f.kinds) > 0 {
		where = append(where, "t.kind IN ("+placeholders(len(f.kinds))+")")
		for _, kind := range f.kinds {
			args = append(args, kind)
		}
	}
	if f.minCents > 0 {
		where, args = append(where, "abs(t.eur_cents) >= ?"), append(args, f.minCents)
	}
	if f.maxCents > 0 {
		where, args = append(where, "abs(t.eur_cents) <= ?"), append(args, f.maxCents)
	}
	inner := `SELECT t.id, t.local_date, t.kind, t.account_id, t.notes, t.currency, t.import_batch_id,
        coalesce(s.category_id, t.category_id) AS category_id,
        coalesce(s.payee_id, t.payee_id, (SELECT o.payee_id FROM transactions o WHERE o.id = t.refund_of_transaction_id)) AS payee_id,
        coalesce(s.eur_cents, t.eur_cents) AS eur_cents,
        coalesce(s.amount_minor, t.amount_minor) AS amount_minor
    FROM transactions t LEFT JOIN transaction_splits s ON s.transaction_id = t.id
    WHERE ` + strings.Join(where, " AND ")
	var outer []string
	if len(f.payees) > 0 {
		outer = append(outer, "payee_id IN ("+placeholders(len(f.payees))+")")
		for _, id := range f.payees {
			args = append(args, id)
		}
	}
	if len(f.categories) > 0 {
		// Lines without a category count as Uncategorized.
		condition := "(category_id IN (" + placeholders(len(f.categories)) + ") OR (category_id IS NULL AND EXISTS (SELECT 1 FROM categories u WHERE u.normalized_name = 'uncategorized' AND u.id IN (" + placeholders(len(f.categories)) + "))))"
		outer = append(outer, condition)
		for range 2 {
			for _, id := range f.categories {
				args = append(args, id)
			}
		}
	}
	if f.search != "" {
		outer = append(outer, `(instr(lower(notes), lower(?)) > 0
            OR EXISTS (SELECT 1 FROM payees p WHERE p.id = payee_id AND instr(lower(p.name), lower(?)) > 0)
            OR EXISTS (SELECT 1 FROM categories c WHERE c.id = category_id AND instr(lower(c.name), lower(?)) > 0))`)
		args = append(args, f.search, f.search, f.search)
	}
	query := "WITH lines AS (SELECT * FROM (" + inner + ")"
	if len(outer) > 0 {
		query += " WHERE " + strings.Join(outer, " AND ")
	}
	return query + ") ", args
}

// Amount expressions over lines. Spending is positive for money out.
const (
	spentSQL  = `CASE WHEN kind IN ('expense', 'refund') THEN -eur_cents ELSE 0 END`
	incomeSQL = `CASE WHEN kind = 'income' THEN eur_cents ELSE 0 END`
)

// valueSQL is the amount a direction ranks by.
func valueSQL(direction string) string {
	if direction == income {
		return incomeSQL
	}
	return spentSQL
}

func (r *run) query(query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := r.db.QueryContext(r.ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// totals sums spending and income and counts transactions.
type totals struct {
	spent, income, refunds, count int64
	first, last                   string
}

func (r *run) totals(f filter) (totals, error) {
	cte, args := f.lines()
	var result totals
	var first, last sql.NullString
	err := r.db.QueryRowContext(r.ctx, cte+`SELECT coalesce(sum(`+spentSQL+`), 0), coalesce(sum(`+incomeSQL+`), 0),
        coalesce(sum(CASE WHEN kind = 'refund' THEN eur_cents ELSE 0 END), 0), count(DISTINCT id), min(local_date), max(local_date) FROM lines`, args...).
		Scan(&result.spent, &result.income, &result.refunds, &result.count, &first, &last)
	result.first, result.last = first.String, last.String
	return result, err
}

// registerLink opens the matching transactions when the register can show
// them, which takes at most one payee, category, and account.
func registerLink(in Interpretation, period Period, kind string) *Link {
	if len(in.Payees) > 1 || len(in.Categories) > 1 || len(in.Accounts) > 1 {
		return nil
	}
	values := url.Values{}
	if period.From != "" {
		values.Set("from", period.From)
	}
	if period.To != "" {
		values.Set("to", period.To)
	}
	if len(in.Payees) == 1 {
		values.Set("payee", strconv.FormatInt(in.Payees[0].ID, 10))
	}
	if len(in.Categories) == 1 {
		values.Set("category", strconv.FormatInt(in.Categories[0].ID, 10))
	}
	if len(in.Accounts) == 1 {
		values.Set("account", strconv.FormatInt(in.Accounts[0].ID, 10))
	}
	if in.Search != "" {
		values.Set("q", in.Search)
	}
	if kind != "" {
		values.Set("kind", kind)
	}
	href := "/transactions/"
	if len(values) > 0 {
		href += "?" + values.Encode()
	}
	return &Link{Label: "Open in Transactions", Href: href}
}

// linkKind is the register kind filter for a direction, when it is one kind.
func linkKind(direction string) string {
	switch direction {
	case income:
		return "income"
	case spending:
		return "expense"
	}
	return ""
}

// scope describes the filters in words, such as " at Lidl from Revolut".
func scope(in Interpretation) string {
	var parts []string
	if len(in.Payees) > 0 {
		parts = append(parts, "at "+joinOr(namesOf(in.Payees)))
	}
	if len(in.Categories) > 0 {
		parts = append(parts, "on "+joinOr(namesOf(in.Categories)))
	}
	if len(in.Accounts) > 0 {
		parts = append(parts, "from "+joinOr(namesOf(in.Accounts)))
	}
	if in.Search != "" {
		parts = append(parts, "matching \""+in.Search+"\"")
	}
	switch {
	case in.MinCents > 0 && in.MaxCents > 0:
		parts = append(parts, "between "+euros(in.MinCents)+" and "+euros(in.MaxCents))
	case in.MinCents > 0:
		parts = append(parts, "of at least "+euros(in.MinCents))
	case in.MaxCents > 0:
		parts = append(parts, "of at most "+euros(in.MaxCents))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func joinOr(names []string) string {
	if len(names) <= 2 {
		return strings.Join(names, " or ")
	}
	return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
}

// scopeNotes explain the period and how names were matched.
func scopeNotes(in Interpretation, period Period) []string {
	var notes []string
	if period.From != "" || period.To != "" {
		notes = append(notes, "Period: "+period.dates()+".")
	}
	if len(in.Payees) > 1 {
		notes = append(notes, "Payees matched: "+joinNames(namesOf(in.Payees))+".")
	}
	if len(in.Categories) > 1 {
		notes = append(notes, "Categories matched: "+joinNames(namesOf(in.Categories))+".")
	}
	if in.Search != "" {
		notes = append(notes, "Includes transactions whose notes, payee, or category contain \""+in.Search+"\".")
	}
	if in.MinCents > 0 || in.MaxCents > 0 {
		notes = append(notes, "Amount limits compare the euro value of whole transactions.")
	}
	return notes
}
