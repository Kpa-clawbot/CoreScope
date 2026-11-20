package main

import (
	"regexp"
	"strings"
)

// urlInTextRe finds URL-looking tokens (scheme://...) in a free-form string:
// a broker URL on its own, or one quoted inside an MQTT error message.
var urlInTextRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s]+`)

// urlSecretParamRe finds query parameters whose value is a credential.
var urlSecretParamRe = regexp.MustCompile(`(?i)([?&](?:password|passwd|pass|pwd|token|access_token|secret|apikey|api_key|auth)=)[^&#\s]*`)

// maskURLSecrets returns s with the credentials in every URL it contains
// replaced by `****`, in the format the server's maskBrokerURL uses:
// `mqtt://user:secret@host:1883` -> `mqtt://user:****@host:1883`. The
// userinfo ends at the LAST `@` before the query, so a password containing
// `@`, `/` or `#` is masked whole (`mqtt://u:p@ss@host` ->
// `mqtt://u:****@host`). Unlike the server's copy it masks every scheme, not
// only the MQTT family, does not depend on URL parsing (a URL quoted in an error message is often
// followed by `":` or `)`), and also masks secret-named query parameters
// (`?token=****`). A URL without a password is returned unchanged.
//
// The ingestor applies it to everything it publishes about its MQTT sources
// (ingestor_stats on Postgres, the stats file on SQLite), so no credential
// leaves the process there, whatever the reader does.
func maskURLSecrets(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	return urlInTextRe.ReplaceAllStringFunc(s, func(u string) string {
		i := strings.Index(u, "://")
		scheme, rest := u[:i], u[i+3:]
		// The userinfo ends at the last `@` before the query, not before the
		// first `/`: a raw `/` or `#` in a password must not leave its end
		// unmasked as if it were the path.
		queryAt := strings.IndexByte(rest, '?')
		if queryAt < 0 {
			queryAt = len(rest)
		}
		if at := strings.LastIndex(rest[:queryAt], "@"); at >= 0 {
			if user, _, hasPass := strings.Cut(rest[:at], ":"); hasPass {
				rest = user + ":****@" + rest[at+1:]
			}
		}
		return scheme + "://" + urlSecretParamRe.ReplaceAllString(rest, "${1}****")
	})
}
