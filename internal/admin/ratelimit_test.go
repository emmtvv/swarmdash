package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestIPRateLimiter_BlocksOverBurstPerIP(t *testing.T) {
	rl := newIPRateLimiter(rate.Every(time.Hour), 2, nil)

	if !rl.allow("203.0.113.1") {
		t.Fatal("1st request should be allowed within burst")
	}
	if !rl.allow("203.0.113.1") {
		t.Fatal("2nd request should be allowed within burst")
	}
	if rl.allow("203.0.113.1") {
		t.Error("3rd request should be blocked once burst is exhausted")
	}

	// A different address has its own bucket.
	if !rl.allow("203.0.113.2") {
		t.Error("a different address should not be throttled by another address's usage")
	}
}

func TestIPRateLimiter_Middleware_Returns429WhenBlocked(t *testing.T) {
	rl := newIPRateLimiter(rate.Every(time.Hour), 1, func(r *http.Request) string { return "203.0.113.1" })
	var calls int
	h := rl.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)

	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r)
	if w1.Code != http.StatusOK {
		t.Fatalf("1st request: status = %d, want %d", w1.Code, http.StatusOK)
	}

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("2nd request: status = %d, want %d", w2.Code, http.StatusTooManyRequests)
	}
	if calls != 1 {
		t.Errorf("next handler called %d times, want 1 (2nd request should have been rejected before reaching it)", calls)
	}
}
