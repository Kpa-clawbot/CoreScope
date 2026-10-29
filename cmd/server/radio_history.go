package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// Which radio setting heard a packet (#1869).
//
// An observer reports its "MHz,kHz,SF,CR" in every status message, but
// observers.radio keeps only the latest one. The ingestor records each
// stretch of time an observer stayed on one setting in observer_radios, and
// the radio id a multi-radio node puts on a packet (`rx_radio_id`) in the
// sparse observation_radios, keyed by observation id (schema and reasoning:
// internal/dbschema/observer_radios.go). The server keeps both in memory and
// resolves the setting of an observation from its observer, its radio id
// and its timestamp.
//
// Freshness: the live poller calls refreshRadioHistory on every tick, so a
// new stretch (a retune) and the radio id of a new observation are known
// before that observation is broadcast. Both checks are primary-key lookups.
// A full reload every radioHistoryFullReload also picks up what the cheap
// checks cannot see: rows deleted by retention and an observation radio id
// rewritten in place.
//
// Cost (rule 0): per observation one map lookup by observer id; observers
// with a single stretch and no radio ids (nearly all) answer from it
// directly, others add a binary search over their few stretches and, on a
// multi-radio node, one map lookup by observation id.
// BenchmarkTxMatchesRadio pins the filter over a 30K-packet store.

const radioHistoryFullReload = 10 * time.Minute

// canonicalRadio is the one normalisation of "MHz,kHz,SF,CR", shared with
// the ingestor.
var canonicalRadio = dbschema.CanonicalRadio

// RadioStint is one stretch of time an observer's radio stayed on one setting.
type RadioStint struct {
	RadioID   string `json:"radio_id"`
	Radio     string `json:"radio"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// observerRadios is one observer's history: the main radio and, on a
// multi-radio node, each radio id. Stints are ordered by FirstSeen.
type observerRadios struct {
	main  []RadioStint
	byID  map[string][]RadioStint
	multi bool // has radio ids: observations may carry rx_radio_id
}

type radioHistorySnapshot struct {
	observers   map[string]*observerRadios
	maxStintID  int64
	stintCount  int64
	maxObsRadio int64
}

var (
	radioHistory atomic.Pointer[radioHistorySnapshot]

	// obsRadioIDs maps observation id -> rx_radio_id. Grows in place on every
	// poll tick (new observations only), rebuilt on the full reload.
	obsRadioMu  sync.RWMutex
	obsRadioIDs = map[int]string{}

	radioRefreshMu sync.Mutex // one refresh at a time (poller, full reload)
)

func tableExists(conn *sql.DB, name string) bool {
	var n int
	return conn.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n) == nil
}

// loadRadioStints reads observer_radios whole. A table the ingestor has not
// created yet (server started first, or an old ingestor) reads as empty and
// is probed again on the next refresh.
func loadRadioStints(conn *sql.DB) (*radioHistorySnapshot, error) {
	snap := &radioHistorySnapshot{observers: map[string]*observerRadios{}}
	if !tableExists(conn, "observer_radios") {
		return snap, nil
	}
	rows, err := conn.Query(`SELECT id, observer_id, radio_id, radio, first_seen, last_seen
		FROM observer_radios ORDER BY observer_id, radio_id, first_seen, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var obs string
		var st RadioStint
		if err := rows.Scan(&id, &obs, &st.RadioID, &st.Radio, &st.FirstSeen, &st.LastSeen); err != nil {
			return nil, err
		}
		// The ingestor stores canonical settings; rows from before it did
		// are normalised here, so lookups compare plain strings.
		st.Radio = canonicalRadio(st.Radio)
		o := snap.observers[obs]
		if o == nil {
			o = &observerRadios{}
			snap.observers[obs] = o
		}
		if st.RadioID == "" {
			o.main = append(o.main, st)
		} else {
			if o.byID == nil {
				o.byID = map[string][]RadioStint{}
			}
			o.byID[st.RadioID] = append(o.byID[st.RadioID], st)
			o.multi = true
		}
		if id > snap.maxStintID {
			snap.maxStintID = id
		}
		snap.stintCount++
	}
	return snap, rows.Err()
}

// loadObservationRadios returns the rx_radio_id rows above sinceID.
func loadObservationRadios(conn *sql.DB, sinceID int64) (map[int]string, int64, error) {
	out := map[int]string{}
	maxID := sinceID
	if !tableExists(conn, "observation_radios") {
		return out, maxID, nil
	}
	rows, err := conn.Query(`SELECT observation_id, radio_id FROM observation_radios
		WHERE observation_id > ? ORDER BY observation_id`, sinceID)
	if err != nil {
		return nil, sinceID, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var radioID string
		if err := rows.Scan(&id, &radioID); err != nil {
			return nil, sinceID, err
		}
		out[int(id)] = radioID
		maxID = id
	}
	return out, maxID, rows.Err()
}

// refreshRadioHistory brings the in-memory history up to date. full=false
// is the per-tick check: reload stretches only when one was added or removed,
// and append observation radio ids above the last one seen.
func refreshRadioHistory(conn *sql.DB, full bool) {
	if conn == nil {
		return
	}
	radioRefreshMu.Lock()
	defer radioRefreshMu.Unlock()

	cur := radioHistory.Load()
	reloadStints := full || cur == nil
	if !reloadStints && tableExists(conn, "observer_radios") {
		var maxID, count int64
		if err := conn.QueryRow(`SELECT COALESCE(MAX(id), 0), COUNT(*) FROM observer_radios`).Scan(&maxID, &count); err != nil {
			log.Printf("[radio-history] check failed: %v", err)
			return
		}
		reloadStints = maxID != cur.maxStintID || count != cur.stintCount
	}
	next := cur
	if reloadStints {
		snap, err := loadRadioStints(conn)
		if err != nil {
			log.Printf("[radio-history] reload failed: %v", err)
			return
		}
		if cur != nil && !full {
			snap.maxObsRadio = cur.maxObsRadio
		}
		next = snap
	} else {
		c := *cur
		next = &c
	}

	since := next.maxObsRadio
	if full {
		since = 0
	}
	ids, maxID, err := loadObservationRadios(conn, since)
	if err != nil {
		log.Printf("[radio-history] observation radios: %v", err)
		return
	}
	next.maxObsRadio = maxID
	obsRadioMu.Lock()
	if full {
		obsRadioIDs = ids
	} else {
		for k, v := range ids {
			obsRadioIDs[k] = v
		}
	}
	obsRadioMu.Unlock()
	radioHistory.Store(next)
}

// runRadioHistory loads the history now and fully again every
// radioHistoryFullReload until ctx ends. The poller does the per-tick part.
func runRadioHistory(ctx context.Context, conn *sql.DB) {
	refreshRadioHistory(conn, true)
	go func() {
		t := time.NewTicker(radioHistoryFullReload)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshRadioHistory(conn, true)
			}
		}
	}()
}

// radioAt returns the setting the observer was on at ts, "" when it never
// reported one. Before its first recorded stretch the first one is assumed:
// the history starts when this feature was deployed, seeded with each
// observer's setting of that moment. obsID is consulted only for multi-radio
// observers.
func (h *radioHistorySnapshot) radioAt(observer string, obsID int, ts string) string {
	if h == nil || observer == "" {
		return ""
	}
	o := h.observers[observer]
	if o == nil {
		return ""
	}
	stints := o.main
	if o.multi {
		obsRadioMu.RLock()
		id := obsRadioIDs[obsID]
		obsRadioMu.RUnlock()
		if s := o.byID[id]; id != "" && len(s) > 0 {
			stints = s
		}
	}
	switch len(stints) {
	case 0:
		return ""
	case 1:
		return stints[0].Radio
	}
	// RFC 3339 strings compare in time order (the observation side carries
	// fractional seconds, which only matters within the same second).
	i := sort.Search(len(stints), func(i int) bool { return stints[i].FirstSeen > ts })
	if i == 0 {
		return stints[0].Radio
	}
	return stints[i-1].Radio
}

// observationRadio is the setting one observation was heard on.
func observationRadio(obsID int, observer, ts string) string {
	return radioHistory.Load().radioAt(observer, obsID, ts)
}

// txRadio is the setting of the observation a transmission is displayed by
// (the header observer and path), with that observation's own timestamp.
func txRadio(tx *StoreTx) string {
	h := radioHistory.Load()
	var fallback *StoreObs
	for _, o := range tx.Observations {
		if o.ObserverID != tx.ObserverID {
			continue
		}
		if o.PathJSON == tx.PathJSON {
			return h.radioAt(o.ObserverID, o.ID, o.Timestamp)
		}
		if fallback == nil {
			fallback = o
		}
	}
	if fallback != nil {
		return h.radioAt(fallback.ObserverID, fallback.ID, fallback.Timestamp)
	}
	return h.radioAt(tx.ObserverID, 0, tx.FirstSeen)
}

// txMatchesRadio reports whether some observation of tx was heard on radio
// (canonical). With observers set, only their observations count: "A heard
// it on R", not "A heard it, and someone heard it on R".
func txMatchesRadio(h *radioHistorySnapshot, tx *StoreTx, radio string, observers map[string]bool) bool {
	for _, obs := range tx.Observations {
		if observers != nil && !observers[obs.ObserverID] {
			continue
		}
		if h.radioAt(obs.ObserverID, obs.ID, obs.Timestamp) == radio {
			return true
		}
	}
	return false
}

// observerRadioStints returns an observer's recorded radio history, oldest
// first.
func observerRadioStints(observer string) []RadioStint {
	h := radioHistory.Load()
	if h == nil || h.observers[observer] == nil {
		return nil
	}
	o := h.observers[observer]
	out := append([]RadioStint(nil), o.main...)
	for _, s := range o.byID {
		out = append(out, s...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FirstSeen != out[j].FirstSeen {
			return out[i].FirstSeen < out[j].FirstSeen
		}
		return out[i].RadioID < out[j].RadioID
	})
	return out
}

// RadioSummary is one setting in GET /api/radios.
type RadioSummary struct {
	Radio     string   `json:"radio"`
	Observers []string `json:"observers"`
	FirstSeen string   `json:"first_seen"`
	LastSeen  string   `json:"last_seen"`
}

// RadiosResponse is the body of GET /api/radios.
type RadiosResponse struct {
	Radios []RadioSummary `json:"radios"`
}

// handleRadios lists every setting some observer has listened on, most
// observers first.
func (s *Server) handleRadios(w http.ResponseWriter, r *http.Request) {
	byRadio := map[string]*RadioSummary{}
	add := func(observer string, st RadioStint) {
		sum := byRadio[st.Radio]
		if sum == nil {
			sum = &RadioSummary{Radio: st.Radio, FirstSeen: st.FirstSeen, LastSeen: st.LastSeen}
			byRadio[st.Radio] = sum
		}
		if st.FirstSeen < sum.FirstSeen {
			sum.FirstSeen = st.FirstSeen
		}
		if st.LastSeen > sum.LastSeen {
			sum.LastSeen = st.LastSeen
		}
		for _, o := range sum.Observers {
			if o == observer {
				return
			}
		}
		sum.Observers = append(sum.Observers, observer)
	}
	if h := radioHistory.Load(); h != nil {
		for id, o := range h.observers {
			for _, st := range o.main {
				add(id, st)
			}
			for _, stints := range o.byID {
				for _, st := range stints {
					add(id, st)
				}
			}
		}
	}
	resp := RadiosResponse{Radios: make([]RadioSummary, 0, len(byRadio))}
	for _, v := range byRadio {
		sort.Strings(v.Observers)
		resp.Radios = append(resp.Radios, *v)
	}
	sort.Slice(resp.Radios, func(i, j int) bool {
		a, b := resp.Radios[i], resp.Radios[j]
		if len(a.Observers) != len(b.Observers) {
			return len(a.Observers) > len(b.Observers)
		}
		return a.Radio < b.Radio
	})
	writeJSON(w, resp)
}
