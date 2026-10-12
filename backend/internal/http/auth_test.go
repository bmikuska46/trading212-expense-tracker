package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/integrations"
)

const (
	ownerEmail    = "owner@example.com"
	ownerPassword = "correct horse battery"
	// Synthetic credentials used only against local test servers.
	syntheticKey    = "synthetic-key-0000-abcd"
	syntheticSecret = "synthetic-secret-0000-wxyz"
)

type testApp struct {
	t         *testing.T
	directory string
	store     *database.Store
	handler   http.Handler
	options   Options
}

func newTestApp(t *testing.T, options Options) *testApp {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "key"), []byte(strings.Repeat("cd", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(directory, "web")
	if err := os.MkdirAll(web, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("app"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &testApp{t: t, directory: directory, options: options}
	app.restart()
	return app
}

// restart closes the database and opens it again, as a container restart would.
func (app *testApp) restart() {
	app.t.Helper()
	if app.store != nil {
		app.store.DB.Close()
	}
	store, err := database.Open(context.Background(), filepath.Join(app.directory, "data", "budget.db"), filepath.Join(app.directory, "key"))
	if err != nil {
		app.t.Fatal(err)
	}
	app.t.Cleanup(func() { store.DB.Close() })
	handler, err := New(store, filepath.Join(app.directory, "web"), app.options)
	if err != nil {
		app.t.Fatal(err)
	}
	app.store, app.handler = store, handler
}

func (app *testApp) request(method, target string, body any, cookie *http.Cookie, headers ...string) *httptest.ResponseRecorder {
	app.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			app.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, request)
	return response
}

func (app *testApp) setup() *http.Cookie {
	app.t.Helper()
	response := app.request("POST", "/api/setup", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil)
	if response.Code != http.StatusCreated {
		app.t.Fatalf("setup: %d %s", response.Code, response.Body)
	}
	return sessionCookie(app.t, response)
}

func sessionCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if (cookie.Name == plainCookieName || cookie.Name == secureCookieName) && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("no session cookie in %v", response.Header().Values("Set-Cookie"))
	return nil
}

func decode[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	return value
}

func TestSetupThenSignInAfterRestart(t *testing.T) {
	app := newTestApp(t, Options{})
	state := decode[sessionView](t, app.request("GET", "/api/session", nil, nil))
	if !state.SetupRequired || state.Authenticated {
		t.Fatalf("fresh database must require setup: %+v", state)
	}
	if response := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil); response.Code != http.StatusConflict {
		t.Fatalf("sign-in before setup: %d", response.Code)
	}
	invalid := app.request("POST", "/api/setup", map[string]string{"email": "not an email", "password": "short", "timezone": "Mars/Base"}, nil)
	fields := decode[errorBody](t, invalid).Fields
	if invalid.Code != http.StatusUnprocessableEntity || fields["email"] == "" || fields["password"] == "" || fields["timezone"] == "" {
		t.Fatalf("invalid setup: %d %s", invalid.Code, invalid.Body)
	}

	response := app.request("POST", "/api/setup", map[string]string{"email": " Owner@Example.com ", "password": ownerPassword}, nil)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "argon2") {
		t.Fatalf("setup: %d %s", response.Code, response.Body)
	}
	created := decode[sessionView](t, response)
	if created.User.Email != "Owner@Example.com" || created.User.Timezone != "Europe/Bratislava" {
		t.Fatalf("owner defaults: %+v", created.User)
	}
	cookie := sessionCookie(t, response)
	if cookie.Name != plainCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure {
		t.Fatalf("plain HTTP cookie flags: %+v", cookie)
	}
	if again := app.request("POST", "/api/setup", map[string]string{"email": "second@example.com", "password": ownerPassword}, nil); again.Code != http.StatusConflict {
		t.Fatalf("second owner: %d", again.Code)
	}

	app.restart()
	if response := app.request("GET", "/api/settings", nil, cookie); response.Code != http.StatusOK {
		t.Fatalf("session lost across restart: %d", response.Code)
	}
	if wrong := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": "wrong password here"}, nil); wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", wrong.Code)
	}
	signIn := app.request("POST", "/api/session", map[string]string{"email": "OWNER@example.com", "password": ownerPassword}, nil,
		"X-Forwarded-Proto", "https")
	if signIn.Code != http.StatusOK {
		t.Fatalf("sign in after restart: %d %s", signIn.Code, signIn.Body)
	}
	secure := sessionCookie(t, signIn)
	if secure.Name != secureCookieName || !secure.Secure || !secure.HttpOnly || secure.SameSite != http.SameSiteStrictMode {
		t.Fatalf("HTTPS cookie flags: %+v", secure)
	}
	state = decode[sessionView](t, app.request("GET", "/api/session", nil, secure))
	if state.SetupRequired || !state.Authenticated || state.User.Email != "Owner@Example.com" {
		t.Fatalf("session state: %+v", state)
	}
	if out := app.request("DELETE", "/api/session", nil, secure); out.Code != http.StatusNoContent {
		t.Fatalf("sign out: %d", out.Code)
	}
	if response := app.request("GET", "/api/settings", nil, secure); response.Code != http.StatusUnauthorized {
		t.Fatalf("session survived sign out: %d", response.Code)
	}
	var stored string
	if err := app.store.DB.QueryRow("SELECT password_hash FROM users WHERE id = 1").Scan(&stored); err != nil || !strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("password must be stored as an argon2id hash: %v", err)
	}
}

func TestConcurrentSetupCreatesOneOwner(t *testing.T) {
	app := newTestApp(t, Options{})
	const requests = 8
	codes := make([]int, requests)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range requests {
		wait.Go(func() {
			<-start
			body := map[string]string{"email": "owner" + string(rune('a'+index)) + "@example.com", "password": ownerPassword}
			codes[index] = app.request("POST", "/api/setup", body, nil).Code
		})
	}
	close(start)
	wait.Wait()
	created := 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("unexpected setup response %d in %v", code, codes)
		}
	}
	var owners int
	if err := app.store.DB.QueryRow("SELECT count(*) FROM owner").Scan(&owners); err != nil || created != 1 || owners != 1 {
		t.Fatalf("created %d owners (%d rows): %v", created, owners, codes)
	}
}

func TestFinancialEndpointsRequireSession(t *testing.T) {
	app := newTestApp(t, Options{})
	app.setup()
	forged := &http.Cookie{Name: plainCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/settings"}, {"PATCH", "/api/settings"}, {"GET", "/api/integrations"},
		{"PUT", "/api/integrations/trading212"}, {"POST", "/api/integrations/typesafe/check"},
		{"DELETE", "/api/integrations/resend"}, {"GET", "/api/currencies"}, {"GET", "/api/accounts"},
		{"POST", "/api/accounts"}, {"GET", "/api/transactions"}, {"POST", "/api/transactions"},
		{"POST", "/api/transactions/bulk"}, {"POST", "/api/transactions/categorize"}, {"GET", "/api/transactions/1"}, {"PATCH", "/api/transactions/1"},
		{"DELETE", "/api/transactions/1"}, {"GET", "/api/statistics"}, {"GET", "/api/categories"},
		{"POST", "/api/categories"}, {"PATCH", "/api/categories/1"}, {"POST", "/api/categories/1/merge"},
		{"GET", "/api/payees"}, {"POST", "/api/payees"}, {"PATCH", "/api/payees/1"}, {"POST", "/api/payees/1/merge"},
		{"GET", "/api/rules"}, {"POST", "/api/rules"}, {"PATCH", "/api/rules/1"}, {"DELETE", "/api/rules/1"},
		{"GET", "/api/imports"}, {"POST", "/api/imports"}, {"GET", "/api/imports/1"}, {"DELETE", "/api/imports/1"},
		{"GET", "/api/imports/1/rows"}, {"PATCH", "/api/imports/1/rows/1"}, {"PUT", "/api/imports/1/mapping"},
		{"POST", "/api/imports/1/suggest-mapping"}, {"POST", "/api/imports/1/validate"}, {"POST", "/api/imports/1/commit"},
		{"POST", "/api/imports/1/confirm"}, {"GET", "/api/scheduled-imports"}, {"POST", "/api/scheduled-imports/2026-08/run"},
		{"GET", "/api/notifications"}, {"POST", "/api/notifications/1/dismiss"}, {"POST", "/api/notifications/1/resend"},
		{"GET", "/api/investing"}, {"PUT", "/api/investing/settings"}, {"POST", "/api/investing/groups"},
		{"PUT", "/api/investing/groups/1"}, {"DELETE", "/api/investing/groups/1"},
	} {
		for _, cookie := range []*http.Cookie{nil, forged, {Name: plainCookieName, Value: "not-a-token"}} {
			response := app.request(route.method, route.path, map[string]string{}, cookie)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with %v: %d %s", route.method, route.path, cookie, response.Code, response.Body)
			}
		}
	}
	state := decode[sessionView](t, app.request("GET", "/api/session", nil, nil))
	if state.Authenticated || state.User != nil {
		t.Fatal("anonymous session exposed the owner")
	}
}

func TestCrossOriginMutationsAreRejected(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	body := map[string]string{"name": "Wallet", "currency": "EUR"}
	for _, headers := range [][]string{
		{"Sec-Fetch-Site", "cross-site"},
		{"Origin", "https://attacker.example"},
	} {
		if response := app.request("POST", "/api/accounts", body, cookie, headers...); response.Code != http.StatusForbidden {
			t.Fatalf("cross-origin %v: %d", headers, response.Code)
		}
	}
	if response := app.request("POST", "/api/accounts", body, cookie, "Sec-Fetch-Site", "same-origin"); response.Code != http.StatusCreated {
		t.Fatalf("same-origin create: %d %s", response.Code, response.Body)
	}
	plain := httptest.NewRequest("POST", "/api/accounts", strings.NewReader(`{"name":"Form","currency":"EUR"}`))
	plain.Header.Set("Content-Type", "text/plain")
	plain.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, plain)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("simple form content type accepted: %d", response.Code)
	}
}

func TestSignInThrottle(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	app.setup()
	wrong := map[string]string{"email": ownerEmail, "password": "wrong password here"}
	for range 5 {
		if response := app.request("POST", "/api/session", wrong, nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("failure: %d", response.Code)
		}
	}
	app.request("POST", "/api/session", wrong, nil)
	limited := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("throttle: %d", limited.Code)
	}
	now = now.Add(time.Minute)
	if response := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil); response.Code != http.StatusOK {
		t.Fatalf("sign in after lockout: %d", response.Code)
	}
}

func TestOnboardingWithIntegrationsSkipped(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	listed := decode[map[string][]integrationView](t, app.request("GET", "/api/integrations", nil, cookie))["integrations"]
	if len(listed) != 3 {
		t.Fatalf("integrations: %+v", listed)
	}
	for _, view := range listed {
		if view.Configured || view.Enabled || view.Check != nil {
			t.Fatalf("skipped integration looks configured: %+v", view)
		}
	}
	if response := app.request("POST", "/api/integrations/typesafe/check", nil, cookie); response.Code != http.StatusConflict {
		t.Fatalf("check without configuration: %d", response.Code)
	}
	if response := app.request("PATCH", "/api/settings", map[string]string{"timezone": "America/New_York"}, cookie); response.Code != http.StatusOK {
		t.Fatalf("timezone update: %d %s", response.Code, response.Body)
	}
	currencies := decode[map[string][]string](t, app.request("GET", "/api/currencies", nil, cookie))["currencies"]
	if len(currencies) < 100 {
		t.Fatalf("currencies: %v", currencies)
	}

	invalid := app.request("POST", "/api/accounts", map[string]string{"name": " ", "currency": "XYZ"}, cookie)
	if fields := decode[errorBody](t, invalid).Fields; invalid.Code != http.StatusUnprocessableEntity || fields["name"] == "" || fields["currency"] == "" {
		t.Fatalf("invalid account: %d %s", invalid.Code, invalid.Body)
	}
	wallet := decode[accountView](t, app.request("POST", "/api/accounts", map[string]string{"name": "Wallet", "currency": "eur"}, cookie))
	savings := decode[accountView](t, app.request("POST", "/api/accounts", map[string]string{"name": "Dollars", "currency": "USD"}, cookie))
	if wallet.Currency != "EUR" || wallet.Balance != "0.00" || savings.Currency != "USD" || savings.Balance != "0.00" {
		t.Fatalf("accounts: %+v %+v", wallet, savings)
	}
	for _, entry := range []map[string]any{
		{"accountId": wallet.ID, "date": "2026-09-29", "kind": "expense", "amount": "12.50", "notes": "Groceries"},
		{"accountId": wallet.ID, "date": "2026-09-30", "kind": "income", "amount": "100"},
	} {
		if response := app.request("POST", "/api/transactions", entry, cookie); response.Code != http.StatusCreated {
			t.Fatalf("manual entry: %d %s", response.Code, response.Body)
		}
	}
	for _, entry := range []map[string]any{
		{"accountId": savings.ID, "date": "2026-09-29", "kind": "expense", "amount": "1"},
		{"accountId": wallet.ID, "date": "2026-09-29", "kind": "expense", "amount": "-1"},
		{"accountId": wallet.ID, "date": "2026-09-29", "kind": "expense", "amount": "1.001"},
		{"accountId": wallet.ID, "date": "29.09.2026", "kind": "expense", "amount": "1"},
		{"accountId": wallet.ID, "date": "2026-09-29", "kind": "transfer", "amount": "1"},
		{"accountId": 999, "date": "2026-09-29", "kind": "expense", "amount": "1"},
	} {
		if response := app.request("POST", "/api/transactions", entry, cookie); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid entry %v accepted: %d", entry, response.Code)
		}
	}
	accounts := decode[map[string][]accountView](t, app.request("GET", "/api/accounts", nil, cookie))["accounts"]
	if len(accounts) != 2 || accounts[1].Name != "Wallet" || accounts[1].Balance != "87.50" || accounts[1].BalanceEURCents != 8750 || accounts[1].TransactionCount != 2 {
		t.Fatalf("balances: %+v", accounts)
	}
	transactions := decode[registerPage](t, app.request("GET", "/api/transactions", nil, cookie)).Transactions
	if len(transactions) != 2 || transactions[1].Amount != "-12.50" || transactions[1].Notes != "Groceries" || transactions[1].ReviewStatus != "confirmed" {
		t.Fatalf("transactions: %+v", transactions)
	}
	var occurredAt string
	if err := app.store.DB.QueryRow("SELECT occurred_at FROM transactions WHERE id = ?", transactions[1].ID).Scan(&occurredAt); err != nil ||
		occurredAt != "2026-09-29T04:00:00Z" {
		t.Fatalf("occurred_at must be local midnight in the owner timezone: %s %v", occurredAt, err)
	}
}

type fakeServices struct {
	summaryStatus, historyStatus, typeSafeStatus atomic.Int32
	accountID                                    atomic.Value
}

func newFakeServices(t *testing.T) (*fakeServices, *integrations.Client) {
	services := &fakeServices{}
	services.summaryStatus.Store(200)
	services.historyStatus.Store(200)
	services.typeSafeStatus.Store(200)
	services.accountID.Store("12345678901234")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/t212/equity/account/summary":
			if key, secret, ok := r.BasicAuth(); !ok || key != syntheticKey || secret != syntheticSecret {
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(int(services.summaryStatus.Load()))
			w.Write([]byte(`{"id":` + services.accountID.Load().(string) + `,"currency":"EUR"}`))
		case "/t212/equity/history/transactions":
			w.WriteHeader(int(services.historyStatus.Load()))
			w.Write([]byte(`{"items":[]}`))
		case "/t212/equity/history/exports":
			w.Write([]byte(`[]`))
		case "/typesafe/models":
			w.WriteHeader(int(services.typeSafeStatus.Load()))
			w.Write([]byte(`{"models":[{"name":"` + integrations.JevModel + `"}]}`))
		case "/resend/domains":
			w.Write([]byte(`{"object":"list","has_more":false,"data":[{"name":"example.com","status":"verified"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client := integrations.NewClient()
	client.Trading212Live, client.Trading212Demo = server.URL+"/t212", server.URL+"/t212"
	client.TypeSafe, client.Resend = server.URL+"/typesafe", server.URL+"/resend"
	return services, client
}

func assertNoSecrets(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	body := response.Body.String()
	if strings.Contains(body, syntheticKey) || strings.Contains(body, syntheticSecret) {
		t.Fatalf("response exposed a credential: %s", body)
	}
}

func TestIntegrationSetupAndChecks(t *testing.T) {
	services, client := newFakeServices(t)
	now := time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC)
	app := newTestApp(t, Options{Integrations: client, Now: func() time.Time { return now }})
	cookie := app.setup()
	trading := map[string]any{"environment": "demo", "apiKey": syntheticKey, "apiSecret": syntheticSecret, "accountName": "Broker", "enabled": true}

	services.historyStatus.Store(403)
	partial := app.request("PUT", "/api/integrations/trading212", trading, cookie)
	assertNoSecrets(t, partial)
	if partial.Code != http.StatusUnprocessableEntity || decode[checkError](t, partial).Check.Status != integrations.StatusPartial {
		t.Fatalf("enabling without history permission: %d %s", partial.Code, partial.Body)
	}
	trading["enabled"] = false
	if partial := app.request("PUT", "/api/integrations/trading212", trading, cookie); partial.Code != http.StatusUnprocessableEntity {
		t.Fatalf("interest is read by default and needs the missing permission: %d %s", partial.Code, partial.Body)
	}
	// Turning off the part that needs the missing permission allows the save.
	trading["sync"] = map[string]bool{"portfolio": true, "pies": true, "dividends": true, "interest": false, "cashback": true}
	saved := app.request("PUT", "/api/integrations/trading212", trading, cookie)
	assertNoSecrets(t, saved)
	view := decode[integrationView](t, saved)
	if saved.Code != http.StatusOK || view.Enabled || view.Check.Status != integrations.StatusPartial || view.EarliestAutomaticPeriod != "" ||
		view.Sync == nil || view.Sync.Interest || !view.Sync.Dividends {
		t.Fatalf("saving disabled partial integration: %d %s", saved.Code, saved.Body)
	}
	delete(trading, "sync")

	services.historyStatus.Store(200)
	trading["enabled"], trading["apiKey"], trading["apiSecret"] = true, "", ""
	enabled := app.request("PUT", "/api/integrations/trading212", trading, cookie)
	assertNoSecrets(t, enabled)
	view = decode[integrationView](t, enabled)
	// 23:30 UTC on 30 September is already October in Bratislava.
	if enabled.Code != http.StatusOK || !view.Enabled || view.EarliestAutomaticPeriod != "2026-10" || view.Account == nil ||
		view.Account.Name != "Broker" || view.Account.Currency != "EUR" || !view.APISecretConfigured || view.APIKeyHint != "…abcd" ||
		view.Sync == nil || view.Sync.Interest {
		t.Fatalf("enabled integration: %d %s", enabled.Code, enabled.Body)
	}
	now = now.AddDate(0, 2, 0)
	signIn := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil)
	cookie = sessionCookie(t, signIn)
	trading["accountName"] = ""
	view = decode[integrationView](t, app.request("PUT", "/api/integrations/trading212", trading, cookie))
	if view.EarliestAutomaticPeriod != "2026-10" || view.Account.Name != "Broker" {
		t.Fatalf("re-saving must keep the first automatic period and the name: %+v", view)
	}

	services.accountID.Store("999")
	checked := app.request("POST", "/api/integrations/trading212/check", nil, cookie)
	if view := decode[integrationView](t, checked); checked.Code != http.StatusOK || view.Check.Status != integrations.StatusPartial {
		t.Fatalf("check with a key for another account: %s", checked.Body)
	}
	services.accountID.Store("12345678901234")

	trading["apiKey"] = "synthetic-wrong-key-0000"
	if rejected := app.request("PUT", "/api/integrations/trading212", trading, cookie); rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected key saved: %d", rejected.Code)
	}
	var credentials string
	if err := app.store.DB.QueryRow("SELECT credentials_json FROM integrations WHERE provider = 'trading212'").Scan(&credentials); err != nil ||
		!strings.Contains(credentials, syntheticKey) {
		t.Fatal("a failed check replaced the stored credentials")
	}

	services.typeSafeStatus.Store(401)
	if response := app.request("PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected TypeSafe key: %d", response.Code)
	}
	services.typeSafeStatus.Store(200)
	if response := app.request("PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie); response.Code != http.StatusOK {
		t.Fatalf("TypeSafe: %d %s", response.Code, response.Body)
	}

	resendInput := map[string]any{"apiKey": syntheticKey, "sender": "Budget <budget@example.com>", "recipient": "owner@example.com",
		"applicationUrl": "https://budget.home.arpa/?next=x", "enabled": true}
	if response := app.request("PUT", "/api/integrations/resend", resendInput, cookie); response.Code != http.StatusUnprocessableEntity ||
		decode[errorBody](t, response).Fields["applicationUrl"] == "" {
		t.Fatalf("application URL with a query accepted: %d", response.Code)
	}
	resendInput["applicationUrl"] = "https://budget.home.arpa/"
	resendView := decode[integrationView](t, app.request("PUT", "/api/integrations/resend", resendInput, cookie))
	if !resendView.Enabled || resendView.ApplicationURL != "https://budget.home.arpa" || resendView.Sender != "Budget <budget@example.com>" {
		t.Fatalf("resend: %+v", resendView)
	}

	all := app.request("GET", "/api/integrations", nil, cookie)
	assertNoSecrets(t, all)
	if response := app.request("DELETE", "/api/integrations/trading212", nil, cookie); response.Code != http.StatusOK {
		t.Fatalf("remove: %d", response.Code)
	}
	app.request("DELETE", "/api/integrations/resend", nil, cookie)
	var notificationsEnabled int
	if err := app.store.DB.QueryRow("SELECT enabled FROM notification_config").Scan(&notificationsEnabled); err != nil || notificationsEnabled != 0 {
		t.Fatal("removing Resend must turn off notifications")
	}
	after := decode[map[string][]integrationView](t, app.request("GET", "/api/integrations", nil, cookie))["integrations"]
	if after[0].Configured || after[0].Enabled || after[0].Account == nil || after[0].EarliestAutomaticPeriod != "2026-10" || after[2].Configured {
		t.Fatalf("removed integrations: %+v", after)
	}
	accounts := decode[map[string][]accountView](t, app.request("GET", "/api/accounts", nil, cookie))["accounts"]
	if len(accounts) != 1 || accounts[0].Provider != "trading212" {
		t.Fatalf("source account must stay after removal: %+v", accounts)
	}
	if response := app.request("PUT", "/api/integrations/unknown", map[string]any{}, cookie); response.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", response.Code)
	}
}
