package imports

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"budget-tracking/backend/internal/ledger"
)

// Mapping interprets the columns of one CSV format. Column indexes are
// zero-based positions in the header. Every field is always present and
// optional columns are null.
type Mapping struct {
	Date                DateField     `json:"date"`
	Amount              AmountField   `json:"amount"`
	Currency            CurrencyField `json:"currency"`
	ExternalIDColumn    *int          `json:"externalIdColumn"`
	PayeeColumn         *int          `json:"payeeColumn"`
	CategoryColumn      *int          `json:"categoryColumn"`
	NotesColumn         *int          `json:"notesColumn"`
	NotesFallbackColumn *int          `json:"notesFallbackColumn"`
	ActionColumn        *int          `json:"actionColumn"`
	FeeAmountColumn     *int          `json:"feeAmountColumn"`
	FeeCurrencyColumn   *int          `json:"feeCurrencyColumn"`
	Actions             []ActionRule  `json:"actions"`
	// Ambiguities lists interpretations the author could not decide. A
	// mapping with any ambiguity is rejected instead of guessed.
	Ambiguities []string `json:"ambiguities"`
}

type DateField struct {
	Column int `json:"column"`
	// Format is iso (year first), dmy, or mdy.
	Format string `json:"format"`
	// Timezone applies to values without an offset: utc or local, where
	// local is the owner's timezone.
	Timezone string `json:"timezone"`
}

type AmountField struct {
	// Mode is single for one amount column or debit_credit for separate
	// outflow and inflow columns holding magnitudes.
	Mode         string `json:"mode"`
	Column       *int   `json:"column"`
	DebitColumn  *int   `json:"debitColumn"`
	CreditColumn *int   `json:"creditColumn"`
	// Sign reads either signed amounts or magnitudes directed by action kind.
	Sign               string `json:"sign"`
	DecimalSeparator   string `json:"decimalSeparator"`
	ThousandsSeparator string `json:"thousandsSeparator"`
}

type CurrencyField struct {
	Column *int    `json:"column"`
	Fixed  *string `json:"fixed"`
}

// ActionRule maps one value of the action column to a transaction kind or
// to skip, which excludes the row entirely.
type ActionRule struct {
	Value string `json:"value"`
	Kind  string `json:"kind"`
}

const (
	KindSkip = "skip"

	DateISO = "iso"
	DateDMY = "dmy"
	DateMDY = "mdy"

	ZoneUTC   = "utc"
	ZoneLocal = "local"

	AmountSingle      = "single"
	AmountDebitCredit = "debit_credit"

	NegativeIsOutflow = "negative_is_outflow"
	PositiveIsOutflow = "positive_is_outflow"
	ActionIsDirection = "action_is_direction"

	maxActions = 100
)

func validActionKind(kind string) bool {
	switch kind {
	case ledger.KindExpense, ledger.KindIncome, ledger.KindTransfer, ledger.KindRefund, KindSkip:
		return true
	}
	return false
}

// normalizeAction compares action values without case or spacing differences.
func normalizeAction(value string) string { return ledger.NormalizeName(value) }

// Validate checks the mapping against the header. It returns problems a
// person can fix; an empty result means the mapping is usable.
func (m *Mapping) Validate(header []string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if len(m.Ambiguities) > 0 {
		add("The mapping is ambiguous: %s", strings.Join(m.Ambiguities, "; "))
	}
	inRange := func(name string, column *int) bool {
		if column == nil {
			return true
		}
		if *column < 0 || *column >= len(header) {
			add("%s refers to column %d, but the file has %d columns", name, *column+1, len(header))
			return false
		}
		return true
	}
	date := m.Date.Column
	inRange("Date", &date)
	for _, column := range []struct {
		name  string
		value *int
	}{
		{"Amount", m.Amount.Column}, {"Debit", m.Amount.DebitColumn}, {"Credit", m.Amount.CreditColumn},
		{"Currency", m.Currency.Column}, {"ID", m.ExternalIDColumn}, {"Payee", m.PayeeColumn},
		{"Category", m.CategoryColumn}, {"Notes", m.NotesColumn}, {"Notes fallback", m.NotesFallbackColumn}, {"Action", m.ActionColumn},
		{"Fee amount", m.FeeAmountColumn}, {"Fee currency", m.FeeCurrencyColumn},
	} {
		inRange(column.name, column.value)
	}
	if m.Date.Format != DateISO && m.Date.Format != DateDMY && m.Date.Format != DateMDY {
		add("Choose a date order")
	}
	if m.Date.Timezone != ZoneUTC && m.Date.Timezone != ZoneLocal {
		add("Choose how to read times without an offset")
	}
	switch m.Amount.Mode {
	case AmountSingle:
		if m.Amount.Column == nil || m.Amount.DebitColumn != nil || m.Amount.CreditColumn != nil {
			add("A single amount needs one amount column and no debit or credit columns")
		}
		if m.Amount.Sign != NegativeIsOutflow && m.Amount.Sign != PositiveIsOutflow && m.Amount.Sign != ActionIsDirection {
			add("Choose which amount sign is money leaving the account")
		}
		if m.Amount.Sign == ActionIsDirection {
			if m.ActionColumn == nil || len(m.Actions) == 0 {
				add("Unsigned amounts need a transaction type column and action interpretations")
			}
			for _, rule := range m.Actions {
				if rule.Kind == ledger.KindTransfer {
					add("Unsigned transfer amounts need an explicit direction; use signed amounts or separate debit and credit columns")
					break
				}
			}
		}
	case AmountDebitCredit:
		if m.Amount.Column != nil || m.Amount.DebitColumn == nil || m.Amount.CreditColumn == nil ||
			*m.Amount.DebitColumn == *m.Amount.CreditColumn {
			add("Separate amounts need two different debit and credit columns and no single amount column")
		}
	default:
		add("Choose a single amount column or separate debit and credit columns")
	}
	if m.Amount.DecimalSeparator != "." && m.Amount.DecimalSeparator != "," {
		add("The decimal separator must be a point or a comma")
	}
	if !slices.Contains([]string{"", ",", ".", " ", "'"}, m.Amount.ThousandsSeparator) ||
		m.Amount.ThousandsSeparator == m.Amount.DecimalSeparator {
		add("The thousands separator must differ from the decimal separator")
	}
	switch {
	case (m.Currency.Column == nil) == (m.Currency.Fixed == nil):
		add("Choose a currency column or one fixed currency")
	case m.Currency.Fixed != nil:
		if _, err := ledger.MinorUnits(*m.Currency.Fixed); err != nil {
			add("The fixed currency is not a supported ISO 4217 code")
		}
	}
	if m.FeeCurrencyColumn != nil && m.FeeAmountColumn == nil {
		add("A fee currency column needs a fee amount column")
	}
	// Core columns must be distinct. Payee, category, and notes may share a
	// description column.
	used := map[int]string{}
	for _, column := range []struct {
		name  string
		value *int
	}{
		{"Date", &date}, {"Amount", m.Amount.Column}, {"Debit", m.Amount.DebitColumn},
		{"Credit", m.Amount.CreditColumn}, {"Currency", m.Currency.Column}, {"ID", m.ExternalIDColumn},
		{"Action", m.ActionColumn}, {"Fee amount", m.FeeAmountColumn},
	} {
		if column.value == nil {
			continue
		}
		if other, ok := used[*column.value]; ok {
			add("%s and %s cannot use the same column", other, column.name)
		}
		used[*column.value] = column.name
	}
	if m.ActionColumn == nil && len(m.Actions) > 0 {
		add("Action interpretations need an action column")
	}
	if len(m.Actions) > maxActions {
		add("Map at most %d action values", maxActions)
	}
	seen := map[string]bool{}
	for _, rule := range m.Actions {
		key := normalizeAction(rule.Value)
		switch {
		case key == "" || len(rule.Value) > 200:
			add("Action values must be between 1 and 200 characters")
		case seen[key]:
			add("The action %q is mapped more than once", rule.Value)
		case !validActionKind(rule.Kind):
			add("Choose expense, income, transfer, refund, or skip for %q", rule.Value)
		}
		seen[key] = true
	}
	return problems
}

// Canonical returns the mapping as stable JSON for storage and comparison.
func (m *Mapping) Canonical() string {
	value := *m
	if value.Actions == nil {
		value.Actions = []ActionRule{}
	}
	if value.Ambiguities == nil {
		value.Ambiguities = []string{}
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// ParseMapping decodes stored or submitted mapping JSON strictly.
func ParseMapping(data []byte) (Mapping, error) {
	var mapping Mapping
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mapping); err != nil {
		return Mapping{}, userError("The mapping is not valid JSON for this contract")
	}
	return mapping, nil
}

func column(index int) *int { return &index }

// trading212Columns lists the verified Trading 212 card export columns.
// The conversion fee columns appear only in some exports.
var trading212Columns = []string{"Action", "Time (UTC)", "Notes", "ID", "Total", "Currency (Total)",
	"Currency conversion fee", "Currency (Currency conversion fee)", "Merchant name", "Merchant category"}

// Trading212 returns the built-in mapping when the header matches the
// Trading 212 export. Unfamiliar actions stay unmapped so their rows need
// an explicit interpretation.
func Trading212(header []string) (Mapping, bool) {
	positions := map[string]int{}
	for index, name := range header {
		if !slices.Contains(trading212Columns, name) {
			return Mapping{}, false
		}
		positions[name] = index
	}
	for _, required := range []string{"Action", "Time (UTC)", "ID", "Total", "Currency (Total)"} {
		if _, ok := positions[required]; !ok {
			return Mapping{}, false
		}
	}
	optional := func(name string) *int {
		if index, ok := positions[name]; ok {
			return column(index)
		}
		return nil
	}
	mapping := Mapping{
		Date: DateField{Column: positions["Time (UTC)"], Format: DateISO, Timezone: ZoneUTC},
		Amount: AmountField{Mode: AmountSingle, Column: column(positions["Total"]), Sign: NegativeIsOutflow,
			DecimalSeparator: ".", ThousandsSeparator: ""},
		Currency:          CurrencyField{Column: column(positions["Currency (Total)"])},
		ExternalIDColumn:  column(positions["ID"]),
		PayeeColumn:       optional("Merchant name"),
		CategoryColumn:    optional("Merchant category"),
		NotesColumn:       optional("Notes"),
		ActionColumn:      column(positions["Action"]),
		FeeAmountColumn:   optional("Currency conversion fee"),
		FeeCurrencyColumn: optional("Currency (Currency conversion fee)"),
		Actions: []ActionRule{
			{Value: "Card debit", Kind: ledger.KindExpense},
			{Value: "Deposit", Kind: ledger.KindTransfer},
			{Value: "Withdrawal", Kind: ledger.KindTransfer},
			{Value: "Spending cashback", Kind: KindSkip},
		},
		Ambiguities: []string{},
	}
	if mapping.FeeAmountColumn == nil {
		mapping.FeeCurrencyColumn = nil
	}
	return mapping, true
}
