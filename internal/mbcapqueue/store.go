package mbcapqueue

import (
	"fmt"
)

// Store is the server->ingestor multi-byte capability handoff.
type Store interface {
	// WriteSnapshot publishes snap (server side). A zero WrittenAt is set to
	// the current time.
	WriteSnapshot(snap Snapshot) error
	// ReadSnapshot returns the newest snapshot (ingestor side), or an empty
	// Snapshot and an error satisfying errors.Is(err, os.ErrNotExist) when
	// none has been written.
	ReadSnapshot() (Snapshot, error)
	// Prune drops all but the newest keep snapshots (ingestor side). keep
	// must not be negative.
	Prune(keep int) error
	// Location describes where snapshots live, for log lines.
	Location() string
}

// fsStore is the single snapshot.json under QueueDir(dbPath). It delegates to
// the package-level functions, so both APIs share one implementation.
type fsStore struct {
	dbPath string
}

// NewFS returns the file Store that lives next to the SQLite database at
// dbPath.
func NewFS(dbPath string) Store { return fsStore{dbPath: dbPath} }

func (s fsStore) WriteSnapshot(snap Snapshot) error { return WriteSnapshot(s.dbPath, snap) }

func (s fsStore) ReadSnapshot() (Snapshot, error) { return ReadSnapshot(s.dbPath) }

// Prune is a no-op beyond validation: each write replaces the one file.
func (s fsStore) Prune(keep int) error { return checkKeep(keep) }

func (s fsStore) Location() string { return QueueDir(s.dbPath) }

func checkKeep(keep int) error {
	if keep < 0 {
		return fmt.Errorf("prune: negative keep %d", keep)
	}
	return nil
}
