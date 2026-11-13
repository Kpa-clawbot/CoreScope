// Package prunequeue defines the on-disk protocol used by the read-only
// server (cmd/server) to ask the writer-owning ingestor (cmd/ingestor) to
// delete nodes outside the configured geo_filter.
//
// Rationale: after #1283/#1289 the server opens SQLite with mode=ro, so it
// cannot execute DELETE statements. The one-click geo-prune feature still
// presents its HTTP API on the server (#669 M4), but the actual write is
// performed by the ingestor's maintenance loop. Communication uses small
// JSON marker files written into a directory next to the SQLite database
// (so it inherits the same backup/permissions story).
//
// Layout (under <dir(dbPath)>/prune-requests/):
//
//	request-<id>.json   — written by server when a confirmed prune is requested
//	result-<id>.json    — written by ingestor after the DELETE completes; the
//	                      ingestor removes the request file in the same step
//	                      using os.Rename (atomic on POSIX).
//
// Writers use a randomly named O_EXCL temp file plus rename and readers
// refuse non-regular files, so a less-privileged peer sharing the directory
// cannot redirect a write or read through a symlink.
//
// The server polls result-<id>.json to surface progress on
// GET /api/admin/prune-geo-filter/status?id=<id>.
package prunequeue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// QueueDirName is the subdirectory (under the SQLite data dir) holding
// request/result marker files.
const QueueDirName = "prune-requests"

// Request is the payload the server writes to request-<id>.json. The
// ingestor honors the pubkeys list verbatim — the server is responsible
// for the TOCTOU snapshot (only nodes that were in the preview).
type Request struct {
	ID          string    `json:"id"`
	RequestedAt time.Time `json:"requestedAt"`
	Reason      string    `json:"reason,omitempty"` // human-readable, e.g. "geo-prune"
	Pubkeys     []string  `json:"pubkeys"`
}

// Result is what the ingestor writes to result-<id>.json after running
// the DELETE. Errors are captured as plain strings — the file is the
// only channel back to the server.
type Result struct {
	ID          string    `json:"id"`
	RequestedAt time.Time `json:"requestedAt"`
	CompletedAt time.Time `json:"completedAt"`
	Deleted     int64     `json:"deleted"`
	Error       string    `json:"error,omitempty"`
}

// NewID returns a 16-hex-char random id suitable for filenames. Random
// (not time-based) so concurrent requests on the same millisecond don't
// collide.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a time-based id so callers don't have to handle
		// crypto/rand failure paths — collision probability remains
		// negligible in practice.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// QueueDir returns the absolute path of the queue directory, given the
// SQLite database path the ingestor and server share.
func QueueDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), QueueDirName)
}

// EnsureDir creates the queue directory if missing.
func EnsureDir(dbPath string) (string, error) {
	dir := QueueDir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// validID rejects anything that could escape the queue directory.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// RequestPath returns the full path for the request-<id>.json marker.
func RequestPath(dbPath, id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid prune request id")
	}
	return filepath.Join(QueueDir(dbPath), "request-"+id+".json"), nil
}

// ResultPath returns the full path for the result-<id>.json marker.
func ResultPath(dbPath, id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid prune request id")
	}
	return filepath.Join(QueueDir(dbPath), "result-"+id+".json"), nil
}

// WriteRequest atomically writes a request file (temp file + rename).
func WriteRequest(dbPath string, req Request) error {
	if !validID(req.ID) {
		return errors.New("invalid prune request id")
	}
	if _, err := EnsureDir(dbPath); err != nil {
		return err
	}
	p, _ := RequestPath(dbPath, req.ID)
	b, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(p, b); err != nil {
		return err
	}
	return nil
}

// WriteResult atomically writes a result file (temp file + rename),
// then removes the matching request file. Callers (the ingestor) hold
// the only writer.
func WriteResult(dbPath string, res Result) error {
	if !validID(res.ID) {
		return errors.New("invalid prune request id")
	}
	if _, err := EnsureDir(dbPath); err != nil {
		return err
	}
	p, _ := ResultPath(dbPath, res.ID)
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(p, b); err != nil {
		return err
	}
	reqPath, _ := RequestPath(dbPath, res.ID)
	_ = os.Remove(reqPath)
	return nil
}

// ReadResult reads result-<id>.json. Returns (nil, nil) if the result
// file does not yet exist (request still pending or unknown id).
func ReadResult(dbPath, id string) (*Result, error) {
	p, err := ResultPath(dbPath, id)
	if err != nil {
		return nil, err
	}
	b, err := readRegularFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RequestExists returns true if request-<id>.json is still present
// (i.e. the ingestor has not processed it yet).
func RequestExists(dbPath, id string) (bool, error) {
	p, err := RequestPath(dbPath, id)
	if err != nil {
		return false, err
	}
	fi, err := os.Lstat(p)
	if err == nil {
		// A symlink or other non-regular entry is not a real request.
		return fi.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ListPending returns all request-<id>.json files in the queue dir, in
// lexicographic order. Used by the ingestor's maintenance loop.
func ListPending(dbPath string) ([]string, error) {
	dir := QueueDir(dbPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "request-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

// ReadRequest reads and parses a request file by full path.
func ReadRequest(path string) (*Request, error) {
	b, err := readRegularFile(path)
	if err != nil {
		return nil, err
	}
	var r Request
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// maxFileBytes caps how much of a queue file is read. The documents are
// small JSON (a request lists pubkeys at ~70 bytes each, so even 100k nodes
// is ~7 MiB); 16 MiB leaves headroom while stopping a hostile peer sharing
// the directory from making the reader allocate unbounded memory.
const maxFileBytes = 16 << 20

// writeFileAtomic writes b to path via a randomly named temp file in the
// same directory (os.CreateTemp is O_EXCL, so a symlink pre-planted at any
// guessable name is never followed), fsyncs, then renames over path. Rename
// replaces a symlink at path itself rather than writing through it. The
// result is mode 0644 so the other process (a different user) can read it.
func writeFileAtomic(path string, b []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o644); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readRegularFile reads path only if it is a regular file (not a symlink,
// directory, device or FIFO) no larger than maxFileBytes. A missing file
// yields an error satisfying os.IsNotExist. The opened handle is re-checked
// against the Lstat result so a swap between the two calls is detected.
func readRegularFile(path string) ([]byte, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !li.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || !os.SameFile(li, fi) {
		return nil, fmt.Errorf("%s: changed while opening", filepath.Base(path))
	}
	if fi.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s: exceeds %d bytes", filepath.Base(path), maxFileBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("%s: exceeds %d bytes", filepath.Base(path), maxFileBytes)
	}
	return b, nil
}
