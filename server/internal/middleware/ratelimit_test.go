package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redismock/v9"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRateLimit_NilRedis(t *testing.T) {
	mw := RateLimit(nil, 5, time.Minute, nil)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d", rec.Code)
	}
	for i := 0; i < 5; i++ {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req.Clone(req.Context()))
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("nil Redis must still enforce the limit; got %d", rec.Code)
	}
}

func TestRateLimit_MemoryConcurrent(t *testing.T) {
	var successes atomic.Int32
	handler := RateLimit(nil, 5, time.Minute, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
			req.RemoteAddr = "203.0.113.42:1234"
			handler.ServeHTTP(httptest.NewRecorder(), req)
		})
	}
	wg.Wait()
	if got := successes.Load(); got != 5 {
		t.Fatalf("allowed %d concurrent requests, want 5", got)
	}
}

func TestRateLimit_RedisUnavailableRejectsRequest(t *testing.T) {
	rdb, mock := redismock.NewClientMock()
	defer rdb.Close()
	mock.ExpectEvalSha(rateLimitScript.Hash(), []string{rateLimitKey("/auth/send-code", "203.0.113.1")}, 60).SetErr(errors.New("unavailable"))
	called := false
	handler := RateLimit(rdb, 5, time.Minute, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest("POST", "/auth/send-code", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if called || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Redis failure: called=%v status=%d", called, rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryRateLimit_ExpiryAndCapacity(t *testing.T) {
	now := time.Now()
	m := &memoryRateLimit{entries: make(map[string]rateLimitEntry)}
	if !m.allow("first", 1, time.Minute, now) || m.allow("first", 1, time.Minute, now) {
		t.Fatal("limit not enforced")
	}
	if !m.allow("first", 1, time.Minute, now.Add(time.Minute)) {
		t.Fatal("expired limit not reset")
	}
	for i := 0; i < 9999; i++ {
		m.entries[fmt.Sprintf("bucket-%d", i)] = rateLimitEntry{expires: now.Add(2 * time.Minute)}
	}
	if m.allow("new", 1, time.Minute, now.Add(time.Minute)) {
		t.Fatal("capacity must not evict active limits")
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 3, time.Minute, nil)
	handler := mw(okHandler)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
		req.RemoteAddr = "10.0.0.1:9000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 2, time.Minute, nil)
	handler := mw(okHandler)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
		req.RemoteAddr = "10.0.0.2:9000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.2:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "too many requests" {
		t.Fatalf("unexpected error message: %q", body["error"])
	}
}

func TestRateLimit_RetryAfterHeader(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 1, 2*time.Minute, nil)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.3:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	req = httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.3:9000"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter != "120" {
		t.Fatalf("expected Retry-After=120, got %q", retryAfter)
	}
}

func TestRateLimit_DifferentIPs(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 1, time.Minute, nil)
	handler := mw(okHandler)

	ips := []string{"10.0.1.1:9000", "10.0.1.2:9000", "10.0.1.3:9000"}
	for _, addr := range ips {
		req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ip %s: expected 200, got %d", addr, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.1.1:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for repeated IP, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.1.3:9000"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for repeated IP 10.0.1.3, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// X-Forwarded-For trust model tests
// ---------------------------------------------------------------------------

func mustParseCIDR(cidr string) *net.IPNet {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return n
}

func TestExtractIP_NoTrustedProxies_IgnoresXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "203.0.113.1:9000"
	req.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.1")

	ip := extractIP(req, nil)
	if ip != "203.0.113.1" {
		t.Fatalf("with no trusted proxies, expected RemoteAddr; got %q", ip)
	}
}

func TestExtractIP_TrustedProxy_HonorsXFF(t *testing.T) {
	trusted := []*net.IPNet{mustParseCIDR("10.0.0.0/8")}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.1:9000"
	req.Header.Set("X-Forwarded-For", "198.51.100.42, 10.0.0.5")

	ip := extractIP(req, trusted)
	// Rightmost non-trusted: 198.51.100.42 (10.0.0.5 is trusted)
	if ip != "198.51.100.42" {
		t.Fatalf("expected rightmost non-trusted IP; got %q", ip)
	}
}

func TestExtractIP_UntrustedSource_IgnoresXFF(t *testing.T) {
	trusted := []*net.IPNet{mustParseCIDR("10.0.0.0/8")}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "203.0.113.99:9000" // Not in trusted CIDR
	req.Header.Set("X-Forwarded-For", "198.51.100.1")

	ip := extractIP(req, trusted)
	if ip != "203.0.113.99" {
		t.Fatalf("untrusted source should use RemoteAddr; got %q", ip)
	}
}

func TestExtractIP_IPv6Normalization(t *testing.T) {
	req1 := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req1.RemoteAddr = "[::1]:9000"

	req2 := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req2.RemoteAddr = "[0:0:0:0:0:0:0:1]:9000"

	ip1 := extractIP(req1, nil)
	ip2 := extractIP(req2, nil)
	if ip1 != ip2 {
		t.Fatalf("IPv6 representations should normalize to same key: %q vs %q", ip1, ip2)
	}
}

func TestRateLimit_LuaScript_SetsTTL(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 10, 30*time.Second, nil)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.99.1:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	key := rateLimitKey("/auth/send-code", "10.0.99.1")
	ttl, err := rdb.TTL(req.Context(), key).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > 31*time.Second {
		t.Fatalf("expected TTL ~30s, got %v", ttl)
	}
}

func TestParseTrustedProxies_Empty(t *testing.T) {
	if nets := ParseTrustedProxies(""); nets != nil {
		t.Fatalf("empty string should return nil, got %v", nets)
	}
	if nets := ParseTrustedProxies("  "); nets != nil {
		t.Fatalf("whitespace should return nil, got %v", nets)
	}
}

func TestParseTrustedProxies_Valid(t *testing.T) {
	nets := ParseTrustedProxies("10.0.0.0/8, 172.16.0.0/12")
	if len(nets) != 2 {
		t.Fatalf("expected 2 CIDRs, got %d", len(nets))
	}
}

func TestParseTrustedProxies_InvalidSkipped(t *testing.T) {
	nets := ParseTrustedProxies("10.0.0.0/8, not-a-cidr, 172.16.0.0/12")
	if len(nets) != 2 {
		t.Fatalf("expected 2 valid CIDRs (invalid skipped), got %d", len(nets))
	}
}
