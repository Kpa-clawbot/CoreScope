// Package dbx lets CoreScope run its SQLite-flavoured SQL against PostgreSQL
// without touching any call site.
//
// The codebase has hundreds of queries written with SQLite's ? placeholders
// and handed straight to a *sql.DB. Postgres only understands $1..$n. Rather
// than rewrite every query (or wrap every call in a helper), this package
// registers a database/sql driver, PostgresDriverName, that wraps pgx's stdlib
// driver and rewrites ? to $n on the way through. Callers open it with
// sql.Open(dbx.PostgresDriverName, dsn) and keep using a plain *sql.DB.
//
// The rule that makes this safe: in CoreScope SQL, a ? outside a string
// literal, quoted identifier, dollar-quoted body or comment is ALWAYS a bind
// placeholder. Postgres' JSON operators ?, ?| and ?& are therefore not
// available through this driver; use jsonb_exists / jsonb_exists_any /
// jsonb_exists_all instead.
package dbx

import (
	"strconv"
	"strings"
	"sync"
)

// Rebind results are cached because the driver calls Rebind on every query
// and the same few hundred query strings are executed over and over. The cache
// is bounded both by entry count and by total bytes (dynamically built queries,
// e.g. variable-length IN lists, would otherwise grow it without limit); when
// either bound would be exceeded it is cleared and refilled.
const (
	rebindCacheMaxEntries = 4096
	rebindCacheMaxBytes   = 4 << 20
)

var rebindCache = struct {
	sync.RWMutex
	m     map[string]string
	bytes int
}{m: make(map[string]string)}

// Rebind rewrites each ? placeholder to $1, $2, ... in order. It leaves ? alone
// inside single-quoted strings (a doubled quote is an escaped quote; E'...'
// backslash escapes too), double-quoted identifiers, dollar-quoted bodies
// ($$...$$ and $tag$...$tag$), -- line comments and /* */ block comments
// (nested, as in Postgres). A query with no ? is returned unchanged. Postgres JSON operators ?, ?|, ?& are not supported: in this
// codebase ? always means a placeholder.
func Rebind(query string) string {
	if strings.IndexByte(query, '?') < 0 {
		return query
	}
	rebindCache.RLock()
	out, ok := rebindCache.m[query]
	rebindCache.RUnlock()
	if ok {
		return out
	}

	out = rebind(query)

	size := len(query) + len(out)
	if size > rebindCacheMaxBytes {
		return out
	}
	rebindCache.Lock()
	if len(rebindCache.m) >= rebindCacheMaxEntries || rebindCache.bytes+size > rebindCacheMaxBytes {
		clear(rebindCache.m)
		rebindCache.bytes = 0
	}
	if _, dup := rebindCache.m[query]; !dup {
		rebindCache.m[query] = out
		rebindCache.bytes += size
	}
	rebindCache.Unlock()
	return out
}

// rebind is the uncached single-pass rewriter. It returns query itself when no
// placeholder was rewritten.
func rebind(query string) string {
	var buf []byte
	n := 0
	copied := 0 // query[:copied] is already in buf
	for i := 0; i < len(query); {
		c := query[i]
		switch {
		case c == '?':
			if buf == nil {
				buf = make([]byte, 0, len(query)+16)
			}
			buf = append(buf, query[copied:i]...)
			n++
			buf = append(buf, '$')
			buf = strconv.AppendInt(buf, int64(n), 10)
			i++
			copied = i
		case c == '\'':
			i = skipQuoted(query, i, '\'', isEscapeStringStart(query, i))
		case c == '"':
			i = skipQuoted(query, i, '"', false)
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			if nl := strings.IndexByte(query[i+2:], '\n'); nl >= 0 {
				i += 2 + nl + 1
			} else {
				i = len(query)
			}
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			i = skipBlockComment(query, i)
		case c == '$' && (i == 0 || !isIdentByte(query[i-1])):
			i = skipDollarQuoted(query, i)
		default:
			i++
		}
	}
	if n == 0 {
		return query
	}
	buf = append(buf, query[copied:]...)
	return string(buf)
}

// skipQuoted returns the index just past the quoted run opening at
// query[start] (a doubled quote is an escaped quote). backslash enables
// E'...' backslash escapes. An unterminated run extends to the end.
func skipQuoted(query string, start int, quote byte, backslash bool) int {
	for j := start + 1; j < len(query); j++ {
		switch query[j] {
		case '\\':
			if backslash {
				j++
			}
		case quote:
			if j+1 < len(query) && query[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(query)
}

// isEscapeStringStart reports whether the ' at query[i] opens an E'...'
// string, i.e. is preceded by a standalone E or e.
func isEscapeStringStart(query string, i int) bool {
	if i == 0 || (query[i-1] != 'E' && query[i-1] != 'e') {
		return false
	}
	return i == 1 || !isIdentByte(query[i-2])
}

// skipBlockComment returns the index just past the /* */ comment opening at
// query[start]. Nesting is honoured, as in Postgres.
func skipBlockComment(query string, start int) int {
	depth := 0
	for j := start; j+1 < len(query); {
		switch {
		case query[j] == '/' && query[j+1] == '*':
			depth++
			j += 2
		case query[j] == '*' && query[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(query)
}

// skipDollarQuoted handles the $ at query[start]: if it opens a $$ or
// $tag$ quoted body, the index just past the matching close is returned;
// otherwise (e.g. a $1 parameter) the index just past the $.
func skipDollarQuoted(query string, start int) int {
	j := start + 1
	if j < len(query) && query[j] != '$' {
		if !isTagStartByte(query[j]) {
			return start + 1
		}
		for j < len(query) && isTagByte(query[j]) {
			j++
		}
		if j >= len(query) || query[j] != '$' {
			return start + 1
		}
	}
	if j >= len(query) {
		return len(query)
	}
	tag := query[start : j+1]
	if end := strings.Index(query[j+1:], tag); end >= 0 {
		return j + 1 + end + len(tag)
	}
	return len(query)
}

// isTagStartByte: letters, underscore and any non-ASCII byte may start a
// dollar-quote tag (digits may follow but not lead).
func isTagStartByte(c byte) bool {
	return c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isTagByte(c byte) bool {
	return isTagStartByte(c) || (c >= '0' && c <= '9')
}

// isIdentByte reports whether c can appear inside an unquoted identifier
// (Postgres allows $ after the first character), in which case a following
// $ belongs to the identifier rather than opening a dollar quote.
func isIdentByte(c byte) bool {
	return isTagByte(c) || c == '$'
}
