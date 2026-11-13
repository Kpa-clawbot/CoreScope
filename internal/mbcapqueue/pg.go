package mbcapqueue

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
)

// DefaultPGSchema is the schema holding the Postgres control tables.
const DefaultPGSchema = "corescope_control"

// pgSchemaPattern bounds the schema name to a plain lower-case identifier, so
// it can be spliced into SQL (quoted) without any escaping concerns.
var pgSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// pgNow renders the database clock as UTC RFC3339 text with a fixed nine-digit
// fraction. The database clock (not each process's) orders rows written by
// different hosts, and the fixed width makes text order (COLLATE "C") equal
// time order; time.Parse(time.RFC3339Nano, ...) reads it back.
const pgNow = `to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"')`

// pgNewestFirst orders snapshots newest first; the random id breaks ties.
const pgNewestFirst = `ORDER BY created_at COLLATE "C" DESC, id COLLATE "C" DESC`

// pgStore is the Store on <schema>.mbcap_snapshots (id, created_at, payload),
// payload being the JSON snapshot.json holds. The server only INSERTs; the
// ingestor SELECTs the newest row and DELETEs old ones.
type pgStore struct {
	db     *sql.DB
	schema string
	table  string // quoted, schema-qualified
}

// NewPG returns the Store backed by the control table in schema ("" means
// DefaultPGSchema). db may use either the dbx "pgx-qmark" driver or plain pgx:
// queries use $n placeholders. It panics on a schema name that is not a plain
// lower-case identifier — the name is a constant, so that is a programming
// error.
func NewPG(db *sql.DB, schema string) Store {
	if schema == "" {
		schema = DefaultPGSchema
	}
	if !pgSchemaPattern.MatchString(schema) {
		panic(fmt.Sprintf("mbcapqueue: invalid Postgres schema name %q", schema))
	}
	return &pgStore{db: db, schema: schema, table: `"` + schema + `".mbcap_snapshots`}
}

func (s *pgStore) Location() string { return "postgres:" + s.schema }

// WriteSnapshot inserts a new row under a random 32-hex-digit id. The payload
// is refused over the cap before it is sent.
func (s *pgStore) WriteSnapshot(snap Snapshot) error {
	b, err := marshalSnapshot(snap)
	if err != nil {
		return err
	}
	if len(b) > maxFileBytes {
		return fmt.Errorf("write: snapshot is %d bytes, exceeds %d", len(b), maxFileBytes)
	}
	var r [16]byte
	if _, err := rand.Read(r[:]); err != nil {
		return fmt.Errorf("write: id: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO `+s.table+` (id, created_at, payload) VALUES ($1, `+pgNow+`, $2)`,
		hex.EncodeToString(r[:]), string(b)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// ReadSnapshot errors are wrapped as the file Store's are ("read:",
// "unmarshal:"), which the ingestor uses to classify a malformed snapshot.
func (s *pgStore) ReadSnapshot() (Snapshot, error) {
	var snap Snapshot
	var payload string
	err := s.db.QueryRow(`SELECT payload FROM ` + s.table + ` ` + pgNewestFirst + ` LIMIT 1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, os.ErrNotExist
	}
	if err != nil {
		return snap, fmt.Errorf("read: %w", err)
	}
	if len(payload) > maxFileBytes {
		return snap, fmt.Errorf("read: snapshot exceeds %d bytes", maxFileBytes)
	}
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		return Snapshot{}, fmt.Errorf("unmarshal: %w", err)
	}
	return snap, nil
}

func (s *pgStore) Prune(keep int) error {
	if err := checkKeep(keep); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM `+s.table+` WHERE id NOT IN (SELECT id FROM `+s.table+` `+pgNewestFirst+` LIMIT $1)`, keep)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	return nil
}
