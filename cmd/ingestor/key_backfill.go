package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Key-set backfill (#2107).
//
// A GRP_TXT stored before its channel key was known keeps channel_hash
// "enc_XX" forever, and a transport-scoped packet stored before its region key
// was known keeps scope_name "" forever: ingest decides once, at insert time.
// When the key set grows - a hashChannels entry is added and the ingestor
// restarts, or the autoRegionKeys tier picks up a newly declared region - this
// worker re-decides exactly those unresolved rows with the keys now in force.
//
// Rules, each pinned by a test in key_backfill_test.go:
//
//   - Only unresolved rows are touched. A decrypted channel or a named scope is
//     never rewritten, even if a newer key would now decide it differently.
//     Every UPDATE carries the unresolved value in its WHERE clause, so a row
//     that live ingest or another writer resolved meanwhile is left alone.
//   - The decision is the one ingest makes today: DecodePacket with the full
//     channel key map, and regionKeySnapshot.match with the full region
//     snapshot - including abstaining on an ambiguous collision (#1609). The
//     ingest-path counters are not touched; they measure live traffic.
//   - Bounded and resumable: fixed windows of transmissions.id, one
//     transaction per window holding the rewrites, their feed rows and the
//     advanced cursor together. A cancelled or crashed run resumes from the
//     last commit; nothing is applied twice and nothing is skipped.
//   - A run is tied to the fingerprint of the key set it started with. If the
//     key set changes mid-run the run restarts from the beginning with the new
//     set, so a key added halfway through still sees all of history.
//   - Each key gets one complete pass. Its fingerprint is recorded when a run
//     finishes; a restart with an unchanged key set does no work. A key that
//     leaves the set loses its record, so if it returns it is replayed over
//     whatever arrived while it was absent.
//   - Every rewritten transmission gets a tx_rewrite_feed row in the same
//     transaction. The server tails that feed (cmd/server/tx_rewrite_feed.go)
//     and refreshes the in-memory copy, so no restart is needed.

const (
	keyBackfillKindChannel = "channel"
	keyBackfillKindRegion  = "region"

	// keyBackfillWindow is how many transmission ids one batch covers. It
	// bounds the READ as well as the write: the ingestor has a single SQLite
	// connection, and unresolved rows are sparse (hundreds in a million), so a
	// "next N matching rows" query would scan most of the table while live
	// ingest waits. A fixed id window keeps every query a short primary-key
	// range scan however few rows match. ~230 batches cover 1.1M ids.
	keyBackfillWindow = 5000
	// keyBackfillYield is the pause between batches that hands the single
	// SQLite writer back to live ingest.
	keyBackfillYield = 10 * time.Millisecond
	// txRewriteFeedRetention bounds the feed. The server only needs rows it
	// has not tailed yet; a server that was down longer than this reloads the
	// rewritten rows from the DB on startup anyway.
	txRewriteFeedRetention = 24 * time.Hour
)

// keyBackfiller re-decides unresolved rows when the key set grows. One
// goroutine runs every pass, so two passes never race each other; Kick only
// asks that goroutine to look again.
type keyBackfiller struct {
	store       *Store
	channelKeys map[string]string // loaded once at startup; immutable
	regionSet   *regionKeySet
	window      int64 // ids per batch; see keyBackfillWindow
	yield       time.Duration
	wake        chan struct{}
}

func newKeyBackfiller(store *Store, channelKeys map[string]string, regionSet *regionKeySet) *keyBackfiller {
	return &keyBackfiller{
		store:       store,
		channelKeys: channelKeys,
		regionSet:   regionSet,
		window:      keyBackfillWindow,
		yield:       keyBackfillYield,
		wake:        make(chan struct{}, 1),
	}
}

// Kick schedules a pass. It never blocks: a pass already pending absorbs it.
func (b *keyBackfiller) Kick() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Start runs a first pass and then one more per Kick, until ctx is cancelled.
// The returned channel closes when the goroutine has exited.
func (b *keyBackfiller) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	b.Kick()
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.wake:
			}
			b.pass(ctx)
		}
	}()
	return done
}

// pass runs both kinds once and prunes the feed. Errors are logged, not
// fatal: the state row keeps the cursor, so the next Kick or restart resumes.
func (b *keyBackfiller) pass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[key-backfill] panic recovered: %v", r)
		}
	}()
	for _, kind := range []string{keyBackfillKindChannel, keyBackfillKindRegion} {
		if err := b.runKind(ctx, kind); err != nil {
			if ctx.Err() != nil {
				log.Printf("[key-backfill] %s pass interrupted; will resume from the last committed batch", kind)
				return
			}
			log.Printf("[key-backfill] %s pass failed, will retry on the next key change or restart: %v", kind, err)
		}
	}
	if _, err := b.store.db.ExecContext(ctx, `DELETE FROM tx_rewrite_feed WHERE created_at < ?`,
		time.Now().Add(-txRewriteFeedRetention).Unix()); err != nil && ctx.Err() == nil {
		log.Printf("[key-backfill] pruning tx_rewrite_feed: %v", err)
	}
}

// keyFingerprint identifies one key by name AND key material, so a channel
// whose secret changes under the same name counts as a new key.
func keyFingerprint(kind, name string, key []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// keySetFingerprint identifies a whole key set independent of map order.
func keySetFingerprint(fps []string) string {
	sorted := append([]string(nil), fps...)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(h[:16])
}

// channelKeyHashByte is the channel-hash byte a GRP_TXT encrypted under key
// carries in clear: the first byte of SHA-256 over the 16-byte secret
// (MeshCore firmware, GroupChannel::hash). ok is false for a malformed key.
func channelKeyHashByte(keyHex string) (byte, bool) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 16 {
		return 0, false
	}
	h := sha256.Sum256(key)
	return h[0], true
}

// keyBackfillPlan is what one kind needs to run: every key fingerprint in
// force, the subset never given a full pass, and for channels the hash bytes
// of the pending keys (only rows with those bytes can newly decrypt).
type keyBackfillPlan struct {
	all         []string
	pending     []string
	channelHash []string // "enc_XX" values, channel kind only
	snapshot    *regionKeySnapshot
}

func (b *keyBackfiller) plan(kind string, done map[string]bool) keyBackfillPlan {
	var p keyBackfillPlan
	switch kind {
	case keyBackfillKindChannel:
		seenHash := map[string]bool{}
		for name, keyHex := range b.channelKeys {
			raw, _ := hex.DecodeString(keyHex)
			fp := keyFingerprint(kind, name, raw)
			p.all = append(p.all, fp)
			if done[fp] {
				continue
			}
			p.pending = append(p.pending, fp)
			if hb, ok := channelKeyHashByte(keyHex); ok {
				enc := fmt.Sprintf("enc_%02X", hb)
				if !seenHash[enc] {
					seenHash[enc] = true
					p.channelHash = append(p.channelHash, enc)
				}
			}
		}
		sort.Strings(p.channelHash)
	case keyBackfillKindRegion:
		p.snapshot = b.regionSet.snapshot()
		for name, key := range p.snapshot.all {
			fp := keyFingerprint(kind, name, key)
			p.all = append(p.all, fp)
			if !done[fp] {
				p.pending = append(p.pending, fp)
			}
		}
	}
	return p
}

// runKind brings one kind up to date with the key set currently in force.
func (b *keyBackfiller) runKind(ctx context.Context, kind string) error {
	db := b.store.db
	done, err := loadDoneKeys(ctx, db, kind)
	if err != nil {
		return err
	}
	p := b.plan(kind, done)

	// A key that left the set forgets its pass, so it is replayed if it
	// returns: rows that arrived while it was absent were decided without it.
	if err := forgetRemovedKeys(ctx, db, kind, p.all, done); err != nil {
		return err
	}

	if len(p.pending) == 0 {
		// Nothing new. Drop a state row a since-superseded run left behind.
		_, err := db.ExecContext(ctx, `DELETE FROM key_backfill_state WHERE kind=?`, kind)
		return err
	}

	runFP := keySetFingerprint(p.all)
	cursor, upper, rewritten, err := b.resumeOrStart(ctx, kind, runFP)
	if err != nil {
		return err
	}
	if cursor == 0 {
		log.Printf("[key-backfill] %s: %d of %d key(s) not yet applied to history; scanning transmissions up to id %d",
			kind, len(p.pending), len(p.all), upper)
	} else {
		log.Printf("[key-backfill] %s: resuming at id %d of %d (%d row(s) rewritten so far)", kind, cursor, upper, rewritten)
	}

	for cursor < upper {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The key set moved under us: abandon this run, the next pass starts
		// a fresh one over the new set. Region keys refresh at runtime;
		// channel keys never change without a restart.
		if kind == keyBackfillKindRegion && b.regionSet.snapshot() != p.snapshot {
			log.Printf("[key-backfill] region key set changed mid-run; restarting from the beginning")
			b.Kick()
			return nil
		}
		next, n, err := b.batchOnce(ctx, kind, p, runFP, cursor, upper)
		if err != nil {
			return err
		}
		rewritten += int64(n)
		cursor = next
		timer := time.NewTimer(b.yield)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	if err := finishRun(ctx, db, kind, runFP, p.all); err != nil {
		return err
	}
	b.store.Stats.IncBackfill("key_" + kind)
	log.Printf("[key-backfill] %s: pass complete, %d row(s) rewritten", kind, rewritten)
	return nil
}

func loadDoneKeys(ctx context.Context, db *sql.DB, kind string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT key_fp FROM key_backfill_done WHERE kind=?`, kind)
	if err != nil {
		return nil, fmt.Errorf("load done keys: %w", err)
	}
	defer rows.Close()
	done := map[string]bool{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		done[fp] = true
	}
	return done, rows.Err()
}

func forgetRemovedKeys(ctx context.Context, db *sql.DB, kind string, all []string, done map[string]bool) error {
	inForce := make(map[string]bool, len(all))
	for _, fp := range all {
		inForce[fp] = true
	}
	for fp := range done {
		if inForce[fp] {
			continue
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM key_backfill_done WHERE kind=? AND key_fp=?`, kind, fp); err != nil {
			return fmt.Errorf("forget removed key: %w", err)
		}
	}
	return nil
}

// resumeOrStart returns the cursor of the run for runFP, starting a new run
// when there is none or the stored one was for a different key set. A new run
// is bounded by the newest transmission now: anything inserted later is
// decided by live ingest with the same keys.
func (b *keyBackfiller) resumeOrStart(ctx context.Context, kind, runFP string) (cursor, upper, rewritten int64, err error) {
	db := b.store.db
	var storedFP string
	err = db.QueryRowContext(ctx, `SELECT run_fp, cursor, upper, rewritten FROM key_backfill_state WHERE kind=?`, kind).
		Scan(&storedFP, &cursor, &upper, &rewritten)
	if err == nil && storedFP == runFP {
		return cursor, upper, rewritten, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, 0, fmt.Errorf("load backfill state: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM transmissions`).Scan(&upper); err != nil {
		return 0, 0, 0, fmt.Errorf("backfill upper bound: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO key_backfill_state(kind, run_fp, cursor, upper, rewritten) VALUES(?,?,0,?,0)`,
		kind, runFP, upper); err != nil {
		return 0, 0, 0, fmt.Errorf("start backfill run: %w", err)
	}
	return 0, upper, 0, nil
}

func finishRun(ctx context.Context, db *sql.DB, kind, runFP string, all []string) error {
	writerMu.Lock()
	defer writerMu.Unlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, fp := range all {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO key_backfill_done(kind, key_fp) VALUES(?,?)`, kind, fp); err != nil {
			return fmt.Errorf("record done key: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM key_backfill_state WHERE kind=? AND run_fp=?`, kind, runFP); err != nil {
		return err
	}
	return tx.Commit()
}

// keyRewrite is one row's new state, decided before the writer is taken.
type keyRewrite struct {
	id          int64
	channelHash string // channel kind: the decrypted channel name
	decodedJSON string // channel kind
	oldChannel  string // channel kind: the enc_XX value the row must still hold
	scopeName   string // region kind
	advertFrom  string // region kind: ADVERT origin, for nodes.default_scope
}

// batchOnce decides the unresolved rows with id in (cursor, cursor+window]
// and commits the rewrites, their feed rows and the advanced cursor together.
// It returns the new cursor (always > cursor, capped at upper) and the number
// of rows actually rewritten.
func (b *keyBackfiller) batchOnce(ctx context.Context, kind string, p keyBackfillPlan, runFP string, cursor, upper int64) (int64, int, error) {
	db := b.store.db
	window := b.window
	if window <= 0 {
		window = keyBackfillWindow
	}
	hi := cursor + window
	if hi > upper {
		hi = upper
	}
	var (
		rows *sql.Rows
		err  error
	)
	switch kind {
	case keyBackfillKindChannel:
		if len(p.channelHash) == 0 {
			// No pending key has a usable hash byte: nothing in the rest of
			// the range can match, so jump the cursor straight to the end.
			return b.commitEmpty(ctx, kind, runFP, upper)
		}
		args := []interface{}{cursor, hi}
		for _, h := range p.channelHash {
			args = append(args, h)
		}
		rows, err = db.QueryContext(ctx, `
			SELECT id, COALESCE(raw_hex,''), channel_hash FROM transmissions
			WHERE id > ? AND id <= ? AND payload_type = 5
			  AND channel_hash IN (`+strings.TrimSuffix(strings.Repeat("?,", len(p.channelHash)), ",")+`)
			ORDER BY id`, args...)
	case keyBackfillKindRegion:
		rows, err = db.QueryContext(ctx, `
			SELECT id, COALESCE(raw_hex,''), '' FROM transmissions
			WHERE id > ? AND id <= ? AND scope_name = ''
			ORDER BY id`, cursor, hi)
	default:
		return cursor, 0, fmt.Errorf("unknown backfill kind %q", kind)
	}
	if err != nil {
		return cursor, 0, fmt.Errorf("select unresolved rows: %w", err)
	}

	var rewrites []keyRewrite
	for rows.Next() {
		var (
			id       int64
			raw, old string
		)
		if err := rows.Scan(&id, &raw, &old); err != nil {
			rows.Close()
			return cursor, 0, err
		}
		// Undecodable rows are simply not rewritten; the window moves past
		// them, so they are never re-selected within this run.
		if r, ok := b.decide(kind, p, id, raw, old); ok {
			rewrites = append(rewrites, r)
		}
	}
	err = rows.Err()
	rows.Close() // the single connection must be free before BeginTx
	if err != nil {
		return cursor, 0, err
	}

	n, err := b.commit(ctx, kind, runFP, hi, rewrites)
	if err != nil {
		return cursor, 0, err
	}
	return hi, n, nil
}

// commitEmpty advances the cursor over a window that cannot contain a match.
func (b *keyBackfiller) commitEmpty(ctx context.Context, kind, runFP string, hi int64) (int64, int, error) {
	if _, err := b.commit(ctx, kind, runFP, hi, nil); err != nil {
		return 0, 0, err
	}
	return hi, 0, nil
}

// decide is the ingest-time decision, re-run with the keys in force now.
func (b *keyBackfiller) decide(kind string, p keyBackfillPlan, id int64, raw, oldChannel string) (keyRewrite, bool) {
	if raw == "" {
		return keyRewrite{}, false
	}
	switch kind {
	case keyBackfillKindChannel:
		decoded, err := DecodePacket(raw, b.channelKeys, false)
		if err != nil || decoded.Payload.Type != "CHAN" || decoded.Payload.Channel == "" {
			return keyRewrite{}, false
		}
		return keyRewrite{
			id:          id,
			channelHash: decoded.Payload.Channel,
			decodedJSON: PayloadJSON(&decoded.Payload),
			oldChannel:  oldChannel,
		}, true
	case keyBackfillKindRegion:
		decoded, err := DecodePacket(raw, nil, false)
		if err != nil || decoded.TransportCodes == nil || decoded.TransportCodes.Code1 == "0000" {
			return keyRewrite{}, false
		}
		m := p.snapshot.match(byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
		if m.Name == "" { // none, or an ambiguous collision: stays unmatched
			return keyRewrite{}, false
		}
		r := keyRewrite{id: id, scopeName: m.Name}
		if decoded.Header.PayloadType == PayloadADVERT && decoded.Payload.PubKey != "" {
			r.advertFrom = strings.ToLower(decoded.Payload.PubKey)
		}
		return r, true
	}
	return keyRewrite{}, false
}

func (b *keyBackfiller) commit(ctx context.Context, kind, runFP string, next int64, rewrites []keyRewrite) (int, error) {
	writerMu.Lock()
	defer writerMu.Unlock()
	tx, err := b.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	n := 0
	for _, r := range rewrites {
		var res sql.Result
		switch kind {
		case keyBackfillKindChannel:
			res, err = tx.ExecContext(ctx, `UPDATE transmissions SET channel_hash=?, decoded_json=? WHERE id=? AND channel_hash=?`,
				r.channelHash, r.decodedJSON, r.id, r.oldChannel)
		case keyBackfillKindRegion:
			res, err = tx.ExecContext(ctx, `UPDATE transmissions SET scope_name=? WHERE id=? AND scope_name=''`, r.scopeName, r.id)
		}
		if err != nil {
			return 0, fmt.Errorf("rewrite tx %d: %w", r.id, err)
		}
		if changed, _ := res.RowsAffected(); changed == 0 {
			continue // resolved meanwhile by someone else; theirs stands
		}
		n++
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_rewrite_feed(tx_id) VALUES(?)`, r.id); err != nil {
			return 0, fmt.Errorf("feed tx %d: %w", r.id, err)
		}
		// A node whose default_scope is still unknown gets the region its own
		// advert now names. A known default_scope is newer evidence than a
		// backfilled advert can be, so it is never overwritten.
		if r.advertFrom != "" {
			for _, table := range []string{"nodes", "inactive_nodes"} {
				if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET default_scope=?
					WHERE public_key=? AND (default_scope IS NULL OR default_scope='')`, r.scopeName, r.advertFrom); err != nil {
					return 0, fmt.Errorf("default_scope for %s: %w", r.advertFrom, err)
				}
			}
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE key_backfill_state SET cursor=?, rewritten=rewritten+? WHERE kind=? AND run_fp=?`,
		next, n, kind, runFP)
	if err != nil {
		return 0, fmt.Errorf("advance cursor: %w", err)
	}
	if moved, _ := res.RowsAffected(); moved != 1 {
		// The state row was replaced by a newer run; this batch's rewrites
		// are still correct, but its cursor belongs to nobody.
		return 0, fmt.Errorf("backfill state for %s run %s vanished", kind, runFP)
	}
	return n, tx.Commit()
}
