package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimit applies a simple per-IP token-bucket limiter (rps requests/sec,
// burst peak) to protect auth endpoints from brute-force attempts.
func RateLimit(rps float64, burst int, next http.Handler) http.Handler {
	type bucket struct {
		tokens   float64
		lastSeen time.Time
	}

	var (
		mu      sync.Mutex
		buckets = make(map[string]*bucket)
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}

		mu.Lock()
		b, ok := buckets[host]
		now := time.Now()
		if !ok {
			b = &bucket{tokens: float64(burst), lastSeen: now}
			buckets[host] = b
		}
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.lastSeen = now
		b.tokens += elapsed * rps
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}

		allowed := b.tokens >= 1
		if allowed {
			b.tokens--
		}
		mu.Unlock()

		if !allowed {
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}
