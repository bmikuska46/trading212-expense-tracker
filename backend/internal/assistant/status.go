package assistant

import (
	"database/sql"
	"fmt"
)

// effectiveReview matches the register: confirming an import confirms the
// transactions in it.
const effectiveReview = `CASE WHEN t.review_status = 'needs_review' AND EXISTS (SELECT 1 FROM import_batches b
    WHERE b.id = t.import_batch_id AND b.review_status = 'confirmed') THEN 'confirmed' ELSE t.review_status END`

func (r *run) reviewStatus() (Answer, error) {
	var needsReview, conflicts, imports int64
	if err := r.db.QueryRowContext(r.ctx, `SELECT count(CASE WHEN review = 'needs_review' THEN 1 END),
        count(CASE WHEN review = 'source_conflict' THEN 1 END)
    FROM (SELECT `+effectiveReview+` AS review FROM transactions t
        WHERE t.deleted_at IS NULL AND t.review_status IN ('needs_review', 'source_conflict'))`).Scan(&needsReview, &conflicts); err != nil {
		return Answer{}, err
	}
	table := &Table{Columns: []Column{{Label: "Import", Kind: "text"}, {Label: "Account", Kind: "text"}, {Label: "New transactions", Kind: "count"}, {Label: "Imported", Kind: "date"}}}
	err := r.query(`SELECT b.filename, a.name, b.inserted_count, coalesce(substr(b.committed_at, 1, 10), '')
    FROM import_batches b JOIN accounts a ON a.id = b.source_account_id
    WHERE b.review_status = 'needs_review' ORDER BY b.committed_at, b.id`, nil, func(rows *sql.Rows) error {
		var name, account, committed string
		var inserted int64
		if err := rows.Scan(&name, &account, &inserted, &committed); err != nil {
			return err
		}
		imports++
		table.Rows = append(table.Rows, []any{name, account, inserted, committed})
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	if needsReview == 0 && conflicts == 0 && imports == 0 {
		return Answer{Text: "Nothing needs your review. Every transaction and import is confirmed.", Suggestions: []string{"When was my last import?", "How much did I spend this month?"}}, nil
	}
	var parts []string
	if needsReview > 0 {
		parts = append(parts, count(needsReview, "transaction needs", "transactions need")+" review")
	}
	if conflicts > 0 {
		parts = append(parts, count(conflicts, "transaction has", "transactions have")+" a conflict with a newer import")
	}
	if imports > 0 {
		parts = append(parts, count(imports, "import is", "imports are")+" waiting for confirmation")
	}
	text := upperFirst(joinNames(parts)) + "."
	if imports == 0 {
		table = nil
	}
	link := &Link{Label: "Open Transactions to review", Href: "/transactions/?review=needs_review"}
	if needsReview == 0 && conflicts > 0 {
		link.Href = "/transactions/?review=source_conflict"
	}
	return Answer{Text: text, Table: table, Link: link}, nil
}

func (r *run) importsStatus(in Interpretation) (Answer, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	var where string
	var args []any
	if in.PeriodGiven && in.Period.From != "" {
		where, args = " WHERE substr(b.created_at, 1, 10) BETWEEN ? AND ?", []any{in.Period.From, in.Period.To}
	}
	table := &Table{Columns: []Column{{Label: "File", Kind: "text"}, {Label: "Account", Kind: "text"}, {Label: "Status", Kind: "text"},
		{Label: "New transactions", Kind: "count"}, {Label: "Imported", Kind: "date"}}}
	type batch struct {
		name, account, status, created string
		inserted                       int64
	}
	var batches []batch
	err := r.query(`SELECT b.filename, a.name, b.status, b.review_status, b.inserted_count, substr(b.created_at, 1, 10)
    FROM import_batches b JOIN accounts a ON a.id = b.source_account_id`+where+`
    ORDER BY b.created_at DESC, b.id DESC LIMIT ?`, append(args, limit), func(rows *sql.Rows) error {
		var b batch
		var review string
		if err := rows.Scan(&b.name, &b.account, &b.status, &review, &b.inserted, &b.created); err != nil {
			return err
		}
		b.status = map[string]string{"staging": "Not finished", "validating": "Checking", "failed": "Failed", "committed": "Imported"}[b.status]
		if review == "needs_review" {
			b.status = "Waiting for review"
		}
		batches = append(batches, b)
		table.Rows = append(table.Rows, []any{b.name, b.account, b.status, b.inserted, b.created})
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	link := &Link{Label: "Open Imports", Href: "/imports/"}
	if len(batches) == 0 {
		return Answer{Text: "You have no imports" + phrase(in.Period) + ".", Link: link}, nil
	}
	last := batches[0]
	text := fmt.Sprintf("Your last import was %s into %s on %s: %s, %s.", last.name, last.account, dayLabel(last.created),
		lower(last.status), count(last.inserted, "new transaction", "new transactions"))
	return Answer{Text: text, Table: table, Link: link}, nil
}

func lower(text string) string {
	if text == "" {
		return text
	}
	return string(text[0]|0x20) + text[1:]
}

func (r *run) dataOverview() (Answer, error) {
	var transactions, payees, categories, accounts, rules, imports, chats int64
	var first, last sql.NullString
	err := r.db.QueryRowContext(r.ctx, `SELECT
        (SELECT count(*) FROM transactions WHERE deleted_at IS NULL),
        (SELECT min(local_date) FROM transactions WHERE deleted_at IS NULL),
        (SELECT max(local_date) FROM transactions WHERE deleted_at IS NULL),
        (SELECT count(*) FROM payees), (SELECT count(*) FROM categories), (SELECT count(*) FROM accounts),
        (SELECT count(*) FROM categorization_rules), (SELECT count(*) FROM import_batches WHERE status = 'committed'),
        (SELECT count(*) FROM chats)`).
		Scan(&transactions, &first, &last, &payees, &categories, &accounts, &rules, &imports, &chats)
	if err != nil {
		return Answer{}, err
	}
	table := &Table{Columns: []Column{{Label: "Item", Kind: "text"}, {Label: "Count", Kind: "count"}}, Rows: [][]any{
		{"Transactions", transactions}, {"Payees", payees}, {"Categories", categories}, {"Accounts", accounts},
		{"Categorization rules", rules}, {"Imports", imports}, {"Chats", chats},
	}}
	text := fmt.Sprintf("You have %s", count(transactions, "transaction", "transactions"))
	if first.Valid && last.Valid {
		text += " from " + dayLabel(first.String) + " to " + dayLabel(last.String)
	}
	text += fmt.Sprintf(", with %s, %s, and %s.", count(payees, "payee", "payees"), count(categories, "category", "categories"), count(accounts, "account", "accounts"))
	return Answer{Text: text, Table: table}, nil
}
