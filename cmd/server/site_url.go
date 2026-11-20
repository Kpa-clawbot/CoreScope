package main

import (
	"bytes"
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// siteURLPlaceholder marks where a top-level page needs the instance's public
// URL: index.html's og:url, og:image and twitter:image, which link previews
// (Discord, Slack, Facebook) require to be absolute.
const siteURLPlaceholder = "__CORESCOPE_URL__"

// siteURLEnv is the environment variable that sets the public URL. The docker
// compose files pass it through; unset, each page is filled in with the
// origin the request reached.
const siteURLEnv = "CORESCOPE_URL"

var (
	// An origin with an optional base path, nothing that could leave an HTML
	// attribute value.
	configuredSiteURLRe = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)
	// A Host header: a DNS name or IPv4 address, or a bracketed IPv6 one,
	// with an optional port.
	requestHostRe = regexp.MustCompile(`^([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)
)

// resolveConfiguredSiteURL validates CORESCOPE_URL. It returns the URL with
// any trailing slash trimmed, or "" when it is unset or invalid; an invalid
// value is logged and the request's own origin is used instead.
func resolveConfiguredSiteURL(raw string) string {
	v := strings.TrimRight(strings.TrimSpace(raw), "/")
	if v == "" {
		return ""
	}
	if !configuredSiteURLRe.MatchString(v) {
		log.Printf("[static] warning: %s=%q is not an http(s)://host[:port][/path] URL; using each request's own origin", siteURLEnv, raw)
		return ""
	}
	return v
}

// requestOrigin is scheme://host of the URL the client asked for. Behind a
// TLS-terminating proxy (the bundled Caddy) the scheme and host come from
// X-Forwarded-Proto / X-Forwarded-Host. They are believed from any client:
// the value only lands in that client's own response, which is never cached
// (spaHandler serves pages no-store), and both are held to a strict shape
// first. A host that fails it falls back to r.Host; when neither passes, the
// result is "" and the page's URLs become root-relative.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if p := strings.ToLower(firstHeaderValue(r.Header.Get("X-Forwarded-Proto"))); p == "https" || p == "http" {
		scheme = p
	}
	host := firstHeaderValue(r.Header.Get("X-Forwarded-Host"))
	if !requestHostRe.MatchString(host) {
		host = r.Host
	}
	if !requestHostRe.MatchString(host) {
		return ""
	}
	return scheme + "://" + strings.ToLower(host)
}

// firstHeaderValue is the first entry of a comma-separated header: the one the
// outermost proxy, nearest the client, wrote.
func firstHeaderValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// withSiteURL fills the placeholder in body with the request's origin. Pages
// whose placeholder was already filled at startup (CORESCOPE_URL set) or that
// have none come back as they are, without a copy.
func withSiteURL(body []byte, r *http.Request) []byte {
	if !bytes.Contains(body, []byte(siteURLPlaceholder)) {
		return body
	}
	return bytes.ReplaceAll(body, []byte(siteURLPlaceholder), []byte(html.EscapeString(requestOrigin(r))))
}
