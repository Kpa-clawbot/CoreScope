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
//	snapshot.json.*.tmp — transient random-named temp (rename source)
//
// The file is rewritten in full each cycle (idempotent overwrite). The
// ingestor reads the file at most once per persist tick; if absent, the
// tick is a no-op.
//
// Store abstracts the handoff: NewFS is this file layout (SQLite, shared data
// directory); NewPG keeps each snapshot as a row of <schema>.mbcap_snapshots,
// so a PostgreSQL deployment needs no shared filesystem.
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
	root, err := openQueueRoot(QueueDir(dbPath), true)
	if err != nil {
		return err
	}
	return root.Close()
}

// SnapshotPath returns the absolute path of snapshot.json under dbPath.
func SnapshotPath(dbPath string) string {
	return filepath.Join(QueueDir(dbPath), SnapshotFileName)
}

// WriteSnapshot atomically replaces snapshot.json with the given payload.
// Uses tmp-then-rename so a reader never sees a torn file.
func WriteSnapshot(dbPath string, snap Snapshot) error {
	b, err := marshalSnapshot(snap)
	if err != nil {
		return err
	}
	if err := checkSize(len(b)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	root, err := openQueueRoot(QueueDir(dbPath), true)
	if err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}
	defer root.Close()
	if err := writeFileAtomic(root, SnapshotFileName, b); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// marshalSnapshot stamps a zero WrittenAt with the current time and encodes
// the snapshot as both stores keep it.
func marshalSnapshot(snap Snapshot) ([]byte, error) {
	if snap.WrittenAt.IsZero() {
		snap.WrittenAt = time.Now().UTC()
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return b, nil
}

// ReadSnapshot loads the current snapshot.json. Returns os.ErrNotExist
// when no snapshot has been written yet — callers should treat that as
// "nothing to persist" rather than an error.
func ReadSnapshot(dbPath string) (Snapshot, error) {
	var snap Snapshot
	root, err := openQueueRoot(QueueDir(dbPath), false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snap, os.ErrNotExist
		}
		return snap, fmt.Errorf("read: %w", err)
	}
	defer root.Close()
	b, err := readRegularFile(root, SnapshotFileName)
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
