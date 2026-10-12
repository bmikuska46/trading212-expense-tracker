package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"budget-tracking/backend/internal/auth"
)

func loginRequest(handler http.Handler, peer, forwarded, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/api/session", strings.NewReader(body))
	request.RemoteAddr = peer
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", forwarded)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestLoginRequestLimitRunsBeforeTurnstileAndJSON(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"Turnstile failure", `{"email":"owner@example.com","password":"wrong","turnstileToken":"token"}`, 503},
		{"malformed JSON", "{", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer upstream.Close()
			verifier := auth.NewTurnstile(syntheticSecret)
			verifier.Endpoint = upstream.URL
			now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
			app := newTestApp(t, Options{Turnstile: verifier, Now: func() time.Time { return now }})
			for i := range 5 {
				response := loginRequest(app.handler, "192.0.2.1:1000", fmt.Sprintf("198.51.100.%d", i), test.body)
				if response.Code != test.status {
					t.Fatalf("attempt %d: %d", i, response.Code)
				}
			}
			before := calls.Load()
			limited := loginRequest(app.handler, "192.0.2.1:2000", "198.51.100.99", test.body)
			if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "3" || calls.Load() != before {
				t.Fatal("spoofed headers or source ports bypassed the request limit")
			}
			if len(limited.Result().Cookies()) != 0 || limited.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("rate limit response issued a cookie or was cacheable")
			}
			now = now.Add(3 * time.Second)
			if response := loginRequest(app.handler, "192.0.2.1:1000", "", test.body); response.Code != test.status {
				t.Fatal("request did not recover after Retry-After")
			}
		})
	}
}

func TestLoginGlobalLimitAndTrustedProxyIsolation(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now },
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")}})
	for i := range 20 {
		// Distinct clients behind one trusted proxy still hit the global cap.
		if response := loginRequest(app.handler, "10.0.0.2:1000", fmt.Sprintf("192.0.2.%d", i+1), "{"); response.Code != 400 {
			t.Fatalf("client isolation failed at request %d: %d", i, response.Code)
		}
	}
	response := loginRequest(app.handler, "10.0.0.2:1000", "192.0.2.99", "{")
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatal("rotating client IPs bypassed global budget or Retry-After rounded down")
	}
}

func TestLoginSuccessDoesNotResetIPBudget(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	app.setup()
	input := map[string]string{"email": ownerEmail, "password": ownerPassword}
	for range 5 {
		if response := app.request("POST", "/api/session", input, nil); response.Code != http.StatusOK {
			t.Fatalf("login: %d", response.Code)
		}
	}
	if response := app.request("POST", "/api/session", input, nil); response.Code != http.StatusTooManyRequests {
		t.Fatal("a valid account reset the IP budget")
	}
}

func TestLoginFailuresOnlyThrottleTheTargetAccount(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	app.setup()
	body := `{"email":"unknown@example.com","password":"wrong"}`
	for i := range 5 {
		response := loginRequest(app.handler, fmt.Sprintf("192.0.2.%d:1000", i+1), "", body)
		if response.Code != http.StatusUnauthorized {
			t.Fatal("initial failure rejected too early")
		}
	}
	response := loginRequest(app.handler, "198.51.100.1:1000", "", `{"email":" UNKNOWN@EXAMPLE.COM ","password":"wrong"}`)
	if response.Code != http.StatusTooManyRequests {
		t.Fatal("IP rotation or email casing bypassed account throttle")
	}
	input := map[string]string{"email": ownerEmail, "password": ownerPassword}
	if response := app.request("POST", "/api/session", input, nil); response.Code != http.StatusOK {
		t.Fatal("failed guesses against another email blocked a real user")
	}
	if response := loginRequest(app.handler, "198.51.100.2:1000", "", body); response.Code != http.StatusTooManyRequests {
		t.Fatal("successful login on another account reset target's delay")
	}
}
