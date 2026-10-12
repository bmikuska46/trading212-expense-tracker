package auth

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

const (
	freeFailures = 5
	maxLockout   = 5 * time.Minute
	accountIdle  = 15 * time.Minute
	maxAccounts  = 4096
)

type LoginResult uint8

const (
	LoginUnchecked LoginResult = iota
	LoginFailed
	LoginSucceeded
)

type accountThrottle struct {
	failures int
	until    time.Time
	lastSeen time.Time
	inFlight bool
}

// Throttle tracks each email separately, including nonexistent accounts.
// Begin reserves an attempt atomically, so parallel guesses cannot bypass it.
type Throttle struct {
	mu        sync.Mutex
	accounts  map[[32]byte]*accountThrottle
	cleanupAt time.Time
}

func accountKey(email string) [32]byte {
	value := []byte(strings.TrimSpace(email))
	// Match SQLite's NOCASE comparison, which folds ASCII letters only.
	for i, c := range value {
		if c >= 'A' && c <= 'Z' {
			value[i] = c + ('a' - 'A')
		}
	}
	return sha256.Sum256(value)
}

func (t *Throttle) Begin(now time.Time, email string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.accounts == nil {
		t.accounts = make(map[[32]byte]*accountThrottle)
	}
	if !now.Before(t.cleanupAt) {
		for key, account := range t.accounts {
			if !account.inFlight && now.Sub(account.lastSeen) >= accountIdle {
				delete(t.accounts, key)
			}
		}
		t.cleanupAt = now.Add(time.Minute)
	}
	key := accountKey(email)
	account := t.accounts[key]
	if account != nil && !account.inFlight && now.Sub(account.lastSeen) >= accountIdle {
		delete(t.accounts, key)
		account = nil
	}
	if account == nil {
		if len(t.accounts) >= maxAccounts {
			return time.Minute
		}
		account = &accountThrottle{}
		t.accounts[key] = account
	}
	if now.Before(account.until) {
		return account.until.Sub(now)
	}
	if account.inFlight {
		return time.Second
	}
	account.inFlight = true
	account.lastSeen = now
	return 0
}

func (t *Throttle) End(now time.Time, email string, result LoginResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := accountKey(email)
	account := t.accounts[key]
	account.inFlight = false
	account.lastSeen = now
	switch result {
	case LoginSucceeded:
		delete(t.accounts, key)
	case LoginFailed:
		// Saturate the counter once the maximum delay has been reached.
		account.failures = min(account.failures+1, freeFailures+9)
		if account.failures >= freeFailures {
			account.until = now.Add(min(time.Second<<(account.failures-freeFailures), maxLockout))
		}
	}
}
