package imports

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"budget-tracking/backend/internal/integrations"
)

// fakeJev answers each question with its configured option, or "none" when
// the question offers it, and records the states it was asked about.
type fakeJev struct {
	choices map[string]string
	// probabilities override the ranked options of a choice.
	probabilities map[string]map[string]float64
	confidence    map[string]float64
	nouls         map[string]float64
	states        []string
}

func (f *fakeJev) ask(_ context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error) {
	encoded, _ := json.Marshal(state)
	f.states = append(f.states, string(encoded))
	answers := map[string]integrations.Answer{}
	for id, question := range questions {
		if question.Type == integrations.QuestionNoul {
			answers[id] = integrations.Answer{Type: question.Type, Noul: f.nouls[id]}
			continue
		}
		picked, ok := f.choices[id]
		if !ok {
			picked = noColumn
		}
		probabilities := f.probabilities[id]
		if probabilities == nil {
			probabilities = map[string]float64{picked: 1}
		}
		confidence, ok := f.confidence[id]
		if !ok {
			confidence = 0.95
		}
		answers[id] = integrations.Answer{Type: question.Type, Choice: picked, Probabilities: probabilities, Confidence: confidence}
	}
	return answers, nil
}

var bankHeader = []string{"Booked", "Counterparty", "Debit", "Credit", "Currency", "Reference"}

var bankRecords = [][]string{
	{"01.08.2026", "Example Shop", "12,40", "", "EUR", "r-1"},
	{"02.08.2026", "Example Employer", "", "1 500,00", "EUR", "r-2"},
	{"03.08.2026", "Example Cafe", "3,10", "", "EUR", "r-3"},
	{"04.08.2026", "MARKER-ROW-FOUR", "1,00", "", "EUR", "r-4"},
}

func bankJev() *fakeJev {
	return &fakeJev{choices: map[string]string{
		"date": "column 1", "amount_mode": AmountDebitCredit, "debit": "column 3", "credit": "column 4",
		"currency": "column 5", "payee": "column 2", "external_id": "column 6",
		"date_format": DateDMY, "decimal": "comma", "thousands": "space",
	}, nouls: map[string]float64{"utc": 0.1}}
}

func TestSuggestMapsABankFile(t *testing.T) {
	jev := bankJev()
	mapping, notes, err := Suggest(t.Context(), jev.ask, bankHeader, bankRecords, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if problems := mapping.Validate(bankHeader); len(problems) != 0 {
		t.Fatalf("mapping: %v", problems)
	}
	if mapping.Amount.Mode != AmountDebitCredit || *mapping.Amount.DebitColumn != 2 || *mapping.Amount.CreditColumn != 3 ||
		mapping.Amount.Column != nil || mapping.Amount.DecimalSeparator != "," || mapping.Amount.ThousandsSeparator != " " ||
		mapping.Date.Format != DateDMY || mapping.Date.Timezone != ZoneLocal || *mapping.Currency.Column != 4 ||
		*mapping.PayeeColumn != 1 || *mapping.ExternalIDColumn != 5 || mapping.CategoryColumn != nil {
		t.Fatalf("mapping: %+v", mapping)
	}
	if record := mapping.Interpret(bankRecords[1], time.UTC).Record; record.AmountMinor != 150000 {
		t.Fatalf("credit: %+v", record)
	}
	// Days up to 4 cannot tell day-first from month-first.
	if !slices.Contains(notes, "The sample does not show whether dates are day-first or month-first. Check the date order.") || len(notes) != 1 {
		t.Fatalf("notes: %q", notes)
	}
	if len(jev.states) != 2 {
		t.Fatalf("requests: %d", len(jev.states))
	}
	for _, state := range jev.states {
		if strings.Contains(state, "MARKER-ROW-FOUR") {
			t.Fatalf("a record past the sample was sent: %s", state)
		}
	}
	if strings.Contains(jev.states[1], "r-1") || !strings.Contains(jev.states[1], "Example Shop") {
		t.Fatalf("the second request must hold only the picked columns it reads: %s", jev.states[1])
	}
}

func TestSuggestReadsUnsignedAmountsFromTransactionTypes(t *testing.T) {
	header := []string{"Date", "Amount", "Currency", "Type"}
	records := [][]string{{"15.09.2026", "12,40", "EUR", "Debet"}, {"16.09.2026", "1500,00", "EUR", "Kredit"}}
	jev := &fakeJev{choices: map[string]string{
		"date": "column 1", "amount_mode": AmountSingle, "amount": "column 2", "currency": "column 3", "action": "column 4",
		"date_format": DateDMY, "sign": ActionIsDirection, "decimal": "comma", "thousands": "none",
		"action 1": "expense", "action 2": "income",
	}}
	mapping, notes, err := Suggest(t.Context(), jev.ask, header, records, "EUR")
	if err != nil || len(notes) != 0 || len(mapping.Validate(header)) != 0 || mapping.Amount.Sign != ActionIsDirection {
		t.Fatalf("unsigned suggestion: %v %v %v", err, notes, mapping.Validate(header))
	}
	for index, expected := range []int64{-1240, 150000} {
		result := mapping.Interpret(records[index], time.UTC)
		if len(result.Errors) != 0 || result.Record.AmountMinor != expected {
			t.Fatalf("row %d: amount = %d, errors = %v", index+1, result.Record.AmountMinor, result.Errors)
		}
	}
}

func TestSuggestSettlesSharedColumnsAndNotesDoubts(t *testing.T) {
	jev := bankJev()
	// The ID role likes the date column too, then its own.
	jev.choices["external_id"] = "column 1"
	jev.probabilities = map[string]map[string]float64{"external_id": {"column 1": 0.6, "column 6": 0.3, noColumn: 0.1}}
	jev.confidence = map[string]float64{"payee": 0.3, "decimal": 0.2}
	jev.nouls["utc"] = 0.6
	mapping, notes, err := Suggest(t.Context(), jev.ask, bankHeader, bankRecords, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if *mapping.ExternalIDColumn != 5 || mapping.Date.Timezone != ZoneUTC {
		t.Fatalf("mapping: %+v", mapping)
	}
	for _, note := range []string{"Check the transaction ID column.", "Check the payee column.", "Check the decimal separator.",
		"Check how times without an offset are read."} {
		if !slices.Contains(notes, note) {
			t.Errorf("missing note %q in %q", note, notes)
		}
	}

	// A required role keeps its column, and validation reports the share.
	jev = bankJev()
	jev.choices["debit"] = "column 1"
	mapping, _, err = Suggest(t.Context(), jev.ask, bankHeader, bankRecords, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if problems := mapping.Validate(bankHeader); len(problems) == 0 {
		t.Fatal("a shared date and debit column must not validate")
	}
}

func TestSuggestFixesTheCurrencyAndReadsActions(t *testing.T) {
	header := []string{"Owner", "Date", "Payee", "Amount (USD)", "Type"}
	records := [][]string{
		{"Owner", "2026-09-01", "Example Shop", "-9.84", "OUT"},
		{"Owner", "2026-09-02", "Example Employer", "1500.00", "IN"},
		{"Owner", "2026-09-03", "Example Cafe", "-6.70", "out"},
	}
	jev := &fakeJev{choices: map[string]string{
		"date": "column 2", "amount_mode": AmountSingle, "amount": "column 4", "payee": "column 3", "action": "column 5",
		"date_format": DateISO, "sign": NegativeIsOutflow, "decimal": "point", "thousands": "point",
		"action 1": "expense", "action 2": "income",
	}}
	mapping, notes, err := Suggest(t.Context(), jev.ask, header, records, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if *mapping.Currency.Fixed != "USD" || mapping.Amount.ThousandsSeparator != "" ||
		!slices.Equal(mapping.Actions, []ActionRule{{Value: "OUT", Kind: "expense"}, {Value: "IN", Kind: "income"}}) {
		t.Fatalf("mapping: %+v", mapping)
	}
	if !slices.Equal(notes, []string{"Check the thousands separator."}) {
		t.Fatalf("notes: %q", notes)
	}
	if problems := mapping.Validate(header); len(problems) != 0 {
		t.Fatalf("mapping: %v", problems)
	}
}

func TestSuggestRejectsAnswersOutsideTheQuestion(t *testing.T) {
	for name, change := range map[string]func(*fakeJev){
		"unknown column": func(jev *fakeJev) { jev.choices["payee"] = "column 9" },
		"unknown option": func(jev *fakeJev) { jev.choices["decimal"] = "semicolon" },
		"no date":        func(jev *fakeJev) { jev.choices["date"] = noColumn },
	} {
		jev := bankJev()
		change(jev)
		_, _, err := Suggest(t.Context(), jev.ask, bankHeader, bankRecords, "EUR")
		var suggestError *integrations.SuggestError
		if !errors.As(err, &suggestError) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
