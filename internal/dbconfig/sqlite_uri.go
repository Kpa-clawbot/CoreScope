package dbconfig

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// SQLiteURI encodes a filesystem path as a native file: URI. It does not open
// the file or choose access/durability options; callers supply those explicitly.
// A path is never interpreted as an existing URI or an in-memory database name.
func SQLiteURI(path string, options url.Values) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("SQLite requires a nonempty filesystem path without NUL")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite filesystem path: %w", err)
	}
	abs = filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && !strings.HasPrefix(abs, "//") {
		abs = "/" + abs
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: options.Encode()}
	return u.String(), nil
}
