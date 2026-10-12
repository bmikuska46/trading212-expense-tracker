package main

import "testing"

func TestServerRequiresTurnstileSecret(t *testing.T) {
	for _, secret := range []string{"", "   "} {
		t.Setenv("APP_TURNSTILE_SECRET_KEY", secret)
		if err := run(); err == nil || err.Error() != "APP_TURNSTILE_SECRET_KEY is required" {
			t.Fatalf("server must refuse to serve login without a secret: %v", err)
		}
	}
}
