package prunequeue

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"
)

// DefaultPGSchema is the schema holding the Postgres control tables.
const DefaultPGSchema = "corescope_control"

// pgIDPattern is the id CHECK on the control tables. It is stricter than
// validID (lower-case only, at least 16 digits); NewID always satisfies it.
var pgIDPattern = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// pgSchemaPattern bounds the schema name to a plain lower-case identifier, so
// it can be spliced into SQL (quoted) without any escaping concerns.
var pgSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// pgNow renders the database clock as UTC RFC3339 text with a fixed nine-digit
// fraction. The database clock (not each process's) orders rows written by
// different hosts, and the fixed width makes text order (COLLATE "C") equal
// time order; time.Parse(time.RFC3339Nano, ...) reads it back.
var pgNow = pgUTCText(`clock_timestamp()`)

// pgUTCText renders the timestamptz SQL expression ts in the created_at format.
func pgUTCText(ts string) string {
	return `to_char((` + ts + `) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"')`
}

// pgQueue is the Queue on the control tables <schema>.prune_requests and
// <schema>.prune_results (id, created_at, payload), payload being the same
// JSON the marker files hold. The server side only INSERTs prune_requests and
// SELECTs both tables; every UPDATE/DELETE is on the ingestor side.
type pgQueue struct {
	db       *sql.DB
	schema   string
	requests string // quoted, schema-qualified table names
	results  string
}

// NewPG returns the Queue backed by the control tables in schema ("" means
// DefaultPGSchema). db may use either the dbx "pgx-qmark" driver or plain pgx:
// queries use $n placeholders. It panics on a schema name that is not a plain
// lower-case identifier — the name is a constant, so that is a programming
// error.
func NewPG(db *sql.DB, schema string) Queue {
	if schema == "" {
		schema = DefaultPGSchema
	}
	if !pgSchemaPattern.MatchString(schema) {
		panic(fmt.Sprintf("prunequeue: invalid Postgres schema name %q", schema))
	}
	return &pgQueue{
		db:       db,
		schema:   schema,
		requests: `"` + schema + `".prune_requests`,
		results:  `"` + schema + `".prune_results`,
	}
}

func (q *pgQueue) Location() string { return "postgres:" + q.schema }

// marshalPayload encodes v exactly as the marker files do and refuses a
// document over the cap before it is sent.
func marshalPayload(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	if len(b) > maxFileBytes {
		return "", fmt.Errorf("prune queue payload is %d bytes, exceeds %d", len(b), maxFileBytes)
	}
	return string(b), nil
}

func unmarshalPayload(payload string, v any) error {
	if len(payload) > maxFileBytes {
		return fmt.Errorf("prune queue payload exceeds %d bytes", maxFileBytes)
	}
	return json.Unmarshal([]byte(payload), v)
}

func (q *pgQueue) WriteRequest(req Request) error {
	if !pgIDPattern.MatchString(req.ID) {
		return errInvalidID
	}
	payload, err := marshalPayload(req)
	if err != nil {
		return err
	}
	// A plain INSERT: the server role has no UPDATE, and ids are random, so a
	// duplicate is an error rather than something to overwrite.
	_, err = q.db.Exec(`INSERT INTO `+q.requests+` (id, created_at, payload) VALUES ($1, `+pgNow+`, $2)`,
		req.ID, payload)
	return err
}

func (q *pgQueue) RequestExists(id string) (bool, error) {
	if !pgIDPattern.MatchString(id) {
		return false, errInvalidID
	}
	var one int
	err := q.db.QueryRow(`SELECT 1 FROM `+q.requests+` WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (q *pgQueue) ReadResult(id string) (*Result, error) {
	if !pgIDPattern.MatchString(id) {
		return nil, errInvalidID
	}
	var payload string
	err := q.db.QueryRow(`SELECT payload FROM `+q.results+` WHERE id = $1`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Result
	if err := unmarshalPayload(payload, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (q *pgQueue) ListPending() ([]string, error) {
	rows, err := q.db.Query(`SELECT id FROM ` + q.requests + ` ORDER BY created_at COLLATE "C", id COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (q *pgQueue) ReadRequest(id string) (*Request, error) {
	if !pgIDPattern.MatchString(id) {
		return nil, errInvalidID
	}
	var payload string
	err := q.db.QueryRow(`SELECT payload FROM `+q.requests+` WHERE id = $1`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("prune request %s: %w", id, os.ErrNotExist)
	}
	if err != nil {
		return nil, err
	}
	var r Request
	if err := unmarshalPayload(payload, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Complete upserts the result and deletes the request in one transaction, so
// the server never sees the request gone without its result (or both).
func (q *pgQueue) Complete(res Result) (err error) {
	if !pgIDPattern.MatchString(res.ID) {
		return errInvalidID
	}
	payload, err := marshalPayload(res)
	if err != nil {
		return err
	}
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`INSERT INTO `+q.results+` (id, created_at, payload) VALUES ($1, `+pgNow+`, $2)
		ON CONFLICT (id) DO UPDATE SET created_at = EXCLUDED.created_at, payload = EXCLUDED.payload`,
		res.ID, payload); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM `+q.requests+` WHERE id = $1`, res.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *pgQueue) PruneResults(olderThan time.Duration) error {
	if olderThan < 0 {
		return fmt.Errorf("prune results: negative age %s", olderThan)
	}
	_, err := q.db.Exec(`DELETE FROM `+q.results+` WHERE created_at COLLATE "C" < `+
		pgUTCText(`clock_timestamp() - make_interval(secs => $1)`),
		olderThan.Seconds())
	return err
}
