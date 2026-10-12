package imports

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/ledger"
)

// Ask evaluates typed questions about state with TypeSafe Jev.
type Ask func(ctx context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error)

const (
	// confident is the lowest choice confidence accepted without a note.
	confident = 0.5
	// plausible is the lowest probability at which a role whose likeliest
	// column another role took moves to its next option.
	plausible = 0.1
	noColumn  = "none"
	// maxChoiceOptions is the TypeSafe limit for one choice question.
	maxChoiceOptions = 255
)

// columnRole is one column the mapping can use. Core columns hold distinct
// values; payee, category, notes, and fee currency may share a column with
// each other. Roles take columns in this order.
type columnRole struct {
	key, label, question string
	core                 bool
}

var columnRoles = []columnRole{
	{"date", "date", "Which column holds the date or time of each transaction?", true},
	{"amount", "amount", "Which column holds every transaction amount, either signed or unsigned with a type column telling money out from money in?", true},
	{"debit", "money out", "Which column holds only amounts of money leaving the account, such as purchases and withdrawals, and is empty for money coming in?", true},
	{"credit", "money in", "Which column holds only amounts of money coming into the account, such as salary and deposits, and is empty for money leaving it?", true},
	{"action", "transaction type", "Which column names the type of each transaction, such as card payment, deposit, or withdrawal, using a few repeated values?", true},
	{"currency", "currency", "Which column holds the ISO 4217 currency code of each transaction amount, such as EUR or USD?", true},
	{"external_id", "transaction ID", "Which column holds an ID that identifies each transaction and differs in every row?", true},
	{"fee_amount", "fee", "Which column holds a separately listed fee amount, such as a currency conversion fee?", true},
	{"fee_currency", "fee currency", "Which column holds the currency code of the separately listed fee?", false},
	{"payee", "payee", "Which column names the merchant or counterparty of each transaction?", false},
	{"category", "category", "Which column holds a merchant category, such as groceries or restaurants?", false},
	{"notes", "notes", "Which column holds a free-text description or reference of each transaction?", false},
}

type sampleColumn struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

func choice(instructions any, criteria map[string]any) integrations.Question {
	return integrations.Question{Type: integrations.QuestionChoice, Instructions: instructions, Criteria: criteria}
}

func columnKey(index int) string { return "column " + strconv.Itoa(index+1) }

// Suggest asks Jev for a mapping of a file from its header and at most
// SampleSize records; nothing else leaves the server. The first request
// picks a column for each role, the second reads the values of the picked
// columns. Code settles what code can: shared columns, the fixed currency,
// and whether the sample shows the day and month order. Answers below the
// confidence threshold come back as notes for the owner to check.
func Suggest(ctx context.Context, ask Ask, header []string, records [][]string, accountCurrency string) (Mapping, []string, error) {
	if len(records) > SampleSize {
		records = records[:SampleSize]
	}
	if len(header)+1 > maxChoiceOptions {
		return Mapping{}, nil, userError("The file has too many columns for a suggestion. Map the columns manually.")
	}
	columns := make([]sampleColumn, len(header))
	for index, name := range header {
		columns[index] = sampleColumn{Name: name, Values: columnRows(records, index)}
	}
	options := func(optional bool) map[string]any {
		criteria := make(map[string]any, len(columns)+1)
		for index, column := range columns {
			criteria[columnKey(index)] = column
		}
		if optional {
			criteria[noColumn] = "No column holds this"
		}
		return criteria
	}
	questions := map[string]integrations.Question{
		"amount_mode": choice("Are the transaction amounts in one column, or split into one column for money leaving the account and another for money coming in?",
			map[string]any{
				AmountSingle:      "One column holds every amount, and a sign or a type column tells money out from money in",
				AmountDebitCredit: "Money leaving the account and money coming in are in two separate columns",
			}),
	}
	for _, role := range columnRoles {
		questions[role.key] = choice(role.question, options(role.key != "date"))
	}
	answers, err := ask(ctx, map[string]any{"columns": columns}, questions)
	if err != nil {
		return Mapping{}, nil, err
	}

	s := suggestion{answers: answers, columns: len(header), core: map[int]bool{}, picked: map[string]*int{}}
	mode := s.option("amount_mode", "Check whether amounts are in one column or in separate money out and money in columns.",
		AmountSingle, AmountDebitCredit)
	for _, role := range columnRoles {
		single := role.key == "amount"
		split := role.key == "debit" || role.key == "credit"
		if (single && mode != AmountSingle) || (split && mode != AmountDebitCredit) {
			continue
		}
		s.take(role, role.key == "date" || single || split)
	}
	if s.err != nil {
		return Mapping{}, nil, s.err
	}
	mapping := Mapping{
		Date: DateField{Column: *s.picked["date"]},
		Amount: AmountField{Mode: mode, Column: s.picked["amount"], DebitColumn: s.picked["debit"],
			CreditColumn: s.picked["credit"], Sign: NegativeIsOutflow},
		Currency:          CurrencyField{Column: s.picked["currency"]},
		ExternalIDColumn:  s.picked["external_id"],
		PayeeColumn:       s.picked["payee"],
		CategoryColumn:    s.picked["category"],
		NotesColumn:       s.picked["notes"],
		ActionColumn:      s.picked["action"],
		FeeAmountColumn:   s.picked["fee_amount"],
		FeeCurrencyColumn: s.picked["fee_currency"],
		Actions:           []ActionRule{},
		Ambiguities:       []string{},
	}
	if mapping.FeeAmountColumn == nil {
		mapping.FeeCurrencyColumn = nil
	}
	if mapping.Currency.Column == nil {
		fixed := namedCurrency(header, mapping.Amount.Column, mapping.Amount.DebitColumn, mapping.Amount.CreditColumn)
		if fixed == "" {
			fixed = accountCurrency
		}
		mapping.Currency.Fixed = &fixed
	}
	if err := s.readValues(ctx, ask, &mapping, header, records); err != nil {
		return Mapping{}, nil, err
	}
	return mapping, s.notes, nil
}

// suggestion collects picked columns and notes from Jev's answers. The first
// answer outside its question stops it.
type suggestion struct {
	answers map[string]integrations.Answer
	columns int
	core    map[int]bool
	picked  map[string]*int
	notes   []string
	err     error
}

var errOutsideQuestion = &integrations.SuggestError{Message: "TypeSafe answered outside the question. Map the columns manually."}

// option returns the chosen option of a fixed choice and notes a choice
// below the confidence threshold.
func (s *suggestion) option(key, note string, allowed ...string) string {
	answer := s.answers[key]
	if !slices.Contains(allowed, answer.Choice) {
		if s.err == nil {
			s.err = errOutsideQuestion
		}
		return allowed[0]
	}
	if answer.Confidence < confident {
		s.notes = append(s.notes, note)
	}
	return answer.Choice
}

// take picks the likeliest column for a role that no core role took. When
// that column is taken, the role moves to its next plausible option, which
// may be no column; a required role keeps its first choice and validation
// reports the shared column.
func (s *suggestion) take(role columnRole, required bool) {
	if s.err != nil {
		return
	}
	answer := s.answers[role.key]
	ranked := slices.SortedFunc(maps.Keys(answer.Probabilities), func(a, b string) int {
		return cmp.Or(cmp.Compare(answer.Probabilities[b], answer.Probabilities[a]), strings.Compare(a, b))
	})
	if len(ranked) == 0 || ranked[0] != answer.Choice {
		ranked = append([]string{answer.Choice}, ranked...)
	}
	var column *int
	moved := true
	for rank, option := range ranked {
		if rank > 0 && answer.Probabilities[option] < plausible {
			break
		}
		if option == noColumn && !required {
			column, moved = nil, rank > 0
			break
		}
		index, ok := s.column(option)
		if !ok {
			s.err = errOutsideQuestion
			return
		}
		if !s.core[index] {
			column, moved = &index, rank > 0
			break
		}
	}
	if column == nil && required {
		index, ok := s.column(answer.Choice)
		if !ok {
			s.err = errOutsideQuestion
			return
		}
		column = &index
	}
	if column != nil && role.core {
		s.core[*column] = true
	}
	// An optional role left without a column is not worth a note.
	if (column != nil || required) && (moved || answer.Confidence < confident) {
		s.notes = append(s.notes, fmt.Sprintf("Check the %s column.", role.label))
	}
	s.picked[role.key] = column
}

func (s *suggestion) column(option string) (int, bool) {
	number, found := strings.CutPrefix(option, "column ")
	index, err := strconv.Atoi(number)
	if !found || err != nil || index < 1 || index > s.columns {
		return 0, false
	}
	return index - 1, true
}

// readValues asks how the picked columns write dates, signs, separators, and
// actions. The request holds only the picked columns of the same records.
func (s *suggestion) readValues(ctx context.Context, ask Ask, mapping *Mapping, header []string, records [][]string) error {
	named := map[string]*int{"date": &mapping.Date.Column, "amount": mapping.Amount.Column,
		"money out": mapping.Amount.DebitColumn, "money in": mapping.Amount.CreditColumn,
		"type": mapping.ActionColumn, "payee": mapping.PayeeColumn, "notes": mapping.NotesColumn}
	names := map[string]string{}
	rows := make([]map[string]string, len(records))
	for index := range rows {
		rows[index] = map[string]string{}
	}
	for name, column := range named {
		if column == nil {
			continue
		}
		names[name] = header[*column]
		for index, values := range columnRows(records, *column) {
			rows[index][name] = values
		}
	}
	questions := map[string]integrations.Question{
		"date_format": choice("In what order are the year, month, and day written in `rows[].date`?", map[string]any{
			DateISO: "Year first, such as 2026-08-31",
			DateDMY: "Day before month, such as 31.08.2026 or 31/08/2026",
			DateMDY: "Month before day, such as 08/31/2026",
		}),
		"utc": {Type: integrations.QuestionNoul,
			Instructions: "Do `columns.date` or the values in `rows[].date` say that the times are in UTC or carry an offset from UTC?",
			Criteria: map[string]string{
				"true":  "The column name mentions UTC or GMT, or the values end in Z or an offset such as +02:00",
				"false": "Neither the column name nor the values mention a timezone",
			}},
		"decimal": choice("Which character separates the whole units from the cents in the amounts in `rows`?", map[string]any{
			"point": "A point, as in 12.40 or 1,500.00",
			"comma": "A comma, as in 12,40 or 1.500,00",
		}),
		"thousands": choice("Which character groups the thousands in the amounts in `rows`?", map[string]any{
			"none":       "Nothing groups the thousands, as in 1500.00, or no amount reaches a thousand",
			"comma":      "A comma, as in 1,500.00",
			"point":      "A point, as in 1.500,00",
			"space":      "A space, as in 1 500,00",
			"apostrophe": "An apostrophe, as in 1'500.00",
		}),
	}
	if mapping.Amount.Mode == AmountSingle {
		questions["sign"] = choice("Do the amount signs or the transaction types determine money out versus money in? Consider the type column name even if the sample contains only outflows.", map[string]any{
			NegativeIsOutflow: "Money leaving the account is negative, such as -12.40 for a purchase",
			PositiveIsOutflow: "Money leaving the account is positive, and money coming in is negative",
			ActionIsDirection: "Amounts are unsigned magnitudes for both directions; the transaction type identifies expenses, income, or refunds",
		})
	}
	var actions []string
	if mapping.ActionColumn != nil {
		seen := map[string]bool{}
		for _, value := range columnValues(records, *mapping.ActionColumn) {
			if key := normalizeAction(value); key != "" && !seen[key] && len(value) <= 200 {
				seen[key] = true
				actions = append(actions, value)
			}
		}
	}
	for index, value := range actions {
		questions["action "+strconv.Itoa(index+1)] = choice(map[string]any{
			"type":     value,
			"question": "What does a transaction whose type is `type` record?",
		}, map[string]any{
			ledger.KindExpense:  "Money spent, such as a purchase, payment, or fee",
			ledger.KindIncome:   "Money earned, such as salary or interest",
			ledger.KindRefund:   "Money returned for an earlier purchase",
			ledger.KindTransfer: "Money moved between the owner's own accounts, such as a deposit or withdrawal",
			KindSkip:            "Not a transaction, or a reward the owner excludes such as cashback",
		})
	}
	answers, err := ask(ctx, map[string]any{"columns": names, "rows": rows}, questions)
	if err != nil {
		return err
	}
	s.answers = answers
	mapping.Date.Format = s.option("date_format", "Check the date order.", DateISO, DateDMY, DateMDY)
	if mapping.Date.Format != DateISO && !dayOrderShown(columnValues(records, mapping.Date.Column)) {
		s.notes = append(s.notes, "The sample does not show whether dates are day-first or month-first. Check the date order.")
	}
	mapping.Date.Timezone = ZoneLocal
	if utc := answers["utc"].Noul; utc >= 0.5 {
		mapping.Date.Timezone = ZoneUTC
	}
	if utc := answers["utc"].Noul; utc > 0.25 && utc < 0.75 {
		s.notes = append(s.notes, "Check how times without an offset are read.")
	}
	separators := map[string]string{"none": "", "point": ".", "comma": ",", "space": " ", "apostrophe": "'"}
	mapping.Amount.DecimalSeparator = separators[s.option("decimal", "Check the decimal separator.", "point", "comma")]
	mapping.Amount.ThousandsSeparator = separators[s.option("thousands", "Check the thousands separator.",
		"none", "comma", "point", "space", "apostrophe")]
	if mapping.Amount.ThousandsSeparator == mapping.Amount.DecimalSeparator {
		mapping.Amount.ThousandsSeparator = ""
		s.notes = append(s.notes, "Check the thousands separator.")
	}
	if mapping.Amount.Mode == AmountSingle {
		mapping.Amount.Sign = s.option("sign", "Check how amount direction is determined.", NegativeIsOutflow, PositiveIsOutflow, ActionIsDirection)
	}
	for index, value := range actions {
		kind := s.option("action "+strconv.Itoa(index+1), fmt.Sprintf("Check how %q rows are treated.", value),
			ledger.KindExpense, ledger.KindIncome, ledger.KindRefund, ledger.KindTransfer, KindSkip)
		mapping.Actions = append(mapping.Actions, ActionRule{Value: value, Kind: kind})
	}
	return s.err
}

// columnRows returns each record's trimmed value in a column, empty when
// the record is short.
func columnRows(records [][]string, column int) []string {
	values := make([]string, len(records))
	for index, record := range records {
		if column < len(record) {
			values[index] = strings.TrimSpace(record[column])
		}
	}
	return values
}

// columnValues returns the non-empty values of a column.
func columnValues(records [][]string, column int) []string {
	return slices.DeleteFunc(columnRows(records, column), func(value string) bool { return value == "" })
}

var digitGroups = regexp.MustCompile(`\d+`)

// dayOrderShown reports whether any date tells day-first from month-first:
// a year-first value, or a first or second part above 12.
func dayOrderShown(values []string) bool {
	for _, value := range values {
		parts := digitGroups.FindAllString(value, 2)
		if len(parts) < 2 {
			continue
		}
		first, _ := strconv.Atoi(parts[0])
		second, _ := strconv.Atoi(parts[1])
		if len(parts[0]) == 4 || first > 12 || second > 12 {
			return true
		}
	}
	return false
}

var namedCode = regexp.MustCompile(`\b[A-Z]{3}\b`)

// namedCurrency returns a supported currency code the names of the amount
// columns hold, such as USD in "Amount (USD)", or "".
func namedCurrency(header []string, columns ...*int) string {
	for _, column := range columns {
		if column == nil {
			continue
		}
		for _, code := range namedCode.FindAllString(header[*column], -1) {
			if _, err := ledger.MinorUnits(code); err == nil {
				return code
			}
		}
	}
	return ""
}
