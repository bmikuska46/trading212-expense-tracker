package assistant

import (
	"cmp"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"time"
)

// inProgress reports whether today falls inside the period.
func (r *run) inProgress(p Period) bool {
	from, okFrom := parseDay(p.From)
	to, okTo := parseDay(p.To)
	return okFrom && okTo && !r.today.Before(from) && !r.today.After(to)
}

func (r *run) progressNote(p Period) []string {
	if r.inProgress(p) {
		return []string{"The period is still running; this counts up to today, " + dayLabel(dateOf(r.today)) + "."}
	}
	return nil
}

func (r *run) total(in Interpretation) (Answer, error) {
	t, err := r.totals(r.filterOf(in, in.Period, in.Direction))
	if err != nil {
		return Answer{}, err
	}
	notes := append(scopeNotes(in, in.Period), r.progressNote(in.Period)...)
	link := registerLink(in, in.Period, linkKind(in.Direction))
	if in.Direction == income {
		if t.count == 0 {
			return Answer{Text: "You have no income" + scope(in) + phrase(in.Period) + ".", Notes: notes, Link: link}, nil
		}
		return Answer{
			Text:  fmt.Sprintf("You received %s%s%s, across %s.", euros(t.income), scope(in), phrase(in.Period), count(t.count, "transaction", "transactions")),
			Notes: notes, Link: link,
		}, nil
	}
	if t.count == 0 {
		return Answer{Text: "You have no spending" + scope(in) + phrase(in.Period) + ".", Notes: notes, Link: link}, nil
	}
	if t.refunds > 0 {
		notes = append(notes, "Refunds of "+euros(t.refunds)+" are subtracted.")
	}
	return Answer{
		Text:  fmt.Sprintf("You spent %s%s%s, across %s.", euros(t.spent), scope(in), phrase(in.Period), count(t.count, "transaction", "transactions")),
		Notes: notes, Link: link,
	}, nil
}

func (r *run) netFlow(in Interpretation) (Answer, error) {
	t, err := r.totals(r.filterOf(in, in.Period, both))
	if err != nil {
		return Answer{}, err
	}
	net := t.income - t.spent
	text := fmt.Sprintf("You earned %s and spent %s%s%s, ", euros(t.income), euros(t.spent), scope(in), phrase(in.Period))
	switch {
	case net > 0:
		text += "so you saved " + euros(net) + "."
	case net < 0:
		text += "so you spent " + euros(-net) + " more than you earned."
	default:
		text += "so you broke even."
	}
	notes := append(scopeNotes(in, in.Period), "Transfers between your accounts are not counted as income or spending.")
	notes = append(notes, r.progressNote(in.Period)...)
	return Answer{Text: text, Notes: notes, Link: registerLink(in, in.Period, "")}, nil
}

func (r *run) savingsRate(in Interpretation) (Answer, error) {
	t, err := r.totals(r.filterOf(in, in.Period, both))
	if err != nil {
		return Answer{}, err
	}
	notes := append(scopeNotes(in, in.Period), "Savings rate is income minus spending, divided by income.")
	notes = append(notes, r.progressNote(in.Period)...)
	if t.income <= 0 {
		return Answer{Text: "You have no income" + scope(in) + phrase(in.Period) + ", so there is no savings rate to show.", Notes: notes}, nil
	}
	net := t.income - t.spent
	rate := float64(net) / float64(t.income)
	text := fmt.Sprintf("Your savings rate%s is %s: you kept %s of %s income.", phrase(in.Period), percent(rate), euros(net), euros(t.income))
	if net < 0 {
		text = fmt.Sprintf("Your savings rate%s is %s: you spent %s more than your %s income.", phrase(in.Period), percent(rate), euros(-net), euros(t.income))
	}
	result := Answer{Text: text, Notes: notes}
	buckets, err := r.series(r.filterOf(in, in.Period, both), "month")
	if err != nil {
		return Answer{}, err
	}
	if len(buckets) > 1 {
		table := &Table{Columns: []Column{{Label: "Month", Kind: "text"}, {Label: "Income", Kind: "money"}, {Label: "Spent", Kind: "money"},
			{Label: "Saved", Kind: "money"}, {Label: "Rate", Kind: "percent"}}}
		for _, b := range buckets {
			var rate any
			if b.income > 0 {
				rate = float64(b.income-b.spent) / float64(b.income)
			}
			table.Rows = append(table.Rows, []any{b.label, b.income, b.spent, b.income - b.spent, rate})
		}
		result.Table = table
	}
	return result, nil
}

func (r *run) summary(in Interpretation) (Answer, error) {
	f := r.filterOf(in, in.Period, both)
	t, err := r.totals(f)
	if err != nil {
		return Answer{}, err
	}
	if t.count == 0 {
		return Answer{Text: "There are no transactions" + scope(in) + phrase(in.Period) + "."}, nil
	}
	spend := r.filterOf(in, in.Period, spending)
	categories, err := r.groups(spend, categoryGroup, spentSQL, "value", 5)
	if err != nil {
		return Answer{}, err
	}
	payees, err := r.groups(spend, payeeGroup, spentSQL, "value", 3)
	if err != nil {
		return Answer{}, err
	}
	biggest, err := r.transactionsOf(withKinds(spend, "expense"), "-eur_cents", "value DESC", 1)
	if err != nil {
		return Answer{}, err
	}
	net := t.income - t.spent
	text := fmt.Sprintf("%s%s you earned %s and spent %s", upperFirst(in.Period.Phrase), scope(in), euros(t.income), euros(t.spent))
	if in.Period.Phrase == "" {
		text = fmt.Sprintf("Overall%s you earned %s and spent %s", scope(in), euros(t.income), euros(t.spent))
	}
	if net >= 0 {
		text += ", saving " + euros(net)
		if t.income > 0 {
			text += " (" + percent(float64(net)/float64(t.income)) + " of income)"
		}
	} else {
		text += ", " + euros(-net) + " more than you earned"
	}
	text += fmt.Sprintf(", across %s.", count(t.count, "transaction", "transactions"))
	if len(payees) > 0 {
		text += fmt.Sprintf(" Your biggest payee was %s (%s).", payees[0].name, euros(payees[0].value))
	}
	if len(biggest) > 0 {
		text += fmt.Sprintf(" Your largest purchase was %s at %s on %s.", euros(biggest[0].value), biggest[0].label(), dayLabel(biggest[0].date))
	}
	previous, ok := previousPeriod(in.Period, "previous")
	notes := scopeNotes(in, in.Period)
	if ok && !r.inProgress(in.Period) {
		before, err := r.totals(r.filterOf(in, previous, both))
		if err != nil {
			return Answer{}, err
		}
		if before.count > 0 {
			notes = append(notes, fmt.Sprintf("Compared with %s: spending %s, income %s.", previous.Label,
				change(t.spent, before.spent, "more", "less"), change(t.income, before.income, "more", "less")))
		}
	}
	notes = append(notes, r.progressNote(in.Period)...)
	return Answer{Text: text, Table: rankTable("Top categories", categories, t.spent), Notes: notes, Link: registerLink(in, in.Period, "")}, nil
}

// firstDate is the date of the earliest transaction, or today when there
// are none.
func (r *run) firstDate() (time.Time, error) {
	var first sql.NullString
	if err := r.db.QueryRowContext(r.ctx, `SELECT min(local_date) FROM transactions WHERE deleted_at IS NULL`).Scan(&first); err != nil {
		return time.Time{}, err
	}
	if start, ok := parseDay(first.String); ok {
		return start, nil
	}
	return r.today, nil
}

// effectiveSpan bounds a period by the data and today, for averages and
// paces, so days before the first transaction do not count.
func (r *run) effectiveSpan(p Period) (time.Time, time.Time, error) {
	from, okFrom := parseDay(p.From)
	to, okTo := parseDay(p.To)
	start, err := r.firstDate()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !okFrom || start.After(from) {
		from = start
	}
	if !okTo || to.After(r.today) {
		to = r.today
	}
	return from, to, nil
}

// beforeDataNote warns when an earlier period starts before the first
// transaction, so its total is low or zero only for lack of data.
func (r *run) beforeDataNote(p Period) []string {
	from, ok := parseDay(p.From)
	first, err := r.firstDate()
	if !ok || err != nil || !from.Before(first) {
		return nil
	}
	return []string{"Your data starts on " + dayLabel(dateOf(first)) + ", so " + p.Label + " may be incomplete."}
}

var stepNames = map[string][2]string{
	"day": {"per day", "days"}, "week": {"per week", "weeks"}, "month": {"per month", "months"}, "year": {"per year", "years"},
}

func (r *run) average(in Interpretation) (Answer, error) {
	direction := in.Direction
	if direction == both {
		direction = spending
	}
	f := r.filterOf(in, in.Period, direction)
	notes := scopeNotes(in, in.Period)
	noun, verb := "spending", "spent"
	if direction == income {
		noun, verb = "income", "received"
	}
	if in.Step == "transaction" {
		t, err := r.totals(withKinds(f, map[string]string{spending: "expense", income: "income"}[direction]))
		if err != nil {
			return Answer{}, err
		}
		amount := t.spent
		if direction == income {
			amount = t.income
		}
		if t.count == 0 {
			return Answer{Text: "There are no transactions" + scope(in) + phrase(in.Period) + " to average.", Notes: notes}, nil
		}
		return Answer{
			Text:  fmt.Sprintf("Your average transaction%s%s is %s, across %s totaling %s.", scope(in), phrase(in.Period), euros(amount/t.count), count(t.count, "transaction", "transactions"), euros(amount)),
			Notes: notes, Link: registerLink(in, in.Period, linkKind(direction)),
		}, nil
	}
	step := in.Step
	if _, ok := stepNames[step]; !ok {
		step = "month"
	}
	t, err := r.totals(f)
	if err != nil {
		return Answer{}, err
	}
	amount := t.spent
	if direction == income {
		amount = t.income
	}
	from, to, err := r.effectiveSpan(in.Period)
	if err != nil {
		return Answer{}, err
	}
	days := daysBetween(from, to)
	if t.count == 0 || days <= 0 {
		return Answer{Text: "There is no " + noun + scope(in) + phrase(in.Period) + " to average.", Notes: notes}, nil
	}
	units := map[string]float64{"day": 1, "week": 7, "month": 365.25 / 12, "year": 365.25}[step]
	periods := float64(days) / units
	average := int64(math.Round(float64(amount) / math.Max(periods, 1)))
	if periods < 1 {
		average = amount
	}
	text := fmt.Sprintf("On average you %s %s %s%s, based on %s from %s to %s.", verb, euros(average), stepNames[step][0], scope(in),
		euros(amount), dayLabel(dateOf(from)), dayLabel(dateOf(to)))
	notes = append(notes, fmt.Sprintf("The average spreads the total over %s, starting no earlier than your first transaction.", count(int64(days), "day", "days")))
	result := Answer{Text: text, Notes: notes, Link: registerLink(in, Period{From: dateOf(from), To: dateOf(to)}, linkKind(direction))}
	if step == "month" || step == "year" {
		buckets, err := r.series(filter{from: dateOf(from), to: dateOf(to), payees: f.payees, categories: f.categories, accounts: f.accounts,
			search: f.search, minCents: f.minCents, maxCents: f.maxCents, kinds: f.kinds}, step)
		if err != nil {
			return Answer{}, err
		}
		if len(buckets) > 1 {
			values := make([]int64, len(buckets))
			for i, b := range buckets {
				values[i] = b.spent
				if direction == income {
					values[i] = b.income
				}
			}
			slices.Sort(values)
			median := values[len(values)/2]
			if len(values)%2 == 0 {
				median = (values[len(values)/2-1] + values[len(values)/2]) / 2
			}
			result.Notes = append(result.Notes, fmt.Sprintf("The median %s is %s; the lowest was %s and the highest %s.", step, euros(median), euros(values[0]), euros(values[len(values)-1])))
			result.Table = seriesTable(buckets, direction, upperFirst(step))
		}
	}
	return result, nil
}

func (r *run) projection(in Interpretation) (Answer, error) {
	direction := in.Direction
	if direction == both {
		direction = spending
	}
	period := in.Period
	from, okFrom := parseDay(period.From)
	to, okTo := parseDay(period.To)
	if !okFrom || !okTo || !r.inProgress(period) {
		period, _ = resolvePeriod("this_month", "", r.today)
		from, _ = parseDay(period.From)
		to, _ = parseDay(period.To)
	}
	t, err := r.totals(r.filterOf(in, period, direction))
	if err != nil {
		return Answer{}, err
	}
	amount, verb, future := t.spent, "spent", "spend"
	if direction == income {
		amount, verb, future = t.income, "received", "receive"
	}
	elapsed, length := daysBetween(from, r.today), daysBetween(from, to)
	projected := int64(math.Round(float64(amount) * float64(length) / float64(elapsed)))
	text := fmt.Sprintf("So far you %s %s%s %s (%d of %d days). At this pace you will %s about %s by %s.",
		verb, euros(amount), scope(in), period.Phrase, elapsed, length, future, euros(projected), dayLabel(period.To))
	notes := []string{"The projection assumes the same daily pace for the rest of the period; large one-off payments make it less reliable."}
	if previous, ok := previousPeriod(period, "previous"); ok {
		before, err := r.totals(r.filterOf(in, previous, direction))
		if err != nil {
			return Answer{}, err
		}
		earlier := before.spent
		if direction == income {
			earlier = before.income
		}
		if before.count > 0 {
			notes = append(notes, fmt.Sprintf("%s: %s, so the projection is %s.", upperFirst(previous.Label), euros(earlier), change(projected, earlier, "more", "less")))
		}
	}
	if period.Label != in.Period.Label && in.PeriodGiven {
		notes = append(notes, "Projections need a period that is still running, so this uses the current month.")
	}
	return Answer{Text: text, Notes: notes, Link: registerLink(in, period, linkKind(direction))}, nil
}

// comparison returns the period to compare and the earlier one. A period
// still running is cut at today and compared with the same days before.
func (r *run) comparison(in Interpretation) (Period, Period, bool) {
	current := in.Period
	if current.allTime() || current.From == "" || current.To == "" {
		current, _ = resolvePeriod("this_month", "", r.today)
	}
	previous, ok := previousPeriod(current, in.Compare)
	if !ok {
		return current, previous, false
	}
	clipped, moved := clip(current, r.today)
	if moved {
		from, _ := parseDay(clipped.From)
		to, _ := parseDay(clipped.To)
		earlierFrom, _ := parseDay(previous.From)
		earlierTo, _ := parseDay(previous.To)
		end := earlierFrom.AddDate(0, 0, daysBetween(from, to)-1)
		if end.After(earlierTo) {
			end = earlierTo
		}
		whole := previous.Label
		previous = relabel(earlierFrom, end, "")
		previous.Label, previous.Phrase = "the same days of "+whole, "in the same days of "+whole
		current = clipped
	}
	return current, previous, true
}

func (r *run) comparePeriods(in Interpretation) (Answer, error) {
	current, previous, ok := r.comparison(in)
	if !ok {
		return Answer{Text: "I need a period with clear dates to compare, such as this month or 2025."}, nil
	}
	now, err := r.totals(r.filterOf(in, current, both))
	if err != nil {
		return Answer{}, err
	}
	before, err := r.totals(r.filterOf(in, previous, both))
	if err != nil {
		return Answer{}, err
	}
	table := &Table{Columns: []Column{{Label: "Period", Kind: "text"}}}
	var text string
	switch in.Direction {
	case income:
		text = fmt.Sprintf("You received %s%s %s, %s than %s (%s).", euros(now.income), scope(in), current.Phrase,
			change(now.income, before.income, "more", "less"), previous.Label, euros(before.income))
		table.Columns = append(table.Columns, Column{Label: "Income", Kind: "money", Bar: true})
		table.Rows = [][]any{{upperFirst(current.Label), now.income}, {upperFirst(previous.Label), before.income}}
	case both:
		text = fmt.Sprintf("%s%s you earned %s and spent %s. %s you earned %s and spent %s.", upperFirst(current.Phrase), scope(in),
			euros(now.income), euros(now.spent), upperFirst(previous.Phrase), euros(before.income), euros(before.spent))
		table.Columns = append(table.Columns, Column{Label: "Income", Kind: "money"}, Column{Label: "Spent", Kind: "money"}, Column{Label: "Net", Kind: "money"})
		table.Rows = [][]any{{upperFirst(current.Label), now.income, now.spent, now.income - now.spent},
			{upperFirst(previous.Label), before.income, before.spent, before.income - before.spent}}
	default:
		verdict := change(now.spent, before.spent, "more", "less")
		text = fmt.Sprintf("You spent %s%s %s, %s than %s (%s).", euros(now.spent), scope(in), current.Phrase, verdict, previous.Label, euros(before.spent))
		table.Columns = append(table.Columns, Column{Label: "Spent", Kind: "money", Bar: true})
		table.Rows = [][]any{{upperFirst(current.Label), now.spent}, {upperFirst(previous.Label), before.spent}}
	}
	notes := append(scopeNotes(in, Period{}), "Compared periods: "+current.dates()+" and "+previous.dates()+".")
	notes = append(notes, r.beforeDataNote(previous)...)
	return Answer{Text: text, Table: table, Notes: notes, Link: registerLink(in, current, linkKind(in.Direction))}, nil
}

func (r *run) categoryChanges(in Interpretation) (Answer, error) {
	current, previous, ok := r.comparison(in)
	if !ok {
		return Answer{Text: "I need a period with clear dates to compare, such as this month or 2025."}, nil
	}
	direction := in.Direction
	if direction == both {
		direction = spending
	}
	value := valueSQL(direction)
	now, err := r.groups(r.filterOf(in, current, direction), categoryGroup, value, "value", 0)
	if err != nil {
		return Answer{}, err
	}
	before, err := r.groups(r.filterOf(in, previous, direction), categoryGroup, value, "value", 0)
	if err != nil {
		return Answer{}, err
	}
	type delta struct {
		name             string
		current, earlier int64
	}
	byName := map[string]*delta{}
	var list []*delta
	for _, g := range now {
		d := &delta{name: g.name, current: g.value}
		byName[g.name] = d
		list = append(list, d)
	}
	for _, g := range before {
		if d, ok := byName[g.name]; ok {
			d.earlier = g.value
			continue
		}
		list = append(list, &delta{name: g.name, earlier: g.value})
	}
	if len(list) == 0 {
		return Answer{Text: "There is nothing to compare" + scope(in) + " " + current.Phrase + " or " + previous.Phrase + "."}, nil
	}
	slices.SortStableFunc(list, func(a, b *delta) int {
		da, db := a.current-a.earlier, b.current-b.earlier
		switch in.Order {
		case "most":
			return compareInt(db, da)
		case "least":
			return compareInt(da, db)
		}
		return compareInt(abs(db), abs(da))
	})
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	table := &Table{Columns: []Column{{Label: "Category", Kind: "text"}, {Label: upperFirst(current.Label), Kind: "money"},
		{Label: upperFirst(previous.Label), Kind: "money"}, {Label: "Change", Kind: "money"}}}
	var up, down *delta
	for _, d := range list {
		if diff := d.current - d.earlier; diff > 0 && (up == nil || diff > up.current-up.earlier) {
			up = d
		} else if diff < 0 && (down == nil || diff < down.current-down.earlier) {
			down = d
		}
	}
	for _, d := range list[:min(limit, len(list))] {
		table.Rows = append(table.Rows, []any{d.name, d.current, d.earlier, d.current - d.earlier})
	}
	var text string
	if up != nil {
		text = fmt.Sprintf("%s grew the most: %s %s against %s %s.", up.name, euros(up.current), current.Phrase, euros(up.earlier), previous.Phrase)
	}
	if down != nil {
		if text != "" {
			text += " "
		}
		text += fmt.Sprintf("%s fell the most: %s against %s.", down.name, euros(down.current), euros(down.earlier))
	}
	if text == "" {
		text = "Your categories did not change between " + current.Label + " and " + previous.Label + "."
	}
	notes := append(scopeNotes(in, Period{}), "Compared periods: "+current.dates()+" and "+previous.dates()+".")
	notes = append(notes, r.beforeDataNote(previous)...)
	return Answer{Text: text, Table: table, Notes: notes}, nil
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// bucket is a total for one month, year, or day.
type bucket struct {
	key, label           string
	spent, income, count int64
}

var bucketKeys = map[string]string{"month": "substr(local_date, 1, 7)", "year": "substr(local_date, 1, 4)", "day": "local_date"}

// series totals each month or year in the filter, with empty months filled
// in between the first and last.
func (r *run) series(f filter, step string) ([]bucket, error) {
	cte, args := f.lines()
	key := bucketKeys[step]
	var result []bucket
	err := r.query(cte+`SELECT `+key+` AS k, coalesce(sum(`+spentSQL+`), 0), coalesce(sum(`+incomeSQL+`), 0), count(DISTINCT id)
        FROM lines GROUP BY k ORDER BY k`, args, func(rows *sql.Rows) error {
		var b bucket
		if err := rows.Scan(&b.key, &b.spent, &b.income, &b.count); err != nil {
			return err
		}
		result = append(result, b)
		return nil
	})
	if err != nil || len(result) == 0 {
		return result, err
	}
	start, end := result[0].key, result[len(result)-1].key
	if step == "month" {
		// Months before the first transaction would only add empty rows.
		first, err := r.firstDate()
		if err != nil {
			return nil, err
		}
		if f.from != "" {
			start = max(f.from, dateOf(first))[:7]
		}
		if f.to != "" {
			end = min(f.to, dateOf(r.today))[:7]
		}
	}
	return fill(result, step, start, end), nil
}

func fill(found []bucket, step, start, end string) []bucket {
	byKey := map[string]bucket{}
	for _, b := range found {
		byKey[b.key] = b
	}
	var result []bucket
	switch step {
	case "month":
		month, err := time.Parse("2006-01", start)
		if err != nil {
			return found
		}
		for key := start; key <= end; key = month.Format("2006-01") {
			b := byKey[key]
			b.key, b.label = key, month.Format("Jan 2006")
			result = append(result, b)
			month = month.AddDate(0, 1, 0)
		}
	case "year":
		year, err1 := time.Parse("2006", start)
		last, err2 := time.Parse("2006", end)
		if err1 != nil || err2 != nil {
			return found
		}
		for ; !year.After(last); year = year.AddDate(1, 0, 0) {
			b := byKey[year.Format("2006")]
			b.key, b.label = year.Format("2006"), year.Format("2006")
			result = append(result, b)
		}
	default:
		for _, b := range found {
			b.label = dayLabel(b.key)
			result = append(result, b)
		}
	}
	return result
}

func seriesTable(buckets []bucket, direction, label string) *Table {
	table := &Table{Columns: []Column{{Label: label, Kind: "text"}}}
	switch direction {
	case both:
		table.Columns = append(table.Columns, Column{Label: "Income", Kind: "money"}, Column{Label: "Spent", Kind: "money"}, Column{Label: "Net", Kind: "money", Bar: true})
	case income:
		table.Columns = append(table.Columns, Column{Label: "Income", Kind: "money", Bar: true}, Column{Label: "Transactions", Kind: "count"})
	default:
		table.Columns = append(table.Columns, Column{Label: "Spent", Kind: "money", Bar: true}, Column{Label: "Transactions", Kind: "count"})
	}
	for _, b := range buckets {
		switch direction {
		case both:
			table.Rows = append(table.Rows, []any{b.label, b.income, b.spent, b.income - b.spent})
		case income:
			table.Rows = append(table.Rows, []any{b.label, b.income, b.count})
		default:
			table.Rows = append(table.Rows, []any{b.label, b.spent, b.count})
		}
	}
	return table
}

func (r *run) byPeriod(in Interpretation, step string) (Answer, error) {
	buckets, err := r.series(r.filterOf(in, in.Period, in.Direction), step)
	if err != nil {
		return Answer{}, err
	}
	notes := scopeNotes(in, in.Period)
	if len(buckets) == 0 {
		return Answer{Text: "There are no transactions" + scope(in) + phrase(in.Period) + ".", Notes: notes}, nil
	}
	value := func(b bucket) int64 {
		switch in.Direction {
		case income:
			return b.income
		case both:
			return b.income - b.spent
		}
		return b.spent
	}
	high, low := buckets[0], buckets[0]
	var sum int64
	for _, b := range buckets {
		sum += value(b)
		if value(b) > value(high) {
			high = b
		}
		if value(b) < value(low) {
			low = b
		}
	}
	noun := map[string]string{spending: "spending", income: "income", both: "net savings"}[in.Direction]
	average := euros(sum / int64(len(buckets)))
	text := fmt.Sprintf("Your %s%s%s by %s: the highest was %s with %s and the lowest %s with %s. The average is %s per %s.",
		noun, scope(in), phrase(in.Period), step, high.label, euros(value(high)), low.label, euros(value(low)), average, step)
	// A ranking question lists the buckets by value instead of by date.
	rows := buckets
	switch in.Order {
	case "most":
		text = fmt.Sprintf("Your highest %s by %s%s%s was %s with %s. The lowest was %s with %s and the average is %s per %s.",
			noun, step, scope(in), phrase(in.Period), high.label, euros(value(high)), low.label, euros(value(low)), average, step)
		rows = slices.Clone(buckets)
		slices.SortStableFunc(rows, func(a, b bucket) int { return cmp.Compare(value(b), value(a)) })
	case "least":
		text = fmt.Sprintf("Your lowest %s by %s%s%s was %s with %s. The highest was %s with %s.", noun, step, scope(in), phrase(in.Period),
			low.label, euros(value(low)), high.label, euros(value(high)))
		rows = slices.Clone(buckets)
		slices.SortStableFunc(rows, func(a, b bucket) int { return cmp.Compare(value(a), value(b)) })
	}
	if len(buckets) == 1 {
		text = fmt.Sprintf("Your %s%s%s covers one %s: %s with %s.", noun, scope(in), phrase(in.Period), step, high.label, euros(value(high)))
	}
	if in.Direction == both {
		notes = append(notes, "Net is income minus spending.")
	}
	notes = append(notes, r.progressNote(in.Period)...)
	if r.inProgress(in.Period) || in.Period.allTime() {
		notes = append(notes, "The latest "+step+" may still be running.")
	}
	return Answer{Text: text, Table: seriesTable(rows, in.Direction, upperFirst(step)), Notes: notes, Link: registerLink(in, in.Period, linkKind(in.Direction))}, nil
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func (r *run) byWeekday(in Interpretation) (Answer, error) {
	direction := in.Direction
	if direction == both {
		direction = spending
	}
	cte, args := r.filterOf(in, in.Period, direction).lines()
	totals := make([]int64, 7)
	counts := make([]int64, 7)
	err := r.query(cte+`SELECT CAST(strftime('%w', local_date) AS INTEGER), coalesce(sum(`+valueSQL(direction)+`), 0), count(DISTINCT id)
        FROM lines GROUP BY 1`, args, func(rows *sql.Rows) error {
		var day int
		var total, number int64
		if err := rows.Scan(&day, &total, &number); err != nil {
			return err
		}
		if day >= 0 && day < 7 {
			totals[day], counts[day] = total, number
		}
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	from, to, err := r.effectiveSpan(in.Period)
	if err != nil {
		return Answer{}, err
	}
	occurrences := make([]int64, 7)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		occurrences[day.Weekday()]++
	}
	table := &Table{Columns: []Column{{Label: "Weekday", Kind: "text"}, {Label: "Total", Kind: "money", Bar: true},
		{Label: "Average per day", Kind: "money"}, {Label: "Transactions", Kind: "count"}}}
	best, worst := -1, -1
	var weekend, all int64
	for _, day := range []int{1, 2, 3, 4, 5, 6, 0} {
		var average int64
		if occurrences[day] > 0 {
			average = totals[day] / occurrences[day]
		}
		table.Rows = append(table.Rows, []any{weekdays[day], totals[day], average, counts[day]})
		if best < 0 || totals[day] > totals[best] {
			best = day
		}
		if worst < 0 || totals[day] < totals[worst] {
			worst = day
		}
		all += totals[day]
		if day == 0 || day == 6 {
			weekend += totals[day]
		}
	}
	if all == 0 {
		return Answer{Text: "There is nothing to break down" + scope(in) + phrase(in.Period) + ".", Notes: scopeNotes(in, in.Period)}, nil
	}
	verb := "spend"
	if direction == income {
		verb = "receive"
	}
	pick := best
	word := "most"
	if in.Order == "least" {
		pick, word = worst, "least"
	}
	text := fmt.Sprintf("You %s the %s on %ss%s: %s%s.", verb, word, weekdays[pick], scope(in), euros(totals[pick]), phrase(in.Period))
	notes := append(scopeNotes(in, in.Period), fmt.Sprintf("Weekends account for %s of the total.", percent(float64(weekend)/float64(all))))
	return Answer{Text: text, Table: table, Notes: notes}, nil
}

func (r *run) byDay(in Interpretation) (Answer, error) {
	direction := in.Direction
	if direction == both {
		direction = spending
	}
	cte, args := r.filterOf(in, in.Period, direction).lines()
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	order := "v DESC"
	if in.Order == "least" {
		order = "v ASC"
	}
	table := &Table{Columns: []Column{{Label: "Date", Kind: "date"}, {Label: "Amount", Kind: "money", Bar: true}, {Label: "Transactions", Kind: "count"}}}
	var first []any
	err := r.query(cte+`SELECT local_date, sum(`+valueSQL(direction)+`) AS v, count(DISTINCT id) FROM lines
        GROUP BY local_date HAVING v > 0 ORDER BY `+order+`, local_date DESC LIMIT ?`, append(args, limit), func(rows *sql.Rows) error {
		var date string
		var value, number int64
		if err := rows.Scan(&date, &value, &number); err != nil {
			return err
		}
		row := []any{date, value, number}
		if first == nil {
			first = row
		}
		table.Rows = append(table.Rows, row)
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	if first == nil {
		return Answer{Text: "There are no days with activity" + scope(in) + phrase(in.Period) + ".", Notes: scopeNotes(in, in.Period)}, nil
	}
	word := "most expensive"
	if direction == income {
		word = "best"
	}
	if in.Order == "least" {
		word = "quietest"
	}
	text := fmt.Sprintf("Your %s day%s%s was %s with %s across %s.", word, scope(in), phrase(in.Period), dayLabel(first[0].(string)),
		euros(first[1].(int64)), count(first[2].(int64), "transaction", "transactions"))
	return Answer{Text: text, Table: table, Notes: scopeNotes(in, in.Period), Link: registerLink(in, in.Period, linkKind(direction))}, nil
}
