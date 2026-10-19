package main

import (
	"os"
	"testing"
)

func TestServerRequiresTurnstileKeys(t *testing.T) {
	for _, key := range []string{"APP_TURNSTILE_SITE_KEY", "APP_TURNSTILE_SECRET_KEY"} {
		for _, input := range []struct{ name, value string }{{"missing", ""}, {"empty", ""}, {"blank", " \t\n "}} {
			t.Run(key+"/"+input.name, func(t *testing.T) {
				t.Setenv("APP_TURNSTILE_SITE_KEY", "synthetic-site-key")
				t.Setenv("APP_TURNSTILE_SECRET_KEY", "synthetic-secret")
				t.Setenv(key, input.value)
				if input.name == "missing" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
				if err := run(); err == nil || err.Error() != key+" is required" {
					t.Fatalf("server must refuse to start without %s: %v", key, err)
				}
			})
		}
	}
}
