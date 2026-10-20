package httpserver

import (
	"net/http"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/jobs"
	"budget-tracking/backend/internal/rates"
)

type rateAttemptView struct {
	Stage      string  `json:"stage"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
	Outcome    *string `json:"outcome"`
	Error      *string `json:"error"`
}

type ratesStatus struct {
	Endpoint          string           `json:"endpoint"`
	Bases             []string         `json:"bases"`
	ProviderPolicy    string           `json:"providerPolicy"`
	Timezone          string           `json:"timezone"`
	Currencies        int64            `json:"currencies"`
	LatestPublication *string          `json:"latestPublication"`
	LastFetchedAt     *string          `json:"lastFetchedAt"`
	NextRunAt         string           `json:"nextRunAt"`
	LastAttempt       *rateAttemptView `json:"lastAttempt"`
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// getRates reports the state of the daily rate job and the cache.
func (a *api) getRates(w http.ResponseWriter, r *http.Request, _ *session) {
	q := a.store.Queries
	summary, err := q.RateSummary(r.Context())
	if err != nil {
		internalError(w, "load rates")
		return
	}
	currencies, err := q.ListAccountCurrencies(r.Context())
	if err != nil {
		internalError(w, "load rates")
		return
	}
	next, err := jobs.NextRun(r.Context(), q, a.now())
	if err != nil {
		internalError(w, "load rates")
		return
	}
	status := ratesStatus{Endpoint: rates.Endpoint, Bases: rates.Bases(currencies), ProviderPolicy: rates.ProviderPolicy, Timezone: rates.Timezone,
		Currencies: summary.Currencies, LatestPublication: optional(summary.LatestPublication),
		LastFetchedAt: optional(summary.LastFetchedAt), NextRunAt: auth.FormatTime(next)}
	attempts, err := q.LatestRatesAttempts(r.Context(), 1)
	if err != nil {
		internalError(w, "load rates")
		return
	}
	if len(attempts) > 0 {
		attempt := attempts[0]
		status.LastAttempt = &rateAttemptView{Stage: attempt.Stage, StartedAt: attempt.StartedAt,
			FinishedAt: nullText(attempt.FinishedAt), Outcome: nullText(attempt.Outcome), Error: nullText(attempt.ErrorDetail)}
	}
	writeJSON(w, http.StatusOK, status)
}
