package selfhost

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// throttle tracks per-IP rate limiting state for authentication failures
// and share-token access failures, each with its own bucket. It supports
// bounded memory via periodic eviction of stale entries.
type throttle struct {
	mu  sync.Mutex
	ips map[string]*ipState

	// Configuration for the auth-failure bucket (looser).
	authWindow time.Duration
	authMax    int
	authBlock  time.Duration

	// Configuration for the share-token bucket (stricter).
	shareWindow time.Duration
	shareMax    int
	shareBlock  time.Duration

	// Configuration for the AI-request bucket (generous but bounded, so a
	// runaway client cannot pin the model).
	aiWindow time.Duration
	aiMax    int
	aiBlock  time.Duration

	// Housekeeping.
	maxEntries      int
	lastCleanup     time.Time
	cleanupInterval time.Duration
	staleTimeout    time.Duration
}

// ipState holds the per-bucket rate-limiting counters for a single source IP.
type ipState struct {
	auth struct {
		count        int
		windowStart  time.Time
		blockedUntil time.Time
	}
	share struct {
		count        int
		windowStart  time.Time
		blockedUntil time.Time
	}
	ai struct {
		count        int
		windowStart  time.Time
		blockedUntil time.Time
	}
	lastSeen time.Time
}

func newThrottle() *throttle {
	return &throttle{
		ips:             make(map[string]*ipState),
		authWindow:      10 * time.Second,
		authMax:         5,
		authBlock:       30 * time.Second,
		shareWindow:     30 * time.Second,
		shareMax:        3,
		shareBlock:      60 * time.Second,
		aiWindow:        30 * time.Second,
		aiMax:           12,
		aiBlock:         60 * time.Second,
		maxEntries:      5000,
		cleanupInterval: 5 * time.Minute,
		staleTimeout:    10 * time.Minute,
	}
}

// recordAuthFailure records a failed authentication attempt from ip.
// Returns (allowed, retryAfter). When allowed is false the caller must
// respond with 429 and the Retry-After header set to retryAfter.
func (t *throttle) recordAuthFailure(ip string) (bool, time.Duration) {
	return t.record(ip, t.authWindow, t.authMax, t.authBlock, "auth")
}

// recordShareFailure records a failed share-token lookup from ip.
// Returns (allowed, retryAfter). When allowed is false the caller must
// respond with 429 and the Retry-After header set to retryAfter.
func (t *throttle) recordShareFailure(ip string) (bool, time.Duration) {
	return t.record(ip, t.shareWindow, t.shareMax, t.shareBlock, "share")
}

// recordAIRequest records a successful AI request from ip against the AI
// bucket. Returns (allowed, retryAfter); when allowed is false the caller
// must respond with 429 and the Retry-After header set to retryAfter.
func (t *throttle) recordAIRequest(ip string) (bool, time.Duration) {
	return t.record(ip, t.aiWindow, t.aiMax, t.aiBlock, "ai")
}

func (t *throttle) record(ip string, window time.Duration, max int, block time.Duration, kind string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cleanupLocked()

	st, ok := t.ips[ip]
	if !ok {
		if len(t.ips) >= t.maxEntries {
			// Map is full — silently drop the oldest entry to stay bounded.
			var oldestIP string
			var oldestTime time.Time
			for k, v := range t.ips {
				if oldestIP == "" || v.lastSeen.Before(oldestTime) {
					oldestIP = k
					oldestTime = v.lastSeen
				}
			}
			delete(t.ips, oldestIP)
		}
		st = &ipState{}
		t.ips[ip] = st
	}
	st.lastSeen = time.Now()

	var count *int
	var windowStart *time.Time
	var blockedUntil *time.Time

	switch kind {
	case "auth":
		count = &st.auth.count
		windowStart = &st.auth.windowStart
		blockedUntil = &st.auth.blockedUntil
	case "share":
		count = &st.share.count
		windowStart = &st.share.windowStart
		blockedUntil = &st.share.blockedUntil
	case "ai":
		count = &st.ai.count
		windowStart = &st.ai.windowStart
		blockedUntil = &st.ai.blockedUntil
	default:
		return true, 0
	}

	now := time.Now()

	// If currently blocked, reject.
	if blockedUntil.After(now) {
		retryAfter := blockedUntil.Sub(now)
		return false, retryAfter
	}

	// Reset the failure window if it has expired.
	if windowStart.IsZero() || now.Sub(*windowStart) > window {
		*count = 0
		*windowStart = now
	}

	*count++

	if *count >= max {
		*blockedUntil = now.Add(block)
		slog.Warn("rate limit triggered — source blocked",
			"ip", ip, "kind", kind, "failures", *count,
			"blocked_until", blockedUntil.UTC().Format(time.RFC3339))
		return false, block
	}

	// Log a warning when failures reach half the threshold so operators
	// can spot scanning behaviour before a hard block engages.
	if *count >= max/2 {
		slog.Warn("repeated failures approaching rate limit",
			"ip", ip, "kind", kind, "failures", *count, "threshold", max)
	}

	return true, 0
}

// cleanupLocked removes entries that have been idle longer than
// staleTimeout. Must be called with t.mu held.
func (t *throttle) cleanupLocked() {
	now := time.Now()
	if now.Sub(t.lastCleanup) < t.cleanupInterval {
		return
	}
	t.lastCleanup = now
	for ip, st := range t.ips {
		if now.Sub(st.lastSeen) > t.staleTimeout {
			delete(t.ips, ip)
		}
	}
}

// clientIP extracts the best-guess client IP from an HTTP request. It
// respects the X-Forwarded-For and X-Real-IP headers for deployments
// behind a reverse proxy, and falls back to r.RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// retryAfterSeconds returns the number of seconds (rounded up) for a
// Retry-After header value as an integer string.
func retryAfterSeconds(d time.Duration) string {
	secs := int64(d.Seconds())
	if d > time.Duration(secs)*time.Second {
		secs++
	}
	return fmt.Sprintf("%d", secs)
}
