// Package ratelimit provides token-bucket rate limiting with an in-memory limiter for
// single-process use and a PostgreSQL limiter shared by all API instances (ADR-021: no
// Valkey). Keys never contain sensitive identifiers.
package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"
)

// Policy is a token bucket: PerMinute sustained rate with Burst capacity.
type Policy struct {
	PerMinute int
	Burst     int
}

func (p Policy) ratePerSecond() float64 { return float64(p.PerMinute) / 60 }

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration // zero when allowed
}

// Limiter decides whether one more request under key is allowed.
type Limiter interface {
	Allow(ctx context.Context, key string, p Policy) (Decision, error)
}

// apply refills a bucket observed at (tokens, updatedAt) to now and consumes one token
// when possible. It is the single source of bucket arithmetic for both limiters.
func apply(tokens float64, updatedAt, now time.Time, p Policy) (newTokens float64, d Decision) {
	burst := float64(p.Burst)
	elapsed := now.Sub(updatedAt).Seconds()
	if elapsed > 0 {
		tokens = math.Min(burst, tokens+elapsed*p.ratePerSecond())
	}
	if tokens >= 1 {
		tokens--
		return tokens, Decision{Allowed: true, Remaining: int(math.Floor(tokens))}
	}
	wait := (1 - tokens) / p.ratePerSecond()
	retry := time.Duration(math.Ceil(wait*1000)) * time.Millisecond
	if retry < time.Second {
		retry = time.Second
	}
	return tokens, Decision{Allowed: false, Remaining: 0, RetryAfter: retry}
}

// Memory is a process-local limiter for tests and single-instance deployments.
type Memory struct {
	mu      sync.Mutex
	buckets map[string]*memBucket
	now     func() time.Time
}

type memBucket struct {
	tokens    float64
	updatedAt time.Time
}

// NewMemory creates a limiter; now may be nil for the wall clock.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{buckets: map[string]*memBucket{}, now: now}
}

// Allow implements Limiter.
func (m *Memory) Allow(_ context.Context, key string, p Policy) (Decision, error) {
	if p.Burst <= 0 || p.PerMinute <= 0 {
		return Decision{Allowed: true}, nil
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[key]
	if !ok {
		b = &memBucket{tokens: float64(p.Burst), updatedAt: now}
		m.buckets[key] = b
	}
	tokens, d := apply(b.tokens, b.updatedAt, now, p)
	b.tokens, b.updatedAt = tokens, now
	return d, nil
}

// Purge drops buckets idle for longer than idle.
func (m *Memory) Purge(idle time.Duration) int {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, b := range m.buckets {
		if now.Sub(b.updatedAt) > idle {
			delete(m.buckets, k)
			n++
		}
	}
	return n
}
