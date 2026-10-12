package assistant

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/ledger"
)

// run answers one question. today is the owner's local date at midnight UTC.
type run struct {
	ctx   context.Context
	db    *sql.DB
	today time.Time
}

const (
	// Below this confidence the assistant asks what was meant.
	lowConfidence = 0.3
	// Noul probabilities above which names of that kind are looked for:
	// whole names at the first, single shared words at the second.
	fullGate    = 0.2
	partialGate = 0.5
	maxLimit    = 50
)

// resolve turns Jev's reading into dates, names, and numbers from the
// question itself. A follow-up starts from the previous interpretation.
func (r *run) resolve(read reading, question string, previous *Turn) (Interpretation, error) {
	follow := read.intent == "follow_up" && previous != nil
	var in Interpretation
	if follow {
		in = previous.Interpretation
		in.Payees, in.Categories, in.Accounts = slices.Clone(in.Payees), slices.Clone(in.Categories), slices.Clone(in.Accounts)
		in.Unmatched, in.Options = nil, nil
		if _, ok := intentByKey[in.Intent]; !ok || in.Intent == "follow_up" {
			in.Intent = "unsupported"
		}
	} else {
		in = Interpretation{Intent: read.intent, Order: "not_mentioned", Step: "not_mentioned", Compare: "not_mentioned"}
	}
	in.Confidence = read.confidence
	in.Options = read.options
	kind := intentByKey[in.Intent]

	r.resolvePeriod(&in, read, question, follow, kind)
	r.resolveDirection(&in, read, follow, kind)
	if read.order != "not_mentioned" || !follow {
		in.Order = read.order
	}
	if read.step != "not_mentioned" || !follow {
		in.Step = read.step
	}
	if read.compare != "not_mentioned" || !follow {
		in.Compare = read.compare
	}

	search, rest := quoted(question)
	if search != "" {
		in.Search = search
	} else if !follow {
		in.Search = ""
	}
	if limit, ok := limitOf(rest); ok {
		in.Limit = limit
	} else if !follow {
		in.Limit = 0
	}
	if low, high, ok := boundsOf(rest); ok {
		in.MinCents, in.MaxCents = low, high
	} else if !follow {
		in.MinCents, in.MaxCents = 0, 0
	}
	if code := currencyOf(rest); code != "" {
		in.Currency = code
	} else if !follow {
		in.Currency = ""
	}
	return in, r.resolveNames(&in, read, rest, follow)
}

func (r *run) resolvePeriod(in *Interpretation, read reading, question string, follow bool, kind intent) {
	key := read.period
	if key == "not_mentioned" {
		// Jev may miss a date written as digits; read it from the text.
		if _, ok := lastN(question, r.today); ok && lastNPattern.MatchString(question) {
			key = "last_n"
		} else if len(mentions(question, r.today)) > 0 {
			key = "date_range"
		}
	}
	if key != "not_mentioned" {
		if period, ok := resolvePeriod(key, question, r.today); ok {
			in.Period, in.PeriodGiven = period, true
			return
		}
	}
	if follow {
		return
	}
	in.PeriodGiven = false
	in.Period = Period{}
	if kind.period != "" && kind.period != "not_mentioned" {
		in.Period, _ = resolvePeriod(kind.period, question, r.today)
	}
}

// resolveDirection keeps the direction an intent implies, such as income
// for income_total, and otherwise takes what the question says.
func (r *run) resolveDirection(in *Interpretation, read reading, follow bool, kind intent) {
	switch in.Intent {
	case "spending_total":
		in.Direction = spending
		if read.direction == income && follow {
			in.Intent, in.Direction = "income_total", income
		}
		return
	case "income_total":
		in.Direction = income
		if read.direction == spending && follow {
			in.Intent, in.Direction = "spending_total", spending
		}
		return
	case "net_flow", "savings_rate", "summary":
		in.Direction = both
		return
	}
	switch {
	case read.direction != "not_mentioned":
		in.Direction = read.direction
	case !follow || in.Direction == "":
		in.Direction = kind.direction
	}
	if in.Direction == "" {
		in.Direction = both
	}
}

// resolveNames finds the payees, categories, and accounts the question
// names. Jev only says whether a name is there; code looks it up, so no
// names leave the server.
func (r *run) resolveNames(in *Interpretation, read reading, question string, follow bool) error {
	gates := map[string]float64{"payee": read.payee, "category": read.category, "account": read.account}
	if gates["payee"] < fullGate && gates["category"] < fullGate && gates["account"] < fullGate {
		if !follow {
			in.Payees, in.Categories, in.Accounts = nil, nil, nil
		}
		return nil
	}
	payees, categories, accounts, err := r.loadEntities()
	if err != nil {
		return err
	}
	byKind := map[string][]entity{"payee": payees, "category": categories, "account": accounts}
	order := []string{"payee", "category", "account"}
	slices.SortStableFunc(order, func(a, b string) int {
		switch {
		case gates[a] > gates[b]:
			return -1
		case gates[a] < gates[b]:
			return 1
		}
		return 0
	})
	tokens := words(question)
	used := make([]bool, len(tokens))
	found := map[string][]Named{}
	for _, name := range order {
		if gates[name] < fullGate {
			continue
		}
		matches, spans := fullMatches(tokens, used, byKind[name])
		found[name] = matches
		mark(used, spans)
	}
	for _, name := range order {
		if gates[name] < partialGate || len(found[name]) > 0 {
			continue
		}
		matches, spans := partialMatches(tokens, used, byKind[name])
		if len(matches) > 8 {
			// A word shared by many names identifies none of them.
			continue
		}
		found[name] = matches
		mark(used, spans)
	}

	named := len(found["payee"])+len(found["category"])+len(found["account"]) > 0
	if named || !follow {
		in.Payees, in.Categories, in.Accounts = found["payee"], found["category"], found["account"]
	}
	if named {
		return nil
	}
	expected := ""
	for _, name := range order {
		if gates[name] >= partialGate {
			expected = name
			break
		}
	}
	rest := leftovers(tokens, used)
	if expected == "" || len(rest) == 0 || in.Search != "" {
		return nil
	}
	// No payee by that name; transaction notes may still hold it, such as
	// card payments whose merchant was never made a payee.
	for _, word := range rest {
		matched, err := r.notesContain(word)
		if err != nil {
			return err
		}
		if matched {
			in.Search = word
			return nil
		}
	}
	in.Unmatched = append([]string{expected}, rest...)
	return nil
}

func (r *run) notesContain(word string) (bool, error) {
	var found bool
	err := r.db.QueryRowContext(r.ctx, `SELECT EXISTS (SELECT 1 FROM transactions
        WHERE deleted_at IS NULL AND instr(lower(notes), ?) > 0)`, strings.ToLower(word)).Scan(&found)
	return found, err
}

var quotePattern = regexp.MustCompile(`"([^"]{2,80})"|“([^”]{2,80})”|‘([^’]{2,80})’|„([^“”]{2,80})[“”]`)

// quoted returns text in quotes to search for, and the question without it.
func quoted(question string) (string, string) {
	match := quotePattern.FindStringSubmatchIndex(question)
	if match == nil {
		return "", question
	}
	for group := 1; group < len(match)/2; group++ {
		if match[2*group] >= 0 {
			text := strings.TrimSpace(question[match[2*group]:match[2*group+1]])
			return text, question[:match[0]] + " " + question[match[1]:]
		}
	}
	return "", question
}

const unitAfter = `(?:\s*(?:%|percent|x\b|times?\b|transactions?\b|purchases?\b|payments?\b|days?\b|weeks?\b|months?\b|years?\b|visits?\b|items?\b))`

var (
	topPattern   = regexp.MustCompile(`(?i)\b(?:top|first|biggest|largest|smallest|highest|lowest|best|worst|last|latest|recent)\s+(\d{1,3}|one|two|three|four|five|six|seven|eight|nine|ten|twelve|fifteen|twenty|thirty)\b`)
	countPattern = regexp.MustCompile(`(?i)\b(\d{1,3}|three|four|five|six|seven|eight|nine|ten|twelve|fifteen|twenty|thirty)\s+(?:most\s+|least\s+|biggest\s+|largest\s+|smallest\s+|top\s+|recent\s+|latest\s+|last\s+|frequent\s+|expensive\s+|cheapest\s+)*(?:payees|merchants|shops|stores|places|companies|categories|transactions|purchases|payments|expenses|incomes|accounts|pies|days|months|subscriptions|items|results|rows)\b`)
	unitPattern  = regexp.MustCompile(`(?i)^` + unitAfter)
	// "last 30 days" is a period even though it reads like "30 days".
	periodCountPattern = regexp.MustCompile(`(?i)^\d{1,3}\s+(?:days|months)\b`)
	recentPattern      = regexp.MustCompile(`(?i)(?:last|past|previous)\s+$`)
	yearPattern        = regexp.MustCompile(`^(19|20)\d{2}$`)
)

// limitOf reads how many rows are asked for, as in "top 5" or "3 biggest
// purchases". A number followed by a time unit is a period, not a limit.
func limitOf(question string) (int, bool) {
	for _, pattern := range []*regexp.Regexp{topPattern, countPattern} {
		for _, match := range pattern.FindAllStringSubmatchIndex(question, -1) {
			number := strings.ToLower(question[match[2]:match[3]])
			if pattern == topPattern && unitPattern.MatchString(question[match[1]:]) {
				continue
			}
			if pattern == countPattern && periodCountPattern.MatchString(question[match[0]:]) && recentPattern.MatchString(question[:match[0]]) {
				continue
			}
			value, err := strconv.Atoi(number)
			if err != nil {
				value = numberWords[number]
			}
			if value > 0 {
				return min(value, maxLimit), true
			}
		}
	}
	return 0, false
}

const amountText = `(?:€|eur\s*|euros?\s*|\$)?\s*(\d[\d,\s]*(?:\.\d{1,2})?)\s*(k\b)?\s*(?:€|eur\b|euros?\b)?`

var (
	lowPattern     = regexp.MustCompile(`(?i)\b(?:over|above|more than|greater than|bigger than|larger than|at least|exceeding|minimum(?: of)?|min)\s*` + amountText + `|>=?\s*` + amountText)
	highPattern    = regexp.MustCompile(`(?i)\b(?:under|below|less than|smaller than|lower than|at most|up to|maximum(?: of)?|max|cheaper than)\s*` + amountText + `|<=?\s*` + amountText)
	betweenPattern = regexp.MustCompile(`(?i)\bbetween\s*` + amountText + `\s*(?:and|-)\s*` + amountText)
)

// boundsOf reads amount limits in euros, such as "over 100" or "between
// 20 and 50 EUR", as cents. Numbers followed by a unit such as "times" or
// "days" are not amounts.
func boundsOf(question string) (int64, int64, bool) {
	if match := betweenPattern.FindStringSubmatchIndex(question); match != nil && !unitPattern.MatchString(question[match[1]:]) && !mentionsYear(question, match) {
		low, okLow := centsOf(question, match, 1)
		high, okHigh := centsOf(question, match, 3)
		if okLow && okHigh {
			return min(low, high), max(low, high), true
		}
	}
	var low, high int64
	found := false
	for _, pattern := range []*regexp.Regexp{lowPattern, highPattern} {
		match := pattern.FindStringSubmatchIndex(question)
		if match == nil || unitPattern.MatchString(question[match[1]:]) || mentionsYear(question, match) {
			continue
		}
		group := 1
		if match[2] < 0 {
			group = 3
		}
		value, ok := centsOf(question, match, group)
		if !ok {
			continue
		}
		if pattern == lowPattern {
			low = value
		} else {
			high = value
		}
		found = true
	}
	return low, high, found
}

// mentionsYear reports an amount that is really a year, as in "since 2024".
func mentionsYear(question string, match []int) bool {
	for group := 1; group < len(match)/2; group += 2 {
		if match[2*group] >= 0 {
			text := strings.TrimSpace(question[match[2*group]:match[2*group+1]])
			whole := strings.ToLower(question[match[0]:match[1]])
			return yearPattern.MatchString(text) && !strings.ContainsAny(whole, "€$") && !strings.Contains(whole, "eur")
		}
	}
	return false
}

func centsOf(question string, match []int, group int) (int64, bool) {
	if match[2*group] < 0 {
		return 0, false
	}
	text := strings.NewReplacer(",", "", " ", "").Replace(question[match[2*group]:match[2*group+1]])
	amount, err := ledger.ParseMoney(text, "EUR")
	if err != nil || amount.Minor() < 0 {
		return 0, false
	}
	cents := amount.Minor()
	if match[2*group+2] >= 0 {
		cents *= 1000
	}
	return cents, true
}

var currencyNames = map[string]string{
	"dollar": "USD", "dollars": "USD", "pound": "GBP", "pounds": "GBP", "sterling": "GBP", "koruna": "CZK", "korun": "CZK",
	"crowns": "CZK", "forint": "HUF", "forints": "HUF", "zloty": "PLN", "zlotys": "PLN", "franc": "CHF", "francs": "CHF",
	"yen": "JPY", "yuan": "CNY", "renminbi": "CNY", "lei": "RON", "leu": "RON", "lev": "BGN", "krona": "SEK", "kronor": "SEK",
	"krone": "NOK", "kroner": "NOK", "lira": "TRY", "rand": "ZAR", "rupee": "INR", "rupees": "INR", "won": "KRW",
}

// lowercase codes that are never ordinary English words.
var lowercaseCodes = []string{"usd", "gbp", "czk", "huf", "pln", "chf", "jpy", "sek", "nok", "dkk", "ron", "bgn", "cad", "aud", "nzd", "cny", "inr", "brl", "mxn", "zar", "sgd", "hkd", "isk", "ils", "krw", "thb", "idr", "myr", "php"}

var codePattern = regexp.MustCompile(`\b[A-Za-z]{3}\b`)

// currencyOf finds a currency other than the euro named in the question.
func currencyOf(question string) string {
	known := ledger.Currencies()
	for _, code := range codePattern.FindAllString(question, -1) {
		upper := strings.ToUpper(code)
		if upper == "EUR" || !slices.Contains(known, upper) {
			continue
		}
		if code == upper || slices.Contains(lowercaseCodes, code) {
			return upper
		}
	}
	for _, word := range words(question) {
		if code, ok := currencyNames[word]; ok && slices.Contains(known, code) {
			return code
		}
	}
	if strings.Contains(question, "$") {
		return "USD"
	}
	if strings.Contains(question, "£") {
		return "GBP"
	}
	return ""
}
