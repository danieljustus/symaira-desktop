package selfhost

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testThrottle returns a throttle with short windows suitable for unit
// tests — no real wall-clock delays needed for most assertions.
func testThrottle(authMax int, authWindow, authBlock time.Duration, shareMax int, shareWindow, shareBlock time.Duration) *throttle {
	return &throttle{
		ips:             make(map[string]*ipState),
		authWindow:      authWindow,
		authMax:         authMax,
		authBlock:       authBlock,
		shareWindow:     shareWindow,
		shareMax:        shareMax,
		shareBlock:      shareBlock,
		maxEntries:      100,
		cleanupInterval: time.Hour,
		staleTimeout:    time.Hour,
	}
}

func TestThrottleRecordsAuthFailures(t *testing.T) {
	t.Parallel()
	th := testThrottle(3, 5*time.Second, 30*time.Second, 5, 10*time.Second, 60*time.Second)

	ip := "10.0.0.1"

	// First two failures should be allowed through.
	for i := 0; i < 2; i++ {
		allowed, _ := th.recordAuthFailure(ip)
		if !allowed {
			t.Fatalf("failure %d should be allowed", i+1)
		}
	}

	// Third failure should trigger a block.
	allowed, retryAfter := th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("third failure should be blocked")
	}
	if retryAfter <= 0 || retryAfter > 30*time.Second {
		t.Fatalf("retryAfter out of range: %v", retryAfter)
	}

	// Subsequent attempts should remain blocked.
	allowed, _ = th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("should still be blocked after threshold hit")
	}
}

func TestThrottleSuccessfulAuthUnaffected(t *testing.T) {
	t.Parallel()
	th := testThrottle(5, 10*time.Second, 30*time.Second, 3, 30*time.Second, 60*time.Second)

	ip := "10.0.0.2"

	// recordAuthFailure is only called for failures; there is no separate
	// "record success" path. This test verifies the negative: calling
	// recordAuthFailure only when auth actually fails, and that many
	// failures do hit the limit. The successful-auth path is verified
	// end-to-end in TestServerAuthRateLimitEnforced.
	for i := 0; i < 4; i++ {
		allowed, _ := th.recordAuthFailure(ip)
		if !allowed {
			t.Fatalf("failure %d should be allowed (max=5)", i+1)
		}
	}
	allowed, _ := th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("fifth failure should be blocked")
	}
}

func TestThrottleShareStricter(t *testing.T) {
	t.Parallel()
	th := testThrottle(5, 10*time.Second, 30*time.Second, 2, 30*time.Second, 60*time.Second)

	ip := "10.0.0.3"

	// First share failure should be allowed.
	allowed, _ := th.recordShareFailure(ip)
	if !allowed {
		t.Fatal("first share failure should be allowed")
	}

	// Second share failure should trigger a block (stricter: max=2).
	allowed, _ = th.recordShareFailure(ip)
	if allowed {
		t.Fatal("second share failure should be blocked (stricter bucket)")
	}
}

func TestThrottleWindowReset(t *testing.T) {
	t.Parallel()
	// Window: 100ms, max=3, block=30s. Hit 2 failures (below max),
	// wait for window to expire, then verify the count resets.
	th := testThrottle(3, 100*time.Millisecond, 30*time.Second, 3, 200*time.Millisecond, 60*time.Second)

	ip := "10.0.0.4"

	// Two failures within the window — stays below the max.
	allowed, _ := th.recordAuthFailure(ip)
	if !allowed {
		t.Fatal("first failure should be allowed")
	}
	allowed, _ = th.recordAuthFailure(ip)
	if !allowed {
		t.Fatal("second failure should be allowed (max=3)")
	}

	// Wait for the window to expire.
	time.Sleep(150 * time.Millisecond)

	// Window should have reset — failure count restarts at 1.
	allowed, _ = th.recordAuthFailure(ip)
	if !allowed {
		t.Fatal("after window expiry, count should reset and first failure be allowed")
	}

	// Two more should be allowed (fresh window, count=1,2).
	allowed, _ = th.recordAuthFailure(ip)
	if !allowed {
		t.Fatal("second failure in new window should be allowed")
	}
	// Third hits the threshold.
	allowed, _ = th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("third failure in new window should be blocked")
	}
}

func TestThrottleBlockDuration(t *testing.T) {
	t.Parallel()
	th := testThrottle(1, 1*time.Second, 100*time.Millisecond, 1, 1*time.Second, 150*time.Millisecond)

	ip := "10.0.0.5"

	// First failure hits the limit immediately (max=1).
	allowed, _ := th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("first failure should be blocked (max=1)")
	}

	// Still blocked within the block window.
	allowed, _ = th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("should still be blocked within block window")
	}

	// Wait for the block to expire. The failure window (1s) is still
	// active, so the count hasn't reset and triggers another block.
	time.Sleep(150 * time.Millisecond)

	// Count still at threshold → another block is issued.
	allowed, _ = th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("failure count should still be >0 and trigger another block")
	}
}

func TestThrottleSeparateBuckets(t *testing.T) {
	t.Parallel()
	th := testThrottle(3, 10*time.Second, 30*time.Second, 2, 10*time.Second, 60*time.Second)

	ip := "10.0.0.6"

	// Exhaust auth bucket should not affect share bucket.
	for i := 0; i < 2; i++ {
		allowed, _ := th.recordAuthFailure(ip)
		if !allowed {
			t.Fatalf("auth failure %d should be allowed", i+1)
		}
	}
	// Third auth failure triggers block.
	allowed, _ := th.recordAuthFailure(ip)
	if allowed {
		t.Fatal("auth should be blocked after 3 failures")
	}

	// Share bucket should still be independent.
	allowed, _ = th.recordShareFailure(ip)
	if !allowed {
		t.Fatal("first share failure should be allowed despite auth block")
	}
	allowed, _ = th.recordShareFailure(ip)
	if allowed {
		t.Fatal("second share failure should be blocked")
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "RemoteAddr",
			remoteAddr: "10.0.0.7:12345",
			headers:    nil,
			want:       "10.0.0.7",
		},
		{
			name:       "X-Forwarded-For",
			remoteAddr: "127.0.0.1:8080",
			headers:    map[string]string{"X-Forwarded-For": "10.0.0.8"},
			want:       "10.0.0.8",
		},
		{
			name:       "X-Forwarded-For with chain",
			remoteAddr: "127.0.0.1:8080",
			headers:    map[string]string{"X-Forwarded-For": "10.0.0.9, 172.16.0.1"},
			want:       "10.0.0.9",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "127.0.0.1:8080",
			headers:    map[string]string{"X-Real-IP": "10.0.0.10"},
			want:       "10.0.0.10",
		},
		{
			name:       "X-Forwarded-For takes priority",
			remoteAddr: "127.0.0.1:8080",
			headers:    map[string]string{"X-Forwarded-For": "10.0.0.11", "X-Real-IP": "10.0.0.12"},
			want:       "10.0.0.11",
		},
		{
			name:       "IPv6",
			remoteAddr: "[::1]:12345",
			headers:    nil,
			want:       "::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.headers != nil {
				for k, v := range tt.headers {
					req.Header.Set(k, v)
				}
			}
			req.RemoteAddr = tt.remoteAddr
			got := clientIP(req)
			if got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0"},
		{500 * time.Millisecond, "1"},
		{1 * time.Second, "1"},
		{1*time.Second + 1*time.Nanosecond, "2"},
		{30 * time.Second, "30"},
		{59*time.Second + 500*time.Millisecond, "60"},
	}

	for _, tt := range tests {
		got := retryAfterSeconds(tt.d)
		if got != tt.want {
			t.Fatalf("retryAfterSeconds(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestThrottleBoundedMemory(t *testing.T) {
	t.Parallel()
	th := testThrottle(5, 10*time.Second, 30*time.Second, 3, 30*time.Second, 60*time.Second)
	th.maxEntries = 5
	th.staleTimeout = time.Millisecond
	th.cleanupInterval = time.Millisecond

	// Fill with 5 IPs.
	for i := 0; i < 5; i++ {
		th.recordAuthFailure(fmt.Sprintf("10.0.0.%d", i))
	}

	// Adding a sixth IP should evict the oldest.
	th.recordAuthFailure("10.0.0.100")

	// After eviction, we should still have at most 5 entries.
	th.mu.Lock()
	count := len(th.ips)
	th.mu.Unlock()
	if count > 5 {
		t.Fatalf("expected at most 5 entries, got %d", count)
	}
}

func TestServerAuthRateLimitEnforced(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Replace with a test throttle: max 2 auth failures in a 10s window.
	// First failure is 401; second failure hits the threshold and gets 429.
	server.throttle = testThrottle(2, 10*time.Second, 60*time.Second, 3, 30*time.Second, 60*time.Second)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	wrongAuth := func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/status", nil)
		req.Header.Set("Authorization", "Bearer bad-token-0123456789abcdef")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// First failure should return 401 (within limit).
	resp := wrongAuth()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := json.MarshalIndent(json.RawMessage(readBody(resp)), "", "  ")
		t.Fatalf("first failure: expected 401, got %d: %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	// Second failure hits the threshold (max=2) — should return 429.
	resp = wrongAuth()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := json.MarshalIndent(json.RawMessage(readBody(resp)), "", "  ")
		t.Fatalf("second failure: expected 429 after threshold, got %d: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 429 response")
	}
	_ = resp.Body.Close()

	// Successful auth should still work (unaffected by rate limit).
	goodResp := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/status", nil, "")
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("successful auth should be unaffected by rate limit, got %d", goodResp.StatusCode)
	}
	_ = goodResp.Body.Close()
}

func TestServerShareTokenRateLimitEnforced(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Replace with a test throttle: max 2 share failures in a 10s window.
	// First failure is 404; second failure hits the threshold and gets 429.
	server.throttle = testThrottle(5, 10*time.Second, 60*time.Second, 2, 10*time.Second, 60*time.Second)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	badShare := func() *http.Response {
		resp, err := http.Get(httpServer.URL + "/s/nonexistent-token-0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// First failure should return 404 (within limit).
	resp := badShare()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("first failure: expected 404, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Second failure hits the threshold (max=2) — should return 429.
	resp = badShare()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second failure: expected 429 after threshold, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 429 response")
	}
	_ = resp.Body.Close()
}

func TestServerShareTokenRateLimitReset(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Window: 200ms, max=2, block: 300ms.
	// Two rapid failures will trigger a block. After the block expires,
	// the window should have also expired (~200ms < 300ms block), so the
	// next failure should start a fresh count and return 404 again.
	server.throttle = testThrottle(5, 10*time.Second, 60*time.Second, 2, 200*time.Millisecond, 300*time.Millisecond)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	badShare := func() *http.Response {
		resp, err := http.Get(httpServer.URL + "/s/nonexistent-token-reset")
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// First failure: 404 (count=1).
	resp := badShare()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("first failure: expected 404, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Second failure: hits threshold (max=2) → 429.
	resp = badShare()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second failure: expected 429, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Third failure during block: should still be 429.
	resp = badShare()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third failure during block: expected 429, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Wait for block (300ms) to expire. By then the window (200ms) has
	// also expired, so the count resets.
	time.Sleep(350 * time.Millisecond)

	// After block + window expiry: 404 again (fresh window).
	resp = badShare()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after reset: expected 404, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestSuccessfulRequestsUnaffected(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Tight throttle: max 1 auth failure triggers block.
	server.throttle = testThrottle(1, 10*time.Second, 60*time.Second, 3, 30*time.Second, 60*time.Second)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	// Trigger the auth block with one bad request.
	req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer bad-token-0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Verify block is engaged.
	req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer bad-token-0123456789abcdef")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for blocked IP, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Successful auth should still work — a valid token bypasses the
	// rate limit entirely.
	goodResp := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/status", nil, "")
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("successful auth should be unaffected: got %d", goodResp.StatusCode)
	}
	_ = goodResp.Body.Close()
}
