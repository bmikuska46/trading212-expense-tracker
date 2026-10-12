package imports

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func readAll(t *testing.T, text string) (*Reader, [][]string, error) {
	t.Helper()
	reader, err := NewReader(strings.NewReader(text))
	if err != nil {
		return nil, nil, err
	}
	var records [][]string
	for {
		record, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return reader, records, nil
		}
		if err != nil {
			return reader, records, err
		}
		records = append(records, record)
	}
}

func TestReaderHandlesQuotingDelimitersAndBOM(t *testing.T) {
	reader, records, err := readAll(t, "\ufeffDate;\"Note; with\"\"quote\";Amount\r\n2026-08-01;\"multi\r\nline; text\";-1,50\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reader.Header(), []string{"Date", `Note; with"quote`, "Amount"}) {
		t.Fatalf("header: %q", reader.Header())
	}
	if len(records) != 1 || records[0][1] != "multi\nline; text" || records[0][2] != "-1,50" {
		t.Fatalf("records: %q", records)
	}
	comma, _, _ := readAll(t, "Date,Note,Amount\n")
	if comma.Fingerprint() == reader.Fingerprint() {
		t.Fatal("the delimiter must be part of the format fingerprint")
	}
	spaced, _, _ := readAll(t, "date , NOTE,amount\n")
	if spaced.Fingerprint() != comma.Fingerprint() {
		t.Fatal("the fingerprint must ignore case and surrounding spaces")
	}

	for name, text := range map[string]string{
		"empty":       "",
		"one column":  "Only\n1\n",
		"bare quote":  "A,B\n1,\"x\"y\n",
		"not utf8":    "A,B\n1,\xff\n",
		"too many":    strings.Repeat("c,", 70) + "c\n",
		"unterminate": "A,B\n\"1,2\n",
	} {
		_, _, err := readAll(t, text)
		var known *Error
		if !errors.As(err, &known) || known.Kind != Invalid {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "\xff") {
			t.Errorf("%s: an error must not echo values", name)
		}
	}
}

func TestExamplesSkipBlankAndShortRows(t *testing.T) {
	records := [][]string{{"", "Example Shop"}, {" 2026-09-01 ", "", "-9.84"}}
	if got := Examples([]string{"Date", "Payee", "Amount", "Notes"}, records); !slices.Equal(got, []string{"2026-09-01", "Example Shop", "-9.84", ""}) {
		t.Fatalf("examples: %q", got)
	}
}

func TestReaderKeepsTheFirstRecordOfAFileWithoutHeader(t *testing.T) {
	reader, records, err := readAll(t, "Owner,2026-09-01,Example Shop,MISCELLANEOUS,-9.84,OUT\nOwner,2026-09-02,Example Cafe,RESTAURANTS,-6.70,OUT\n")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reader.Header(), []string{"Column 1", "Column 2", "Column 3", "Column 4", "Column 5", "Column 6"}) {
		t.Fatalf("header: %q", reader.Header())
	}
	if len(records) != 2 || records[0][2] != "Example Shop" || records[1][4] != "-6.70" {
		t.Fatalf("records: %q", records)
	}
	dayFirst, records, _ := readAll(t, "31.08.2026;Example;12,40\n")
	if dayFirst.Header()[0] != "Column 1" || len(records) != 1 {
		t.Fatalf("a day-first date marks a record: %q %q", dayFirst.Header(), records)
	}
	for _, text := range []string{"Date,Amount\n2026-09-01,1.00\n", "Action,Time (UTC),Total\nDeposit,2026-09-01 10:00:00,5.00\n", "Year 2026,Total\n1,2\n"} {
		named, _, _ := readAll(t, text)
		if named.Header()[0] == "Column 1" {
			t.Errorf("a header of names is not a record: %q", text)
		}
	}
	if _, _, err := readAll(t, "2026-09-01,\xff\n"); err == nil {
		t.Fatal("a first record must be UTF-8")
	}
}

func baseMapping() Mapping {
	return Mapping{
		Date:        DateField{Column: 0, Format: DateDMY, Timezone: ZoneLocal},
		Amount:      AmountField{Mode: AmountSingle, Column: column(1), Sign: NegativeIsOutflow, DecimalSeparator: ",", ThousandsSeparator: "."},
		Currency:    CurrencyField{Fixed: new("EUR")},
		PayeeColumn: column(2), ExternalIDColumn: column(3),
	}
}

func TestMappingValidationRejectsUnsafeInterpretations(t *testing.T) {
	header := []string{"Date", "Amount", "Payee", "ID"}
	valid := baseMapping()
	if problems := valid.Validate(header); len(problems) != 0 {
		t.Fatalf("valid mapping: %v", problems)
	}
	for name, change := range map[string]func(*Mapping){
		"ambiguous":          func(m *Mapping) { m.Ambiguities = []string{"The sign is unclear"} },
		"out of range":       func(m *Mapping) { m.PayeeColumn = column(9) },
		"bad fallback":       func(m *Mapping) { m.NotesFallbackColumn = column(9) },
		"shared column":      func(m *Mapping) { m.ExternalIDColumn = column(1) },
		"two currencies":     func(m *Mapping) { m.Currency.Column = column(2) },
		"no currency":        func(m *Mapping) { m.Currency.Fixed = nil },
		"unknown currency":   func(m *Mapping) { m.Currency.Fixed = new("XYZ") },
		"same separators":    func(m *Mapping) { m.Amount.ThousandsSeparator = "," },
		"no sign":            func(m *Mapping) { m.Amount.Sign = "" },
		"debit and single":   func(m *Mapping) { m.Amount.DebitColumn = column(2) },
		"bad date order":     func(m *Mapping) { m.Date.Format = "ymd" },
		"actions no column":  func(m *Mapping) { m.Actions = []ActionRule{{Value: "Card debit", Kind: "expense"}} },
		"fee currency alone": func(m *Mapping) { m.FeeCurrencyColumn = column(2) },
		"duplicate action": func(m *Mapping) {
			m.ActionColumn = column(2)
			m.PayeeColumn = nil
			m.Actions = []ActionRule{{Value: "Card debit", Kind: "expense"}, {Value: "card  DEBIT", Kind: "refund"}}
		},
		"bad kind": func(m *Mapping) {
			m.ActionColumn = column(2)
			m.PayeeColumn = nil
			m.Actions = []ActionRule{{Value: "Card debit", Kind: "cashback"}}
		},
	} {
		mapping := baseMapping()
		change(&mapping)
		if problems := mapping.Validate(header); len(problems) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseMapping([]byte(`{"date":{"column":0},"surprise":true}`)); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
}

func TestInterpretRows(t *testing.T) {
	location, _ := time.LoadLocation("Europe/Bratislava")
	mapping := baseMapping()
	result := mapping.Interpret([]string{"31.12.2026 23:30", "-1.234,50", " Example Store ", "x-1"}, location)
	record := result.Record
	if len(result.Errors) != 0 || record.AmountMinor != -123450 || record.Amount != "-1234.50" || record.Kind != "expense" ||
		record.OccurredAt != "2026-12-31T22:30:00Z" || record.Date != "2026-12-31" || record.Payee != "Example Store" {
		t.Fatalf("record: %+v %v", record, result.Errors)
	}
	if income := mapping.Interpret([]string{"1.8.2026", "10,00", "", "x-2"}, location).Record; income.Kind != "income" ||
		income.OccurredAt != "2026-07-31T22:00:00Z" || income.Date != "2026-08-01" {
		t.Fatalf("date-only income: %+v", income)
	}
	for name, values := range map[string][]string{
		"zero":           {"1.8.2026", "0,00", "", "x"},
		"point decimal":  {"1.8.2026", "1.50", "", "x"},
		"too precise":    {"1.8.2026", "1,505", "", "x"},
		"bad date":       {"31.2.2026", "1,00", "", "x"},
		"old date":       {"1.8.1999", "1,00", "", "x"},
		"missing ID":     {"1.8.2026", "1,00", "", ""},
		"empty amount":   {"1.8.2026", "", "", "x"},
		"text as amount": {"1.8.2026", "abc", "", "x"},
	} {
		if result := mapping.Interpret(values, location); len(result.Errors) == 0 {
			t.Errorf("%s: accepted %+v", name, result.Record)
		}
	}

	split := baseMapping()
	split.Amount = AmountField{Mode: AmountDebitCredit, DebitColumn: column(1), CreditColumn: column(4), Sign: NegativeIsOutflow, DecimalSeparator: ","}
	if record := split.Interpret([]string{"1.8.2026", "5,00", "", "x", ""}, location).Record; record.AmountMinor != -500 {
		t.Fatalf("debit: %+v", record)
	}
	if result := split.Interpret([]string{"1.8.2026", "5,00", "", "x", "1,00"}, location); len(result.Errors) == 0 {
		t.Fatal("a row with both debit and credit must be rejected")
	}
}

func TestUnsignedAmountsUseTransactionType(t *testing.T) {
	mapping := baseMapping()
	mapping.ExternalIDColumn = nil
	mapping.ActionColumn = column(3)
	mapping.Amount.Sign = ActionIsDirection
	mapping.Actions = []ActionRule{{Value: "Debet", Kind: "expense"}, {Value: "Kredit", Kind: "income"},
		{Value: "Refund", Kind: "refund"}, {Value: "Ignored", Kind: KindSkip}}
	if problems := mapping.Validate([]string{"Date", "Amount", "Payee", "Type"}); len(problems) != 0 {
		t.Fatalf("mapping: %v", problems)
	}
	for _, test := range []struct {
		action string
		minor  int64
	}{
		{"Debet", -45}, {"Kredit", 45}, {" kredit ", 45}, {"Refund", 45},
	} {
		result := mapping.Interpret([]string{"01.09.2026", "0,45", "Example Shop", test.action}, time.UTC)
		if len(result.Errors) != 0 || result.Record.AmountMinor != test.minor {
			t.Errorf("%s: amount = %d, errors = %v", test.action, result.Record.AmountMinor, result.Errors)
		}
	}
	for _, row := range [][]string{
		{"01.09.2026", "0,45", "", "Unknown"},
		{"01.09.2026", "0,45", "", ""},
		{"01.09.2026", "-0,45", "", "Debet"},
		{"01.09.2026", "0,00", "", "Kredit"},
	} {
		if result := mapping.Interpret(row, time.UTC); len(result.Errors) == 0 {
			t.Errorf("invalid unsigned row accepted: %+v", result.Record)
		}
	}
	if skipped := mapping.Interpret([]string{"", "", "", "Ignored"}, time.UTC); len(skipped.Errors) != 0 || skipped.Record.Kind != KindSkip {
		t.Fatalf("skipped row: %+v", skipped)
	}
	for _, change := range []func(*Mapping){
		func(m *Mapping) { m.ActionColumn = nil },
		func(m *Mapping) { m.Actions = nil },
		func(m *Mapping) { m.Actions = []ActionRule{{Value: "Transfer", Kind: "transfer"}} },
	} {
		invalid := mapping
		change(&invalid)
		if len(invalid.Validate([]string{"Date", "Amount", "Payee", "Type"})) == 0 {
			t.Fatal("an unsigned mapping without a known direction must be rejected")
		}
	}
}

func TestSlovakBankMapping(t *testing.T) {
	mapping, ok := SlovakBank(slovakBankColumns)
	if !ok || len(mapping.Validate(slovakBankColumns)) != 0 {
		t.Fatalf("mapping: %v %v", ok, mapping.Validate(slovakBankColumns))
	}
	if mapping.ExternalIDColumn != nil || mapping.PayeeColumn != nil || mapping.CategoryColumn != nil {
		t.Fatal("payment references, card details, and bank descriptions must not be mistaken for IDs, payees, or categories")
	}
	for _, header := range [][]string{
		slovakBankColumns[:5],
		append(slices.Clone(slovakBankColumns), "Extra"),
		append([]string{"Unknown"}, slovakBankColumns[1:]...),
	} {
		if _, matched := SlovakBank(header); matched {
			t.Fatal("an unfamiliar layout must require a mapping")
		}
	}
}

func TestNotesFallback(t *testing.T) {
	mapping := baseMapping()
	mapping.ExternalIDColumn = nil
	mapping.NotesColumn, mapping.NotesFallbackColumn = column(2), column(3)
	for _, test := range []struct {
		primary, fallback, want string
	}{
		{" Card purchase ", "Bank description", "Card purchase"},
		{"  ", " Bank description ", "Bank description"},
		{"", "", ""},
	} {
		result := mapping.Interpret([]string{"01.09.2026", "-0,45", test.primary, test.fallback}, time.UTC)
		if len(result.Errors) != 0 || result.Record.Notes != test.want {
			t.Fatalf("notes = %q, want %q, errors = %v", result.Record.Notes, test.want, result.Errors)
		}
	}
	if result := mapping.Interpret([]string{"01.09.2026", "-0,45", "", strings.Repeat("x", maxNotesLength+1)}, time.UTC); len(result.Errors) == 0 {
		t.Fatal("fallback notes must obey the notes length limit")
	}
}

func TestTrading212Mapping(t *testing.T) {
	header := []string{"Action", "Time (UTC)", "Notes", "ID", "Total", "Currency (Total)", "Currency conversion fee",
		"Currency (Currency conversion fee)", "Merchant name", "Merchant category"}
	mapping, ok := Trading212(header)
	if !ok || len(mapping.Validate(header)) != 0 {
		t.Fatalf("trading 212: %v %v", ok, mapping.Validate(header))
	}
	if _, ok := Trading212(append(slices.Clone(header), "Extra")); ok {
		t.Fatal("an unknown column must not match the built-in mapping")
	}
	if _, ok := Trading212(header[:4]); ok {
		t.Fatal("missing required columns must not match")
	}
	values := []string{"Card debit", "2026-08-02 09:14:22", "", "id-1", "-8.10", "EUR", "0.02", "EUR", "Store", "RETAIL_STORES"}
	record := mapping.Interpret(values, time.UTC).Record
	if record.AmountMinor != -810 || record.FeeAmount != "0.02" || record.FeeCurrency != "EUR" || record.Category != "RETAIL_STORES" {
		t.Fatalf("fee must stay separate from the total: %+v", record)
	}
	values[0] = "Spending cashback"
	if skipped := mapping.Interpret(values, time.UTC); skipped.Record.Kind != KindSkip || len(skipped.Errors) != 0 || skipped.Record.Payee != "" {
		t.Fatalf("cashback: %+v", skipped)
	}
	values[0] = "Card credit"
	if unknown := mapping.Interpret(values, time.UTC); unknown.Unknown != "Card credit" || len(unknown.Errors) == 0 {
		t.Fatalf("unknown action: %+v", unknown)
	}
	values[0], values[4] = "Card debit", "8.10"
	if wrongSign := mapping.Interpret(values, time.UTC); len(wrongSign.Errors) == 0 {
		t.Fatal("a card debit inflow must be rejected")
	}
}
