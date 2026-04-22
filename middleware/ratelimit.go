package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{buckets: make(map[string]*tokenBucket)}
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			rl.mu.Lock()
			now := time.Now()
			for ip, b := range rl.buckets {
				if now.Sub(b.lastRefill) > 5*time.Minute {
					delete(rl.buckets, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

// allow checks if a request is allowed under the token bucket rate limit.
// rps = requests per second (sustained), burst = max burst size.
func (rl *rateLimiter) allow(ip string, rps float64, burst int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[ip]
	if !exists {
		rl.buckets[ip] = &tokenBucket{
			tokens:     float64(burst) - 1,
			maxTokens:  float64(burst),
			refillRate: rps,
			lastRefill: now,
		}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

var (
	authLimiter    = newRateLimiter()
	generalLimiter = newRateLimiter()
)

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host
}

// RateLimitMiddleware enforces per-IP rate limits using token buckets.
// Auth endpoints: 1 req/s, burst 5. General API: 30 req/s, burst 60.
func RateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip rate limiting for non-API paths
		if path == "/healthcheck" || strings.HasPrefix(path, "/ws/") ||
			path == "/auth/sso/config" || path == "/auth/sso/login" ||
			path == "/auth/sso/callback" || path == "/version" ||
			path == "/install/runner" {
			next.ServeHTTP(w, r)
			return
		}

		ip := getClientIP(r)

		// Deploy token + auth endpoints: 1 rps, burst 5
		if strings.HasPrefix(path, "/api/deploy/") ||
			path == "/auth/login" || path == "/auth/refresh" {
			if !authLimiter.allow(ip, 1, 5) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"success":false,"error":"rate_limited","error_message":"too many requests, try again later","error_code":4290}`))
				return
			}
		}

		// General API: 30 rps, burst 60
		if strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, "/auth/") {
			if !generalLimiter.allow(ip, 30, 60) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"success":false,"error":"rate_limited","error_message":"too many requests, try again later","error_code":4290}`))
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
