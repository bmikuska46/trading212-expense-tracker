package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"budget-tracking/backend/internal/auth"
)

func TestSessionLifecycleExposesRuntimeTurnstileSiteKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("secret") != syntheticSecret {
			t.Error("missing server secret in verification request")
		}
		w.Write([]byte(`{"success":true,"action":"login"}`))
	}))
	defer upstream.Close()
	verifier := auth.NewTurnstile(syntheticSecret)
	verifier.Endpoint = upstream.URL
	app := newTestApp(t, Options{TurnstileSiteKey: "runtime-site-key", Turnstile: verifier})
	check := func(response *httptest.ResponseRecorder, status int, setup, authenticated bool) {
		t.Helper()
		assertNoSecrets(t, response)
		if response.Code != status {
			t.Fatalf("session response: %d %s", response.Code, response.Body)
		}
		view := decode[sessionView](t, response)
		if view.TurnstileSiteKey != app.options.TurnstileSiteKey || view.SetupRequired != setup || view.Authenticated != authenticated {
			t.Fatalf("session configuration or state lost: %+v", view)
		}
		if strings.Contains(response.Body.String(), "turnstileSecretKey") {
			t.Fatal("session response included the secret key field")
		}
	}
	check(app.request("GET", "/api/session", nil, nil), http.StatusOK, true, false)
	created := app.request("POST", "/api/setup", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil)
	check(created, http.StatusCreated, false, true)
	cookie := sessionCookie(t, created)
	check(app.request("GET", "/api/session", nil, cookie), http.StatusOK, false, true)
	const newPassword = "replacement owner password"
	changed := app.request("POST", "/api/password", map[string]string{"currentPassword": ownerPassword, "newPassword": newPassword}, cookie)
	check(changed, http.StatusOK, false, true)
	if response := app.request("DELETE", "/api/session", nil, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("sign out: %d", response.Code)
	}
	check(app.request("GET", "/api/session", nil, cookie), http.StatusOK, false, false)
	signedIn := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": newPassword, "turnstileToken": "valid-token"}, nil)
	check(signedIn, http.StatusOK, false, true)
	cookie = sessionCookie(t, signedIn)
	app.options.TurnstileSiteKey = "replacement-runtime-site-key"
	app.restart()
	check(app.request("GET", "/api/session", nil, nil), http.StatusOK, false, false)
	check(app.request("GET", "/api/session", nil, cookie), http.StatusOK, false, true)
}

func TestSignInRequiresFreshTurnstileVerification(t *testing.T) {
	used := map[string]bool{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("secret") != syntheticSecret {
			t.Error("missing server secret in verification request")
		}
		token := r.Form.Get("response")
		if token == "unavailable" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if token == "malformed" {
			w.Write([]byte("not JSON"))
			return
		}
		if token == "wrong-action" {
			w.Write([]byte(`{"success":true,"action":"signup"}`))
			return
		}
		if strings.HasPrefix(token, "valid-") && !used[token] {
			used[token] = true
			w.Write([]byte(`{"success":true,"action":"login"}`))
			return
		}
		w.Write([]byte(`{"success":false,"error-codes":["timeout-or-duplicate"]}`))
	}))
	defer upstream.Close()
	verifier := auth.NewTurnstile(syntheticSecret)
	verifier.Endpoint = upstream.URL
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Turnstile: verifier, Now: func() time.Time { return now }})
	setupCookie := app.setup()
	app.request("DELETE", "/api/session", nil, setupCookie)
	input := map[string]string{"email": ownerEmail, "password": ownerPassword}
	for _, token := range []string{"", strings.Repeat("x", 2049), "invalid", "expired", "wrong-action", "unavailable", "malformed"} {
		now = now.Add(3 * time.Second)
		input["turnstileToken"] = token
		response := app.request("POST", "/api/session", input, nil)
		want := http.StatusForbidden
		if token == "unavailable" || token == "malformed" {
			want = http.StatusServiceUnavailable
		}
		assertNoSecrets(t, response)
		if response.Code != want || len(response.Result().Cookies()) != 0 {
			t.Fatalf("verification failure issued a session: %d", response.Code)
		}
	}
	var sessions int
	if err := app.store.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("verification failures changed sessions: %d, %v", sessions, err)
	}
	input["turnstileToken"] = "valid-login"
	response := app.request("POST", "/api/session", input, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("verified login: %d %s", response.Code, response.Body)
	}
	cookie := sessionCookie(t, response)
	if view := decode[sessionView](t, app.request("GET", "/api/session", nil, cookie)); !view.Authenticated {
		t.Fatal("verified login did not authenticate")
	}
	if replay := app.request("POST", "/api/session", input, nil); replay.Code != http.StatusForbidden || len(replay.Result().Cookies()) != 0 {
		t.Fatal("replayed token authenticated")
	}
	input["turnstileToken"], input["password"] = "valid-wrong-password", "incorrect-password"
	if response := app.request("POST", "/api/session", input, nil); response.Code != http.StatusUnauthorized {
		t.Fatal("Turnstile verification bypassed the password check")
	}
	input["password"] = ownerPassword
	if response := app.request("POST", "/api/session", input, nil); response.Code != http.StatusForbidden {
		t.Fatal("failed password attempt left the token reusable")
	}
	input["turnstileToken"] = "valid-retry"
	now = now.Add(3 * time.Second)
	if response := app.request("POST", "/api/session", input, nil); response.Code != http.StatusOK {
		t.Fatal("fresh token could not retry after an incorrect password")
	}
}
