package prunequeue

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// fsQueue is the marker-file Queue under QueueDir(dbPath). It delegates to the
// package-level functions, so both APIs share one implementation.
type fsQueue struct {
	dbPath string
}

// NewFS returns the marker-file Queue that lives next to the SQLite database
// at dbPath.
func NewFS(dbPath string) Queue { return fsQueue{dbPath: dbPath} }

func (q fsQueue) WriteRequest(req Request) error { return WriteRequest(q.dbPath, req) }

func (q fsQueue) RequestExists(id string) (bool, error) { return RequestExists(q.dbPath, id) }

func (q fsQueue) ReadResult(id string) (*Result, error) { return ReadResult(q.dbPath, id) }

func (q fsQueue) Complete(res Result) error { return WriteResult(q.dbPath, res) }

func (q fsQueue) Location() string { return QueueDir(q.dbPath) }

func (q fsQueue) ReadRequest(id string) (*Request, error) {
	p, err := RequestPath(q.dbPath, id)
	if err != nil {
		return nil, err
	}
	return ReadRequest(p)
}

// queueEntry is one regular marker file named <prefix><id>.json.
type queueEntry struct {
	id  string
	mod time.Time
}

// listMarkers returns the regular files named <prefix><id>.json with a valid
// id. A missing queue directory is an empty list.
func (q fsQueue) listMarkers(prefix string) ([]queueEntry, error) {
	root, err := openQueueRoot(QueueDir(q.dbPath), false)
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
	var out []queueEntry
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
		if !validID(id) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue // removed since ReadDir
			}
			return nil, err
		}
		out = append(out, queueEntry{id: id, mod: fi.ModTime()})
	}
	return out, nil
}

// ListPending orders requests by file modification time (the write time),
// ties broken by id.
func (q fsQueue) ListPending() ([]string, error) {
	ents, err := q.listMarkers("request-")
	if err != nil {
		return nil, err
	}
	sort.Slice(ents, func(i, j int) bool {
		if !ents[i].mod.Equal(ents[j].mod) {
			return ents[i].mod.Before(ents[j].mod)
		}
		return ents[i].id < ents[j].id
	})
	if len(ents) > MaxPendingList {
		ents = ents[:MaxPendingList]
	}
	ids := make([]string, len(ents))
	for i, e := range ents {
		ids[i] = e.id
	}
	return ids, nil
}

// PendingCount counts the request files, however many ListPending returns.
func (q fsQueue) PendingCount() (int, error) {
	ents, err := q.listMarkers("request-")
	return len(ents), err
}

// PruneResults removes result files last written more than olderThan ago.
func (q fsQueue) PruneResults(olderThan time.Duration) error {
	if olderThan < 0 {
		return fmt.Errorf("prune results: negative age %s", olderThan)
	}
	ents, err := q.listMarkers("result-")
	if err != nil || len(ents) == 0 {
		return err
	}
	cutoff := time.Now().Add(-olderThan)
	root, err := openQueueRoot(QueueDir(q.dbPath), false)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, e := range ents {
		if !e.mod.Before(cutoff) {
			continue
		}
		if err := root.Remove("result-" + e.id + ".json"); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
