// Package infraqueue defines the on-disk protocol used by the read-only
// server (cmd/server) to ask the writer-owning ingestor (cmd/ingestor) to
// set/clear a node's `infrastructure` flag.
//
// Rationale: the server opens SQLite with mode=ro (#1283/#1289), so it
// cannot execute UPDATE statements. The admin panel's infrastructure
// toggle still presents its HTTP API on the server, but the actual write
// is performed by the ingestor's maintenance loop. Communication uses
// small JSON marker files written into a directory next to the SQLite
// database — the same shape as internal/prunequeue, which solved this
// exact problem for the geo-prune admin action.
//
// Layout (under <dir(dbPath)>/infra-requests/):
//
//	request-<id>.json   — written by server when an admin toggles a node
//	result-<id>.json    — written by ingestor after the UPDATE completes; the
//	                      ingestor removes the request file in the same step
//	                      using os.Rename (atomic on POSIX).
//
// The server polls result-<id>.json to surface progress on
// GET /api/admin/nodes/infrastructure/status?id=<id>.
package infraqueue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// QueueDirName is the subdirectory (under the SQLite data dir) holding
// request/result marker files.
const QueueDirName = "infra-requests"

// Request is the payload the server writes to request-<id>.json.
// ActorAdminID/ActorUsername are for the ingestor's log line only (a
// lightweight audit trail) — they are not persisted anywhere queryable.
type Request struct {
	ID             string    `json:"id"`
	RequestedAt    time.Time `json:"requestedAt"`
	PublicKey      string    `json:"publicKey"`
	Infrastructure bool      `json:"infrastructure"`
	ActorAdminID   *int64    `json:"actorAdminId,omitempty"`
	ActorUsername  string    `json:"actorUsername,omitempty"`
}

// Result is what the ingestor writes to result-<id>.json after running
// the UPDATE. Applied is false (with Error set) when the public key
// matched no row in either nodes or inactive_nodes.
type Result struct {
	ID          string    `json:"id"`
	RequestedAt time.Time `json:"requestedAt"`
	CompletedAt time.Time `json:"completedAt"`
	Applied     bool      `json:"applied"`
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
		return "", errors.New("invalid infra request id")
	}
	return filepath.Join(QueueDir(dbPath), "request-"+id+".json"), nil
}

// ResultPath returns the full path for the result-<id>.json marker.
func ResultPath(dbPath, id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid infra request id")
	}
	return filepath.Join(QueueDir(dbPath), "result-"+id+".json"), nil
}

// WriteRequest atomically writes a request file (temp file + rename).
func WriteRequest(dbPath string, req Request) error {
	if !validID(req.ID) {
		return errors.New("invalid infra request id")
	}
	if _, err := EnsureDir(dbPath); err != nil {
		return err
	}
	p, _ := RequestPath(dbPath, req.ID)
	b, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// WriteResult atomically writes a result file (temp file + rename),
// then removes the matching request file. Callers (the ingestor) hold
// the only writer.
func WriteResult(dbPath string, res Result) error {
	if !validID(res.ID) {
		return errors.New("invalid infra request id")
	}
	if _, err := EnsureDir(dbPath); err != nil {
		return err
	}
	p, _ := ResultPath(dbPath, res.ID)
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
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
	b, err := os.ReadFile(p)
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
	_, err = os.Stat(p)
	if err == nil {
		return true, nil
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
		if e.IsDir() {
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Request
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
