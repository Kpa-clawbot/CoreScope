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
//
// Queue abstracts the channel: NewFS is this marker-file layout (SQLite,
// shared data directory); NewPG carries the same JSON documents in the
// <schema>.prune_requests / prune_results control tables, so a PostgreSQL
// deployment needs no shared filesystem.
package prunequeue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	root, err := openQueueRoot(dir, true)
	if err != nil {
		return "", err
	}
	_ = root.Close()
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
		return "", errInvalidID
	}
	return filepath.Join(QueueDir(dbPath), "request-"+id+".json"), nil
}

// ResultPath returns the full path for the result-<id>.json marker.
func ResultPath(dbPath, id string) (string, error) {
	if !validID(id) {
		return "", errInvalidID
	}
	return filepath.Join(QueueDir(dbPath), "result-"+id+".json"), nil
}

// WriteRequest atomically writes a request file (temp file + rename).
func WriteRequest(dbPath string, req Request) error {
	if !validID(req.ID) {
		return errInvalidID
	}
	b, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	root, err := openQueueRoot(QueueDir(dbPath), true)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeFileAtomic(root, "request-"+req.ID+".json", b)
}

// WriteResult atomically writes a result file (temp file + rename),
// then removes the matching request file. Callers (the ingestor) hold
// the only writer.
func WriteResult(dbPath string, res Result) error {
	if !validID(res.ID) {
		return errInvalidID
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	root, err := openQueueRoot(QueueDir(dbPath), true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := writeFileAtomic(root, "result-"+res.ID+".json", b); err != nil {
		return err
	}
	_ = root.Remove("request-" + res.ID + ".json")
	return nil
}

// ReadResult reads result-<id>.json. Returns (nil, nil) if the result
// file does not yet exist (request still pending or unknown id).
func ReadResult(dbPath, id string) (*Result, error) {
	if !validID(id) {
		return nil, errInvalidID
	}
	root, err := openQueueRoot(QueueDir(dbPath), false)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	b, err := readRegularFile(root, "result-"+id+".json")
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
	if !validID(id) {
		return false, errInvalidID
	}
	root, err := openQueueRoot(QueueDir(dbPath), false)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer root.Close()
	fi, err := root.Lstat("request-" + id + ".json")
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
	root, err := openQueueRoot(dir, false)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
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

// ReadRequest reads and parses a request file by full path. Only the file's
// own name is resolved inside its (non-symlink) parent directory.
func ReadRequest(path string) (*Request, error) {
	root, err := openQueueRoot(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := readRegularFile(root, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	var r Request
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
