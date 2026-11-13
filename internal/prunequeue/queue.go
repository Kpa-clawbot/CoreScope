package prunequeue

import (
	"errors"
	"time"
)

// Queue is the server<->ingestor geo-prune channel. NewFS keeps the marker
// files described in the package comment (SQLite deployments, which share a
// data directory); NewPG uses control tables so the two processes need no
// shared filesystem (PostgreSQL deployments).
type Queue interface {
	// server side

	// WriteRequest enqueues req; req.ID comes from NewID().
	WriteRequest(req Request) error
	// RequestExists reports whether the request is still pending (not yet
	// completed by the ingestor).
	RequestExists(id string) (bool, error)
	// ReadResult returns the result for id, or (nil, nil) while none exists
	// (request still pending, or unknown id).
	ReadResult(id string) (*Result, error)

	// ingestor side

	// ListPending returns the ids of pending requests, oldest first.
	ListPending() ([]string, error)
	// ReadRequest returns the pending request id. A request that does not
	// exist yields an error satisfying errors.Is(err, os.ErrNotExist).
	ReadRequest(id string) (*Request, error)
	// Complete stores the result for res.ID (replacing any earlier one) and
	// removes its request.
	Complete(res Result) error
	// PruneResults removes results stored more than olderThan ago, so
	// completed requests do not accumulate.
	PruneResults(olderThan time.Duration) error
	// Location describes where the queue lives, for log lines.
	Location() string
}

// errInvalidID is the error every Queue returns for a malformed id. The
// server's status handler matches its text to answer 400.
var errInvalidID = errors.New("invalid prune request id")
