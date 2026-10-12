package httpserver

import (
	"net/http"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/income"
	"budget-tracking/backend/internal/ledger"
)

func (a *api) getSettings(w http.ResponseWriter, r *http.Request, current *session) {
	writeJSON(w, http.StatusOK, current.view())
}

func (a *api) updateSettings(w http.ResponseWriter, r *http.Request, current *session) {
	var input struct {
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !auth.ValidTimezone(input.Timezone) {
		writeFieldErrors(w, map[string]string{"timezone": "Choose a timezone from the list"})
		return
	}
	if err := a.store.Queries.UpdateOwnerTimezone(r.Context(), input.Timezone); err != nil {
		internalError(w, "update timezone")
		return
	}
	current.timezone = input.Timezone
	writeJSON(w, http.StatusOK, current.view())
}

func taxPercentMap(row dbgen.GetTaxPercentsRow) map[string]int64 {
	return map[string]int64{income.KindDividend: row.DividendTaxPercent, income.KindInterest: row.InterestTaxPercent,
		income.KindCashback: row.CashbackTaxPercent}
}

// getTaxSettings returns the percentage the income page taxes each kind at.
func (a *api) getTaxSettings(w http.ResponseWriter, r *http.Request, _ *session) {
	row, err := a.store.Queries.GetTaxPercents(r.Context())
	if err != nil {
		internalError(w, "load tax settings")
		return
	}
	writeJSON(w, http.StatusOK, taxPercentMap(row))
}

func (a *api) updateTaxSettings(w http.ResponseWriter, r *http.Request, _ *session) {
	var input struct {
		Dividend *int64 `json:"dividend"`
		Interest *int64 `json:"interest"`
		Cashback *int64 `json:"cashback"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	fields := map[string]string{}
	for kind, value := range map[string]*int64{income.KindDividend: input.Dividend, income.KindInterest: input.Interest,
		income.KindCashback: input.Cashback} {
		if value == nil || *value < 0 || *value > 100 {
			fields[kind] = "Enter a whole percentage from 0 to 100"
		}
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	params := dbgen.UpdateTaxPercentsParams{DividendTaxPercent: *input.Dividend, InterestTaxPercent: *input.Interest,
		CashbackTaxPercent: *input.Cashback}
	if err := a.store.Queries.UpdateTaxPercents(r.Context(), params); err != nil {
		internalError(w, "update tax settings")
		return
	}
	writeJSON(w, http.StatusOK, taxPercentMap(dbgen.GetTaxPercentsRow(params)))
}

func (a *api) listCurrencies(w http.ResponseWriter, r *http.Request, _ *session) {
	writeJSON(w, http.StatusOK, map[string][]string{"currencies": ledger.Currencies()})
}
