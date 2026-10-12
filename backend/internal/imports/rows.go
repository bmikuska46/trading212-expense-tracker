package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/ledger"
	"budget-tracking/backend/internal/rates"
)

// Record is one interpreted CSV row. Amount is signed so outflows are
// negative, matching the ledger convention.
type Record struct {
	ExternalID    string `json:"externalId"`
	OccurredAt    string `json:"occurredAt"`
	Date          string `json:"date"`
	Kind          string `json:"kind"`
	Amount        string `json:"amount"`
	AmountMinor   int64  `json:"amountMinor"`
	Currency      string `json:"currency"`
	Payee         string `json:"payee"`
	Category      string `json:"category"`
	Notes         string `json:"notes"`
	Action        string `json:"action"`
	AmountText    string `json:"amountText"`
	TimestampText string `json:"timestampText"`
	FeeAmount     string `json:"feeAmount,omitempty"`
	FeeCurrency   string `json:"feeCurrency,omitempty"`
	// Conversion is set on valid foreign rows; EUR rows use rate 1.
	Conversion *rates.Conversion `json:"conversion,omitempty"`
}

const (
	maxIDLength    = 200
	maxNotesLength = 1000
	// generatedPrefix marks IDs derived from row content for files without
	// an ID column.
	generatedPrefix = "row:"
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Interpretation is the outcome of applying a mapping to one row.
type Interpretation struct {
	Record  Record
	Errors  []string
	Unknown string // action value without an interpretation
}

func (i *Interpretation) fail(format string, args ...any) {
	i.Errors = append(i.Errors, fmt.Sprintf(format, args...))
}

func cell(values []string, index *int) string {
	if index == nil || *index >= len(values) {
		return ""
	}
	return strings.TrimSpace(values[*index])
}

// Interpret applies the mapping to one row. Skipped actions return early so
// rows the owner never wants are not validated further.
func (m *Mapping) Interpret(values []string, location *time.Location) Interpretation {
	var result Interpretation
	record := &result.Record
	record.Action = cell(values, m.ActionColumn)
	if m.ActionColumn != nil {
		key := normalizeAction(record.Action)
		for _, rule := range m.Actions {
			if normalizeAction(rule.Value) == key {
				record.Kind = rule.Kind
				break
			}
		}
		if record.Kind == KindSkip {
			record.ExternalID = cell(values, m.ExternalIDColumn)
			return result
		}
		if record.Kind == "" {
			result.Unknown = record.Action
			if record.Action == "" {
				result.fail("The action is empty")
			} else {
				result.fail("The action %q has no interpretation; map it in the mapping", truncate(record.Action, 60))
			}
		}
	}

	date := m.Date.Column
	record.TimestampText = cell(values, &date)
	instant, dateOnly, err := parseTime(record.TimestampText, m.Date.Format, m.Date.Timezone, location)
	if err != nil {
		result.fail("%s", err)
	} else {
		if dateOnly {
			instant = time.Date(instant.Year(), instant.Month(), instant.Day(), 0, 0, 0, 0, location)
		}
		record.OccurredAt = auth.FormatTime(instant)
		record.Date = instant.In(location).Format(time.DateOnly)
	}

	record.Currency = strings.ToUpper(cell(values, m.Currency.Column))
	if m.Currency.Fixed != nil {
		record.Currency = *m.Currency.Fixed
	}
	switch {
	case record.Currency == "":
		result.fail("The currency is empty")
	default:
		if _, err := ledger.MinorUnits(record.Currency); err != nil {
			result.fail("The currency is not a supported ISO 4217 code")
		}
	}

	m.interpretAmount(values, &result)

	if record.Kind == "" && m.ActionColumn == nil && record.AmountMinor != 0 {
		record.Kind = ledger.KindIncome
		if record.AmountMinor < 0 {
			record.Kind = ledger.KindExpense
		}
	}
	if record.Kind != "" && record.AmountMinor != 0 && !ledger.SignMatchesKind(record.Kind, record.AmountMinor) {
		direction := "an inflow"
		if record.Kind == ledger.KindExpense {
			direction = "an outflow"
		}
		result.fail("A %s row must be %s", record.Kind, direction)
	}

	if m.ExternalIDColumn != nil {
		record.ExternalID = cell(values, m.ExternalIDColumn)
		switch {
		case record.ExternalID == "":
			result.fail("The ID is empty")
		case len(record.ExternalID) > maxIDLength:
			result.fail("The ID is longer than %d characters", maxIDLength)
		}
	}

	record.Payee = cell(values, m.PayeeColumn)
	record.Category = cell(values, m.CategoryColumn)
	record.Notes = cell(values, m.NotesColumn)
	if record.Notes == "" {
		record.Notes = cell(values, m.NotesFallbackColumn)
	}
	if utf8.RuneCountInString(record.Notes) > maxNotesLength {
		result.fail("Notes are longer than %d characters", maxNotesLength)
	}

	if fee := cell(values, m.FeeAmountColumn); fee != "" {
		record.FeeAmount = fee
		record.FeeCurrency = strings.ToUpper(cell(values, m.FeeCurrencyColumn))
		if record.FeeCurrency == "" {
			record.FeeCurrency = record.Currency
		}
		if !currencyCode.MatchString(record.FeeCurrency) {
			result.fail("The fee currency is not a three-letter code")
		}
	}
	return result
}

func (m *Mapping) interpretAmount(values []string, result *Interpretation) {
	record := &result.Record
	if _, err := ledger.MinorUnits(record.Currency); err != nil {
		return
	}
	if m.Amount.Mode == AmountSingle {
		record.AmountText = cell(values, m.Amount.Column)
		if record.AmountText == "" {
			result.fail("The amount is empty")
			return
		}
		minor, err := m.parseAmount(record.AmountText, record.Currency)
		if err != nil {
			result.fail("The amount %s", err)
			return
		}
		if m.Amount.Sign == PositiveIsOutflow {
			minor = -minor
		}
		if m.Amount.Sign == ActionIsDirection {
			if minor < 0 {
				result.fail("The amount must be unsigned when the transaction type determines its direction")
				return
			}
			switch record.Kind {
			case ledger.KindExpense:
				minor = -minor
			case ledger.KindIncome, ledger.KindRefund:
			default:
				result.fail("The transaction type does not determine the amount direction")
				return
			}
		}
		record.AmountMinor = minor
	} else {
		debitText, creditText := cell(values, m.Amount.DebitColumn), cell(values, m.Amount.CreditColumn)
		var debit, credit int64
		var err error
		if debitText != "" {
			if debit, err = m.parseAmount(debitText, record.Currency); err != nil {
				result.fail("The debit amount %s", err)
				return
			}
		}
		if creditText != "" {
			if credit, err = m.parseAmount(creditText, record.Currency); err != nil {
				result.fail("The credit amount %s", err)
				return
			}
		}
		// Debit and credit columns hold magnitudes; their sign is ignored.
		debit, credit = absolute(debit), absolute(credit)
		switch {
		case debit != 0 && credit != 0:
			result.fail("Both the debit and credit amounts are filled")
			return
		case debit != 0:
			record.AmountText, record.AmountMinor = debitText, -debit
		default:
			record.AmountText, record.AmountMinor = creditText, credit
		}
	}
	if record.AmountMinor == 0 {
		result.fail("The amount is zero")
		return
	}
	amount, err := ledger.NewMoney(record.AmountMinor, record.Currency)
	if err == nil {
		record.Amount, err = amount.Decimal()
	}
	if err != nil {
		result.fail("The amount cannot be stored")
	}
}

// parseAmount checks the mapped digit grouping, removes the thousands
// separator, reads the decimal separator, and rejects precision below the
// currency's minor unit. Grouping is checked so "1.50" is never read as 150
// when the point separates thousands.
func (m *Mapping) parseAmount(text, currency string) (int64, error) {
	value := strings.NewReplacer("\u00a0", " ", "\u202f", " ").Replace(text)
	decimal, thousands := m.Amount.DecimalSeparator, m.Amount.ThousandsSeparator
	if decimal == "," && strings.Contains(value, ".") && thousands != "." {
		return 0, fmt.Errorf("uses a point, but the mapping expects a decimal comma")
	}
	if decimal == "." && strings.Contains(value, ",") && thousands != "," {
		return 0, fmt.Errorf("uses a comma, but the mapping expects a decimal point")
	}
	whole, fraction, hasFraction := strings.Cut(value, decimal)
	if thousands != "" && strings.Contains(whole, thousands) {
		groups := strings.Split(strings.TrimLeft(whole, "+-"), thousands)
		for index, group := range groups {
			if (index == 0 && (len(group) < 1 || len(group) > 3)) || (index > 0 && len(group) != 3) {
				return 0, fmt.Errorf("does not group thousands the way the mapping expects")
			}
		}
		whole = strings.ReplaceAll(whole, thousands, "")
	}
	value = whole
	if hasFraction {
		value += "." + fraction
	}
	money, err := ledger.ParseMoney(value, currency)
	if err != nil {
		return 0, fmt.Errorf("is not a number in the mapped format")
	}
	return money.Minor(), nil
}

func absolute(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "..."
}

var (
	isoLayouts = []string{
		time.RFC3339, "2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05Z0700",
		"2006-01-02 15:04:05 Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02 15:04Z07:00",
	}
	isoLocalLayouts = []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"}
)

// parseTime reads a timestamp in the mapped order. Values with an offset
// keep it; others use UTC or the owner's timezone. dateOnly reports values
// without a time of day.
func parseTime(text, format, zone string, location *time.Location) (time.Time, bool, error) {
	if text == "" {
		return time.Time{}, false, fmt.Errorf("The date is empty")
	}
	assumed := time.UTC
	if zone == ZoneLocal {
		assumed = location
	}
	valid := func(value time.Time) bool { return value.Year() >= 2000 && value.Year() <= 2100 }
	if format == DateISO {
		for _, layout := range isoLayouts {
			if value, err := time.Parse(layout, text); err == nil && valid(value) {
				return value, false, nil
			}
		}
		for _, layout := range isoLocalLayouts {
			if value, err := time.ParseInLocation(layout, text, assumed); err == nil && valid(value) {
				return value, false, nil
			}
		}
		if value, err := time.ParseInLocation(time.DateOnly, text, location); err == nil && valid(value) {
			return value, true, nil
		}
		return time.Time{}, false, fmt.Errorf("The date is not a year-first date between 2000 and 2100")
	}
	for _, separator := range []string{".", "/", "-"} {
		day := "2" + separator + "1" + separator + "2006"
		if format == DateMDY {
			day = "1" + separator + "2" + separator + "2006"
		}
		for _, suffix := range []string{" 15:04:05", " 15:04"} {
			if value, err := time.ParseInLocation(day+suffix, text, assumed); err == nil && valid(value) {
				return value, false, nil
			}
		}
		if value, err := time.ParseInLocation(day, text, location); err == nil && valid(value) {
			return value, true, nil
		}
	}
	order := "day, month, year"
	if format == DateMDY {
		order = "month, day, year"
	}
	return time.Time{}, false, fmt.Errorf("The date is not in %s order between 2000 and 2100", order)
}

// generatedID derives a stable ID from the row content. The occurrence
// suffix keeps identical rows in one file apart, so two equal purchases on
// the same day both import.
func generatedID(values []string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return generatedPrefix + hex.EncodeToString(sum[:12])
}

// ParseUTC reads a year-first timestamp written in UTC, the format of the
// Trading 212 time column. Dates without a time are rejected.
func ParseUTC(text string) (time.Time, bool) {
	value, dateOnly, err := parseTime(strings.TrimSpace(text), DateISO, ZoneUTC, time.UTC)
	return value, err == nil && !dateOnly
}
