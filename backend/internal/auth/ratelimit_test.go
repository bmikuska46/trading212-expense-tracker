package auth

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginLimiterClientBurstRefillAndAddressNormalization(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter()
	client := netip.MustParseAddr("192.0.2.1")
	for range clientBurst {
		if wait := limiter.Allow(now, client); wait != 0 {
			t.Fatalf("burst request denied: %v", wait)
		}
	}
	if wait := limiter.Allow(now, netip.MustParseAddr("::ffff:192.0.2.1")); wait != clientInterval {
		t.Fatalf("mapped IPv4 bypassed limit: %v", wait)
	}
	if wait := limiter.Allow(now.Add(time.Second), client); wait != 2*time.Second {
		t.Fatalf("partial refill: %v", wait)
	}
	if wait := limiter.Allow(now.Add(clientInterval), client); wait != 0 {
		t.Fatalf("refilled token denied: %v", wait)
	}
	if wait := limiter.Allow(now, netip.MustParseAddr("192.0.2.2")); wait != 0 {
		t.Fatal("another client was blocked by the first client's quota")
	}
}

func TestLoginLimiterGlobalLimitAndIdleCleanup(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter()
	for i := range globalBurst {
		if limiter.Allow(now, netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i+1))) != 0 {
			t.Fatal("global burst denied too early")
		}
	}
	other := netip.MustParseAddr("198.51.100.1")
	if wait := limiter.Allow(now, other); wait != globalInterval {
		t.Fatalf("rotating addresses bypassed global limit: %v", wait)
	}
	if wait := limiter.Allow(now.Add(globalInterval), other); wait != 0 {
		t.Fatalf("global token did not refill: %v", wait)
	}
	if wait := limiter.Allow(now.Add(clientIdle+time.Minute), other); wait != 0 || len(limiter.clients) != 1 {
		t.Fatal("idle clients were not removed")
	}
}

func TestLoginLimiterConcurrentRequests(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter()
	var allowed atomic.Int32
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if limiter.Allow(now, netip.MustParseAddr("192.0.2.1")) == 0 {
				allowed.Add(1)
			}
		})
	}
	group.Wait()
	if allowed.Load() != clientBurst {
		t.Fatalf("concurrent requests bypassed burst: %d", allowed.Load())
	}
}

func TestThrottleIsolationNormalizationAndIdleRecovery(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	var throttle Throttle
	email := "owner@example.com"
	for range freeFailures {
		if throttle.Begin(now, email) != 0 {
			t.Fatal("account blocked before threshold")
		}
		throttle.End(now, email, LoginFailed)
	}
	if wait := throttle.Begin(now, " OWNER@EXAMPLE.COM "); wait != time.Second {
		t.Fatal("email whitespace or case bypassed lockout")
	}
	if throttle.Begin(now, "member@example.com") != 0 {
		t.Fatal("owner's failures blocked an unrelated account")
	}
	throttle.End(now, "member@example.com", LoginSucceeded)
	if throttle.Begin(now, email) == 0 {
		t.Fatal("another account's success reset owner's failures")
	}
	now = now.Add(accountIdle)
	if throttle.Begin(now, email) != 0 {
		t.Fatal("account did not recover after inactivity")
	}
	throttle.End(now, email, LoginFailed)
	if throttle.Begin(now, email) != 0 {
		t.Fatal("old failures survived inactivity")
	}
	throttle.End(now, email, LoginUnchecked)
}

func TestThrottleSerializesAccountAttemptsAndBoundsMemory(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	var throttle Throttle
	var allowed atomic.Int32
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if throttle.Begin(now, "owner@example.com") == 0 {
				allowed.Add(1)
			}
		})
	}
	group.Wait()
	if allowed.Load() != 1 {
		t.Fatal("parallel guesses were admitted")
	}
	// Active attempts remain reserved even if the idle cleanup time passes.
	if throttle.Begin(now.Add(accountIdle), "owner@example.com") == 0 {
		t.Fatal("cleanup removed an active reservation")
	}
	throttle.End(now, "owner@example.com", LoginUnchecked)
	for i := range maxAccounts - 1 {
		email := fmt.Sprintf("user%d@example.com", i)
		if throttle.Begin(now, email) != 0 {
			t.Fatal("account capacity filled too early")
		}
		throttle.End(now, email, LoginUnchecked)
	}
	if throttle.Begin(now, "overflow@example.com") == 0 || len(throttle.accounts) != maxAccounts {
		t.Fatal("account cache exceeded its limit")
	}
	if throttle.Begin(now.Add(accountIdle+time.Minute), "overflow@example.com") != 0 || len(throttle.accounts) != 1 {
		t.Fatal("expired account cache did not recover")
	}
}
