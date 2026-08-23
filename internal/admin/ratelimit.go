package admin

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipRateLimiter throttles requests per client IP (see clientIP in auth.go)
// using one token bucket per address - the panel has no rate limiting at
// all otherwise, which matters once it's reachable on a public port rather
// than just a trusted cluster network. Entries for addresses that have
// gone quiet are swept lazily on access rather than by a background
// goroutine, so the map doesn't grow without bound against an
// internet-facing instance, without needing a shutdown hook.
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    rate.Limit
	burst    int

	// clientIP resolves the bucket key for a request - normally
	// (*Server).clientIP, bound at construction time so middleware doesn't
	// need a *Server receiver of its own. Tests that only exercise allow()
	// directly never call this.
	clientIP func(*http.Request) string
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const rateLimiterIdleTTL = 10 * time.Minute
const rateLimiterSweepThreshold = 4096

func newIPRateLimiter(limit rate.Limit, burst int, clientIP func(*http.Request) string) *ipRateLimiter {
	return &ipRateLimiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		burst:    burst,
		clientIP: clientIP,
	}
}

func (rl *ipRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if len(rl.visitors) > rateLimiterSweepThreshold {
		for k, v := range rl.visitors {
			if now.Sub(v.lastSeen) > rateLimiterIdleTTL {
				delete(rl.visitors, k)
			}
		}
	}

	v, ok := rl.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(rl.limit, rl.burst)}
		rl.visitors[ip] = v
	}
	v.lastSeen = now
	return v.limiter.Allow()
}

// middleware rejects requests over the limit with 429 before next ever
// sees them.
func (rl *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(rl.clientIP(r)) {
			http.Error(w, "too many requests - slow down and try again shortly", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
