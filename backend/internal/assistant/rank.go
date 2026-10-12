package assistant

import (
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strings"
)

// grouping names what lines are grouped by and how a group is named.
type grouping struct {
	key, join, name, noun, plural string
}

var (
	payeeGroup = grouping{key: "payee_id", join: "JOIN payees g ON g.id = lines.payee_id", name: "g.name", noun: "payee", plural: "payees"}
	// Lines without a category belong to Uncategorized.
	categoryGroup = grouping{
		key:  "coalesce(category_id, (SELECT id FROM categories WHERE normalized_name = 'uncategorized'))",
		join: "LEFT JOIN categories g ON g.id = coalesce(lines.category_id, (SELECT id FROM categories WHERE normalized_name = 'uncategorized'))",
		name: "coalesce(g.name, 'Uncategorized')", noun: "category", plural: "categories",
	}
	accountGroup = grouping{key: "account_id", join: "JOIN accounts g ON g.id = lines.account_id", name: "g.name", noun: "account", plural: "accounts"}
)

type group struct {
	id          int64
	name        string
	value       int64
	count       int64
	first, last string
}

// groups totals value per group, keeping groups with a positive total.
// order is "value", "least", or "count"; limit 0 keeps every group.
func (r *run) groups(f filter, by grouping, value, order string, limit int) ([]group, error) {
	cte, args := f.lines()
	orderBy := "v DESC, c DESC"
	switch order {
	case "least":
		orderBy = "v ASC, c DESC"
	case "count":
		orderBy = "c DESC, v DESC"
	}
	query := cte + `SELECT ` + by.key + ` AS k, ` + by.name + `, sum(` + value + `) AS v, count(DISTINCT lines.id) AS c,
        min(local_date), max(local_date)
    FROM lines ` + by.join + ` GROUP BY k HAVING v > 0 ORDER BY ` + orderBy + `, 2`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	var result []group
	err := r.query(query, args, func(rows *sql.Rows) error {
		var g group
		var id sql.NullInt64
		if err := rows.Scan(&id, &g.name, &g.value, &g.count, &g.first, &g.last); err != nil {
			return err
		}
		g.id = id.Int64
		result = append(result, g)
		return nil
	})
	return result, err
}

// rankTable lists groups with their share of total when total is known.
func rankTable(label string, groups []group, total int64) *Table {
	if len(groups) == 0 {
		return nil
	}
	table := &Table{Columns: []Column{{Label: label, Kind: "text"}, {Label: "Amount", Kind: "money", Bar: true}, {Label: "Transactions", Kind: "count"}}}
	if total > 0 {
		table.Columns = append(table.Columns, Column{Label: "Share", Kind: "percent"})
	}
	for _, g := range groups {
		row := []any{g.name, g.value, g.count}
		if total > 0 {
			row = append(row, float64(g.value)/float64(total))
		}
		table.Rows = append(table.Rows, row)
	}
	return table
}

func withKinds(f filter, kinds ...string) filter {
	f.kinds = kinds
	return f
}

func rankDirection(direction string) string {
	if direction == both {
		return spending
	}
	return direction
}

func (r *run) rank(in Interpretation, by grouping) (Answer, error) {
	direction := rankDirection(in.Direction)
	f := r.filterOf(in, in.Period, direction)
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	order := "value"
	if in.Order == "least" {
		order = "least"
	}
	groups, err := r.groups(f, by, valueSQL(direction), order, limit)
	if err != nil {
		return Answer{}, err
	}
	t, err := r.totals(f)
	if err != nil {
		return Answer{}, err
	}
	total := t.spent
	if direction == income {
		total = t.income
	}
	notes := scopeNotes(in, in.Period)
	if len(groups) == 0 {
		noun := "spending"
		if direction == income {
			noun = "income"
		}
		return Answer{Text: "There is no " + noun + scope(in) + phrase(in.Period) + " to rank by " + by.noun + ".", Notes: notes}, nil
	}
	top := groups[0]
	var text string
	switch {
	case direction == income && in.Order == "least":
		text = fmt.Sprintf("The %s that paid you the least%s%s is %s with %s.", by.noun, scope(in), phrase(in.Period), top.name, euros(top.value))
	case direction == income:
		text = fmt.Sprintf("Your biggest source of income by %s%s%s is %s with %s across %s.", by.noun, scope(in), phrase(in.Period), top.name,
			euros(top.value), count(top.count, "transaction", "transactions"))
	case in.Order == "least":
		text = fmt.Sprintf("Of the %s you spent money on%s%s, the smallest is %s with %s.", by.plural, scope(in), phrase(in.Period), top.name, euros(top.value))
	default:
		text = fmt.Sprintf("Your biggest %s%s%s is %s with %s across %s", by.noun, scope(in), phrase(in.Period), top.name, euros(top.value),
			count(top.count, "transaction", "transactions"))
		if total > 0 {
			text += ", " + percent(float64(top.value)/float64(total)) + " of the total"
		}
		text += "."
	}
	if by == payeeGroup && direction == spending {
		unassigned, err := r.unassignedPayee(f)
		if err != nil {
			return Answer{}, err
		}
		if unassigned > 0 {
			notes = append(notes, euros(unassigned)+" of spending has no payee and is not ranked.")
		}
	}
	if t.refunds > 0 && direction == spending {
		notes = append(notes, "Refunds are subtracted from each "+by.noun+".")
	}
	return Answer{Text: text, Table: rankTable(upperFirst(by.noun), groups, total), Notes: notes, Link: registerLink(in, in.Period, linkKind(direction))}, nil
}

func (r *run) unassignedPayee(f filter) (int64, error) {
	cte, args := f.lines()
	var value int64
	err := r.db.QueryRowContext(r.ctx, cte+`SELECT coalesce(sum(`+spentSQL+`), 0) FROM lines WHERE payee_id IS NULL`, args...).Scan(&value)
	return value, err
}

func (r *run) topPayees(in Interpretation) (Answer, error)     { return r.rank(in, payeeGroup) }
func (r *run) topCategories(in Interpretation) (Answer, error) { return r.rank(in, categoryGroup) }
func (r *run) topAccounts(in Interpretation) (Answer, error)   { return r.rank(in, accountGroup) }

func (r *run) frequentPayees(in Interpretation) (Answer, error) {
	direction := rankDirection(in.Direction)
	f := r.filterOf(in, in.Period, direction)
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	groups, err := r.groups(f, payeeGroup, valueSQL(direction), "count", limit)
	if err != nil {
		return Answer{}, err
	}
	notes := scopeNotes(in, in.Period)
	if len(groups) == 0 {
		return Answer{Text: "There are no payees" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	table := &Table{Columns: []Column{{Label: "Payee", Kind: "text"}, {Label: "Transactions", Kind: "count", Bar: true},
		{Label: "Total", Kind: "money"}, {Label: "Average", Kind: "money"}, {Label: "Last", Kind: "date"}}}
	for _, g := range groups {
		table.Rows = append(table.Rows, []any{g.name, g.count, g.value, g.value / max(g.count, 1), g.last})
	}
	top := groups[0]
	text := fmt.Sprintf("You pay %s most often%s: %s%s, %s in total.", top.name, scope(in), count(top.count, "time", "times"), phrase(in.Period), euros(top.value))
	if direction == income {
		text = fmt.Sprintf("%s pays you most often%s: %s%s, %s in total.", top.name, scope(in), count(top.count, "time", "times"), phrase(in.Period), euros(top.value))
	}
	return Answer{Text: text, Table: table, Notes: notes}, nil
}

// row is one transaction, with the names of its lines joined.
type row struct {
	id                             int64
	date, account, payee, category string
	notes, currency, kind          string
	value, eurCents, amountMinor   int64
}

func (t row) label() string {
	switch {
	case t.payee != "":
		return t.payee
	case t.notes != "":
		return t.notes
	case t.kind == "transfer":
		return "a transfer"
	}
	return t.account
}

// transactionsOf lists whole transactions matching the filter. value is
// an expression over lines summed per transaction.
func (r *run) transactionsOf(f filter, value, order string, limit int) ([]row, error) {
	cte, args := f.lines()
	query := cte + `SELECT lines.id, lines.local_date, a.name, coalesce(group_concat(DISTINCT p.name), ''),
        CASE WHEN lines.kind = 'transfer' THEN 'Transfer' ELSE coalesce(group_concat(DISTINCT coalesce(c.name, 'Uncategorized')), '') END, lines.notes, lines.currency, lines.kind,
        sum(` + value + `) AS value, sum(lines.eur_cents), sum(lines.amount_minor)
    FROM lines JOIN accounts a ON a.id = lines.account_id
    LEFT JOIN payees p ON p.id = lines.payee_id LEFT JOIN categories c ON c.id = lines.category_id
    GROUP BY lines.id ORDER BY ` + order + `, lines.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	var result []row
	err := r.query(query, args, func(rows *sql.Rows) error {
		var t row
		if err := rows.Scan(&t.id, &t.date, &t.account, &t.payee, &t.category, &t.notes, &t.currency, &t.kind,
			&t.value, &t.eurCents, &t.amountMinor); err != nil {
			return err
		}
		t.payee = strings.ReplaceAll(t.payee, ",", ", ")
		t.category = strings.ReplaceAll(t.category, ",", ", ")
		result = append(result, t)
		return nil
	})
	return result, err
}

func transactionTable(rows []row) *Table {
	table := &Table{Columns: []Column{{Label: "Date", Kind: "date"}, {Label: "Payee", Kind: "text"}, {Label: "Category", Kind: "text"},
		{Label: "Account", Kind: "text"}, {Label: "Amount", Kind: "money"}}}
	for _, t := range rows {
		payee := t.payee
		if payee == "" {
			payee = t.notes
		}
		table.Rows = append(table.Rows, []any{t.date, payee, t.category, t.account, t.eurCents})
	}
	return table
}

func (r *run) largestTransactions(in Interpretation) (Answer, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	f := r.filterOf(in, in.Period, in.Direction)
	value, noun := "-eur_cents", "purchases"
	switch in.Direction {
	case income:
		f, value, noun = withKinds(f, "income"), "eur_cents", "incomes"
	case both:
		f, value, noun = withKinds(f, "expense", "income"), "abs(eur_cents)", "transactions"
	default:
		f = withKinds(f, "expense")
	}
	order := "value DESC"
	adjective := "biggest"
	if in.Order == "least" {
		order, adjective = "value ASC", "smallest"
	}
	rows, err := r.transactionsOf(f, value, order, limit)
	if err != nil {
		return Answer{}, err
	}
	notes := scopeNotes(in, in.Period)
	if len(rows) == 0 {
		return Answer{Text: "There are no " + noun + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	top := rows[0]
	text := fmt.Sprintf("Here are your %s %s%s%s. The %s was %s at %s on %s.", adjective, noun, scope(in), phrase(in.Period),
		adjective, euros(top.value), top.label(), dayLabel(top.date))
	if limit == 1 {
		single := map[string]string{"purchases": "purchase", "incomes": "income", "transactions": "transaction"}[noun]
		text = fmt.Sprintf("Your %s single %s%s%s was %s, %s on %s.", adjective, single, scope(in), phrase(in.Period),
			euros(top.value), top.label(), dayLabel(top.date))
	}
	return Answer{Text: text, Table: transactionTable(rows), Notes: notes, Link: registerLink(in, in.Period, linkKind(in.Direction))}, nil
}

func (r *run) listTransactions(in Interpretation) (Answer, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	f := r.filterOf(in, in.Period, in.Direction)
	if in.Direction == both {
		f = withKinds(f, "expense", "refund", "income", "transfer")
	}
	order := "lines.local_date DESC"
	switch in.Order {
	case "most":
		order = "abs(sum(lines.eur_cents)) DESC"
	case "least":
		order = "abs(sum(lines.eur_cents)) ASC"
	}
	rows, err := r.transactionsOf(f, "eur_cents", order, limit)
	if err != nil {
		return Answer{}, err
	}
	t, err := r.totals(f)
	if err != nil {
		return Answer{}, err
	}
	notes := scopeNotes(in, in.Period)
	if len(rows) == 0 {
		return Answer{Text: "No transactions match" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	text := fmt.Sprintf("I found %s%s%s", count(t.count, "transaction", "transactions"), scope(in), phrase(in.Period))
	switch in.Direction {
	case income:
		text += ", totaling " + euros(t.income)
	case spending:
		text += ", totaling " + euros(t.spent) + " spent"
	}
	text += "."
	if t.count > int64(len(rows)) {
		which := "latest"
		if in.Order != "not_mentioned" {
			which = "largest"
			if in.Order == "least" {
				which = "smallest"
			}
		}
		text += fmt.Sprintf(" Here are the %s %d.", which, len(rows))
	}
	return Answer{Text: text, Table: transactionTable(rows), Notes: notes, Link: registerLink(in, in.Period, linkKind(in.Direction))}, nil
}

func (r *run) transactionCount(in Interpretation) (Answer, error) {
	f := r.filterOf(in, in.Period, in.Direction)
	noun, plural := "transaction", "transactions"
	switch in.Direction {
	case spending:
		f, noun, plural = withKinds(f, "expense"), "purchase", "purchases"
	case income:
		noun, plural = "payment received", "payments received"
	default:
		f = withKinds(f, "expense", "refund", "income", "transfer")
	}
	t, err := r.totals(f)
	if err != nil {
		return Answer{}, err
	}
	text := fmt.Sprintf("You have %s%s%s.", count(t.count, noun, plural), scope(in), phrase(in.Period))
	if t.count > 0 && in.Direction == spending {
		text = fmt.Sprintf("You made %s%s%s, %s in total, %s on average.", count(t.count, noun, plural), scope(in), phrase(in.Period),
			euros(t.spent), euros(t.spent/t.count))
	}
	notes := scopeNotes(in, in.Period)
	if t.count > 1 && t.first != "" && t.last != "" {
		notes = append(notes, "From "+dayLabel(t.first)+" to "+dayLabel(t.last)+".")
	}
	return Answer{Text: text, Notes: notes, Link: registerLink(in, in.Period, linkKind(in.Direction))}, nil
}

func (r *run) edgeTransaction(in Interpretation, latest bool) (Answer, error) {
	f := r.filterOf(in, in.Period, in.Direction)
	if in.Direction == both {
		f = withKinds(f, "expense", "refund", "income", "transfer")
	}
	order, word := "lines.local_date ASC", "first"
	if latest {
		order, word = "lines.local_date DESC", "last"
	}
	limit := max(in.Limit, 1)
	rows, err := r.transactionsOf(f, "eur_cents", order, limit)
	if err != nil {
		return Answer{}, err
	}
	notes := scopeNotes(in, in.Period)
	if len(rows) == 0 {
		return Answer{Text: "There are no transactions" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	t := rows[0]
	when := dayLabel(t.date)
	if day, ok := parseDay(t.date); ok {
		if ago := int(r.today.Sub(day).Hours() / 24); ago == 0 {
			when += " (today)"
		} else if ago == 1 {
			when += " (yesterday)"
		} else if ago > 1 {
			when += fmt.Sprintf(" (%s ago)", elapsedText(ago))
		}
	}
	amount := money(abs(t.amountMinor), t.currency)
	if t.currency != "EUR" {
		amount += " (" + euros(abs(t.eurCents)) + ")"
	}
	var text string
	switch t.kind {
	case "income":
		text = fmt.Sprintf("Your %s income%s was %s from %s on %s.", word, scope(in), amount, t.label(), when)
	case "refund":
		text = fmt.Sprintf("Your %s transaction%s was a refund of %s from %s on %s.", word, scope(in), amount, t.label(), when)
	case "transfer":
		text = fmt.Sprintf("Your %s transaction%s was a transfer of %s on %s, from %s.", word, scope(in), amount, when, t.account)
	default:
		text = fmt.Sprintf("Your %s payment%s was %s at %s on %s.", word, scope(in), amount, t.label(), when)
	}
	result := Answer{Text: text, Notes: notes, Link: registerLink(in, in.Period, linkKind(in.Direction))}
	if len(rows) > 1 {
		result.Table = transactionTable(rows)
	}
	return result, nil
}

func elapsedText(days int) string {
	switch {
	case days < 14:
		return count(int64(days), "day", "days")
	case days < 60:
		return count(int64(days/7), "week", "weeks")
	case days < 730:
		return count(int64(days/30), "month", "months")
	}
	return count(int64(days/365), "year", "years")
}

// recurring finds payees paid in at least three different months with a
// steady amount, which is how subscriptions and bills look in a ledger.
func (r *run) recurring(in Interpretation) (Answer, error) {
	direction := rankDirection(in.Direction)
	f := r.filterOf(in, in.Period, direction)
	if direction == spending {
		f = withKinds(f, "expense")
	}
	cte, args := f.lines()
	value := "-eur_cents"
	if direction == income {
		value = "eur_cents"
	}
	type payee struct {
		name   string
		months map[string]int64
		last   string
	}
	payees := map[int64]*payee{}
	var orderIDs []int64
	err := r.query(cte+`SELECT lines.payee_id, p.name, substr(local_date, 1, 7), sum(`+value+`), max(local_date)
        FROM lines JOIN payees p ON p.id = lines.payee_id GROUP BY 1, 3 ORDER BY 1, 3`, args, func(rows *sql.Rows) error {
		var id, amount int64
		var name, month, last string
		if err := rows.Scan(&id, &name, &month, &amount, &last); err != nil {
			return err
		}
		p, ok := payees[id]
		if !ok {
			p = &payee{name: name, months: map[string]int64{}}
			payees[id] = p
			orderIDs = append(orderIDs, id)
		}
		p.months[month] = amount
		p.last = max(p.last, last)
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	type found struct {
		name          string
		typical       int64
		months        int
		last          string
		active, fixed bool
	}
	var list []found
	recent := dateOf(r.today.AddDate(0, 0, -45))
	for _, id := range orderIDs {
		p := payees[id]
		if len(p.months) < 3 {
			continue
		}
		amounts := make([]int64, 0, len(p.months))
		for _, amount := range p.months {
			if amount > 0 {
				amounts = append(amounts, amount)
			}
		}
		if len(amounts) < 3 {
			continue
		}
		slices.Sort(amounts)
		median := amounts[len(amounts)/2]
		steady := 0
		for _, amount := range amounts {
			if math.Abs(float64(amount-median)) <= 0.2*float64(median) {
				steady++
			}
		}
		if float64(steady) < 0.6*float64(len(amounts)) {
			continue
		}
		list = append(list, found{name: p.name, typical: median, months: len(amounts), last: p.last,
			active: p.last >= recent, fixed: amounts[0] == amounts[len(amounts)-1]})
	}
	notes := append(scopeNotes(in, in.Period),
		"A payment counts as recurring when the same payee was paid in at least 3 different months with an amount within 20% of its usual one.",
		"Active means paid in the last 45 days. Yearly subscriptions need three years of data to show up.")
	if len(list) == 0 {
		return Answer{Text: "I found no recurring payments" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	slices.SortStableFunc(list, func(a, b found) int {
		if a.active != b.active {
			if a.active {
				return -1
			}
			return 1
		}
		return compareInt(b.typical, a.typical)
	})
	if in.Limit > 0 && len(list) > in.Limit {
		list = list[:in.Limit]
	}
	table := &Table{Columns: []Column{{Label: "Payee", Kind: "text"}, {Label: "Usual amount", Kind: "money", Bar: true},
		{Label: "Months paid", Kind: "count"}, {Label: "Last paid", Kind: "date"}, {Label: "Status", Kind: "text"}}}
	var monthly int64
	active := 0
	for _, item := range list {
		status := "Active"
		if !item.active {
			status = "Stopped?"
		} else {
			monthly += item.typical
			active++
		}
		table.Rows = append(table.Rows, []any{item.name, item.typical, item.months, item.last, status})
	}
	text := fmt.Sprintf("I found %s%s. The %d still active cost about %s a month, or %s a year.",
		count(int64(len(list)), "recurring payment", "recurring payments"), phrase(in.Period), active, euros(monthly), euros(monthly*12))
	if direction == income {
		text = fmt.Sprintf("I found %s%s, about %s a month from the active ones.", count(int64(len(list)), "regular income", "regular incomes"), phrase(in.Period), euros(monthly))
	}
	return Answer{Text: text, Table: table, Notes: notes}, nil
}

func (r *run) newPayees(in Interpretation) (Answer, error) {
	direction := rankDirection(in.Direction)
	period := in.Period
	if period.From == "" {
		period, _ = resolvePeriod("this_month", "", r.today)
	}
	whole := r.filterOf(in, Period{To: period.To}, direction)
	cte, args := whole.lines()
	table := &Table{Columns: []Column{{Label: "Payee", Kind: "text"}, {Label: "First paid", Kind: "date"}, {Label: "Amount", Kind: "money", Bar: true},
		{Label: "Transactions", Kind: "count"}}}
	var names []string
	var total int64
	err := r.query(cte+`SELECT p.name, min(local_date) AS first, sum(CASE WHEN local_date >= ? THEN `+valueSQL(direction)+` ELSE 0 END),
        count(DISTINCT CASE WHEN local_date >= ? THEN lines.id END)
    FROM lines JOIN payees p ON p.id = lines.payee_id GROUP BY lines.payee_id HAVING first >= ? ORDER BY 3 DESC, 1`,
		append(args, period.From, period.From, period.From), func(rows *sql.Rows) error {
			var name, first string
			var amount, number int64
			if err := rows.Scan(&name, &first, &amount, &number); err != nil {
				return err
			}
			names = append(names, name)
			total += amount
			table.Rows = append(table.Rows, []any{name, first, amount, number})
			return nil
		})
	if err != nil {
		return Answer{}, err
	}
	notes := scopeNotes(in, period)
	if len(names) == 0 {
		return Answer{Text: "You paid no new payees" + scope(in) + phrase(period) + ".", Notes: notes}, nil
	}
	text := fmt.Sprintf("You paid %s for the first time%s%s, %s in total.", count(int64(len(names)), "new payee", "new payees"), scope(in), phrase(period), euros(total))
	if direction == income {
		text = fmt.Sprintf("You received money from %s for the first time%s%s, %s in total.", count(int64(len(names)), "new payer", "new payers"), scope(in), phrase(period), euros(total))
	}
	if limit := in.Limit; limit > 0 && len(table.Rows) > limit {
		table.Rows = table.Rows[:limit]
	}
	return Answer{Text: text, Table: table, Notes: notes}, nil
}

func (r *run) uncategorizedID() (int64, error) {
	var id int64
	err := r.db.QueryRowContext(r.ctx, `SELECT id FROM categories WHERE normalized_name = 'uncategorized'`).Scan(&id)
	return id, err
}

func (r *run) uncategorized(in Interpretation) (Answer, error) {
	direction := rankDirection(in.Direction)
	id, err := r.uncategorizedID()
	if err != nil {
		return Answer{}, err
	}
	all := r.filterOf(in, in.Period, direction)
	all.categories = nil
	t, err := r.totals(all)
	if err != nil {
		return Answer{}, err
	}
	only := all
	only.categories = []int64{id}
	u, err := r.totals(only)
	if err != nil {
		return Answer{}, err
	}
	whole, part := t.spent, u.spent
	noun := "spending"
	if direction == income {
		whole, part, noun = t.income, u.income, "income"
	}
	scoped := in
	scoped.Categories = []Named{{ID: id, Name: "Uncategorized"}}
	notes := scopeNotes(in, in.Period)
	link := registerLink(scoped, in.Period, linkKind(direction))
	if u.count == 0 {
		return Answer{Text: "All of your " + noun + scope(in) + phrase(in.Period) + " has a category.", Notes: notes}, nil
	}
	text := fmt.Sprintf("%s of your %s%s%s has no category, across %s.", euros(part), noun, scope(in), phrase(in.Period), count(u.count, "transaction", "transactions"))
	if share, ok := ratio(part, whole); ok && share > 0 {
		text = fmt.Sprintf("%s of your %s%s%s (%s) has no category, across %s.", euros(part), noun, scope(in), phrase(in.Period), percent(share), count(u.count, "transaction", "transactions"))
	}
	groups, err := r.groups(only, payeeGroup, valueSQL(direction), "value", max(in.Limit, 10))
	if err != nil {
		return Answer{}, err
	}
	return Answer{Text: text, Table: rankTable("Payee", groups, part), Notes: notes, Link: link}, nil
}

func (r *run) refunds(in Interpretation) (Answer, error) {
	f := withKinds(r.filterOf(in, in.Period, both), "refund")
	t, err := r.totals(f)
	if err != nil {
		return Answer{}, err
	}
	notes := append(scopeNotes(in, in.Period), "Refunds are subtracted from spending everywhere else.")
	if t.count == 0 {
		return Answer{Text: "You have no refunds" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	rows, err := r.transactionsOf(f, "eur_cents", "value DESC", limit)
	if err != nil {
		return Answer{}, err
	}
	text := fmt.Sprintf("You got %s back in %s%s%s.", euros(t.refunds), count(t.count, "refund", "refunds"), scope(in), phrase(in.Period))
	return Answer{Text: text, Table: transactionTable(rows), Notes: notes, Link: registerLink(in, in.Period, "refund")}, nil
}

func (r *run) transfers(in Interpretation) (Answer, error) {
	var where []string
	var args []any
	if in.Period.From != "" {
		where, args = append(where, "ot.local_date >= ?"), append(args, in.Period.From)
	}
	if in.Period.To != "" {
		where, args = append(where, "ot.local_date <= ?"), append(args, in.Period.To)
	}
	if accounts := ids(in.Accounts); len(accounts) > 0 {
		where = append(where, "(ot.account_id IN ("+placeholders(len(accounts))+") OR it.account_id IN ("+placeholders(len(accounts))+"))")
		for range 2 {
			for _, id := range accounts {
				args = append(args, id)
			}
		}
	}
	condition := ""
	if len(where) > 0 {
		condition = " AND " + strings.Join(where, " AND ")
	}
	table := &Table{Columns: []Column{{Label: "From", Kind: "text"}, {Label: "To", Kind: "text"}, {Label: "Amount", Kind: "money", Bar: true}, {Label: "Transfers", Kind: "count"}}}
	var total, number int64
	err := r.query(`SELECT fa.name, ta.name, sum(-ot.eur_cents), count(*)
    FROM transfer_legs o
    JOIN transfer_legs i ON i.transfer_id = o.transfer_id AND i.direction = 'incoming'
    JOIN transactions ot ON ot.id = o.transaction_id JOIN transactions it ON it.id = i.transaction_id
    JOIN accounts fa ON fa.id = ot.account_id JOIN accounts ta ON ta.id = it.account_id
    WHERE o.direction = 'outgoing' AND ot.deleted_at IS NULL AND it.deleted_at IS NULL`+condition+`
    GROUP BY 1, 2 ORDER BY 3 DESC`, args, func(rows *sql.Rows) error {
		var from, to string
		var amount, transfers int64
		if err := rows.Scan(&from, &to, &amount, &transfers); err != nil {
			return err
		}
		total += amount
		number += transfers
		table.Rows = append(table.Rows, []any{from, to, amount, transfers})
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	f := withKinds(r.filterOf(in, in.Period, both), "transfer")
	f.payees, f.categories = nil, nil
	cte, lineArgs := f.lines()
	var unlinkedOut, unlinkedIn, unlinked int64
	if err := r.db.QueryRowContext(r.ctx, cte+`SELECT coalesce(sum(CASE WHEN eur_cents < 0 THEN -eur_cents END), 0),
        coalesce(sum(CASE WHEN eur_cents > 0 THEN eur_cents END), 0), count(DISTINCT id)
    FROM lines WHERE NOT EXISTS (SELECT 1 FROM transfer_legs l WHERE l.transaction_id = lines.id)`, lineArgs...).
		Scan(&unlinkedOut, &unlinkedIn, &unlinked); err != nil {
		return Answer{}, err
	}
	notes := append(scopeNotes(in, in.Period), "Transfers are never counted as income or spending.")
	if unlinked > 0 {
		notes = append(notes, fmt.Sprintf("%s without a matching side moved %s out and %s in.", count(unlinked, "transfer", "transfers"), euros(unlinkedOut), euros(unlinkedIn)))
	}
	if number == 0 && unlinked == 0 {
		return Answer{Text: "You made no transfers between your accounts" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	text := fmt.Sprintf("You moved %s between your accounts in %s%s%s.", euros(total), count(number, "transfer", "transfers"), scope(in), phrase(in.Period))
	if number == 0 {
		text = fmt.Sprintf("You have %s%s%s that are not linked to the other account.", count(unlinked, "transfer", "transfers"), scope(in), phrase(in.Period))
		table = nil
	}
	return Answer{Text: text, Table: table, Notes: notes, Link: registerLink(Interpretation{Accounts: in.Accounts}, in.Period, "transfer")}, nil
}

func (r *run) listCategories(in Interpretation) (Answer, error) {
	direction := rankDirection(in.Direction)
	f := r.filterOf(in, in.Period, direction)
	f.categories = nil
	cte, args := f.lines()
	table := &Table{Columns: []Column{{Label: "Category", Kind: "text"}, {Label: "Amount", Kind: "money", Bar: true}, {Label: "Transactions", Kind: "count"}}}
	var number int
	err := r.query(cte+`, totals AS (SELECT coalesce(category_id, (SELECT id FROM categories WHERE normalized_name = 'uncategorized')) AS k,
            sum(`+valueSQL(direction)+`) AS v, count(DISTINCT lines.id) AS c FROM lines GROUP BY k)
        SELECT c.name, coalesce(t.v, 0), coalesce(t.c, 0) FROM categories c LEFT JOIN totals t ON t.k = c.id
        ORDER BY coalesce(t.v, 0) DESC, c.name COLLATE NOCASE`, args, func(rows *sql.Rows) error {
		var name string
		var amount, transactions int64
		if err := rows.Scan(&name, &amount, &transactions); err != nil {
			return err
		}
		number++
		table.Rows = append(table.Rows, []any{name, amount, transactions})
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	noun := "spending"
	if direction == income {
		noun = "income"
	}
	text := fmt.Sprintf("You have %s. Here is the %s in each%s%s.", count(int64(number), "category", "categories"), noun, scope(in), phrase(in.Period))
	return Answer{Text: text, Table: table, Notes: scopeNotes(in, in.Period), Link: &Link{Label: "Open Categories", Href: "/categories/"}}, nil
}
