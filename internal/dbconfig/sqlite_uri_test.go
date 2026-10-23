package dbconfig

import (
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSQLiteURILiteralPathsAndOptions(t *testing.T) {
	for _, name := range []string{"plain.db", "literal # ? % space µ.db"} {
		path := filepath.Join(t.TempDir(), name)
		opts := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)"}}
		before := opts.Encode()
		text, err := SQLiteURI(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		if u.Scheme != "file" || u.Host != "" || u.Fragment != "" || !reflect.DeepEqual(u.Query(), opts) || opts.Encode() != before {
			t.Fatal("URI changed path boundaries/options")
		}
		want := filepath.ToSlash(path)
		if runtime.GOOS == "windows" && !strings.HasPrefix(want, "//") {
			want = "/" + want
		}
		if u.Path != want {
			t.Fatalf("path=%q want=%q", u.Path, want)
		}
		if strings.Contains(text, "#") || strings.Contains(text, " µ") {
			t.Fatal("literal path was not escaped")
		}
	}
}

func TestSQLiteURIInvalidAndRelativePaths(t *testing.T) {
	for _, path := range []string{"", "bad\x00name"} {
		if _, err := SQLiteURI(path, nil); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	text, err := SQLiteURI("relative.sqlite", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("relative.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.ToSlash(want)
	if runtime.GOOS == "windows" && !strings.HasPrefix(want, "//") {
		want = "/" + want
	}
	if u.Path != want {
		t.Fatalf("relative path=%q want=%q", u.Path, want)
	}
}
