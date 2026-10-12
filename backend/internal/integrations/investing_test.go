package integrations

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestFetchPortfolio(t *testing.T) {
	var paths []string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if user, secret, ok := r.BasicAuth(); !ok || user != "synthetic-key" || secret != syntheticSecret {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/live/equity/account/summary":
			w.Write([]byte(`{"id":12345678901234,"currency":"EUR","totalValue":10250.505,
				"cash":{"availableToTrade":50.1,"inPies":0.004,"reservedForOrders":0},
				"investments":{"currentValue":10200.4,"totalCost":9000,"unrealizedProfitLoss":1200.4,"realizedProfitLoss":-3.125}}`))
		case "/live/equity/pies":
			w.Write([]byte(`[{"id":7,"cash":1.5,"progress":0.5,"status":"AHEAD",
				"dividendDetails":{"gained":12.345,"reinvested":10,"inCash":2.345},
				"result":{"priceAvgInvestedValue":1000,"priceAvgValue":1100.999,"priceAvgResult":100.999,"priceAvgResultCoef":0.1}},
				{"id":8,"dividendDetails":null,"result":{"priceAvgInvestedValue":5,"priceAvgValue":4,"priceAvgResult":-1}}]`))
		case "/live/equity/pies/7":
			w.Write([]byte(`{"instruments":[],"settings":{"id":7,"name":"  Global\tgrowth "}}`))
		default:
			w.WriteHeader(404)
		}
	})
	credentials := Trading212Credentials{Environment: "live", APIKey: "synthetic-key", APISecret: syntheticSecret}
	summary, err := client.FetchAccountSummary(context.Background(), credentials)
	want := AccountSummary{AccountID: "12345678901234", Currency: "EUR", TotalValue: 1025051, InvestmentsValue: 1020040,
		CostBasis: 900000, Unrealized: 120040, Realized: -313, CashAvailable: 5010, CashInPies: 0, CashReserved: 0}
	if err != nil || summary != want {
		t.Fatalf("summary %+v, %v", summary, err)
	}
	pies, err := client.FetchPies(context.Background(), credentials, summary.Currency)
	if err != nil || len(pies) != 2 {
		t.Fatalf("pies %+v, %v", pies, err)
	}
	if pies[0] != (PieSummary{ID: 7, Invested: 100000, Value: 110100, Result: 10100, Cash: 150,
		DividendsGained: 1235, DividendsReinvested: 1000, DividendsInCash: 235}) {
		t.Fatalf("first pie %+v", pies[0])
	}
	if pies[1] != (PieSummary{ID: 8, Invested: 500, Value: 400, Result: -100}) {
		t.Fatalf("second pie %+v", pies[1])
	}
	name, err := client.FetchPieName(context.Background(), credentials, 7)
	if err != nil || name != "Global growth" {
		t.Fatalf("name %q, %v", name, err)
	}
}

func TestFetchPortfolioFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		permanent bool
		detail    string
	}{
		{"bad key", 401, "", true, "rejected the API key"},
		{"no pies permission", 403, "", true, "Pies read"},
		{"rate limited", 429, "", false, "rate limiting"},
		{"unreadable", 200, `{"echo":"` + syntheticSecret + `"`, false, "cannot read"},
		{"bad id", 200, `[{"id":"x"}]`, false, "cannot read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			})
			_, err := client.FetchPies(context.Background(), Trading212Credentials{APIKey: "k", APISecret: syntheticSecret}, "EUR")
			var failed *FetchError
			if !errors.As(err, &failed) || failed.Permanent != test.permanent || !strings.Contains(failed.Detail, test.detail) {
				t.Fatalf("error %#v", err)
			}
			if strings.Contains(failed.Detail, syntheticSecret) {
				t.Fatal("error echoed a secret")
			}
		})
	}
}
