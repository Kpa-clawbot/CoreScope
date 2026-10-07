package main

import (
	"database/sql"
	"strings"
	"time"
)

// notifySource is the analyzer side the notifier reads. serverNotifySource
// is the production one; tests substitute a fake.
type notifySource interface {
	ready() bool // false while the packet store is still loading after startup
	health() HealthThresholds
	lowBatteryMv() int
	nodes(pubkeys []string, withForeign bool) (map[string]notifyNode, error)
	lastHeard(pubkeys []string) map[string]time.Time
	lastRelayed(pubkeys []string) map[string]time.Time
	observers() ([]notifyObserver, error)
}

type serverNotifySource struct{ s *Server }

// ready follows /api/healthz: the store load, the best-observation pick and
// the neighbor graph are done.
func (src serverNotifySource) ready() bool              { return readiness.Load() != 0 }
func (src serverNotifySource) health() HealthThresholds { return src.s.cfg.GetHealthThresholds() }
func (src serverNotifySource) lowBatteryMv() int        { return src.s.cfg.LowBatteryMv() }

func (src serverNotifySource) nodes(pubkeys []string, withForeign bool) (map[string]notifyNode, error) {
	return src.s.db.NotifyNodes(pubkeys, withForeign)
}

func (src serverNotifySource) lastHeard(pubkeys []string) map[string]time.Time {
	if src.s.store == nil {
		return map[string]time.Time{}
	}
	return src.s.store.LastHeardMap(pubkeys)
}

// lastRelayed reads the cached relay metrics (the map /api/nodes uses; the
// background recomputer refreshes it). Pass repeaters and rooms only.
func (src serverNotifySource) lastRelayed(pubkeys []string) map[string]time.Time {
	if src.s.store == nil || len(pubkeys) == 0 {
		return map[string]time.Time{}
	}
	return relayTimes(src.s.store.GetRepeaterRelayInfoMap(src.s.cfg.GetHealthThresholds().RelayActiveHours), pubkeys)
}

func (src serverNotifySource) observers() ([]notifyObserver, error) {
	list, err := src.s.db.GetObservers()
	if err != nil {
		return nil, err
	}
	return notifyObserversFrom(list), nil
}

// relayTimes parses LastRelayed for the given keys; keys without a relay
// time are absent.
func relayTimes(m map[string]RepeaterRelayInfo, pubkeys []string) map[string]time.Time {
	out := make(map[string]time.Time, len(pubkeys))
	for _, pk := range pubkeys {
		info, ok := lookupRelayInfo(m, pk)
		if !ok || info.LastRelayed == "" {
			continue
		}
		if t, ok := parseRelayTS(info.LastRelayed); ok {
			out[pk] = t
		}
	}
	return out
}

func notifyObserversFrom(list []Observer) []notifyObserver {
	out := make([]notifyObserver, 0, len(list))
	for _, o := range list {
		n := notifyObserver{ID: o.ID}
		if o.Name != nil {
			n.Name = *o.Name
		}
		if o.LastSeen != nil {
			n.LastSeen = *o.LastSeen
		}
		out = append(out, n)
	}
	return out
}

// notifyNodeChunk bounds the IN list of one query, far below SQLite's
// variable limit.
const notifyNodeChunk = 500

const notifyNodeCols = `SELECT public_key, COALESCE(name, ''), COALESCE(role, ''), COALESCE(last_seen, ''), battery_mv, COALESCE(foreign_advert, 0) FROM nodes`

// NotifyNodes reads the node rows notifications need: the given pubkeys
// (primary-key lookups, in chunks) and, with withForeign, every node with
// foreign_advert = 1 (partial index idx_nodes_foreign_advert). Keyed by
// lowercase pubkey. Read-only, on the server's mode=ro handle.
func (db *DB) NotifyNodes(pubkeys []string, withForeign bool) (map[string]notifyNode, error) {
	out := make(map[string]notifyNode, len(pubkeys))
	scan := func(rows *sql.Rows) error {
		defer rows.Close()
		for rows.Next() {
			var n notifyNode
			var mv sql.NullInt64
			var foreign int
			if err := rows.Scan(&n.Pubkey, &n.Name, &n.Role, &n.LastSeen, &mv, &foreign); err != nil {
				return err
			}
			n.Pubkey = strings.ToLower(n.Pubkey)
			if mv.Valid {
				v := int(mv.Int64)
				n.BatteryMv = &v
			}
			n.Foreign = foreign != 0
			out[n.Pubkey] = n
		}
		return rows.Err()
	}
	for i := 0; i < len(pubkeys); i += notifyNodeChunk {
		chunk := pubkeys[i:min(i+notifyNodeChunk, len(pubkeys))]
		args := make([]any, len(chunk))
		for j, pk := range chunk {
			args[j] = pk
		}
		rows, err := db.conn.Query(notifyNodeCols+` WHERE public_key IN (`+sqlPlaceholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		if err := scan(rows); err != nil {
			return nil, err
		}
	}
	if withForeign {
		rows, err := db.conn.Query(notifyNodeCols + ` WHERE foreign_advert = 1`)
		if err != nil {
			return nil, err
		}
		if err := scan(rows); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LastHeardMap returns, per pubkey with packets in the store, the newest
// FirstSeen among them: the node page's "Last Heard" (GetNodeHealth) in
// bulk. One read lock, O(packets of the given nodes), string compares
// only; parsing happens after the lock is released.
func (s *PacketStore) LastHeardMap(pubkeys []string) map[string]time.Time {
	latest := make(map[string]string, len(pubkeys))
	s.mu.RLock()
	for _, pk := range pubkeys {
		for _, tx := range s.byNode[pk] {
			if tx != nil && tx.FirstSeen > latest[pk] {
				latest[pk] = tx.FirstSeen
			}
		}
	}
	s.mu.RUnlock()
	out := make(map[string]time.Time, len(latest))
	for pk, ts := range latest {
		if t, ok := parseRelayTS(ts); ok {
			out[pk] = t
		}
	}
	return out
}
