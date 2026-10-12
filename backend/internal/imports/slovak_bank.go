package imports

import (
	"slices"

	"budget-tracking/backend/internal/ledger"
)

var slovakBankColumns = []string{
	"Dátum spracovania", "Dátum zúčtovania", "Suma", "Mena", "Typ", "Predčíslie", "Číslo účtu", "Kód banky", "IBAN",
	"Variabilný symbol", "Špecifický symbol", "Konštantný symbol", "Referencia platiteľa", "Informácia pre príjemcu", "Popis",
}

// SlovakBank recognizes the verified export layout. Amounts are unsigned,
// and Debet/Kredit determines their direction. Payment symbols identify
// purposes, not individual transactions, so IDs are generated from rows.
func SlovakBank(header []string) (Mapping, bool) {
	if !slices.Equal(header, slovakBankColumns) {
		return Mapping{}, false
	}
	return Mapping{
		Date: DateField{Column: 1, Format: DateDMY, Timezone: ZoneLocal},
		Amount: AmountField{Mode: AmountSingle, Column: column(2), Sign: ActionIsDirection,
			DecimalSeparator: ",", ThousandsSeparator: " "},
		Currency:            CurrencyField{Column: column(3)},
		NotesColumn:         column(13),
		NotesFallbackColumn: column(14),
		ActionColumn:        column(4),
		Actions: []ActionRule{
			{Value: "Debet", Kind: ledger.KindExpense},
			{Value: "Kredit", Kind: ledger.KindIncome},
		},
		Ambiguities: []string{},
	}, true
}
