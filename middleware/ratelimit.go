package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type visitor struct {
	count       int
	windowStart time.Time
}

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{visitors: make(map[string]*visitor)}
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			rl.mu.Lock()
			now := time.Now()
			for ip, v := range rl.visitors {
				if now.Sub(v.windowStart) > 2*time.Minute {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimiter) allow(ip string, limit int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	v, exists := rl.visitors[ip]
	if !exists || now.Sub(v.windowStart) > window {
		rl.visitors[ip] = &visitor{count: 1, windowStart: now}
		return true
	}
	v.count++
	return v.count <= limit
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

// RateLimitMiddleware enforces per-IP rate limits.
// Auth endpoints: 10 req/min. General admin API: 600 req/min.
func RateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip rate limiting for non-API paths
		if path == "/healthcheck" || strings.HasPrefix(path, "/ws/") ||
			path == "/auth/sso/config" || path == "/auth/sso/login" ||
			path == "/version" || path == "/install/runner" {
			next.ServeHTTP(w, r)
			return
		}

		ip := getClientIP(r)

		// Deploy token endpoints: strict limit (brute-force protection)
		if strings.HasPrefix(path, "/api/deploy/") {
			if !authLimiter.allow(ip, 10, time.Minute) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"success":false,"error":"rate_limited","error_message":"too many requests, try again later","error_code":4290}`))
				return
			}
		}

		// Auth endpoints: strict limit (brute-force protection)
		if path == "/auth/login" || path == "/auth/refresh" || path == "/auth/sso/callback" {
			if !authLimiter.allow(ip, 20, time.Minute) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"success":false,"error":"rate_limited","error_message":"too many requests, try again later","error_code":4290}`))
				return
			}
		}

		// General API: generous limit (normal dashboard use is ~10-15 req per page load)
		if strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, "/auth/") {
			if !generalLimiter.allow(ip, 600, time.Minute) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"success":false,"error":"rate_limited","error_message":"too many requests, try again later","error_code":4290}`))
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
