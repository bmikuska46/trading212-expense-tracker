package assistant

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
)

type balance struct {
	id                    int64
	name, currency        string
	provider              string
	archived              bool
	minor, eurCents, rows int64
}

// balances adds up each account's transactions, as the Accounts page does,
// up to a date when one is given.
func (r *run) balances(in Interpretation, until string) ([]balance, error) {
	query := `SELECT a.id, a.name, a.currency, coalesce(a.integration_provider, ''), a.archived,
        coalesce(sum(t.amount_minor), 0), coalesce(sum(t.eur_cents), 0), count(t.id)
    FROM accounts a LEFT JOIN transactions t ON t.account_id = a.id AND t.deleted_at IS NULL AND (? = '' OR t.local_date <= ?)`
	args := []any{until, until}
	if accounts := ids(in.Accounts); len(accounts) > 0 {
		query += ` WHERE a.id IN (` + placeholders(len(accounts)) + `)`
		for _, id := range accounts {
			args = append(args, id)
		}
	}
	var result []balance
	err := r.query(query+` GROUP BY a.id ORDER BY a.archived, a.name COLLATE NOCASE, a.id`, args, func(rows *sql.Rows) error {
		var b balance
		var archived int64
		if err := rows.Scan(&b.id, &b.name, &b.currency, &b.provider, &archived, &b.minor, &b.eurCents, &b.rows); err != nil {
			return err
		}
		b.archived = archived == 1
		result = append(result, b)
		return nil
	})
	return result, err
}

func (r *run) accountBalances(in Interpretation) (Answer, error) {
	until := ""
	if in.PeriodGiven && in.Period.To != "" && in.Period.To < dateOf(r.today) {
		until = in.Period.To
	}
	list, err := r.balances(in, until)
	if err != nil {
		return Answer{}, err
	}
	if len(list) == 0 {
		return Answer{Text: "You have no accounts yet.", Link: &Link{Label: "Open Accounts", Href: "/accounts/"}}, nil
	}
	table := &Table{Columns: []Column{{Label: "Account", Kind: "text"}, {Label: "Balance", Kind: "text"}, {Label: "In euros", Kind: "money", Bar: true}}}
	var total int64
	shown := 0
	for _, b := range list {
		if b.archived && len(in.Accounts) == 0 {
			continue
		}
		shown++
		total += b.eurCents
		table.Rows = append(table.Rows, []any{b.name, money(b.minor, b.currency), b.eurCents})
	}
	when := ""
	if until != "" {
		when = " on " + dayLabel(until)
	}
	text := fmt.Sprintf("You have %s across %s%s.", euros(total), count(int64(shown), "account", "accounts"), when)
	if shown == 1 {
		row := table.Rows[0]
		text = fmt.Sprintf("%s has %s%s.", row[0], row[1], when)
	}
	notes := []string{"Balances add up the transactions recorded in this app, starting from zero, so they match your bank only if the full history is imported.",
		"Euro values use the rate of each transaction's date, not today's rate."}
	if len(in.Accounts) == 0 && shown < len(list) {
		notes = append(notes, "Archived accounts are left out.")
	}
	return Answer{Text: text, Table: table, Notes: notes, Link: &Link{Label: "Open Accounts", Href: "/accounts/"}}, nil
}

type snapshot struct {
	id                                         int64
	fetchedAt, currency                        string
	total, investments, cost, unrealized, real int64
	cash                                       int64
}

// snapshotAt is the latest snapshot on or before the date, or the latest
// overall for an empty date. earliest picks the first one on or after it.
func (r *run) snapshotAt(date string, earliest bool) (snapshot, bool, error) {
	query := `SELECT id, fetched_at, currency, total_value_minor, investments_value_minor, cost_basis_minor, unrealized_minor,
        realized_minor, cash_available_minor + cash_in_pies_minor + cash_reserved_minor FROM investing_snapshots`
	var args []any
	switch {
	case date == "":
		query += ` ORDER BY fetched_at DESC LIMIT 1`
	case earliest:
		query += ` WHERE substr(fetched_at, 1, 10) >= ? ORDER BY fetched_at LIMIT 1`
		args = append(args, date)
	default:
		query += ` WHERE substr(fetched_at, 1, 10) <= ? ORDER BY fetched_at DESC LIMIT 1`
		args = append(args, date)
	}
	var s snapshot
	err := r.db.QueryRowContext(r.ctx, query, args...).Scan(&s.id, &s.fetchedAt, &s.currency, &s.total, &s.investments, &s.cost,
		&s.unrealized, &s.real, &s.cash)
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}

var noInvesting = Answer{
	Text:        "There is no investing data yet. Connect Trading 212 in Integrations and snapshots will appear here.",
	Link:        &Link{Label: "Open Investing", Href: "/investing/"},
	Suggestions: []string{"What are my account balances?"},
}

func fetchedLabel(fetchedAt string) string {
	if len(fetchedAt) >= 10 {
		return dayLabel(fetchedAt[:10])
	}
	return fetchedAt
}

func (r *run) netWorth(in Interpretation) (Answer, error) {
	until := ""
	if in.PeriodGiven && in.Period.To != "" && in.Period.To < dateOf(r.today) {
		until = in.Period.To
	}
	list, err := r.balances(Interpretation{}, until)
	if err != nil {
		return Answer{}, err
	}
	latest, ok, err := r.snapshotAt(until, false)
	if err != nil {
		return Answer{}, err
	}
	table := &Table{Columns: []Column{{Label: "Part", Kind: "text"}, {Label: "Value", Kind: "text"}, {Label: "In euros", Kind: "money", Bar: true}}}
	var cash int64
	var skipped []string
	for _, b := range list {
		// The broker account's cash is part of the portfolio value.
		if ok && b.provider == "trading212" {
			skipped = append(skipped, b.name)
			continue
		}
		cash += b.eurCents
		if b.eurCents != 0 || b.minor != 0 {
			table.Rows = append(table.Rows, []any{b.name, money(b.minor, b.currency), b.eurCents})
		}
	}
	total := cash
	notes := []string{"Account balances add up the transactions recorded in this app, in euros at each transaction's rate."}
	if ok {
		if latest.currency == "EUR" {
			total += latest.total
			table.Rows = append(table.Rows, []any{"Investments", money(latest.total, latest.currency), latest.total})
		} else {
			table.Rows = append(table.Rows, []any{"Investments", money(latest.total, latest.currency), nil})
			notes = append(notes, "The portfolio is in "+latest.currency+" and is not included in the euro total.")
		}
		notes = append(notes, "Portfolio value as of "+fetchedLabel(latest.fetchedAt)+".")
		if len(skipped) > 0 {
			notes = append(notes, joinNames(skipped)+" is counted through the portfolio value.")
		}
	}
	when := ""
	if until != "" {
		when = " on " + dayLabel(until)
	}
	text := fmt.Sprintf("Your net worth%s is %s: %s in accounts", when, euros(total), euros(cash))
	if ok && latest.currency == "EUR" {
		text += " and " + euros(latest.total) + " in investments"
	}
	text += "."
	return Answer{Text: text, Table: table, Notes: notes}, nil
}

type pie struct {
	name                          string
	invested, value, result, cash int64
	gained, reinvested, inCash    int64
}

func (r *run) pies(snapshotID int64) ([]pie, error) {
	var result []pie
	err := r.query(`SELECT coalesce(p.name, 'Pie ' || p.id), s.invested_minor, s.value_minor, s.result_minor, s.cash_minor,
        s.dividends_gained_minor, s.dividends_reinvested_minor, s.dividends_in_cash_minor
    FROM investing_pie_snapshots s JOIN investing_pies p ON p.id = s.pie_id WHERE s.snapshot_id = ? ORDER BY s.value_minor DESC`,
		[]any{snapshotID}, func(rows *sql.Rows) error {
			var p pie
			if err := rows.Scan(&p.name, &p.invested, &p.value, &p.result, &p.cash, &p.gained, &p.reinvested, &p.inCash); err != nil {
				return err
			}
			result = append(result, p)
			return nil
		})
	return result, err
}

func returnOf(result, invested int64) any {
	if invested <= 0 {
		return nil
	}
	return float64(result) / float64(invested)
}

func (r *run) portfolioValue(in Interpretation) (Answer, error) {
	until := ""
	if in.PeriodGiven && in.Period.To != "" && in.Period.To < dateOf(r.today) {
		until = in.Period.To
	}
	latest, ok, err := r.snapshotAt(until, false)
	if err != nil || !ok {
		return noInvesting, err
	}
	c := latest.currency
	text := fmt.Sprintf("Your portfolio is worth %s as of %s. You have %s invested", money(latest.total, c), fetchedLabel(latest.fetchedAt), money(latest.cost, c))
	if latest.unrealized >= 0 {
		text += ", with an unrealized gain of " + money(latest.unrealized, c)
	} else {
		text += ", with an unrealized loss of " + money(-latest.unrealized, c)
	}
	if latest.cost > 0 {
		text += " (" + percent(float64(latest.unrealized)/float64(latest.cost)) + ")"
	}
	text += "."
	notes := []string{fmt.Sprintf("Realized result so far: %s. Cash at the broker: %s.", money(latest.real, c), money(latest.cash, c))}
	var started sql.NullString
	if err := r.db.QueryRowContext(r.ctx, `SELECT started_on FROM investing_state WHERE id = 1`).Scan(&started); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Answer{}, err
	}
	if started.Valid {
		notes = append(notes, "Investing since "+dayLabel(started.String)+".")
	}
	pies, err := r.pies(latest.id)
	if err != nil {
		return Answer{}, err
	}
	result := Answer{Text: text, Notes: notes, Link: &Link{Label: "Open Investing", Href: "/investing/"}}
	if len(pies) > 0 {
		table := &Table{Columns: []Column{{Label: "Pie", Kind: "text"}, {Label: "Value", Kind: "money", Currency: c, Bar: true},
			{Label: "Invested", Kind: "money", Currency: c}, {Label: "Result", Kind: "money", Currency: c}, {Label: "Return", Kind: "percent"}}}
		for _, p := range pies {
			table.Rows = append(table.Rows, []any{p.name, p.value, p.invested, p.result, returnOf(p.result, p.invested)})
		}
		result.Table = table
	}
	return result, nil
}

func (r *run) portfolioChange(in Interpretation) (Answer, error) {
	period := in.Period
	if period.allTime() {
		period = Period{Label: "all time", Phrase: "since the first snapshot"}
	}
	start, ok, err := r.snapshotAt(period.From, true)
	if err != nil || !ok {
		return noInvesting, err
	}
	end, ok, err := r.snapshotAt(period.To, false)
	if err != nil || !ok || end.fetchedAt < start.fetchedAt {
		return Answer{Text: "There are no investing snapshots" + phrase(period) + " to compare.", Link: &Link{Label: "Open Investing", Href: "/investing/"}}, err
	}
	c := end.currency
	valueChange := end.total - start.total
	invested := end.cost - start.cost
	market := valueChange - invested
	text := fmt.Sprintf("%s your portfolio went from %s to %s, %s.", upperFirst(strings.TrimPrefix(period.Phrase, " ")), money(start.total, c), money(end.total, c), signed(valueChange, c))
	if period.Phrase == "" {
		text = fmt.Sprintf("Your portfolio went from %s to %s, %s.", money(start.total, c), money(end.total, c), signed(valueChange, c))
	}
	text += fmt.Sprintf(" Of that, %s came from new money invested and %s from market movement.", signed(invested, c), signed(market, c))
	notes := []string{"Compared snapshots from " + fetchedLabel(start.fetchedAt) + " and " + fetchedLabel(end.fetchedAt) + "."}
	if start.currency != end.currency {
		notes = append(notes, "The account currency changed between the snapshots, so the change is not exact.")
	}
	return Answer{Text: text, Notes: notes, Link: &Link{Label: "Open Investing", Href: "/investing/"}}, nil
}

func signed(minor int64, currency string) string {
	if minor > 0 {
		return "up " + money(minor, currency)
	}
	if minor < 0 {
		return "down " + money(-minor, currency)
	}
	return "unchanged"
}

func (r *run) piePerformance(in Interpretation) (Answer, error) {
	latest, ok, err := r.snapshotAt("", false)
	if err != nil || !ok {
		return noInvesting, err
	}
	pies, err := r.pies(latest.id)
	if err != nil {
		return Answer{}, err
	}
	if len(pies) == 0 {
		return Answer{Text: "Your portfolio has no pies.", Link: &Link{Label: "Open Investing", Href: "/investing/"}}, nil
	}
	rate := func(p pie) float64 {
		if p.invested <= 0 {
			return 0
		}
		return float64(p.result) / float64(p.invested)
	}
	slices.SortStableFunc(pies, func(a, b pie) int {
		if in.Order == "least" {
			return compareFloat(rate(a), rate(b))
		}
		return compareFloat(rate(b), rate(a))
	})
	c := latest.currency
	table := &Table{Columns: []Column{{Label: "Pie", Kind: "text"}, {Label: "Return", Kind: "percent"}, {Label: "Result", Kind: "money", Currency: c, Bar: true},
		{Label: "Value", Kind: "money", Currency: c}, {Label: "Invested", Kind: "money", Currency: c}}}
	for _, p := range pies {
		table.Rows = append(table.Rows, []any{p.name, returnOf(p.result, p.invested), p.result, p.value, p.invested})
	}
	top := pies[0]
	word := "best"
	if in.Order == "least" {
		word = "worst"
	}
	text := fmt.Sprintf("Your %s performing pie is %s with a return of %s (%s on %s invested).", word, top.name, percent(rate(top)), money(top.result, c), money(top.invested, c))
	return Answer{Text: text, Table: table, Notes: []string{"As of " + fetchedLabel(latest.fetchedAt) + ". Return is the pie result divided by the amount invested."},
		Link: &Link{Label: "Open Investing", Href: "/investing/"}}, nil
}

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (r *run) dividends(in Interpretation) (Answer, error) {
	latest, ok, err := r.snapshotAt(in.Period.To, false)
	if err != nil || !ok {
		return noInvesting, err
	}
	pies, err := r.pies(latest.id)
	if err != nil {
		return Answer{}, err
	}
	earlier := map[string]pie{}
	since := ""
	if in.PeriodGiven && in.Period.From != "" {
		start, found, err := r.snapshotAt(in.Period.From, true)
		if err != nil {
			return Answer{}, err
		}
		if found && start.id != latest.id {
			previous, err := r.pies(start.id)
			if err != nil {
				return Answer{}, err
			}
			for _, p := range previous {
				earlier[p.name] = p
			}
			since = start.fetchedAt
		}
	}
	c := latest.currency
	table := &Table{Columns: []Column{{Label: "Pie", Kind: "text"}, {Label: "Gained", Kind: "money", Currency: c, Bar: true},
		{Label: "Reinvested", Kind: "money", Currency: c}, {Label: "Paid as cash", Kind: "money", Currency: c}}}
	var gained, reinvested, inCash int64
	for _, p := range pies {
		before := earlier[p.name]
		g, re, ca := p.gained-before.gained, p.reinvested-before.reinvested, p.inCash-before.inCash
		gained, reinvested, inCash = gained+g, reinvested+re, inCash+ca
		if g != 0 || re != 0 || ca != 0 {
			table.Rows = append(table.Rows, []any{p.name, g, re, ca})
		}
	}
	if len(table.Rows) == 0 {
		table = nil
	}
	when := " so far"
	if since != "" {
		when = " between " + fetchedLabel(since) + " and " + fetchedLabel(latest.fetchedAt)
	}
	text := fmt.Sprintf("Your pies earned %s in dividends%s: %s reinvested and %s paid out as cash.", money(gained, c), when, money(reinvested, c), money(inCash, c))
	notes := []string{"Dividends are tracked per pie by Trading 212; holdings outside pies are not included."}
	if in.PeriodGiven && since == "" && !in.Period.allTime() {
		notes = append(notes, "There is no snapshot from the start of that period, so this shows the total so far.")
	}
	return Answer{Text: text, Table: table, Notes: notes, Link: &Link{Label: "Open Investing", Href: "/investing/"}}, nil
}

func (r *run) exchangeRate(in Interpretation) (Answer, error) {
	date := ""
	if in.PeriodGiven && in.Period.To != "" {
		date = in.Period.To
	}
	if in.Currency == "" {
		table := &Table{Columns: []Column{{Label: "Currency", Kind: "text"}, {Label: "1 EUR buys", Kind: "text"}, {Label: "Published", Kind: "date"}}}
		err := r.query(`SELECT quote_currency, rate, publication_date FROM exchange_rates e
            WHERE base_currency = 'EUR' AND publication_date = (SELECT max(publication_date) FROM exchange_rates x
                WHERE x.base_currency = 'EUR' AND x.quote_currency = e.quote_currency AND (? = '' OR x.publication_date <= ?))
            ORDER BY quote_currency`, []any{date, date}, func(rows *sql.Rows) error {
			var code, rate, published string
			if err := rows.Scan(&code, &rate, &published); err != nil {
				return err
			}
			table.Rows = append(table.Rows, []any{code, rate + " " + code, published})
			return nil
		})
		if err != nil {
			return Answer{}, err
		}
		if len(table.Rows) == 0 {
			return Answer{Text: "No exchange rates are stored yet. They are fetched when you import transactions in another currency."}, nil
		}
		return Answer{Text: "These are the latest euro exchange rates stored in the app.", Table: table,
			Notes: []string{"Rates are fetched for the currencies of your transactions. Ask about one currency, such as USD, for more detail."}}, nil
	}
	var rate, published string
	err := r.db.QueryRowContext(r.ctx, `SELECT rate, publication_date FROM exchange_rates
        WHERE base_currency = 'EUR' AND quote_currency = ? AND (? = '' OR publication_date <= ?) ORDER BY publication_date DESC LIMIT 1`, in.Currency, date, date).Scan(&rate, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return Answer{Text: "No " + in.Currency + " exchange rate is stored. Rates are fetched when you import transactions in that currency."}, nil
	}
	if err != nil {
		return Answer{}, err
	}
	text := fmt.Sprintf("On %s, 1 EUR was %s %s", dayLabel(published), rate, in.Currency)
	if value, ok := new(big.Float).SetString(rate); ok && value.Sign() > 0 {
		inverse, _ := new(big.Float).Quo(big.NewFloat(1), value).Float64()
		text += fmt.Sprintf(", so 1 %s was %s EUR", in.Currency, trimFloat(inverse))
	}
	text += "."
	return Answer{Text: text, Notes: []string{"This is the latest rate stored in the app, used to convert your transactions to euros."}}, nil
}

func trimFloat(value float64) string {
	text := fmt.Sprintf("%.6f", value)
	text = strings.TrimRight(text, "0")
	return strings.TrimSuffix(text, ".")
}
