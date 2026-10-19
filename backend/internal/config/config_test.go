package config

import (
	"net/netip"
	"path/filepath"
	"testing"
)

func TestSettingsRemainServerSideAndKeyStaysOutsideData(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("APP_TURNSTILE_SECRET_KEY", "synthetic-turnstile-secret")
	t.Setenv("APP_LISTEN_ADDR", "127.0.0.1:8080")
	t.Setenv("APP_DATABASE_PATH", filepath.Join(directory, "data", "budget.db"))
	t.Setenv("APP_WEB_DIR", "web/out")
	for _, key := range []string{
		filepath.Join(directory, "data", "database.key"),
		filepath.Join(directory, "data", "secrets", "database.key"),
	} {
		t.Setenv("APP_DATABASE_KEY_FILE", key)
		if _, err := Load(); err == nil {
			t.Fatal("key accepted inside the database volume")
		}
	}
	t.Setenv("APP_DATABASE_KEY_FILE", filepath.Join(directory, "secrets", "database.key"))
	if cfg, err := Load(); err != nil || cfg.CookieSecure != "auto" || cfg.TurnstileSecretKey != "synthetic-turnstile-secret" {
		t.Fatal(err)
	}
	for mode, valid := range map[string]bool{"always": true, "never": true, "true": false, "yes": false} {
		t.Setenv("APP_COOKIE_SECURE", mode)
		if _, err := Load(); (err == nil) != valid {
			t.Errorf("APP_COOKIE_SECURE=%s: %v", mode, err)
		}
	}
	t.Setenv("APP_COOKIE_SECURE", "")
	t.Setenv("APP_LISTEN_ADDR", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid listener accepted")
	}
}

func TestTurnstileRuntimeKeys(t *testing.T) {
	t.Setenv("APP_TURNSTILE_SITE_KEY", "  configured-runtime-site-key\n")
	t.Setenv("APP_TURNSTILE_SECRET_KEY", "\tconfigured-runtime-secret  ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TurnstileSiteKey != "configured-runtime-site-key" || cfg.TurnstileSecretKey != "configured-runtime-secret" {
		t.Fatal("runtime Turnstile keys were not loaded and trimmed")
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	t.Setenv("APP_LISTEN_ADDR", "127.0.0.1:8080")
	t.Setenv("APP_COOKIE_SECURE", "auto")
	t.Setenv("APP_DATABASE_PATH", filepath.Join(t.TempDir(), "data", "budget.db"))
	t.Setenv("APP_DATABASE_KEY_FILE", filepath.Join(t.TempDir(), "database.key"))
	for _, input := range []string{"", "  ", "10.0.0.2/32, 2001:db8::1/64"} {
		t.Setenv("APP_TRUSTED_PROXY_CIDRS", input)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("valid trusted proxies rejected: %v", err)
		}
		if input == "10.0.0.2/32, 2001:db8::1/64" && (len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1] != netip.MustParsePrefix("2001:db8::/64")) {
			t.Fatal("trusted prefixes were not normalized")
		}
	}
	for _, input := range []string{"10.0.0.1", "all", "10.0.0.1/32,", "::ffff:192.0.2.1/128"} {
		t.Setenv("APP_TRUSTED_PROXY_CIDRS", input)
		if _, err := Load(); err == nil {
			t.Fatalf("invalid proxy range accepted: %q", input)
		}
	}
}
