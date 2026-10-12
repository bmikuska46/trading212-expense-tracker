package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTurnstileVerification(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
		wantError  bool
	}{
		{"valid login", `{"success":true,"action":"login"}`, 200, true, false},
		{"invalid token", `{"success":false,"error-codes":["invalid-input-response"]}`, 200, false, false},
		{"expired or reused", `{"success":false,"error-codes":["timeout-or-duplicate"]}`, 200, false, false},
		{"wrong action", `{"success":true,"action":"signup"}`, 200, false, false},
		{"missing action", `{"success":true}`, 200, false, false},
		{"test action with real key", `{"success":true,"action":"test"}`, 200, false, false},
		{"missing success", `{"action":"login"}`, 200, false, false},
		{"malformed response", `not JSON`, 200, false, true},
		{"wrong success type", `{"success":"true","action":"login"}`, 200, false, true},
		{"extra JSON", `{"success":true,"action":"login"}{}`, 200, false, true},
		{"upstream error", `{"success":true,"action":"login"}`, 503, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil || r.Method != "POST" || r.Form.Get("secret") != "synthetic-secret" || r.Form.Get("response") != "synthetic-token" {
					t.Error("verification must POST the secret and token")
				}
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			}))
			defer upstream.Close()
			verifier := NewTurnstile("synthetic-secret")
			verifier.Endpoint = upstream.URL
			valid, err := verifier.Verify(context.Background(), "synthetic-token")
			if valid != test.valid || (err != nil) != test.wantError {
				t.Fatalf("valid=%v, err=%v", valid, err)
			}
		})
	}
}

func TestTurnstileRejectsMissingAndOversizedTokensWithoutRequests(t *testing.T) {
	verifier := NewTurnstile("synthetic-secret")
	verifier.Endpoint = ":invalid endpoint"
	for _, token := range []string{"", strings.Repeat("x", 2049)} {
		if valid, err := verifier.Verify(context.Background(), token); valid || err != nil {
			t.Fatalf("invalid token reached upstream: valid=%v, err=%v", valid, err)
		}
	}
	verifier.SecretKey = ""
	if valid, err := verifier.Verify(context.Background(), "token"); valid || err == nil {
		t.Fatal("missing secret did not fail closed")
	}
}

func TestTurnstileTimeoutAndCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	verifier := NewTurnstile("synthetic-secret")
	verifier.Endpoint = upstream.URL
	verifier.HTTP.Timeout = 20 * time.Millisecond
	if valid, err := verifier.Verify(context.Background(), "token"); valid || err == nil {
		t.Fatal("timeout did not fail closed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if valid, err := verifier.Verify(ctx, "token"); valid || err == nil {
		t.Fatal("cancellation did not fail closed")
	}
}

func TestTurnstilePublicTestKey(t *testing.T) {
	for _, body := range []string{`{"success":true,"action":"test"}`, `{"success":true}`} {
		t.Run(body, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			}))
			defer upstream.Close()
			verifier := NewTurnstile("1x0000000000000000000000000000000AA")
			verifier.Endpoint = upstream.URL
			if valid, err := verifier.Verify(context.Background(), "XXXX.DUMMY.TOKEN.XXXX"); !valid || err != nil {
				t.Fatalf("Cloudflare's public test key failed: %v", err)
			}
		})
	}
}
