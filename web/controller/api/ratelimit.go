package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimitMiddleware throttles every authenticated /api/v1 request
// keyed on (token id, fallback to IP). It's a small token-bucket: each
// key gets `burst` tokens and refills at `refillPerSec` tokens/sec.
//
// The defaults (30 burst, 10/s) tolerate the panel's own polling
// (status / inbounds list every few seconds) plus normal business
// integrations, but break a runaway integration loop or a credential
// stuffer hammering /api/v1 with millions of requests.
//
// In-memory only — restarts reset the buckets. Single-node panels are
// the only deployment topology, so that's fine.
func RateLimitMiddleware() gin.HandlerFunc {
	return rateLimitWith(30, 10)
}

func rateLimitWith(burst int, refillPerSec float64) gin.HandlerFunc {
	rl := newRateLimiter(burst, refillPerSec)
	// Periodic GC: drop buckets idle > 10 minutes so a long-running
	// process can't accrete a giant map keyed on every IP that ever
	// hit the panel.
	go rl.gcLoop()
	return func(c *gin.Context) {
		key := bucketKey(c)
		if !rl.allow(key) {
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, ErrorResp{
				Error:   true,
				Code:    "rate_limited",
				Message: "too many requests; slow down or split traffic across tokens",
			})
			return
		}
		c.Next()
	}
}

func bucketKey(c *gin.Context) string {
	if id, ok := c.Get("api_token_id"); ok {
		if n, ok := id.(int); ok && n > 0 {
			return "tok:" + itoa(n)
		}
	}
	if name, ok := c.Get("api_token_name"); ok {
		if s, ok := name.(string); ok && s != "" {
			return "name:" + s
		}
	}
	return "ip:" + c.ClientIP()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	refill  float64 // tokens per second
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(burst int, refillPerSec float64) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		burst:   float64(burst),
		refill:  refillPerSec,
	}
}

func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &bucket{tokens: rl.burst - 1, last: now}
		return true
	}
	// Refill since last hit, capped at burst.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * rl.refill
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *rateLimiter) gcLoop() {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for range tick.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		rl.mu.Lock()
		for k, b := range rl.buckets {
			if b.last.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}
