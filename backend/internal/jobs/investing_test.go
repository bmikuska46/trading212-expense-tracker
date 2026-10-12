package jobs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
)

// fakePortfolio serves the account summary, the pie list, and pie details.
type fakePortfolio struct {
	mu        sync.Mutex
	value     int
	pies      int
	piesState int
	names     map[string]string
	calls     map[string]int
}

func (f *fakePortfolio) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[r.URL.Path]++
	switch r.URL.Path {
	case "/api/v0/equity/account/summary":
		fmt.Fprintf(w, `{"id":42,"currency":"EUR","totalValue":%d.5,"cash":{"availableToTrade":10,"inPies":0.5,"reservedForOrders":0},
			"investments":{"currentValue":%d,"totalCost":900,"unrealizedProfitLoss":%d,"realizedProfitLoss":3}}`, f.value, f.value-10, f.value-910)
	case "/api/v0/equity/pies":
		if f.piesState != 0 {
			w.WriteHeader(f.piesState)
			return
		}
		fmt.Fprint(w, "[")
		for id := 1; id <= f.pies; id++ {
			if id > 1 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%d,"cash":0,"dividendDetails":{"gained":1,"reinvested":1,"inCash":0},
				"result":{"priceAvgInvestedValue":100,"priceAvgValue":%d,"priceAvgResult":%d}}`, id, 100+id, id)
		}
		fmt.Fprint(w, "]")
	default:
		name, ok := f.names[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"settings":{"name":%q}}`, name)
	}
}

type investingHarness struct {
	t      *testing.T
	store  *database.Store
	fake   *fakePortfolio
	job    *Investing
	now    time.Time
	pauses int
}

func newInvestingHarness(t *testing.T, configured bool) *investingHarness {
	t.Helper()
	h := &investingHarness{t: t, store: openStore(t), now: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
	h.fake = &fakePortfolio{value: 1000, pies: 2, calls: map[string]int{},
		names: map[string]string{"/api/v0/equity/pies/1": "Dividends", "/api/v0/equity/pies/2": "Growth"}}
	server := httptest.NewServer(h.fake)
	t.Cleanup(server.Close)
	client := integrations.NewClient()
	client.Trading212Live = server.URL + "/api/v0"
	h.job = NewInvesting(h.store, client)
	h.job.Now = func() time.Time { return h.now }
	h.job.Pause = func(context.Context, time.Duration) bool { h.pauses++; return true }
	if configured {
		if err := h.store.Queries.SaveIntegration(context.Background(), dbgen.SaveIntegrationParams{Provider: "trading212",
			CredentialsJson: `{"environment":"live","apiKey":"key-1","apiSecret":"secret-1"}`}); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *investingHarness) step(now time.Time) time.Time {
	h.t.Helper()
	h.now = now
	next, err := h.job.Step(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return next
}

func (h *investingHarness) snapshots() []dbgen.InvestingSnapshot {
	h.t.Helper()
	rows, err := h.store.Queries.ListInvestingSnapshots(context.Background(),
		dbgen.ListInvestingSnapshotsParams{Since: "2000-01-01T00:00:00Z", BucketSeconds: 1})
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

func (h *investingHarness) state() dbgen.InvestingState {
	h.t.Helper()
	state, err := h.store.Queries.GetInvestingState(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return state
}

func TestInvestingWaitsForKey(t *testing.T) {
	h := newInvestingHarness(t, false)
	if next := h.step(h.now); !next.Equal(h.now.Add(time.Hour)) {
		t.Fatalf("next %v", next)
	}
	if len(h.fake.calls) != 0 || len(h.snapshots()) != 0 {
		t.Fatal("fetched without a key")
	}
}

func TestInvestingSnapshotsHourlyAndNamesPies(t *testing.T) {
	h := newInvestingHarness(t, true)
	start := h.now
	if next := h.step(start); !next.Equal(start.Add(time.Hour)) {
		t.Fatalf("next %v", next)
	}
	snapshots := h.snapshots()
	if len(snapshots) != 1 || snapshots[0].TotalValueMinor != 100050 || snapshots[0].UnrealizedMinor != 9000 ||
		snapshots[0].CashInPiesMinor != 50 || snapshots[0].AccountID != "42" {
		t.Fatalf("snapshots %+v", snapshots)
	}
	pies, err := h.store.Queries.ListInvestingPies(context.Background())
	if err != nil || len(pies) != 2 || pies[0].Name.String != "Dividends" || pies[1].Name.String != "Growth" {
		t.Fatalf("pies %+v, %v", pies, err)
	}
	// Requests after the first are spaced to respect the rate limit.
	if h.pauses != 1 {
		t.Fatalf("paused %d times", h.pauses)
	}
	// A restart half an hour later keeps the schedule.
	if next := h.step(start.Add(30 * time.Minute)); !next.Equal(start.Add(time.Hour)) {
		t.Fatalf("next after restart %v", next)
	}
	// Names are refreshed only once a day.
	h.fake.value, h.fake.pies = 1100, 3
	h.fake.names["/api/v0/equity/pies/3"] = "New"
	h.step(start.Add(time.Hour))
	if h.fake.calls["/api/v0/equity/pies/1"] != 1 || h.fake.calls["/api/v0/equity/pies/3"] != 1 {
		t.Fatalf("calls %v", h.fake.calls)
	}
	h.step(start.Add(25 * time.Hour))
	if h.fake.calls["/api/v0/equity/pies/1"] != 2 || len(h.snapshots()) != 3 {
		t.Fatalf("calls %v", h.fake.calls)
	}
	if state := h.state(); state.LastError.Valid || state.LastAttemptAt.String != "2026-09-02T11:00:00Z" {
		t.Fatalf("state %+v", state)
	}
}

func TestInvestingKeepsAccountWhenPiesFail(t *testing.T) {
	h := newInvestingHarness(t, true)
	h.fake.piesState = http.StatusForbidden
	if next := h.step(h.now); !next.Equal(h.now.Add(time.Hour)) {
		t.Fatalf("next %v", next)
	}
	if len(h.snapshots()) != 1 {
		t.Fatal("account snapshot was not kept")
	}
	if state := h.state(); !state.LastError.Valid || state.LastError.String == "" {
		t.Fatalf("state %+v", state)
	}
	if h.fake.calls["/api/v0/equity/pies/1"] != 0 {
		t.Fatal("refreshed names without a pie list")
	}
}

func TestInvestingRetriesTransientFailures(t *testing.T) {
	h := newInvestingHarness(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	h.job.Client.Trading212Live = server.URL + "/api/v0"
	start := h.now
	if next := h.step(start); !next.Equal(start.Add(investingRetry)) {
		t.Fatalf("next %v", next)
	}
	if len(h.snapshots()) != 0 || !h.state().LastError.Valid {
		t.Fatal("failure was not recorded")
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if next := h.step(start.Add(investingRetry)); !next.Equal(start.Add(investingRetry + time.Hour)) {
		t.Fatalf("next after a rejected key %v", next)
	}
}

func TestInvestingFollowsSyncSelection(t *testing.T) {
	h := newInvestingHarness(t, false)
	save := func(sync string) {
		if err := h.store.Queries.SaveIntegration(context.Background(), dbgen.SaveIntegrationParams{Provider: "trading212",
			CredentialsJson: `{"environment":"live","apiKey":"key-1","apiSecret":"secret-1","sync":` + sync + `}`}); err != nil {
			t.Fatal(err)
		}
	}
	save(`{"portfolio":true,"pies":false,"dividends":true,"interest":true,"cashback":true}`)
	h.step(h.now)
	if len(h.snapshots()) != 1 || h.fake.calls["/api/v0/equity/pies"] != 0 || h.fake.calls["/api/v0/equity/pies/1"] != 0 {
		t.Fatalf("pies are off: %v", h.fake.calls)
	}
	// Pies need the portfolio snapshot, so turning the portfolio off stops both.
	save(`{"portfolio":false,"pies":true,"dividends":true,"interest":true,"cashback":true}`)
	if next := h.step(h.now.Add(2 * time.Hour)); !next.Equal(h.now.Add(time.Hour)) {
		t.Fatalf("next %v", next)
	}
	if len(h.snapshots()) != 1 || h.fake.calls["/api/v0/equity/account/summary"] != 1 {
		t.Fatalf("portfolio is off: %v", h.fake.calls)
	}
}
