// Package mbcapqueue defines the on-disk handoff used by the read-only
// server (cmd/server) to publish multi-byte capability snapshots that
// the writer-owning ingestor (cmd/ingestor) persists to the nodes /
// inactive_nodes tables.
//
// Rationale: PR #903 originally added a server-side persistMultibyteCapability
// that executed UPDATEs on nodes/inactive_nodes — a hard violation of the
// read-only-server invariant established in #1283/#1287/#1289 (the server
// opens SQLite with mode=ro). The capability computation is heavy and lives
// in the server's analytics cycle; rather than duplicate it in the ingestor,
// the server writes a snapshot file under <dataDir>/mbcap-snapshot/ and the
// ingestor's maintenance loop picks it up and writes to the DB.
//
// Pattern mirrors internal/prunequeue (#669/#738).
//
// Layout (under <dir(dbPath)>/mbcap-snapshot/):
//
//	snapshot.json       — atomic-replaced by the server each analytics cycle
//	snapshot.json.tmp   — transient (rename target)
//
// The file is rewritten in full each cycle (idempotent overwrite). The
// ingestor reads the file at most once per persist tick; if absent, the
// tick is a no-op.
package mbcapqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// QueueDirName is the subdirectory (under the SQLite data dir) holding
// the snapshot file.
const QueueDirName = "mbcap-snapshot"

// SnapshotFileName is the canonical snapshot file written by the server.
const SnapshotFileName = "snapshot.json"

// Entry is one node's multi-byte capability as derived by the server's
// analytics cycle. Status is the human label ("confirmed", "suspected",
// "unknown"); the ingestor maps it to the DB sup integer.
//
// Entries with Status=="unknown" are NEVER persisted (the writer must
// not overwrite a previously confirmed/suspected DB value with a
// snapshot blank — same data-destruction guard the server enforced).
type Entry struct {
	PublicKey string `json:"public_key"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence,omitempty"`
}

// Snapshot is the full payload the server writes.
type Snapshot struct {
	WrittenAt time.Time `json:"writtenAt"`
	Entries   []Entry   `json:"entries"`
}

// QueueDir returns the absolute path of the snapshot directory, given
// the SQLite database path the ingestor and server share.
func QueueDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), QueueDirName)
}

// EnsureDir creates the snapshot directory if missing.
func EnsureDir(dbPath string) error {
	return os.MkdirAll(QueueDir(dbPath), 0o755)
}

// SnapshotPath returns the absolute path of snapshot.json under dbPath.
func SnapshotPath(dbPath string) string {
	return filepath.Join(QueueDir(dbPath), SnapshotFileName)
}

// WriteSnapshot atomically replaces snapshot.json with the given payload.
// Uses tmp-then-rename so a reader never sees a torn file.
func WriteSnapshot(dbPath string, snap Snapshot) error {
	if err := EnsureDir(dbPath); err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}
	if snap.WrittenAt.IsZero() {
		snap.WrittenAt = time.Now().UTC()
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	final := SnapshotPath(dbPath)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// ReadSnapshot loads the current snapshot.json. Returns os.ErrNotExist
// when no snapshot has been written yet — callers should treat that as
// "nothing to persist" rather than an error.
func ReadSnapshot(dbPath string) (Snapshot, error) {
	var snap Snapshot
	b, err := os.ReadFile(SnapshotPath(dbPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snap, os.ErrNotExist
		}
		return snap, fmt.Errorf("read: %w", err)
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return snap, fmt.Errorf("unmarshal: %w", err)
	}
	return snap, nil
}
