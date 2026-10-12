package httpserver

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

type accountView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Provider string `json:"provider"`
	Archived bool   `json:"archived"`
	// Balance is the sum of tracked activity since the account was added,
	// not the balance reported by a bank or broker.
	Balance          string `json:"balance"`
	BalanceEURCents  int64  `json:"balanceEurCents"`
	TransactionCount int64  `json:"transactionCount"`
	CreatedAt        string `json:"createdAt"`
}

func decimal(minor int64, currency string) (string, error) {
	money, err := ledger.NewMoney(minor, currency)
	if err != nil {
		return "", err
	}
	return money.Decimal()
}

func (a *api) listAccounts(w http.ResponseWriter, r *http.Request, _ *session) {
	rows, err := a.store.Queries.ListAccounts(r.Context())
	if err != nil {
		internalError(w, "list accounts")
		return
	}
	views := make([]accountView, 0, len(rows))
	for _, row := range rows {
		balance, err := decimal(row.BalanceMinor, row.Currency)
		if err != nil {
			internalError(w, "format balance")
			return
		}
		views = append(views, accountView{
			ID: row.ID, Name: row.Name, Currency: row.Currency, Provider: row.IntegrationProvider.String,
			Archived: row.Archived == 1, Balance: balance, BalanceEURCents: row.BalanceEurCents,
			TransactionCount: row.TransactionCount, CreatedAt: row.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string][]accountView{"accounts": views})
}

func (a *api) createAccount(w http.ResponseWriter, r *http.Request, _ *session) {
	var input struct {
		Name     string `json:"name"`
		Currency string `json:"currency"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	fields := map[string]string{}
	if name == "" || utf8.RuneCountInString(name) > 100 || !utf8.ValidString(name) {
		fields["name"] = "Enter a name of up to 100 characters"
	}
	if _, err := ledger.MinorUnits(currency); err != nil {
		fields["currency"] = "Choose a currency from the list"
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	account, err := a.store.Queries.CreateAccount(r.Context(), dbgen.CreateAccountParams{Name: name, Currency: currency})
	if err != nil {
		internalError(w, "create account")
		return
	}
	balance, err := decimal(0, account.Currency)
	if err != nil {
		internalError(w, "format balance")
		return
	}
	writeJSON(w, http.StatusCreated, accountView{
		ID: account.ID, Name: account.Name, Currency: account.Currency, Balance: balance,
		CreatedAt: account.CreatedAt,
	})
}
