package web

// Regression coverage for the v1.0.4-introduced gzip middleware bug that
// silently dropped sub-threshold responses (axios-init.js, /server/status,
// any small JSON), surfacing as ERR_CONTENT_LENGTH_MISMATCH in browsers.
//
// The bug: gzipResponseWriter.Write buffers payloads under gzipMinSize=1024
// without writing them to the socket. The middleware originally only called
// gz.Close() after the handler returned — never flushing that buffer.
// Result: every response under 1KB came back as zero bytes (or an empty
// gzip header for some clients), breaking dashboard polls and small assets.

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newGinWithGzip mounts the production gzipMiddleware on a fresh router so
// tests exercise the exact code path the panel does.
func newGinWithGzip() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gzipMiddleware())
	return r
}

func doRequest(t *testing.T, r *gin.Engine, path string, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestGzip_SmallResponseSurvives is the regression test for v1.0.6's
// ERR_CONTENT_LENGTH_MISMATCH. A 100-byte JSON response with gzip accepted
// must round-trip to the client unmodified — either compressed (with
// Content-Encoding: gzip) or pass-through plaintext, but NEVER lost.
func TestGzip_SmallResponseSurvives(t *testing.T) {
	r := newGinWithGzip()
	body := strings.Repeat("x", 100) // < gzipMinSize (1024)
	r.GET("/small", func(c *gin.Context) {
		c.Data(200, "application/json", []byte(body))
	})

	rec := doRequest(t, r, "/small", "gzip")
	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	// Whether the middleware decided to gzip or pass-through, the bytes
	// the client gets must decode back to `body`.
	got := rec.Body.Bytes()
	if rec.Header().Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(bytes.NewReader(got))
		if err != nil {
			t.Fatalf("response claims gzip but unreadable: %v", err)
		}
		dec, _ := io.ReadAll(gz)
		got = dec
	}
	if string(got) != body {
		t.Fatalf("body lost or corrupted:\n  got:  %q (len %d)\n  want: %q (len %d)",
			truncate(string(got), 60), len(got), truncate(body, 60), len(body))
	}
}

// TestGzip_LargeResponseCompressed verifies the >= 1KB path: gzip kicks in
// and the bytes round-trip cleanly.
func TestGzip_LargeResponseCompressed(t *testing.T) {
	r := newGinWithGzip()
	body := strings.Repeat("hello-world-", 200) // ~2400 bytes
	r.GET("/large", func(c *gin.Context) {
		c.Data(200, "application/javascript", []byte(body))
	})

	rec := doRequest(t, r, "/large", "gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	gz, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	dec, _ := io.ReadAll(gz)
	if string(dec) != body {
		t.Fatalf("body lost across compress/decompress")
	}
	// And the wire size really is smaller (sanity check on the win).
	if rec.Body.Len() >= len(body) {
		t.Errorf("compressed size (%d) not smaller than original (%d)",
			rec.Body.Len(), len(body))
	}
}

// TestGzip_NoAcceptEncoding pins the bypass path: clients that didn't ask
// for gzip get raw bytes, no Content-Encoding header.
func TestGzip_NoAcceptEncoding(t *testing.T) {
	r := newGinWithGzip()
	body := "axios-init.js fake content under 1KB"
	r.GET("/asset", func(c *gin.Context) {
		c.Data(200, "application/javascript", []byte(body))
	})

	rec := doRequest(t, r, "/asset", "")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("unexpected Content-Encoding=%q for client that didn't ask for gzip",
			rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != body {
		t.Fatalf("body changed without compression: got %q want %q", rec.Body.String(), body)
	}
}

// TestGzip_ImagePassThrough verifies pre-compressed content types skip gzip
// — no wasted CPU on PNG/JPEG.
func TestGzip_ImagePassThrough(t *testing.T) {
	r := newGinWithGzip()
	body := strings.Repeat("imagedata", 300) // > 1KB, but image/*
	r.GET("/img", func(c *gin.Context) {
		c.Data(200, "image/png", []byte(body))
	})

	rec := doRequest(t, r, "/img", "gzip")
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Errorf("image/png was gzipped — wasted CPU on already-compressed content")
	}
	if rec.Body.String() != body {
		t.Errorf("image body corrupted")
	}
}

// TestGzip_EmptyResponse — handlers that write nothing must not crash the
// finalize path.
func TestGzip_EmptyResponse(t *testing.T) {
	r := newGinWithGzip()
	r.GET("/empty", func(c *gin.Context) {
		c.Status(204) // No Content
	})

	rec := doRequest(t, r, "/empty", "gzip")
	if rec.Code != 204 {
		t.Fatalf("status=%d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 response had non-empty body: %d bytes", rec.Body.Len())
	}
}

// TestGzip_MultipleSmallWritesSurvive — gin handlers can call Write more
// than once (e.g. template rendering, c.String + c.Header etc.). The buffer
// must accumulate them all and flush together at finalize.
func TestGzip_MultipleSmallWritesSurvive(t *testing.T) {
	r := newGinWithGzip()
	r.GET("/multi", func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/plain")
		_, _ = c.Writer.WriteString("part-A-")
		_, _ = c.Writer.WriteString("part-B-")
		_, _ = c.Writer.WriteString("part-C")
	})

	rec := doRequest(t, r, "/multi", "gzip")
	got := rec.Body.Bytes()
	if rec.Header().Get("Content-Encoding") == "gzip" {
		gz, _ := gzip.NewReader(bytes.NewReader(got))
		got, _ = io.ReadAll(gz)
	}
	if string(got) != "part-A-part-B-part-C" {
		t.Fatalf("multi-write body lost: got %q", string(got))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
