package auth

import (
	"net/netip"
	"sync"
	"time"
)

const (
	clientInterval = 3 * time.Second // 20 requests per minute.
	clientBurst    = 5
	globalInterval = 500 * time.Millisecond // 120 requests per minute.
	globalBurst    = 20
	clientIdle     = 2 * time.Minute
	maxClients     = 4096
)

type loginBucket struct {
	credit  time.Duration
	updated time.Time
}

func (b *loginBucket) refill(now time.Time, interval time.Duration, capacity int) {
	if now.After(b.updated) {
		maximum := time.Duration(capacity) * interval
		b.credit = min(maximum, b.credit+min(maximum, now.Sub(b.updated)))
		b.updated = now
	}
}

func (b loginBucket) wait(interval time.Duration) time.Duration {
	if b.credit >= interval {
		return 0
	}
	return interval - b.credit
}

// LoginLimiter charges every login request before JSON, Turnstile, or hashing.
// Successful logins do not reset request budgets.
type LoginLimiter struct {
	mu        sync.Mutex
	global    loginBucket
	clients   map[netip.Addr]*loginBucket
	cleanupAt time.Time
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{global: loginBucket{credit: globalBurst * globalInterval}, clients: make(map[netip.Addr]*loginBucket)}
}

func (l *LoginLimiter) Allow(now time.Time, client netip.Addr) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.cleanupAt) {
		for key, bucket := range l.clients {
			if now.Sub(bucket.updated) >= clientIdle {
				delete(l.clients, key)
			}
		}
		l.cleanupAt = now.Add(time.Minute)
	}
	l.global.refill(now, globalInterval, globalBurst)
	if wait := l.global.wait(globalInterval); wait > 0 {
		return wait
	}
	client = client.Unmap()
	bucket := l.clients[client]
	if bucket == nil {
		if len(l.clients) >= maxClients {
			return time.Minute
		}
		bucket = &loginBucket{credit: clientBurst * clientInterval, updated: now}
		l.clients[client] = bucket
	}
	bucket.refill(now, clientInterval, clientBurst)
	if wait := bucket.wait(clientInterval); wait > 0 {
		return wait
	}
	bucket.credit -= clientInterval
	l.global.credit -= globalInterval
	return 0
}
