package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/service"
)

const (
	bearerPrefix = "Bearer "
	headerToken  = "X-API-Token"
)

// AuthMiddleware accepts a token via Authorization: Bearer <t> or
// X-API-Token. The query-string transport (?api_token=) was intentionally
// removed: it leaked the secret into access logs, browser history, and
// any HTTP-aware proxy along the path. Callers that previously passed
// the token in the URL must move it to a header.
//
// The middleware first checks the multi-token table (api_tokens), where
// values are stored as SHA256 hashes; on miss it falls back to the
// legacy single token stored in settings.apiToken so existing
// deployments keep working.
func AuthMiddleware(settings *service.SettingService, tokens *service.APITokenService) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := extractToken(c)
		if got == "" {
			Unauthorized(c, "missing_api_token")
			return
		}

		if t, err := tokens.FindActiveByValue(got); err == nil {
			tokens.TouchLastUsed(t.Id)
			c.Set("api_token_id", t.Id)
			c.Set("api_token_name", t.Name)
			c.Set("api_token_scope", t.Scope)
			c.Next()
			return
		} else if !errors.Is(err, service.ErrTokenNotFound) {
			Internal(c, "auth_db_error", err)
			return
		}

		want, err := settings.GetAPIToken()
		if err == nil && want != "" &&
			subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			warnLegacyTokenOnce(c.ClientIP())
			c.Set("api_token_name", "legacy")
			c.Set("api_token_scope", service.ScopeAdmin)
			c.Next()
			return
		}

		Unauthorized(c, "invalid_api_token")
	}
}

func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if strings.HasPrefix(h, bearerPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(h, bearerPrefix))
		}
	}
	if h := c.GetHeader(headerToken); h != "" {
		return strings.TrimSpace(h)
	}
	return ""
}

func Unauthorized(c *gin.Context, code string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResp{
		Error:   true,
		Code:    code,
		Message: "authentication required",
	})
}

// legacyTokenSeen rate-limits the "legacy single-token in use" warning
// so a busy integration doesn't spam the log: at most one line per IP
// per hour. Bounded to legacyTokenSeenCap entries with LRU eviction —
// the previous "delete stale entries when over 1024" approach left
// space for unbounded growth if the entries weren't actually stale,
// e.g. a constant-rate scanner from many IPs.
const legacyTokenSeenCap = 4096

var (
	legacyTokenSeenMu sync.Mutex
	legacyTokenSeen   = map[string]time.Time{}
)

func warnLegacyTokenOnce(ip string) {
	now := time.Now()
	legacyTokenSeenMu.Lock()
	last := legacyTokenSeen[ip]
	if !last.IsZero() && now.Sub(last) < time.Hour {
		legacyTokenSeenMu.Unlock()
		return
	}
	if len(legacyTokenSeen) >= legacyTokenSeenCap {
		// Hard cap: walk the map once and drop the oldest half. O(N)
		// but only fires every 4096/2 = 2048 distinct hours' worth of
		// new IPs, so amortized cost is negligible. Using a real LRU
		// (doubly-linked list) here would be overkill for a logging
		// gate.
		oldest := time.Now()
		var oldestKey string
		for k, v := range legacyTokenSeen {
			if v.Before(oldest) {
				oldest = v
				oldestKey = k
			}
		}
		// Drop everything older than the oldest+30min — typically half
		// the map.
		cutoff := oldest.Add(30 * time.Minute)
		for k, v := range legacyTokenSeen {
			if v.Before(cutoff) {
				delete(legacyTokenSeen, k)
			}
		}
		_ = oldestKey
	}
	legacyTokenSeen[ip] = now
	legacyTokenSeenMu.Unlock()
	logger.Warningf(
		"legacy single-token in use from %s — issue a scoped multi-token via "+
			"`nexcore-x-ui` panel → API 控制台 and rotate the legacy one with "+
			"POST /api/v1/settings/api-token/rotate", ip)
}

// RequireScope produces a middleware that aborts with 403 unless the
// authenticated token's scope is in the allowed set. Use it to gate
// dangerous endpoints (admin-only) or read-only ones.
//
// Mount this AFTER AuthMiddleware so api_token_scope is populated.
func RequireScope(allowed ...string) gin.HandlerFunc {
	allow := make(map[string]bool, len(allowed))
	for _, s := range allowed {
		allow[s] = true
	}
	return func(c *gin.Context) {
		got, _ := c.Get("api_token_scope")
		scope, _ := got.(string)
		if scope == "" || !allow[scope] {
			c.AbortWithStatusJSON(http.StatusForbidden, ErrorResp{
				Error:   true,
				Code:    "scope_forbidden",
				Message: "this token's scope does not allow this action",
			})
			return
		}
		c.Next()
	}
}
