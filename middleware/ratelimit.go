package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/env"
)

// maxBuckets caps the number of per-IP buckets a single limiter tracks, so a
// flood of unique source IPs (or spoofed IPs, before the trusted-proxy fix)
// cannot grow the map without bound and exhaust memory.
const maxBuckets = 50000

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
			rl.evictStaleLocked(time.Now(), 5*time.Minute)
			rl.mu.Unlock()
		}
	}()
	return rl
}

// evictStaleLocked removes buckets not touched within maxAge. Caller holds mu.
func (rl *rateLimiter) evictStaleLocked(now time.Time, maxAge time.Duration) {
	for ip, b := range rl.buckets {
		if now.Sub(b.lastRefill) > maxAge {
			delete(rl.buckets, ip)
		}
	}
}

// allow checks if a request is allowed under the token bucket rate limit.
// rps = requests per second (sustained), burst = max burst size.
func (rl *rateLimiter) allow(ip string, rps float64, burst int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[ip]
	if !exists {
		// Bound memory: if we've hit the cap, aggressively prune stale buckets
		// before admitting a new one. If still full, drop the oldest so the map
		// can never exceed maxBuckets.
		if len(rl.buckets) >= maxBuckets {
			rl.evictStaleLocked(now, 1*time.Minute)
			for len(rl.buckets) >= maxBuckets {
				var oldestIP string
				var oldest time.Time
				for k, v := range rl.buckets {
					if oldestIP == "" || v.lastRefill.Before(oldest) {
						oldestIP = k
						oldest = v.lastRefill
					}
				}
				if oldestIP == "" {
					break
				}
				delete(rl.buckets, oldestIP)
			}
		}
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

// trustedProxyNets is the parsed TRUSTED_PROXIES list. Forwarding headers are
// only honored when the TCP peer (RemoteAddr) falls within one of these.
var trustedProxyNets = parseTrustedProxies(env.TrustedProxies)

func parseTrustedProxies(raw string) []*net.IPNet {
	var nets []*net.IPNet
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, n, err := net.ParseCIDR(entry); err == nil {
				nets = append(nets, n)
			}
			continue
		}
		// Bare IP — treat as a single-host CIDR.
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return nets
}

// remoteIP returns the TCP peer address (RemoteAddr) with any port stripped.
func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func isTrustedProxy(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range trustedProxyNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// getClientIP extracts the client IP address for rate limiting.
//
// Trust model: forwarding headers (X-Forwarded-For / X-Real-IP) are attacker-
// controllable, so we only honor them when the direct TCP peer is a configured
// trusted proxy (TRUSTED_PROXIES). Otherwise the client IP is the TCP peer
// itself. This prevents an attacker from spoofing headers to evade the auth
// brute-force limiter or to flood the bucket map with fake keys.
func getClientIP(r *http.Request) string {
	peer := remoteIP(r)
	if !isTrustedProxy(peer) {
		return peer
	}

	// Peer is a trusted proxy — honor the IP it forwarded.
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		// The rightmost entry is the address our trusted proxy observed.
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return peer
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
