package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// backend says which database engine a dbRef names.
type backend int

const (
	sqliteBackend backend = iota
	postgresBackend
)

func (b backend) String() string {
	if b == postgresBackend {
		return "postgres"
	}
	return "sqlite"
}

// dbRef is one parsed -from/-to/-a/-b argument.
type dbRef struct {
	kind    backend
	path    string // sqlite: absolute file path
	url     string // postgres: the URL exactly as given (holds credentials)
	display string // safe to print: password redacted
	secrets []string
}

// parseDBRef accepts "sqlite:<path>" or a postgres:// / postgresql:// URL.
// Errors never echo the argument: it may carry a password.
func parseDBRef(s string) (dbRef, error) {
	switch {
	case strings.HasPrefix(s, "sqlite:"):
		p := strings.TrimPrefix(s, "sqlite:")
		if p == "" {
			return dbRef{}, errors.New("sqlite: needs a file path (sqlite:<path>)")
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return dbRef{}, fmt.Errorf("resolve %s: %w", p, err)
		}
		return dbRef{kind: sqliteBackend, path: abs, display: "sqlite:" + abs}, nil
	case strings.HasPrefix(s, "postgres://"), strings.HasPrefix(s, "postgresql://"):
		u, err := url.Parse(s)
		if err != nil {
			// Not wrapped: url.Error quotes the whole URL, password included.
			return dbRef{}, errors.New("unparseable postgres URL")
		}
		ref := dbRef{kind: postgresBackend, url: s, display: redactURL(u)}
		if pw, ok := u.User.Password(); ok {
			ref.secrets = append(ref.secrets, pw, url.QueryEscape(pw), url.PathEscape(pw))
		}
		if pw := u.Query().Get("password"); pw != "" {
			ref.secrets = append(ref.secrets, pw, url.QueryEscape(pw))
		}
		return ref, nil
	default:
		return dbRef{}, errors.New("unrecognised database argument: want sqlite:<path> or a postgres:// URL")
	}
}

// redactURL renders u with the userinfo password and any password= query
// parameter replaced by "xxxxx".
func redactURL(u *url.URL) string {
	c := *u
	if c.RawQuery != "" {
		q := c.Query()
		if q.Has("password") {
			q.Set("password", "xxxxx")
			c.RawQuery = q.Encode()
		}
	}
	return c.Redacted()
}

// minScrubLen is the shortest secret the output scrubber replaces. Shorter
// ones would mangle unrelated text; the primary defence is that URLs are only
// ever printed through redactURL.
const minScrubLen = 4

// scrubWriter replaces every known secret in what is written through it. It
// is defence in depth behind redactURL, for driver errors that might quote a
// connection string. Each Write is one log line, so a secret cannot straddle
// two writes.
type scrubWriter struct {
	w       io.Writer
	secrets [][]byte
}

func newScrubWriter(w io.Writer) *scrubWriter {
	sw := &scrubWriter{w: w}
	sw.addSecret(os.Getenv("PGPASSWORD"))
	return sw
}

// add registers the secrets of refs. Call it before anything about them is
// logged.
func (s *scrubWriter) add(refs ...dbRef) {
	for _, r := range refs {
		for _, sec := range r.secrets {
			s.addSecret(sec)
		}
	}
}

func (s *scrubWriter) addSecret(sec string) {
	if len(sec) >= minScrubLen {
		s.secrets = append(s.secrets, []byte(sec))
	}
}

func (s *scrubWriter) Write(p []byte) (int, error) {
	out := p
	for _, sec := range s.secrets {
		if bytes.Contains(out, sec) {
			out = bytes.ReplaceAll(out, sec, []byte("xxxxx"))
		}
	}
	if _, err := s.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
