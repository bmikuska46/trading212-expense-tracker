package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	ctx := context.Background()
	password := "synthetic correct horse battery"
	hash, err := HashPassword(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(hash, password) {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	other, err := HashPassword(ctx, password)
	if err != nil || other == hash {
		t.Fatal("hashes must use a random salt")
	}
	if ok, err := VerifyPassword(ctx, hash, password); err != nil || !ok {
		t.Fatalf("correct password rejected: %v", err)
	}
	if ok, err := VerifyPassword(ctx, hash, password+"x"); err != nil || ok {
		t.Fatal("wrong password accepted")
	}
	for _, invalid := range []string{
		"", "plain", strings.Replace(hash, "argon2id", "argon2i", 1),
		strings.Replace(hash, "m=19456", "m=4194304", 1), strings.Replace(hash, "t=2", "t=0", 1),
		hash[:len(hash)-10],
	} {
		if _, err := VerifyPassword(ctx, invalid, password); err == nil {
			t.Errorf("invalid hash accepted: %q", invalid)
		}
	}
}

func TestSessionTokens(t *testing.T) {
	token, hash, err := NewSessionToken()
	if err != nil || len(hash) != 32 {
		t.Fatal(err)
	}
	actual, ok := SessionTokenHash(token)
	if !ok || string(actual) != string(hash) {
		t.Fatal("token hash mismatch")
	}
	for _, invalid := range []string{"", "short", token + "A", strings.Repeat("!", len(token))} {
		if _, ok := SessionTokenHash(invalid); ok {
			t.Errorf("invalid token accepted: %q", invalid)
		}
	}
}

func TestThrottleBacksOffAndResets(t *testing.T) {
	var throttle Throttle
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	email := "owner@example.com"
	for failure := 1; failure <= freeFailures+12; failure++ {
		if wait := throttle.Begin(now, email); wait != 0 {
			t.Fatalf("admitted attempt blocked: %v", wait)
		}
		throttle.End(now, email, LoginFailed)
		if failure < freeFailures {
			continue
		}
		want := min(time.Second<<(failure-freeFailures), maxLockout)
		if wait := throttle.Begin(now, email); wait != want {
			t.Fatalf("failure %d: wait=%v, want=%v", failure, wait, want)
		}
		now = now.Add(want)
	}
	if throttle.Begin(now, email) != 0 {
		t.Fatal("expired lockout did not release")
	}
	throttle.End(now, email, LoginSucceeded)
	if throttle.Begin(now, email) != 0 {
		t.Fatal("success did not reset account")
	}
	throttle.End(now, email, LoginUnchecked)
}

func TestValidation(t *testing.T) {
	for input, valid := range map[string]bool{
		"owner@example.com": true, " owner@example.com ": true, "Owner <owner@example.com>": false,
		"owner": false, "owner@localhost": false, "a@b@example.com": false, "": false,
	} {
		if _, ok := NormalizeEmail(input); ok != valid {
			t.Errorf("email %q: got %v", input, ok)
		}
	}
	for password, valid := range map[string]bool{
		"short": false, strings.Repeat(" ", 12): false, "twelve chars": true,
		strings.Repeat("é", 256): true, strings.Repeat("a", 257): false, "\xff\xfe invalid utf8": false,
	} {
		if (PasswordProblem(password) == "") != valid {
			t.Errorf("password %q: %s", password, PasswordProblem(password))
		}
	}
	for zone, valid := range map[string]bool{
		"Europe/Bratislava": true, "UTC": true, "America/New_York": true,
		"Local": false, "": false, "Mars/Olympus": false, "../etc/passwd": false,
	} {
		if ValidTimezone(zone) != valid {
			t.Errorf("timezone %q: expected %v", zone, valid)
		}
	}
}
