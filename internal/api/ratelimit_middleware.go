package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// ipCounter tracks request count and window start for a single IP.
type ipCounter struct {
	count     int
	windowEnd time.Time
}

// apiRateLimiter returns chi-compatible middleware that applies per-IP rate limiting.
// rpm is the maximum number of requests per minute per source IP.
// Requests to /health and /metrics are exempt (monitoring must never be throttled).
// Requests exceeding the limit receive HTTP 429 Too Many Requests with Retry-After header.
func apiRateLimiter(rpm int) func(http.Handler) http.Handler {
	var mu sync.Mutex
	counters := make(map[string]*ipCounter)

	// Background cleanup — remove expired entries every 2 minutes.
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			mu.Lock()
			for ip, c := range counters {
				if now.After(c.windowEnd) {
					delete(counters, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Exempt monitoring endpoints.
			path := r.URL.Path
			if path == "/health" || path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			ip := r.RemoteAddr
			if host, _, err := net.SplitHostPort(ip); err == nil {
				ip = host
			}

			mu.Lock()
			now := time.Now()
			c, ok := counters[ip]
			if !ok || now.After(c.windowEnd) {
				c = &ipCounter{
					count:     0,
					windowEnd: now.Add(time.Minute),
				}
				counters[ip] = c
			}
			c.count++
			exceeded := c.count > rpm
			mu.Unlock()

			if exceeded {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":"rate limit exceeded"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
