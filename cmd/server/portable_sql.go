package main

// SQL fragments shared by the packet-store loaders (Load, loadChunk,
// LoadChunked, IngestNewFromDB, IngestNewObservations), which must produce
// byte-identical in-memory packets from SQLite and Postgres.

// obsTimestampISOSQL renders observations.timestamp (alias o, epoch seconds)
// as the UTC text the store keeps in StoreObs.Timestamp and serves from the
// API, e.g. "2026-11-01T12:34:56.000Z". SQLite's %f is seconds with three
// decimals, always ".000" for the integer column, which is exactly to_char's
// MS of a whole second; AT TIME ZONE 'UTC' makes the Postgres form independent
// of the session time zone.
func (db *DB) obsTimestampISOSQL() string {
	return db.sqlFor(
		`strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')`,
		`to_char(to_timestamp(o.timestamp) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`)
}

// The loaders' row orders. Each starts with the order the loader has always
// used and ends in ids, so the order is total: ties (two transmissions first
// seen in the same second, two observations of one transmission stamped in the
// same second) come back the same on both backends instead of in whatever
// order each planner happens to produce. Observation ties go newest id first,
// the order SQLite already returns them in when it reads
// idx_observations_tx_ts (transmission_id, timestamp, rowid) backwards.
//
// NULLS LAST is SQLite's default for DESC, and Postgres's opposite. o.timestamp
// is NULL only on a transmission's single no-observation row, which ordering by
// first_seen interleaves with other transmissions' rows, so it decides where
// that transmission lands in the store's packet order.
const (
	loadOrderByFirstSeen = `ORDER BY t.first_seen ASC, o.timestamp DESC NULLS LAST, t.id ASC, o.id DESC`
	loadOrderByTxID      = `ORDER BY t.id ASC, o.timestamp DESC, o.id DESC`
)
