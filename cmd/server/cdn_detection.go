package main

// Issue #1561: detect CDN-fronted deployments and warn ONCE.
//
// When operators put CoreScope behind Cloudflare/Fastly without
// configuring a /api/* cache bypass, dashboards go stale — the origin
// emits Cache-Control: no-store (#1551), but the CDN's zone-level
// caching policy can still cache JSON responses for hours
// (cf-cache-status: HIT, age > 0). We can't fix the CDN config from
// the server side; the best we can do is detect the situation and
// loudly tell the operator at the logs.
//
// Detection: presence of any CDN-typical request header
// (CF-Connecting-IP, CF-Ray, X-Forwarded-For, X-Real-IP,
//  Fastly-Client-IP, True-Client-IP).
//
// Side effects: a single log line per process boot — never blocks
// the request, never modifies the response, never logs again.

import (
	"log"
	"net/http"
	"sync"
)

var cdnWarnOnce sync.Once

// cdnHeaders are HTTP request headers typically injected by a CDN
// when it fronts an origin. Detected case-insensitively by
// http.Header.Get.
var cdnHeaders = []string{
	"CF-Connecting-IP",
	"CF-Ray",
	"X-Forwarded-For",
	"X-Real-IP",
	"Fastly-Client-IP",
	"True-Client-IP",
}

// cdnDetectionMiddleware inspects each incoming request for CDN
// headers and, on the FIRST one observed, logs a single warning
// pointing the operator at docs/deployment-behind-cdn.md. The
// middleware always calls next; it never blocks or rewrites.
func cdnDetectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hdr := firstCDNHeader(r.Header); hdr != "" {
			cdnWarnOnce.Do(func() {
				log.Printf("[security] WARNING: detected request via CDN (%s header present). "+
					"Ensure /api/* is bypassed in your CDN config — see docs/deployment-behind-cdn.md. "+
					"Cached API responses cause observer-flap and incorrect dashboards.", hdr)
			})
		}
		next.ServeHTTP(w, r)
	})
}

func firstCDNHeader(h http.Header) string {
	for _, name := range cdnHeaders {
		if h.Get(name) != "" {
			return name
		}
	}
	return ""
}
