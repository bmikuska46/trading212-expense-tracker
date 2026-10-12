package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const syntheticSecret = "synthetic-secret-3c1f"

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient()
	client.Trading212Live, client.Trading212Demo = server.URL+"/live", server.URL+"/demo"
	client.TypeSafe, client.Resend = server.URL+"/typesafe", server.URL+"/resend"
	return client
}

func assertNoSecret(t *testing.T, check Check) {
	t.Helper()
	text := check.Message
	for _, item := range check.Items {
		text += item.Label + item.Detail
	}
	if strings.Contains(text, syntheticSecret) {
		t.Fatal("check result echoed a secret")
	}
}

func TestTrading212Check(t *testing.T) {
	for _, test := range []struct {
		name                                              string
		summary, history, reports, dividends              int
		status, historyState, reportsState, dividendState string
	}{
		{"all permissions", 200, 200, 200, 200, StatusOK, StateOK, StateOK, StateOK},
		{"missing history", 200, 403, 200, 200, StatusPartial, StateMissing, StateOK, StateOK},
		{"reports rate limited", 200, 200, 429, 200, StatusOK, StateOK, StateUnverified, StateOK},
		{"missing dividends", 200, 200, 200, 403, StatusPartial, StateOK, StateOK, StateMissing},
		{"bad key", 401, 200, 200, 200, StatusFailed, "", "", ""},
		{"no account scope", 403, 200, 200, 200, StatusFailed, "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var paths []string
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.RequestURI())
				expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("synthetic-key:"+syntheticSecret))
				if r.Header.Get("Authorization") != expected {
					w.WriteHeader(401)
					return
				}
				switch r.URL.Path {
				case "/demo/equity/account/summary":
					w.WriteHeader(test.summary)
					w.Write([]byte(`{"id":12345678901234,"currency":"EUR","totalValue":1,"echo":"` + syntheticSecret + `"}`))
				case "/demo/equity/history/transactions":
					w.WriteHeader(test.history)
					w.Write([]byte(`{"items":[],"nextPagePath":null}`))
				case "/demo/equity/history/exports":
					w.WriteHeader(test.reports)
					w.Write([]byte(`[]`))
				case "/demo/equity/history/dividends":
					w.WriteHeader(test.dividends)
					w.Write([]byte(`{"items":[],"nextPagePath":null}`))
				default:
					w.WriteHeader(404)
				}
			})
			check, account := client.CheckTrading212(context.Background(), Trading212Credentials{Environment: "demo", APIKey: "synthetic-key", APISecret: syntheticSecret})
			assertNoSecret(t, check)
			if check.Status != test.status {
				t.Fatalf("status %s: %+v", check.Status, check)
			}
			if test.status == StatusFailed {
				if account != nil || len(paths) != 1 {
					t.Fatalf("failed check must stop after the summary: %v", paths)
				}
				return
			}
			if account == nil || account.ID != "12345678901234" || account.Currency != "EUR" {
				t.Fatalf("account: %+v", account)
			}
			if check.Items[1].State != test.historyState || check.Items[2].State != test.reportsState ||
				check.Items[3].State != test.dividendState {
				t.Fatalf("items: %+v", check.Items)
			}
			if paths[1] != "/demo/equity/history/transactions?limit=1" || paths[3] != "/demo/equity/history/dividends?limit=1" {
				t.Fatalf("history checks must request one record: %v", paths)
			}
		})
	}
}

func TestTrading212UnreachableAndLiveEnvironment(t *testing.T) {
	var live atomic.Bool
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		live.Store(strings.HasPrefix(r.URL.Path, "/live/"))
		w.WriteHeader(401)
	})
	client.CheckTrading212(context.Background(), Trading212Credentials{Environment: "live", APIKey: "k", APISecret: "s"})
	if !live.Load() {
		t.Fatal("live environment must use the live URL")
	}
	client.Trading212Live = "http://127.0.0.1:1"
	if check, _ := client.CheckTrading212(context.Background(), Trading212Credentials{Environment: "live"}); check.Status != StatusFailed {
		t.Fatal("unreachable service must fail")
	}
}

func TestTypeSafeCheck(t *testing.T) {
	listed := `{"models":[{"name":"jev-latest","description":"Latest","release_date":"2026-06-01"}]}`
	for name, test := range map[string]struct {
		code         int
		body, status string
	}{
		"listed":     {200, listed, StatusOK},
		"not listed": {200, `{"models":[{"name":"jev-preview"}]}`, StatusPartial},
		"bad key":    {401, `{"error":"Invalid key ` + syntheticSecret + `"}`, StatusFailed},
		"overloaded": {529, `{}`, StatusOK},
		"server":     {500, `{}`, StatusFailed},
		"unreadable": {200, `not json`, StatusFailed},
	} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/typesafe/models" || r.Header.Get("Authorization") != "Bearer "+syntheticSecret {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(test.code)
			w.Write([]byte(test.body))
		})
		check := client.CheckTypeSafe(context.Background(), syntheticSecret)
		assertNoSecret(t, check)
		if check.Status != test.status {
			t.Errorf("%s: got %s", name, check.Status)
		}
	}
}

func TestResendCheck(t *testing.T) {
	for _, test := range []struct {
		name, body, status, state string
		code                      int
	}{
		{"verified", `{"object":"list","has_more":false,"data":[{"name":"Example.com","status":"verified"}]}`, StatusOK, StateOK, 200},
		{"pending", `{"object":"list","has_more":false,"data":[{"name":"example.com","status":"pending"}]}`, StatusPartial, StateMissing, 200},
		{"absent", `{"object":"list","has_more":false,"data":[{"name":"other.example","status":"verified"}]}`, StatusPartial, StateMissing, 200},
		{"sending only", `{"statusCode":401,"name":"restricted_api_key","message":"This API key is restricted to only send emails"}`, StatusOK, StateUnverified, 401},
		{"invalid", `{"statusCode":401,"name":"validation_error","message":"API key is invalid"}`, StatusFailed, "", 401},
		{"inactive", `{"statusCode":403,"name":"restricted_api_key","message":"API key is not active"}`, StatusFailed, "", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/resend/domains" || r.Header.Get("Authorization") != "Bearer "+syntheticSecret {
					w.WriteHeader(400)
					return
				}
				w.WriteHeader(test.code)
				w.Write([]byte(test.body))
			})
			check := client.CheckResend(context.Background(), syntheticSecret, "example.com")
			assertNoSecret(t, check)
			if check.Status != test.status || (test.state != "" && check.Items[0].State != test.state) {
				t.Fatalf("%+v", check)
			}
		})
	}
}

func TestAskSendsTypedQuestions(t *testing.T) {
	var bodies []string
	status := 200
	output := `{"model":"jev-1","answers":{"pick":{"type":"choice","choice":"b","probabilities":{"a":0.1,"b":0.9},"confidence":0.8},` +
		`"utc":{"type":"noul","noul":0.2}},"usage":{"input_tokens":10}}`
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if r.Method != http.MethodPost || r.URL.Path != "/typesafe/systemone" || r.Header.Get("Authorization") != "Bearer "+syntheticSecret {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(output))
	})
	questions := map[string]Question{
		"pick": {Type: QuestionChoice, Instructions: "Pick one", Criteria: map[string]string{"a": "First", "b": "Second"}},
		"utc":  {Type: QuestionNoul, Instructions: "Is it UTC?"},
	}
	answers, err := client.Ask(context.Background(), syntheticSecret, map[string]any{"rows": []string{"row-one"}}, questions)
	if err != nil || answers["pick"].Choice != "b" || answers["pick"].Confidence != 0.8 || answers["utc"].Noul != 0.2 {
		t.Fatalf("answers: %+v %v", answers, err)
	}
	var sent struct {
		State     map[string]any             `json:"state"`
		Model     string                     `json:"model"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if len(bodies) != 1 || json.Unmarshal([]byte(bodies[0]), &sent) != nil || sent.Model != JevModel ||
		len(sent.Questions) != 2 || strings.Contains(string(sent.Questions["utc"]), "criteria") || !strings.Contains(bodies[0], "row-one") {
		t.Fatalf("outbound body: %v", bodies)
	}

	for name, test := range map[string]struct {
		status int
		output string
	}{
		"missing answer": {200, `{"answers":{"pick":{"type":"choice","choice":"a"}}}`},
		"wrong type":     {200, `{"answers":{"pick":{"type":"noul","noul":1},"utc":{"type":"noul","noul":0}}}`},
		"unreadable":     {200, `not json`},
		"bad key":        {401, `{"error":"Invalid key ` + syntheticSecret + `"}`},
		"bad request":    {422, `{}`},
		"server down":    {503, `{}`},
	} {
		bodies, status, output = nil, test.status, test.output
		_, err := client.Ask(context.Background(), syntheticSecret, map[string]any{}, questions)
		var suggestion *SuggestError
		if !errors.As(err, &suggestion) || strings.Contains(err.Error(), syntheticSecret) {
			t.Errorf("%s: %v", name, err)
		}
		expected := 1
		if test.status >= 500 {
			expected = 1 + askRetries
		}
		if len(bodies) != expected || (len(bodies) > 1 && bodies[0] != bodies[len(bodies)-1]) {
			t.Errorf("%s: %d requests", name, len(bodies))
		}
	}
}
