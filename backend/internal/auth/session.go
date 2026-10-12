package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

const (
	SessionLifetime = 30 * 24 * time.Hour
	// Sessions used after half their lifetime are extended, so an owner who
	// keeps using the app stays signed in and an abandoned session expires.
	SessionRenewAfter = SessionLifetime / 2
	TimeLayout        = "2006-01-02T15:04:05Z"
	tokenBytes        = 32
)

// NewSessionToken returns the cookie value and the hash stored in the database.
// Only the hash is stored, so a database read does not reveal usable sessions.
func NewSessionToken() (string, []byte, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

// SessionTokenHash validates a cookie value and returns its stored hash.
func SessionTokenHash(token string) ([]byte, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

func FormatTime(value time.Time) string { return value.UTC().Format(TimeLayout) }
