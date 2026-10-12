package jobs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/nbs"
)

const cashbackReport = "Action,Time (UTC),Notes,ID,Total,Currency (Total),Merchant name,Merchant category\n" +
	"Card debit,2026-08-03 17:40:00,,t212-card,-12.40,EUR,Example Bistro,RESTAURANTS\n" +
	"Spending cashback,2026-08-03 17:45:11,,t212-cashback,0.27,EUR,,\n"

// fakeHistory serves dividend and transaction pages by cursor and the NBS
// files by path, and counts the requests.
type fakeHistory struct {
	mu        sync.Mutex
	dividends map[string]string
	interest  map[string]string
	// limited answers this many dividend requests with 429 first.
	limited   int
	forbidden bool
	nbs       map[string]string
	requests  map[string]int
}

func (f *fakeHistory) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[key]
}

func (f *fakeHistory) serveHistory(w http.ResponseWriter, r *http.Request, pages map[string]string, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cursor := r.URL.Query().Get("cursor")
	f.requests[name+":"+cursor]++
	f.requests[name]++
	if name == "dividends" && f.forbidden {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if name == "dividends" && f.limited > 0 {
		f.limited--
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	page, ok := pages[cursor]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	fmt.Fprint(w, page)
}

func (f *fakeHistory) serveNBS(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests["nbs"]++
	f.requests[r.URL.Path]++
	body, ok := f.nbs[r.URL.Path]
	if !ok {
		// A future month is a file with only the byte order mark.
		body = "\xEF\xBB\xBF"
	}
	fmt.Fprint(w, body)
}

// nbsMonth is a month file with one USD rate on the 1st and its average.
func nbsMonth(year int, month time.Month, rate string) (string, string) {
	return fmt.Sprintf("/exchange-rate-month/%04d-%02d/csv", year, month),
		fmt.Sprintf("Dátum;USD\n\"1. %d. %d\";%s\nPriemer*;%s\n", int(month), year, rate, rate)
}

type incomeHarness struct {
	t       *testing.T
	store   *database.Store
	trading *fakeTrading212
	history *fakeHistory
	job     *Income
	now     time.Time
	pauses  []time.Duration
}

func newIncomeHarness(t *testing.T, configured bool) *incomeHarness {
	t.Helper()
	h := &incomeHarness{t: t, store: openStore(t), now: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)}
	h.trading = &fakeTrading212{now: func() time.Time { return h.now }, status: integrations.ReportQueued, csv: cashbackReport}
	h.history = &fakeHistory{requests: map[string]int{}, nbs: map[string]string{},
		dividends: map[string]string{
			"": `{"items":[
				{"reference":"d-3","paidOn":"2026-09-15T12:00:00Z","type":"ORDINARY","amount":1.7,"currency":"EUR",
					"grossAmountPerShare":0.25,"quantity":8,"instrument":{"currency":"USD","isin":"US0378331005","name":"Apple","ticker":"AAPL_US_EQ"}},
				{"reference":"d-2","paidOn":"2026-03-02T12:00:00Z","type":"ORDINARY","amount":0.85,"currency":"EUR",
					"grossAmountPerShare":0.25,"quantity":4,"instrument":{"currency":"USD","ticker":"AAPL_US_EQ"}}],
				"nextPagePath":"/api/v0/equity/history/dividends?limit=50&cursor=2"}`,
			"2": `{"items":[
				{"reference":"d-1","paidOn":"2026-01-01T10:00:00Z","type":"ORDINARY","amount":1,"currency":"EUR","instrument":{"currency":"EUR"}},
				{"reference":"d-0","paidOn":"2025-12-20T10:00:00Z","type":"ORDINARY","amount":1,"currency":"EUR"}],
				"nextPagePath":"/api/v0/equity/history/dividends?limit=50&cursor=3"}`,
			"3": `{"items":[{"reference":"d-old","paidOn":"2025-11-01T10:00:00Z","type":"ORDINARY","amount":1,"currency":"EUR"}],
				"nextPagePath":"/api/v0/equity/history/dividends?limit=50&cursor=4"}`,
		},
		interest: map[string]string{
			"": `{"items":[
				{"reference":"t-3","type":"INTEREST_ON_FREE_CASH","dateTime":"2026-09-30T23:30:00Z","amount":0.05,"currency":"EUR"},
				{"reference":"t-2","type":"DEPOSIT","dateTime":"2026-05-01T08:00:00Z","amount":100,"currency":"EUR"},
				{"reference":"t-1","type":"LENDING_INTEREST","dateTime":"2026-02-01T08:00:00Z","amount":0.02,"currency":"EUR"}],
				"nextPagePath":null}`,
		},
	}
	for month := time.Month(1); month <= 9; month++ {
		path, body := nbsMonth(2026, month, fmt.Sprintf("1.1%d00", month))
		h.history.nbs[path] = body
	}
	path, body := nbsMonth(2025, time.December, "1.1700")
	h.history.nbs[path] = body
	// October has one rate so far; its average is not final.
	path, body = nbsMonth(2026, time.October, "1.2000")
	h.history.nbs[path] = body
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v0/equity/history/dividends":
			h.history.serveHistory(w, r, h.history.dividends, "dividends")
		case r.URL.Path == "/api/v0/equity/history/transactions":
			h.history.serveHistory(w, r, h.history.interest, "transactions")
		case strings.HasPrefix(r.URL.Path, "/exchange-rate-"):
			h.history.serveNBS(w, r)
		default:
			h.trading.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	h.trading.server = server
	client := integrations.NewClient()
	client.HTTP.Transport = server.Client().Transport
	client.Trading212Live = server.URL + "/api/v0"
	h.job = NewIncome(h.store, client)
	h.job.NBS = &nbs.Client{HTTP: server.Client(), BaseURL: server.URL}
	h.job.Now = func() time.Time { return h.now }
	h.job.Pause = func(_ context.Context, wait time.Duration) bool { h.pauses = append(h.pauses, wait); return true }
	if configured {
		h.saveKey("2026-09-01T00:00:00Z")
	}
	return h
}

// saveKey stores the key as saved at a given time.
func (h *incomeHarness) saveKey(at string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.store.Queries.SaveIntegration(ctx, dbgen.SaveIntegrationParams{Provider: "trading212",
		CredentialsJson: `{"environment":"live","apiKey":"key-1","apiSecret":"secret-1"}`}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.store.DB.ExecContext(ctx, "UPDATE integrations SET updated_at = ?", at); err != nil {
		h.t.Fatal(err)
	}
}

func (h *incomeHarness) step(now time.Time) time.Time {
	h.t.Helper()
	h.now = now
	next, err := h.job.Step(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return next
}

func (h *incomeHarness) state() dbgen.InvestmentIncomeState {
	h.t.Helper()
	state, err := h.store.Queries.GetIncomeState(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return state
}

func (h *incomeHarness) income() map[string]dbgen.InvestmentIncome {
	h.t.Helper()
	rows, err := h.store.Queries.ListIncome(context.Background(), dbgen.ListIncomeParams{Since: "2000-01-01", Until: "2100-01-01"})
	if err != nil {
		h.t.Fatal(err)
	}
	found := map[string]dbgen.InvestmentIncome{}
	for _, row := range rows {
		found[row.SourceID] = row
	}
	return found
}

func TestIncomeWaitsForKey(t *testing.T) {
	h := newIncomeHarness(t, false)
	if next := h.step(h.now); !next.Equal(h.now.Add(time.Hour)) {
		t.Fatalf("next %v", next)
	}
	if h.history.count("dividends") != 0 || h.trading.count(http.MethodPost) != 0 || h.history.count("nbs") != 0 {
		t.Fatalf("requests without a key: %v", h.history.requests)
	}
	if state := h.state(); state.StartedOn.Valid {
		t.Fatalf("started without a key: %+v", state)
	}
}

func TestIncomeImportsTheYear(t *testing.T) {
	h := newIncomeHarness(t, true)
	h.history.limited = 1
	start := h.now
	next := h.step(start)
	if want := start.Add(integrations.ReportListInterval); !next.Equal(want) {
		t.Fatalf("next %v, want the report poll at %v", next, want)
	}
	// The history is read back to the 1st of January and no further.
	if h.history.count("dividends:4") != 0 || h.history.count("dividends:3") != 1 || h.history.count("transactions") != 1 {
		t.Fatalf("history requests %v", h.history.requests)
	}
	waited := false
	for _, wait := range h.pauses {
		waited = waited || wait == rateLimitWait
	}
	if !waited {
		t.Fatalf("a rate limited page must wait a minute: %v", h.pauses)
	}
	state := h.state()
	if state.StartedOn.String != "2026-01-01" || state.DividendsSyncedAt.String != "2026-10-01T10:00:00Z" ||
		state.InterestSyncedAt.String != "2026-10-01T10:00:00Z" || state.HistoryError.Valid ||
		state.HistoryNextAt.String != "2026-10-01T22:00:00Z" {
		t.Fatalf("state %+v", state)
	}
	// The cashback report starts at midnight in Bratislava.
	if state.ReportFrom.String != "2025-12-31T23:00:00Z" || state.ReportTo.String != "2026-10-01T10:00:00Z" || state.ReportID.String != "101" {
		t.Fatalf("report %+v", state)
	}
	found := h.income()
	if len(found) != 5 {
		t.Fatalf("income %v", found)
	}
	dividend := found["dividend:d-3"]
	if dividend.Kind != "dividend" || dividend.Currency != "USD" || dividend.AmountMinor != 200 || dividend.ReceivedCurrency != "EUR" ||
		dividend.ReceivedMinor != 170 || dividend.PaidOn != "2026-09-15" || dividend.Ticker.String != "AAPL_US_EQ" ||
		dividend.PerShare.String != "0.25" || dividend.Quantity.String != "8" || dividend.SourceType != "ORDINARY" {
		t.Fatalf("dividend %+v", dividend)
	}
	// Interest credited at 01:30 in Bratislava belongs to that day.
	if interest := found["transaction:t-3"]; interest.Kind != "interest" || interest.PaidOn != "2026-10-01" || interest.AmountMinor != 5 ||
		!interest.InformativeEurCents.Valid || interest.InformativeEurCents.Int64 != 5 || interest.InformativeRate.String != "1" {
		t.Fatalf("interest %+v", interest)
	}
	if lending := found["transaction:t-1"]; lending.Kind != "interest" || lending.SourceType != "LENDING_INTEREST" {
		t.Fatalf("lending %+v", lending)
	}
	if _, ok := found["dividend:d-0"]; ok {
		t.Fatal("a dividend from before the year was saved")
	}
	// No cached rate yet, so the informative EUR value waits.
	if dividend.InformativeEurCents.Valid {
		t.Fatalf("informative %+v", dividend)
	}
	// NBS months from the December before through October were read once.
	files, err := h.store.Queries.ListNBSFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 11 || files[0].Period != "2025-12" || files[0].Complete != 1 || files[10].Period != "2026-10" || files[10].Complete != 0 {
		t.Fatalf("files %+v", files)
	}
	saved, err := h.store.Queries.ListNBSRates(context.Background(), dbgen.ListNBSRatesParams{Since: "2026-10", Until: "2027"})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Period != "2026-10-01" {
		t.Fatalf("an unfinished month must keep only its days: %+v", saved)
	}

	// The report is finished on the first poll.
	h.trading.setStatus(integrations.ReportFinished)
	polled := next.Add(time.Second)
	next = h.step(polled)
	state = h.state()
	if state.ReportFrom.Valid || state.CashbackThrough.String != "2026-10-01T10:00:00Z" || state.CashbackError.Valid {
		t.Fatalf("state after the report %+v", state)
	}
	cashback := h.income()["cashback:t212-cashback"]
	if cashback.Kind != "cashback" || cashback.AmountMinor != 27 || cashback.PaidOn != "2026-08-03" || cashback.SourceType != "Spending cashback" {
		t.Fatalf("cashback %+v", cashback)
	}
	if len(h.income()) != 6 {
		t.Fatalf("income %v", h.income())
	}
	// Nothing is due for hours, so the worker checks back within the hour.
	if want := polled.Add(time.Hour); !next.Equal(want) {
		t.Fatalf("next %v, want %v", next, want)
	}

	// Later reads stop a week before the last one and keep the past.
	nbsBefore := h.history.count("nbs")
	h.step(start.Add(IncomeInterval))
	if h.history.count("dividends:") != 3 || h.history.count("dividends:2") != 1 {
		t.Fatalf("the second read must stop on the first page: %v", h.history.requests)
	}
	// Only the unfinished month is read again.
	if h.history.count("nbs") != nbsBefore+1 || h.history.count("/exchange-rate-month/2026-10/csv") != 2 {
		t.Fatalf("NBS requests %v", h.history.requests)
	}

	// The next cashback report overlaps the last one by three days.
	h.step(polled.Add(CashbackInterval))
	if state := h.state(); state.ReportFrom.String != "2026-09-28T10:00:00Z" || state.ReportTo.String != "2026-10-02T10:01:01Z" {
		t.Fatalf("second report %+v", state)
	}
}

func TestIncomeKeepsInterestWhenDividendsAreForbidden(t *testing.T) {
	h := newIncomeHarness(t, true)
	h.history.forbidden = true
	h.step(h.now)
	state := h.state()
	if state.DividendsSyncedAt.Valid || !state.InterestSyncedAt.Valid || !strings.Contains(state.HistoryError.String, "Dividend history") ||
		state.HistoryNextAt.String != "2026-10-01T22:00:00Z" {
		t.Fatalf("state %+v", state)
	}
	if found := h.income(); len(found) != 2 {
		t.Fatalf("income %v", found)
	}
	// A new key is tried right away.
	h.history.forbidden = false
	h.saveKey("2026-10-01T10:30:00Z")
	h.step(h.now.Add(time.Hour))
	if state := h.state(); !state.DividendsSyncedAt.Valid || state.HistoryError.Valid {
		t.Fatalf("state after the new key %+v", state)
	}
}

func TestIncomeFollowsSyncSelection(t *testing.T) {
	h := newIncomeHarness(t, false)
	if err := h.store.Queries.SaveIntegration(context.Background(), dbgen.SaveIntegrationParams{Provider: "trading212",
		CredentialsJson: `{"environment":"live","apiKey":"key-1","apiSecret":"secret-1",` +
			`"sync":{"portfolio":true,"pies":true,"dividends":false,"interest":true,"cashback":false}}`}); err != nil {
		t.Fatal(err)
	}
	h.step(h.now)
	if h.history.count("dividends") != 0 || h.history.count("transactions") != 1 || h.trading.count(http.MethodPost) != 0 {
		t.Fatalf("requests %v", h.history.requests)
	}
	if state := h.state(); state.DividendsSyncedAt.Valid || !state.InterestSyncedAt.Valid || state.CashbackAttemptAt.Valid {
		t.Fatalf("state %+v", state)
	}
}

func TestIncomeFindsReportAfterLostResponse(t *testing.T) {
	h := newIncomeHarness(t, true)
	h.trading.dropPost = true
	h.trading.status = integrations.ReportFinished
	next := h.step(h.now)
	state := h.state()
	if !state.ReportFrom.Valid || state.ReportID.Valid || !state.CashbackError.Valid {
		t.Fatalf("state %+v", state)
	}
	if want := h.now.Add(incomeRetry); !next.Equal(want) {
		t.Fatalf("next %v, want %v", next, want)
	}
	h.step(next)
	if state := h.state(); state.ReportFrom.Valid || state.CashbackError.Valid || h.trading.count(http.MethodPost) != 1 {
		t.Fatalf("state %+v", state)
	}
	if _, ok := h.income()["cashback:t212-cashback"]; !ok {
		t.Fatal("cashback missing")
	}
}

func TestIncomeReadsAnnualRatesOnceTheYearEnds(t *testing.T) {
	h := newIncomeHarness(t, false)
	if _, err := h.store.Queries.SaveIncome(context.Background(), dbgen.SaveIncomeParams{SourceID: "dividend:x", Kind: "dividend",
		PaidAt: "2026-06-01T10:00:00Z", PaidOn: "2026-06-01", Currency: "USD", AmountMinor: 100, SourceType: "ORDINARY",
		ReceivedCurrency: "EUR", ReceivedMinor: 85, ImportedAt: "2026-06-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.ExecContext(context.Background(), `INSERT INTO exchange_rates
		(quote_currency, publication_date, rate, provider_policy, fetched_at) VALUES ('USD', '2027-01-08', '1.25', 'test', '2027-01-08T16:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h.history.nbs["/exchange-rate-month/2026-11/csv"] = "Dátum;USD\n\"2. 11. 2026\";1,1500\nPriemer*;1,1500\n"
	h.history.nbs["/exchange-rate-month/2026-12/csv"] = "Dátum;USD\n\"1. 12. 2026\";1,1600\nPriemer*;1,1600\n"
	h.history.nbs["/exchange-rate-cumulative/2026/csv"] = "Obdobie;USD\n12/2026;1,1600\nPriemer/2026;1,1450\n"
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	if next := h.step(start); !next.Equal(start.Add(time.Hour)) {
		t.Fatalf("next %v", next)
	}
	// December 2025 through December 2026, then the year.
	if h.history.count("nbs") != 14 || h.history.count("/exchange-rate-cumulative/2026/csv") != 1 {
		t.Fatalf("NBS requests %v", h.history.requests)
	}
	annual, err := h.store.Queries.ListNBSRates(context.Background(), dbgen.ListNBSRatesParams{Since: "2026", Until: "2026-"})
	if err != nil || len(annual) != 1 || annual[0].Rate != "1.1450" {
		t.Fatalf("annual %+v %v", annual, err)
	}
	if row := h.income()["dividend:x"]; row.InformativeEurCents.Int64 != 80 || row.InformativeRate.String != "1.25" ||
		row.InformativeRateDate.String != "2027-01-08" {
		t.Fatalf("informative %+v", row)
	}
	// Every file is complete, so nothing is read again.
	h.step(start.Add(IncomeInterval))
	if h.history.count("nbs") != 14 {
		t.Fatalf("NBS requests %v", h.history.requests)
	}
}
