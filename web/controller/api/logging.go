package api

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/web/service"
)

// AccessLogMiddleware records one APILog row per /api/v1 call. We log
// only the *route template* (FullPath) — never the raw request URI —
// so a leaked token or password that some future caller stuffs into a
// query string can't end up persisted. The fallback when FullPath is
// empty (route didn't match) uses URL.Path which by definition excludes
// the query.
//
// Skipped paths are listed below to avoid drowning the table in
// health-check noise — adjust as needed.
func AccessLogMiddleware(logs *service.APILogService) gin.HandlerFunc {
	skip := map[string]bool{
		"health": true,
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			// URL.Path is the path component only — no scheme, no host,
			// no query. Safe to log even if the request had ?api_token=.
			path = c.Request.URL.Path
		}
		// FullPath uses the route template (e.g. /api/v1/inbounds/:id).
		// Strip the basePath + /api/v1 prefix for cleaner display.
		short := strings.TrimPrefix(path, c.GetString("base_path")+"api/v1/")
		if skip[short] {
			return
		}
		tokenName, _ := c.Get("api_token_name")
		entry := &model.APILog{
			At:         start.Unix(),
			Method:     c.Request.Method,
			Path:       short,
			Status:     c.Writer.Status(),
			DurationMs: time.Since(start).Milliseconds(),
			IP:         c.ClientIP(),
		}
		if s, ok := tokenName.(string); ok {
			entry.TokenName = s
		}
		logs.Record(entry)
	}
}
