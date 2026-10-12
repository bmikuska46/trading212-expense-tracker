package income

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/ledger"
)

// Cashback is one cashback credit from a Trading 212 transactions report.
type Cashback struct {
	ID       string
	Action   string
	At       time.Time
	Currency string
	Amount   int64
}

// ParseCashback reads the cashback rows of a Trading 212 transactions
// report, such as "Spending cashback", and skips every other row.
func ParseCashback(data []byte) ([]Cashback, error) {
	reader, err := imports.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	header := reader.Header()
	columns := map[string]int{}
	for _, name := range []string{"Action", "Time (UTC)", "ID", "Total", "Currency (Total)"} {
		index := slices.Index(header, name)
		if index < 0 && name == "Time (UTC)" {
			index = slices.Index(header, "Time")
		}
		if index < 0 {
			return nil, fmt.Errorf("the report has no %s column", name)
		}
		columns[name] = index
	}
	var credits []Cashback
	for row := 1; ; row++ {
		record, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return credits, nil
		}
		if err != nil {
			return nil, err
		}
		value := func(name string) string {
			if index := columns[name]; index < len(record) {
				return strings.TrimSpace(record[index])
			}
			return ""
		}
		action := value("Action")
		if !strings.Contains(strings.ToLower(action), "cashback") {
			continue
		}
		at, ok := imports.ParseUTC(value("Time (UTC)"))
		if !ok || value("ID") == "" {
			return nil, fmt.Errorf("row %d: the cashback has no time or ID", row)
		}
		amount, err := ledger.RoundMoney(value("Total"), value("Currency (Total)"))
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", row, err)
		}
		credits = append(credits, Cashback{ID: value("ID"), Action: ledger.DisplayName(action), At: at,
			Currency: amount.Currency(), Amount: amount.Minor()})
	}
}
