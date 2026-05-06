package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"x-ui/web/service"
)

const (
	bearerPrefix = "Bearer "
	headerToken  = "X-API-Token"
	queryToken   = "api_token"
)

// AuthMiddleware accepts a token via Authorization: Bearer <t>, X-API-Token,
// or ?api_token=. It first checks the multi-token table (api_tokens); on miss
// it falls back to the legacy single token stored in settings.apiToken so
// existing deployments keep working.
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
			c.Next()
			return
		} else if !errors.Is(err, service.ErrTokenNotFound) {
			Internal(c, "auth_db_error", err)
			return
		}

		want, err := settings.GetAPIToken()
		if err == nil && want != "" &&
			subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			c.Set("api_token_name", "legacy")
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
	if q := c.Query(queryToken); q != "" {
		return strings.TrimSpace(q)
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
