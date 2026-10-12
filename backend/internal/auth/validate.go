package auth

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	// Embed the zone database so validation does not depend on host tzdata.
	_ "time/tzdata"
)

const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
	DefaultTimezone   = "Europe/Bratislava"
)

// NormalizeEmail returns the trimmed address when it is a single plain address.
func NormalizeEmail(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 254 {
		return "", false
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value || !strings.Contains(value[strings.LastIndex(value, "@")+1:], ".") {
		return "", false
	}
	return value, true
}

// PasswordProblem describes why a new password is unacceptable, or returns "".
func PasswordProblem(password string) string {
	if !utf8.ValidString(password) {
		return "Password contains invalid characters"
	}
	length := utf8.RuneCountInString(password)
	if length < MinPasswordLength {
		return "Use at least 12 characters"
	}
	if length > MaxPasswordLength {
		return "Use at most 256 characters"
	}
	if strings.TrimSpace(password) == "" {
		return "Password cannot be only spaces"
	}
	return ""
}

// ValidTimezone accepts IANA zone names. "Local" depends on the host, so it is rejected.
func ValidTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > 64 {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
