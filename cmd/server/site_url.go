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
// URL: index.html's og:url (configured URL only, see withoutOGURL), og:image
// and twitter:image, which link previews (Discord, Slack, Facebook) require to
// be absolute.
const siteURLPlaceholder = "__CORESCOPE_URL__"

// siteURLEnv is the environment variable that sets the public URL. The docker
// compose files pass it through; unset, og:url is left out and the image
// links are filled in with the origin the request reached.
const siteURLEnv = "CORESCOPE_URL"

var (
	// An origin with an optional base path, nothing that could leave an HTML
	// attribute value.
	configuredSiteURLRe = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)
	// A Host header: a DNS name or IPv4 address, or a bracketed IPv6 one,
	// with an optional port.
	requestHostRe = regexp.MustCompile(`^([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)
	// The og:url line of index.html, as scripts/static/build.sh drops it.
	ogURLTagRe = regexp.MustCompile(`[ \t]*<meta property="og:url" content="` + siteURLPlaceholder + `/">(\r?\n)?`)
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

// withoutOGURL drops the og:url tag, which a page keeps only with a configured
// URL: the fragment of a deep link never reaches the server, so og:url can
// only name the site root, and some platforms open it from a preview (#1890).
func withoutOGURL(page []byte) []byte {
	return ogURLTagRe.ReplaceAll(page, nil)
}

// requestOrigin is scheme://host of the URL the client asked for. The host is
// the Host header only (the bundled Caddy passes it through): a CDN caches by
// Host but not by X-Forwarded-Host, so believing the latter would let one
// request choose the host for later visitors. The scheme is TLS, else
// X-Forwarded-Proto, which can only say http or https. A Host that fails
// requestHostRe gives "" and the page's URLs become root-relative.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if p := strings.ToLower(firstHeaderValue(r.Header.Get("X-Forwarded-Proto"))); p == "https" || p == "http" {
		scheme = p
	}
	host := r.Host
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

// withSiteURL fills the placeholder in body with the request's origin, when
// no URL is configured. A page without one comes back as it is, without a copy.
func withSiteURL(body []byte, r *http.Request) []byte {
	if !bytes.Contains(body, []byte(siteURLPlaceholder)) {
		return body
	}
	return bytes.ReplaceAll(body, []byte(siteURLPlaceholder), []byte(html.EscapeString(requestOrigin(r))))
}
