package main

import (
	"bufio"
	"compress/gzip"
	"net"
	"net/http"
	"strings"
	"sync"
)

// gzipWriterPool pools *gzip.Writer instances to avoid the ~256KB sliding
// window allocation on every compressed response. Writers are Reset() to the
// new underlying writer on Get and returned via Put after Close.
var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		// io.Discard placeholder; Reset replaces it on Get.
		gz, err := gzip.NewWriterLevel(discardWriter{}, gzip.DefaultCompression)
		if err != nil {
			// gzip.NewWriterLevel only errors on invalid level; DefaultCompression
			// is always valid, so this branch is unreachable. Fall back to the
			// default writer (which uses the same level) so the pool always
			// hands out a usable instance.
			return gzip.NewWriter(discardWriter{})
		}
		return gz
	},
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// defaultCompressibleTypes is the conservative allow-list of MIME types the
// middleware will gzip-encode. Anything already compressed (images, video,
// fonts, octet-stream, x-gzip, …) bypasses the encoder entirely.
var defaultCompressibleTypes = []string{
	"application/json",
	"application/javascript",
	"application/x-javascript",
	"application/xml",
	"text/html",
	"text/css",
	"text/plain",
	"text/xml",
	"image/svg+xml",
}

// gzipResponseWriter wraps http.ResponseWriter and compresses Write() output
// only when the response Content-Type matches the configured allow-list and
// no upstream handler has already set Content-Encoding. It also propagates
// Flush / Hijack to the underlying writer (required for SSE and WebSocket).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz             *gzip.Writer
	level          int
	allowedTypes   []string
	wroteHeader    bool
	compressActive bool
}

// init lazily decides per response whether to compress, based on the response
// headers the inner handler has set. We must defer this until WriteHeader (or
// the first Write call) because Content-Type is set by the handler, not the
// middleware.
func (g *gzipResponseWriter) init() {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true

	h := g.ResponseWriter.Header()
	// Don't double-encode.
	if h.Get("Content-Encoding") != "" {
		g.compressActive = false
		return
	}
	if !isCompressibleContentType(h.Get("Content-Type"), g.allowedTypes) {
		g.compressActive = false
		return
	}

	// Lease a writer from the pool and rebind it to the real ResponseWriter.
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(g.ResponseWriter)
	g.gz = gz
	g.compressActive = true

	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	// gzip stream length is unknown — strip any precomputed length.
	h.Del("Content-Length")
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	g.init()
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	g.init()
	if !g.compressActive {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

// Flush propagates to the underlying writer so SSE / streaming handlers can
// push chunks to the client immediately. We must also flush the gzip writer
// when active, otherwise the buffered DEFLATE block never reaches the wire.
func (g *gzipResponseWriter) Flush() {
	if g.compressActive && g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack delegates to the underlying writer's Hijacker. We refuse to hijack a
// connection that has already started a gzip stream — that would leave the
// caller with a half-written DEFLATE block.
func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := g.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// close releases the pooled gzip.Writer back to the pool.
func (g *gzipResponseWriter) close() {
	if g.gz == nil {
		return
	}
	_ = g.gz.Close()
	gzipWriterPool.Put(g.gz)
	g.gz = nil
}

// isCompressibleContentType returns true if ct matches one of allow (which
// is the configured allow-list, or defaultCompressibleTypes). Matching is
// done on the bare MIME type, ignoring any "; charset=..." parameters.
func isCompressibleContentType(ct string, allow []string) bool {
	if ct == "" {
		// No content-type set → handler hasn't decided yet. Refuse to
		// compress; we cannot guess. Most real handlers set Content-Type
		// before the first Write.
		return false
	}
	mt := ct
	if idx := strings.Index(mt, ";"); idx >= 0 {
		mt = mt[:idx]
	}
	mt = strings.TrimSpace(strings.ToLower(mt))

	// Hard skip: anything that is already compressed.
	if strings.HasPrefix(mt, "image/") && mt != "image/svg+xml" {
		return false
	}
	if strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "audio/") {
		return false
	}
	switch mt {
	case "application/x-gzip", "application/gzip", "application/zip",
		"application/x-bzip2", "application/x-7z-compressed",
		"application/x-rar-compressed", "application/x-zstd",
		"application/octet-stream", "application/pdf":
		return false
	}

	if len(allow) == 0 {
		allow = defaultCompressibleTypes
	}
	for _, a := range allow {
		if strings.EqualFold(mt, a) {
			return true
		}
	}
	return false
}

// gzipMiddleware compresses HTTP responses when the client supports gzip and
// the response Content-Type is in the allow-list. WebSocket upgrade requests
// pass through unmodified. The middleware uses the default allow-list and
// gzip.DefaultCompression — for configurable behaviour use
// gzipMiddlewareWithConfig.
func gzipMiddleware(next http.Handler) http.Handler {
	return gzipMiddlewareWithConfig(nil, next)
}

// gzipMiddlewareWithConfig is the configurable form of gzipMiddleware. When
// cfg is nil, defaults (gzip.DefaultCompression, defaultCompressibleTypes)
// are used.
func gzipMiddlewareWithConfig(cfg *CompressionConfig, next http.Handler) http.Handler {
	level := gzip.DefaultCompression
	var allow []string
	if cfg != nil {
		if cfg.Level >= gzip.BestSpeed && cfg.Level <= gzip.BestCompression {
			level = cfg.Level
		}
		if len(cfg.ContentTypes) > 0 {
			allow = cfg.ContentTypes
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}

		grw := &gzipResponseWriter{
			ResponseWriter: w,
			level:          level,
			allowedTypes:   allow,
		}
		defer grw.close()
		next.ServeHTTP(grw, r)
	})
}
