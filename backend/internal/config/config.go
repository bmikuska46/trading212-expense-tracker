package config

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ListenAddress      string
	DatabasePath       string
	KeyFile            string
	WebDirectory       string
	TurnstileSiteKey   string
	TurnstileSecretKey string
	TrustedProxies     []netip.Prefix
	// CookieSecure is "auto", "always", or "never". Auto marks the session
	// cookie Secure when the request arrived over HTTPS directly or through
	// a reverse proxy that sets X-Forwarded-Proto.
	CookieSecure string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:      value("APP_LISTEN_ADDR", "127.0.0.1:8080"),
		DatabasePath:       value("APP_DATABASE_PATH", ".local/data/budget.db"),
		KeyFile:            value("APP_DATABASE_KEY_FILE", ".local/secrets/database.key"),
		WebDirectory:       value("APP_WEB_DIR", "web/out"),
		CookieSecure:       value("APP_COOKIE_SECURE", "auto"),
		TurnstileSiteKey:   strings.TrimSpace(os.Getenv("APP_TURNSTILE_SITE_KEY")),
		TurnstileSecretKey: strings.TrimSpace(os.Getenv("APP_TURNSTILE_SECRET_KEY")),
	}
	if proxies := strings.TrimSpace(os.Getenv("APP_TRUSTED_PROXY_CIDRS")); proxies != "" {
		for _, raw := range strings.Split(proxies, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil || prefix.Addr().Is4In6() {
				return Config{}, errors.New("APP_TRUSTED_PROXY_CIDRS must be comma-separated IPv4 or IPv6 CIDRs")
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
		}
	}
	switch cfg.CookieSecure {
	case "auto", "always", "never":
	default:
		return Config{}, errors.New("APP_COOKIE_SECURE must be auto, always, or never")
	}
	if _, _, err := net.SplitHostPort(cfg.ListenAddress); err != nil {
		return Config{}, errors.New("APP_LISTEN_ADDR must be a host:port address")
	}
	databasePath, err := filepath.Abs(cfg.DatabasePath)
	if err != nil {
		return Config{}, errors.New("invalid database path")
	}
	keyPath, err := filepath.Abs(cfg.KeyFile)
	if err != nil {
		return Config{}, errors.New("invalid key file path")
	}
	relativeKey, err := filepath.Rel(filepath.Dir(databasePath), keyPath)
	if err != nil {
		return Config{}, errors.New("invalid key file path")
	}
	if relativeKey != ".." && !strings.HasPrefix(relativeKey, ".."+string(filepath.Separator)) {
		return Config{}, errors.New("mount the encryption key separately from the database directory")
	}
	return cfg, nil
}

func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
