package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestHandler_StaticAssetsExemptFromGlobalRateLimit guards against
// globalLimiter wrapping /static/: a single page load pulls in several
// static assets (css, js, vendor bundles, favicon) alongside the dynamic
// request, so counting them against the same per-IP burst as the rest of
// the app starves the page before it even finishes loading.
func TestHandler_StaticAssetsExemptFromGlobalRateLimit(t *testing.T) {
	s := newTestServer(t, nil)
	s.globalLimiter = newIPRateLimiter(rate.Every(time.Hour), 1, s.clientIP)
	h := s.Handler()

	req := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "203.0.113.5:1111"
		return r
	}

	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req("/login"))
	if w1.Code == http.StatusTooManyRequests {
		t.Fatalf("first /login request should be within the burst, got %d", w1.Code)
	}

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req("/login"))
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second /login request should have exhausted the burst, got %d", w2.Code)
	}

	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req("/static/app.css"))
	if w3.Code == http.StatusTooManyRequests {
		t.Error("/static/ should not be subject to globalLimiter")
	}
}
