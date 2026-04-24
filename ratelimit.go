package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const authLimiterMaxEntries = 10_000

type authLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	max      int
	block    time.Duration
	failures map[string][]time.Time
	blocked  map[string]time.Time
}

func newAuthLimiter(max int, window, block time.Duration) *authLimiter {
	return &authLimiter{
		window:   window,
		max:      max,
		block:    block,
		failures: make(map[string][]time.Time),
		blocked:  make(map[string]time.Time),
	}
}

func (l *authLimiter) allow(ip string) (bool, time.Duration) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if until, ok := l.blocked[ip]; ok {
		if now.Before(until) {
			return false, until.Sub(now)
		}
		delete(l.blocked, ip)
	}
	return true, 0
}

func (l *authLimiter) recordFailure(ip string) {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	times := l.failures[ip]
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	l.failures[ip] = kept
	if len(kept) >= l.max {
		l.blocked[ip] = now.Add(l.block)
		delete(l.failures, ip)
	}
	if len(l.failures)+len(l.blocked) > authLimiterMaxEntries {
		l.failures = make(map[string][]time.Time)
		l.blocked = make(map[string]time.Time)
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func authLimiterFromEnv(getenv func(string) string) *authLimiter {
	limit := 10
	if v := getenv("AUTH_FAIL_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	block := 15 * time.Minute
	if v := getenv("AUTH_BLOCK_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			block = time.Duration(n) * time.Minute
		}
	}
	return newAuthLimiter(limit, time.Minute, block)
}
