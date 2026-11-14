package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/channel"
)

// Catalogue key tier (#2107; the retroactive backfill landed in #2133).
//
// Channel and region names from a public catalogue are NOT imported. A name
// becomes a key only when traffic this instance already stored confirms it,
// and channels and regions are confirmed by different gates because their
// failure modes differ:
//
//   - Channel: at least channelMinHits stored GRP_TXT transmissions that are
//     still "enc_XX" verify under the candidate key (the 2-byte MAC), decrypt
//     to printable UTF-8 with a non-empty text, AND carry a sender_timestamp
//     within a year before and a day after the row's first_seen. A wrong key
//     passes the MAC once in 65536 tries; it then has to produce readable text
//     and a plausible 4-byte timestamp as well. Confirmed channels are
//     promoted directly.
//
//   - Region: code1 is HMAC(key, payload_type||payload) truncated to 16 bits,
//     so a wrong name matches any one transmission independently with
//     p ~ 1/65536. Evidence counts DISTINCT transmissions (transmissions is
//     already deduplicated by content hash, so re-observations of the same
//     packet count once), only those whose scope is still unknown, only those
//     that no key in force matches, and only UNIQUE matches: a transmission
//     that matches two catalogue names is evidence for neither and stays
//     unnamed (#1609). The threshold is not a constant: it is the smallest k
//     >= regionMinHits for which the expected number of names reaching k by
//     chance, M * P(Poisson(N/65536) >= k), is at most regionAlpha, with N the
//     transmissions counted and M the candidate names. Confirmed regions are
//     recorded as "proposed" and only put in force with autoPromoteRegions;
//     without it an operator confirms one by adding it to hashRegions.
//
// Promoted names are the lowest tier of each key set. A catalogue region never
// wins a tie-break against an explicit or a derived one (regionKeySnapshot.
// match), and a catalogue channel whose name or secret is already in force is
// skipped. Promotion is recorded in catalog_keys and reloaded at startup, so a
// catalogue that is unreachable later does not drop keys already in force.
// When the promoted set grows, the key backfill (#2133) is kicked and
// re-decides history with the same decode path ingest uses.
//
// An operator can set a row to "rejected": it is taken out of force at the
// next refresh or restart and never proposed or promoted again. How to undo a
// promotion, history included, is in docs/catalogue-keys.md.

const (
	catalogStatusProposed = "proposed"
	catalogStatusPromoted = "promoted"
	// catalogStatusRejected is only ever set by an operator (see
	// docs/catalogue-keys.md); the worker keeps such a name out for good.
	catalogStatusRejected = "rejected"

	// catalogMaxBytes caps the catalogue we parse (today's are < 1 MB).
	catalogMaxBytes = 8 << 20
	// catalogChannelSample is how many of the newest still-encrypted rows per
	// channel hash byte a candidate is tried against.
	catalogChannelSample = 500
	// catalogTimestampPast/Future bound a plausible sender_timestamp around
	// the row's first_seen.
	catalogTimestampPast   = 365 * 24 * time.Hour
	catalogTimestampFuture = 24 * time.Hour
	// catalogRegionWindowIDs bounds one read of the region scan, like
	// keyBackfillWindow: a short primary-key range per query.
	catalogRegionWindowIDs = 5000
	// catalogRegionScanCap bounds the transmissions one region gate counts.
	catalogRegionScanCap = 250000
	// catalogFirstRunDelay keeps the first fetch out of the startup burst.
	catalogFirstRunDelay = 2 * time.Minute
	// regionCodeSpace is the number of distinct code1 values.
	regionCodeSpace = 65536
)

// catalogChannel is one channel candidate from a catalogue.
type catalogChannel struct {
	Name     string
	KeyHex   string
	Explicit bool // the key is not derived from "#name"
}

// keyCatalog is a parsed catalogue: channel candidates and region names
// (canonical, without "#").
type keyCatalog struct {
	Channels []catalogChannel
	Regions  []string
}

type catalogEntry struct {
	N       string `json:"n"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
	S       string `json:"s"`
	Secret  string `json:"secret"`
	Key     string `json:"key"`
}

type catalogPayload struct {
	Channels  []catalogEntry            `json:"channels"`
	Regions   []json.RawMessage         `json:"regions"`
	Countries map[string][]catalogEntry `json:"countries"`
}

// parseKeyCatalog accepts the knownChannelsUrl shape ({"countries":{cc:[{
// "channel","key"}]}}, channels only) and {"channels":[{"n","s"}],"regions":
// [name | {"n"}]}. Unusable entries are dropped, not fatal.
func parseKeyCatalog(raw []byte) (*keyCatalog, error) {
	var p catalogPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode catalogue: %w", err)
	}
	out := &keyCatalog{}
	seen := map[string]bool{}
	add := func(e catalogEntry) {
		c, ok := catalogChannelFromEntry(e)
		if !ok || seen[c.Name] {
			return
		}
		seen[c.Name] = true
		out.Channels = append(out.Channels, c)
	}
	for _, e := range p.Channels {
		add(e)
	}
	codes := make([]string, 0, len(p.Countries))
	for code := range p.Countries {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		for _, e := range p.Countries[code] {
			add(e)
		}
	}
	seenRegion := map[string]bool{}
	for _, r := range p.Regions {
		var name string
		if err := json.Unmarshal(r, &name); err != nil {
			var e catalogEntry
			if json.Unmarshal(r, &e) != nil {
				continue
			}
			name = firstNonEmpty(e.N, e.Name)
		}
		canonical, ok := regionNameAcceptable(strings.TrimSpace(name))
		if !ok || seenRegion[canonical] {
			continue
		}
		seenRegion[canonical] = true
		out.Regions = append(out.Regions, canonical)
	}
	if len(out.Channels) == 0 && len(out.Regions) == 0 {
		return nil, errors.New("catalogue lists no usable channels or regions")
	}
	return out, nil
}

// catalogChannelFromEntry turns one entry into a candidate. Without a secret
// the name must be a valid hashtag name (the same rule approved channels
// follow) and the key is derived from it. A secret that is just the derived
// key does not make the channel private.
func catalogChannelFromEntry(e catalogEntry) (catalogChannel, bool) {
	name := strings.TrimSpace(firstNonEmpty(e.N, e.Name, e.Channel))
	if name == "" {
		return catalogChannel{}, false
	}
	secret, ok := catalogSecretHex(firstNonEmpty(e.S, e.Secret, e.Key))
	if !ok {
		return catalogChannel{}, false
	}
	tag, tagErr := channel.ValidateHashtagName(name)
	if secret == "" {
		if tagErr != nil {
			return catalogChannel{}, false
		}
		return catalogChannel{Name: tag, KeyHex: deriveHashtagChannelKey(tag)}, true
	}
	if tagErr == nil && deriveHashtagChannelKey(tag) == secret {
		return catalogChannel{Name: tag, KeyHex: secret}, true
	}
	return catalogChannel{Name: name, KeyHex: secret, Explicit: true}, true
}

// catalogSecretHex normalises a channel secret given as hex or base64 to
// lowercase hex. The firmware accepts 16-byte (128-bit) and 32-byte (256-bit)
// secrets; a 32-byte secret whose upper half is zero is the 16-byte key and is
// returned as such. "" is no secret; ok=false is a secret that cannot be read.
func catalogSecretHex(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	usable := func(b []byte) (string, bool) {
		if len(b) != 16 && len(b) != 32 {
			return "", false
		}
		return hex.EncodeToString(trimChannelSecret(b)), true
	}
	if b, err := hex.DecodeString(s); err == nil {
		if out, ok := usable(b); ok {
			return out, true
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if out, ok := usable(b); ok {
				return out, true
			}
		}
	}
	return "", false
}

// regionEvidenceThreshold returns the smallest k >= minHits with
// m * P(Poisson(n/65536) >= k) <= alpha: the number of unique hits a region
// name needs so that, across all m candidates, fewer than alpha names are
// expected to get there by chance. It grows with the traffic counted, which a
// fixed threshold does not.
func regionEvidenceThreshold(n, m int, alpha float64, minHits int) int {
	if minHits < 1 {
		minHits = 1
	}
	lambda := float64(n) / regionCodeSpace
	k := minHits
	for ; k < 1000; k++ {
		if float64(m)*poissonTail(lambda, k) <= alpha {
			break
		}
	}
	return k
}

// poissonTail returns P(X >= k) for X ~ Poisson(lambda), summed upward from
// the k-th term so small tails do not vanish in 1 - CDF.
func poissonTail(lambda float64, k int) float64 {
	if k <= 0 {
		return 1
	}
	if lambda <= 0 {
		return 0
	}
	// term_k = e^-lambda * lambda^k / k!, in logs to avoid overflow.
	lg, _ := math.Lgamma(float64(k + 1))
	term := math.Exp(-lambda + float64(k)*math.Log(lambda) - lg)
	sum := 0.0
	for i := k; i < k+10000 && term > 0; i++ {
		sum += term
		term *= lambda / float64(i+1)
		if term < sum*1e-16 {
			break
		}
	}
	return sum
}

// catalogEvidence is one confirmed name and what confirmed it.
type catalogEvidence struct {
	name, keyHex string
	hits         int // distinct transmissions that confirm it
	threshold    int
	n, m         int // transmissions examined, candidates competing
	promote      bool
}

// catalogKeyWorker fetches the catalogue, runs both gates, records the
// evidence and keeps the promoted tier in force.
type catalogKeyWorker struct {
	store    *Store
	set      catalogKeySettings
	channels *channelKeySet
	regions  *regionKeySet
	kick     func() // asks the key backfill to look again
	client   *http.Client
	now      func() time.Time
	yield    time.Duration
}

func newCatalogKeyWorker(store *Store, set catalogKeySettings, channels *channelKeySet, regions *regionKeySet, kick func()) *catalogKeyWorker {
	return &catalogKeyWorker{
		store:    store,
		set:      set,
		channels: channels,
		regions:  regions,
		kick:     kick,
		client:   &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
		yield:    keyBackfillYield,
	}
}

// Start runs the first refresh after catalogFirstRunDelay and then every
// refresh interval until ctx is cancelled.
func (w *catalogKeyWorker) Start(ctx context.Context) <-chan struct{} {
	if strings.HasPrefix(w.set.url, "http://") {
		log.Printf("[catalog] WARNING: %s is fetched over plain http; anyone on the path can change the candidate list (the evidence gates still apply)", w.set.url)
	}
	if w.set.channels && w.set.includeExplicitSecrets {
		log.Printf("[catalog] includeExplicitSecrets is on: private channels whose secrets a public catalogue lists are decrypted and shown in plaintext once traffic confirms them")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(catalogFirstRunDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			w.runOnce(ctx)
			timer.Reset(w.set.refresh)
		}
	}()
	return done
}

func (w *catalogKeyWorker) runOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[catalog] panic recovered: %v", r)
		}
	}()
	raw, err := w.fetch(ctx)
	if err != nil {
		log.Printf("[catalog] fetch from %s failed, keeping the promoted keys in force: %v", w.set.url, err)
		return
	}
	cat, err := parseKeyCatalog(raw)
	if err != nil {
		log.Printf("[catalog] %s unusable, keeping the promoted keys in force: %v", w.set.url, err)
		return
	}
	if err := w.refresh(ctx, cat); err != nil && ctx.Err() == nil {
		log.Printf("[catalog] refresh failed, will retry next interval: %v", err)
	}
}

func (w *catalogKeyWorker) fetch(ctx context.Context) ([]byte, error) {
	src := w.set.url
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, err
		}
		resp, err := w.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return readCatalog(resp.Body)
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readCatalog(f)
}

func readCatalog(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, catalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > catalogMaxBytes {
		return nil, fmt.Errorf("catalogue larger than %d bytes", catalogMaxBytes)
	}
	return b, nil
}

// refresh runs the enabled gates against cat, records what they confirmed
// and puts the promoted tier in force.
func (w *catalogKeyWorker) refresh(ctx context.Context, cat *keyCatalog) error {
	if w.set.channels {
		ev, err := w.gateChannels(ctx, cat.Channels)
		if err != nil {
			return fmt.Errorf("channel gate: %w", err)
		}
		if err := w.record(ctx, keyBackfillKindChannel, ev); err != nil {
			return err
		}
		log.Printf("[catalog] channels: %d catalogue candidate(s), %d confirmed by stored traffic", len(cat.Channels), len(ev))
	}
	if w.set.regions {
		g, err := w.gateRegions(ctx, cat.Regions)
		if err != nil {
			return fmt.Errorf("region gate: %w", err)
		}
		if err := w.record(ctx, keyBackfillKindRegion, g.evidence); err != nil {
			return err
		}
		verb := "proposed (autoPromoteRegions off; add to hashRegions to confirm)"
		if w.set.autoPromoteRegions {
			verb = "promoted"
		}
		log.Printf("[catalog] regions: %d candidate(s), %d unknown-scope transmission(s) in %v, %d ambiguous, threshold %d unique hit(s): %d %s",
			g.m, g.n, w.set.regionWindow, g.ambiguous, g.k, len(g.evidence), verb)
	}
	return w.apply(ctx)
}

type encryptedRow struct{ raw, firstSeen string }

// gateChannels returns the catalogue channels that stored traffic confirms.
func (w *catalogKeyWorker) gateChannels(ctx context.Context, chans []catalogChannel) ([]catalogEvidence, error) {
	rejected, err := w.rejected(ctx, keyBackfillKindChannel)
	if err != nil {
		return nil, err
	}
	inForce := w.channels.Snapshot()
	secrets := make(map[string]bool, len(inForce))
	for _, k := range inForce {
		secrets[strings.ToLower(k)] = true
	}
	byTag := map[string][]catalogChannel{}
	for _, c := range chans {
		if (c.Explicit && !w.set.includeExplicitSecrets) || rejected[c.Name] {
			continue
		}
		if _, ok := inForce[c.Name]; ok || secrets[c.KeyHex] {
			continue
		}
		hb, ok := channelKeyHashByte(c.KeyHex)
		if !ok {
			continue
		}
		tag := fmt.Sprintf("enc_%02X", hb)
		byTag[tag] = append(byTag[tag], c)
	}
	tags := make([]string, 0, len(byTag))
	for tag := range byTag {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	var out []catalogEvidence
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := w.sampleEncrypted(ctx, tag)
		if err != nil {
			return nil, err
		}
		for _, c := range byTag[tag] {
			hits := 0
			for _, r := range rows {
				if channelEvidence(r.raw, r.firstSeen, c) {
					hits++
				}
			}
			if hits >= w.set.channelMinHits {
				out = append(out, catalogEvidence{
					name: c.Name, keyHex: c.KeyHex, hits: hits, threshold: w.set.channelMinHits,
					n: len(rows), m: len(byTag[tag]), promote: true,
				})
			}
		}
		w.pause(ctx)
	}
	return out, nil
}

// sampleEncrypted returns the newest still-encrypted GRP_TXT rows with tag.
func (w *catalogKeyWorker) sampleEncrypted(ctx context.Context, tag string) ([]encryptedRow, error) {
	rows, err := w.store.db.QueryContext(ctx, `
		SELECT COALESCE(raw_hex,''), first_seen FROM transmissions
		WHERE channel_hash = ? AND payload_type = 5
		ORDER BY first_seen DESC LIMIT ?`, tag, catalogChannelSample)
	if err != nil {
		return nil, fmt.Errorf("sample %s: %w", tag, err)
	}
	defer rows.Close()
	var out []encryptedRow
	for rows.Next() {
		var r encryptedRow
		if err := rows.Scan(&r.raw, &r.firstSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// channelEvidence reports whether one stored transmission confirms c: it
// decodes as a channel message under c's key alone (MAC and printable text,
// decryptChannelMessage), its text is not empty, and its sender_timestamp is
// plausible for when the row was first seen.
func channelEvidence(raw, firstSeen string, c catalogChannel) bool {
	if raw == "" {
		return false
	}
	d, err := DecodePacket(raw, map[string]string{c.Name: c.KeyHex}, false)
	if err != nil || d.Payload.Type != "CHAN" || strings.TrimSpace(d.Payload.Text) == "" {
		return false
	}
	seen, _, err := parseEnvelopeTime(firstSeen)
	if err != nil {
		return false
	}
	sent := time.Unix(int64(d.Payload.SenderTimestamp), 0)
	return !sent.Before(seen.Add(-catalogTimestampPast)) && !sent.After(seen.Add(catalogTimestampFuture))
}

// regionGate is the outcome of one region gate run.
type regionGate struct {
	evidence  []catalogEvidence
	n         int // unknown-scope transmissions no key in force matches
	m         int // catalogue names not already in force
	k         int // unique hits a name needed
	ambiguous int // transmissions that matched two or more candidates
}

// gateRegions counts unique matches per catalogue region name over the
// window and returns the names at or above the population-aware threshold.
func (w *catalogKeyWorker) gateRegions(ctx context.Context, names []string) (regionGate, error) {
	rejected, err := w.rejected(ctx, keyBackfillKindRegion)
	if err != nil {
		return regionGate{}, err
	}
	snap := w.regions.snapshot()
	cand := map[string][]byte{}
	for _, raw := range names {
		canonical, ok := regionNameAcceptable(raw)
		if !ok {
			continue
		}
		name := "#" + canonical
		if _, inForce := snap.all[name]; inForce || rejected[name] {
			continue
		}
		h := sha256.Sum256([]byte(name))
		cand[name] = h[:16]
	}
	g := regionGate{m: len(cand)}
	if g.m == 0 {
		g.k = regionEvidenceThreshold(0, 0, w.set.regionAlpha, w.set.regionMinHits)
		return g, nil
	}
	hits := map[string]int{}
	// One keyed HMAC per name, reset per transmission: the gate is rows x
	// names HMACs, so re-keying and hex-formatting per pair would dominate.
	inForce, candidates := newRegionMatcher(snap.all), newRegionMatcher(cand)
	var got []string
	err = w.scanUnknownScope(ctx, func(payloadType byte, payload []byte, code1 uint16) {
		// Matched by a key in force: either the backfill will name it or it
		// is an ambiguous collision. Neither is evidence for a new name.
		if inForce.any(payloadType, payload, code1) {
			return
		}
		g.n++
		got = candidates.match(payloadType, payload, code1, got)
		switch len(got) {
		case 0:
		case 1:
			hits[got[0]]++
		default:
			g.ambiguous++
		}
	})
	if err != nil {
		return g, err
	}
	g.k = regionEvidenceThreshold(g.n, g.m, w.set.regionAlpha, w.set.regionMinHits)
	for name, h := range hits {
		if h >= g.k {
			g.evidence = append(g.evidence, catalogEvidence{
				name: name, keyHex: hex.EncodeToString(cand[name]), hits: h, threshold: g.k,
				n: g.n, m: g.m, promote: w.set.autoPromoteRegions,
			})
		}
	}
	sortEvidence(g.evidence)
	return g, nil
}

// scanUnknownScope calls fn for every transport-coded transmission in the
// window whose scope is still unknown, newest first, in bounded id windows
// that release the single connection between reads.
func (w *catalogKeyWorker) scanUnknownScope(ctx context.Context, fn func(payloadType byte, payload []byte, code1 uint16)) error {
	db := w.store.db
	cutoff := w.now().Add(-w.set.regionWindow).UTC().Format(time.RFC3339)
	var lo int64
	err := db.QueryRowContext(ctx, `SELECT id FROM transmissions WHERE first_seen >= ? ORDER BY first_seen LIMIT 1`, cutoff).Scan(&lo)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("window start: %w", err)
	}
	var hi int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM transmissions`).Scan(&hi); err != nil {
		return fmt.Errorf("window end: %w", err)
	}
	counted := 0
	for top := hi; top >= lo && counted < catalogRegionScanCap; top -= catalogRegionWindowIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		bottom := top - catalogRegionWindowIDs
		if bottom < lo-1 {
			bottom = lo - 1
		}
		raws, err := w.unknownScopeRaws(ctx, bottom, top)
		if err != nil {
			return err
		}
		for _, raw := range raws {
			d, err := DecodePacket(raw, nil, false)
			if err != nil || d.TransportCodes == nil || d.TransportCodes.Code1 == "0000" || len(d.payloadRaw) == 0 {
				continue
			}
			code1, ok := parseCode1(d.TransportCodes.Code1)
			if !ok {
				continue
			}
			fn(byte(d.Header.PayloadType), d.payloadRaw, code1)
			counted++
		}
		w.pause(ctx)
	}
	return nil
}

// unknownScopeRaws reads the same rows the region backfill reads
// (unknownScopeWindowSQL).
func (w *catalogKeyWorker) unknownScopeRaws(ctx context.Context, bottom, top int64) ([]string, error) {
	rows, err := w.store.db.QueryContext(ctx, unknownScopeWindowSQL, bottom, top)
	if err != nil {
		return nil, fmt.Errorf("scan unknown scope: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var (
			id       int64
			raw, old string
		)
		if err := rows.Scan(&id, &raw, &old); err != nil {
			return nil, err
		}
		if raw != "" {
			out = append(out, raw)
		}
	}
	return out, rows.Err()
}

func (w *catalogKeyWorker) pause(ctx context.Context) {
	if w.yield <= 0 {
		return
	}
	t := time.NewTimer(w.yield)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func sortEvidence(ev []catalogEvidence) {
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].hits != ev[j].hits {
			return ev[i].hits > ev[j].hits
		}
		return ev[i].name < ev[j].name
	})
}

// record stores what one gate confirmed. Promotion is sticky: a promoted row
// stays promoted when later evidence is weaker (it is in force and history
// was rewritten with it), also when autoPromoteRegions is switched off later.
// A rejected row stays rejected; the gates skip rejected names, so this only
// guards a row an operator rejects while a run is in flight. A proposed row the gate no longer confirms is
// dropped, so the proposals always reflect the current window.
func (w *catalogKeyWorker) record(ctx context.Context, kind string, ev []catalogEvidence) error {
	sortEvidence(ev)
	if len(ev) > w.set.maxPromoted {
		ev = ev[:w.set.maxPromoted]
	}
	writerMu.Lock()
	defer writerMu.Unlock()
	tx, err := w.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := make(map[string]bool, len(ev))
	for _, e := range ev {
		status := catalogStatusProposed
		if e.promote {
			status = catalogStatusPromoted
		}
		keep[e.name] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO catalog_keys(kind, name, key_hex, status, hits, threshold, window_n, candidates)
			VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(kind, name) DO UPDATE SET
				key_hex = excluded.key_hex,
				hits = excluded.hits,
				threshold = excluded.threshold,
				window_n = excluded.window_n,
				candidates = excluded.candidates,
				updated_at = CAST(strftime('%s','now') AS INTEGER),
				status = CASE WHEN catalog_keys.status IN ('promoted','rejected') THEN catalog_keys.status ELSE excluded.status END`,
			kind, e.name, e.keyHex, status, e.hits, e.threshold, e.n, e.m); err != nil {
			return fmt.Errorf("record %s %s: %w", kind, e.name, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM catalog_keys WHERE kind=? AND status='proposed'`, kind)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !keep[name] {
			stale = append(stale, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_keys WHERE kind=? AND name=? AND status='proposed'`, kind, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// apply loads the promoted rows of the enabled kinds into the key sets and
// kicks the backfill when that put a new key in force. Startup calls it
// before ingest begins, so promoted keys survive an unreachable catalogue.
func (w *catalogKeyWorker) apply(ctx context.Context) error {
	rows, err := w.store.db.QueryContext(ctx, `
		SELECT kind, name, key_hex FROM catalog_keys
		WHERE status = 'promoted' ORDER BY hits DESC, name`)
	if err != nil {
		return fmt.Errorf("load promoted catalogue keys: %w", err)
	}
	chans := map[string]string{}
	regs := map[string][]byte{}
	for rows.Next() {
		var kind, name, keyHex string
		if err := rows.Scan(&kind, &name, &keyHex); err != nil {
			rows.Close()
			return err
		}
		switch {
		case kind == keyBackfillKindChannel && w.set.channels && len(chans) < w.set.maxPromoted:
			if _, err := hex.DecodeString(keyHex); err == nil && (len(keyHex) == 32 || len(keyHex) == 64) {
				chans[name] = strings.ToLower(keyHex)
			}
		case kind == keyBackfillKindRegion && w.set.regions && len(regs) < w.set.maxPromoted:
			if key, err := hex.DecodeString(keyHex); err == nil && len(key) == 16 {
				regs[name] = key
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	addedC := w.channels.setCatalog(chans)
	addedR := w.regions.setCatalog(regs)
	if len(addedC) > 0 {
		log.Printf("[catalog] catalogue channel key(s) now in force: %v", addedC)
	}
	if len(addedR) > 0 {
		log.Printf("[catalog] catalogue region key(s) now in force: %v", addedR)
	}
	if (len(addedC) > 0 || len(addedR) > 0) && w.kick != nil {
		w.kick()
	}
	return nil
}

// rejected returns the names of kind an operator rejected.
func (w *catalogKeyWorker) rejected(ctx context.Context, kind string) (map[string]bool, error) {
	rows, err := w.store.db.QueryContext(ctx, `SELECT name FROM catalog_keys WHERE kind=? AND status=?`, kind, catalogStatusRejected)
	if err != nil {
		return nil, fmt.Errorf("load rejected %s names: %w", kind, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// regionMatcher matches a transmission's code1 against a fixed set of region
// keys. Each key's HMAC is set up once and reset per transmission, and the
// code is compared as a uint16, so a match costs one HMAC-SHA256 over the
// payload and no allocation. Not safe for concurrent use: the region gate
// runs on one goroutine. Ingest keeps matchingRegions, which is.
type regionMatcher struct {
	names []string
	macs  []hash.Hash
	sum   [sha256.Size]byte
	pt    [1]byte
}

func newRegionMatcher(keys map[string][]byte) *regionMatcher {
	m := &regionMatcher{names: make([]string, 0, len(keys))}
	for name := range keys {
		m.names = append(m.names, name)
	}
	sort.Strings(m.names)
	m.macs = make([]hash.Hash, len(m.names))
	for i, name := range m.names {
		m.macs[i] = hmac.New(sha256.New, keys[name])
	}
	return m
}

func (m *regionMatcher) code(i int, payloadType byte, payload []byte) uint16 {
	mac := m.macs[i]
	mac.Reset()
	m.pt[0] = payloadType
	mac.Write(m.pt[:])
	mac.Write(payload)
	return regionCode(mac.Sum(m.sum[:0]))
}

// match appends to out[:0] the names whose key yields code1, in name order.
func (m *regionMatcher) match(payloadType byte, payload []byte, code1 uint16, out []string) []string {
	out = out[:0]
	if len(payload) == 0 {
		return out
	}
	for i := range m.macs {
		if m.code(i, payloadType, payload) == code1 {
			out = append(out, m.names[i])
		}
	}
	return out
}

// any reports whether some key yields code1, stopping at the first.
func (m *regionMatcher) any(payloadType byte, payload []byte, code1 uint16) bool {
	if len(payload) == 0 {
		return false
	}
	for i := range m.macs {
		if m.code(i, payloadType, payload) == code1 {
			return true
		}
	}
	return false
}
