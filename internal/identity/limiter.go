package identity

import (
	"sync"
	"time"
)

const maxLimiterEntries = 2048

type attempt struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
	updatedAt    time.Time
}

type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string]attempt
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{entries: make(map[string]attempt)}
}

func (l *attemptLimiter) Allowed(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return true
	}
	if now.After(entry.blockedUntil) && now.Sub(entry.windowStart) >= 10*time.Minute {
		delete(l.entries, key)
		return true
	}
	return now.After(entry.blockedUntil) || now.Equal(entry.blockedUntil)
}

func (l *attemptLimiter) Failed(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= 10*time.Minute {
		entry = attempt{windowStart: now}
	}
	entry.count++
	entry.updatedAt = now
	if entry.count >= 5 {
		entry.blockedUntil = now.Add(10 * time.Minute)
	}
	l.entries[key] = entry
	if len(l.entries) > maxLimiterEntries {
		l.evictOldest()
	}
}

func (l *attemptLimiter) Succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *attemptLimiter) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.entries {
		if oldestKey == "" || entry.updatedAt.Before(oldest) {
			oldestKey = key
			oldest = entry.updatedAt
		}
	}
	delete(l.entries, oldestKey)
}
