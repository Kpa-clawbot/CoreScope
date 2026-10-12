package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type packetLoadCursor struct {
	id      int
	recency string
}

type loadedPacket struct {
	tx       *StoreTx
	cursor   packetLoadCursor
	bytes    int64
	hops     int
	resolved map[string]bool
	nodes    map[string]bool
}

// loadPacketRange is shared by the hot and background loaders. The SQL cursor
// walks newest-first for a bounded cache, including timestamp ties and ID zero.
// Unlimited loads keep the indexed ID walk; they do not need recency sorting.
// Memory is charged
// per observation, not from an assumed average fanout. An over-budget packet is
// discarded before publishing any of its indexes; SQL still owns its full data.
func (s *PacketStore) loadPacketRange(from, to time.Time, chunkSize int, onChunk func(int, int)) (int, error) {
	_, relayPM := s.getCachedNodesAndPM()
	var cursor packetLoadCursor
	total := 0
	for {
		s.mu.RLock()
		limited := s.startupMemoryLimited
		s.mu.RUnlock()
		if limited {
			return total, nil
		}
		count, next, err := s.loadPacketChunk(from, to, cursor, chunkSize, relayPM)
		if err != nil {
			return total, err
		}
		if count == 0 {
			return total, nil
		}
		total += count
		if onChunk != nil {
			onChunk(count, total)
		}
		if count < chunkSize {
			return total, nil
		}
		cursor = next
	}
}

func (s *PacketStore) packetLoadQuery(from, to time.Time, cursor packetLoadCursor, limit int) (string, []any) {
	axis := "first_seen"
	if s.db.hasLastSeen {
		axis = "last_seen"
	}
	var conditions []string
	var args []any
	parameter := func(value any) string {
		args = append(args, value)
		return s.db.parameter(len(args))
	}
	timestamp := func(t time.Time) any {
		if s.db.hasLastSeen {
			return t.Unix()
		}
		return t.UTC().Format(time.RFC3339)
	}
	if !from.IsZero() {
		conditions = append(conditions, "t2."+axis+" >= "+parameter(timestamp(from)))
	}
	if !to.IsZero() {
		conditions = append(conditions, "t2."+axis+" < "+parameter(timestamp(to)))
	}
	if cursor.recency != "" {
		if s.maxMemoryMB > 0 {
			// Separate bind slots also work with SQLite's positional '?' syntax.
			conditions = append(conditions, "(t2."+axis+" < "+parameter(cursor.recency)+" OR (t2."+axis+" = "+parameter(cursor.recency)+" AND t2.id < "+parameter(cursor.id)+"))")
		} else {
			conditions = append(conditions, "t2.id > "+parameter(cursor.id))
		}
	}
	order := "id ASC"
	if s.maxMemoryMB > 0 {
		order = axis + " DESC, %s.id DESC"
	}
	innerOrder := strings.ReplaceAll("t2."+order, "%s", "t2")
	outerOrder := strings.ReplaceAll("t."+order, "%s", "t")
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	query := `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
		t.payload_type, t.decoded_json, o.id, `
	if s.db.isV3 {
		query += s.db.nativeSQL(`obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
			o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')`,
			`obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
			o.snr, o.rssi, o.score, o.path_json, to_char(to_timestamp(o.timestamp) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`)
	} else {
		query += `o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
			o.snr, o.rssi, o.score, o.path_json, o.timestamp`
	}
	if s.db.hasResolvedPath {
		query += ", o.resolved_path"
	}
	if s.db.hasScopeName {
		query += ", t.scope_name"
	}
	query += ", t." + axis + ` FROM (SELECT * FROM transmissions t2` + where +
		" ORDER BY " + innerOrder + " LIMIT " + strconv.Itoa(limit) + `) AS t
		LEFT JOIN observations o ON o.transmission_id = t.id`
	if s.db.isV3 {
		query += " LEFT JOIN observers obs ON obs.rowid = o.observer_idx"
	} else {
		query += " LEFT JOIN observers obs ON obs.id = o.observer_id"
	}
	query += " ORDER BY " + outerOrder + ", o.timestamp DESC, o.id DESC"

	return query, args
}

func (s *PacketStore) loadPacketChunk(from, to time.Time, cursor packetLoadCursor, limit int, relayPM *prefixMap) (int, packetLoadCursor, error) {
	query, args := s.packetLoadQuery(from, to, cursor, limit)

	// Serialize with live advert ingestion before opening a cursor, but do not
	// hold the packet-store lock while doing SQL or decoding observations.
	s.advertEvidenceMu.Lock()
	defer s.advertEvidenceMu.Unlock()
	rows, err := s.db.conn.Query(query, args...)
	if err != nil {
		return 0, cursor, err
	}
	defer rows.Close()
	s.mu.RLock()
	remaining := int64(s.maxMemoryMB)*1048576 - s.trackedBytes
	s.mu.RUnlock()
	var packets []loadedPacket
	var current *loadedPacket
	var currentCursor packetLoadCursor
	var bytes int64
	limited := false
	seen := 0
	skippedHops := 0
	finish := func() {
		if current == nil {
			return
		}
		pickBestObservation(current.tx)
		current.bytes += rechargeTx(current.tx)
		packets = append(packets, *current)
		bytes += current.bytes
		cursor = currentCursor
		current = nil
	}
	for rows.Next() {
		var id int
		var raw, hash, first, decoded, recency sql.NullString
		var route, payload, obsID sql.NullInt64
		var observer, name, iata, direction, path, stamp, resolved, scope sql.NullString
		var snr, rssi, score sql.NullFloat64
		scan := []any{&id, &raw, &hash, &first, &route, &payload, &decoded,
			&obsID, &observer, &name, &iata, &direction, &snr, &rssi, &score, &path, &stamp}
		if s.db.hasResolvedPath {
			scan = append(scan, &resolved)
		}
		if s.db.hasScopeName {
			scan = append(scan, &scope)
		}
		scan = append(scan, &recency)
		if err := rows.Scan(scan...); err != nil {
			return 0, cursor, err
		}
		if current == nil || current.tx.ID != id {
			finish()
			seen++
			currentCursor = packetLoadCursor{id: id, recency: recency.String}
			current = &loadedPacket{cursor: currentCursor, tx: &StoreTx{
				ID: id, RawHex: raw.String, Hash: hash.String, FirstSeen: first.String, LatestSeen: first.String,
				RouteType: nullIntPtr(route), PayloadType: nullIntPtr(payload), DecodedJSON: decoded.String,
				ScopeName: nullStrPtr(scope), obsKeys: make(map[string]bool), observerSet: make(map[string]bool),
			}, resolved: make(map[string]bool), nodes: make(map[string]bool)}
			current.bytes = rechargeTx(current.tx)
		}
		tx := current.tx
		if obsID.Valid {
			key := observer.String + "|" + path.String
			if tx.obsKeys[key] {
				continue
			}
			obs := &StoreObs{ID: int(obsID.Int64), TransmissionID: id,
				ObserverID: observer.String, ObserverName: name.String, ObserverIATA: iata.String,
				Direction: direction.String, SNR: nullFloatPtr(snr), RSSI: nullFloatPtr(rssi), Score: nullFloatPtr(score),
				PathJSON: path.String, Timestamp: normalizeTimestamp(stamp.String)}
			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[key] = true
			if observer.String != "" && !tx.observerSet[observer.String] {
				tx.observerSet[observer.String] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}
			current.bytes += estimateStoreObsBytes(obs)
			if hops := pathLen(path.String); len(tx.Observations) == 1 || hops > current.hops {
				current.hops = hops
				tx.PathJSON = path.String
				tx.pathParsed = false
				current.bytes += rechargeTx(tx)
			}
			if resolved.String != "" {
				for _, pk := range extractResolvedPubkeys(unmarshalResolvedPath(resolved.String)) {
					current.resolved[pk] = true
				}
			} else if relayPM != nil && path.String != "" && path.String != "[]" {
				for _, pk := range extractResolvedPubkeys(resolvePathForObsColdLoad(path.String, observer.String, tx, relayPM, &skippedHops)) {
					current.nodes[pk] = true
				}
			}
		}
		if s.maxMemoryMB > 0 && bytes+current.bytes > remaining {
			limited = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return 0, cursor, err
	}
	rows.Close()
	if !limited {
		finish()
	}
	ids := make([]int, 0, len(packets))
	for _, packet := range packets {
		if packet.tx.PayloadType != nil && *packet.tx.PayloadType == PayloadADVERT {
			ids = append(ids, packet.tx.ID)
		}
	}
	masks, err := s.db.advertEvidenceForIDs(ids)
	if err != nil {
		return 0, cursor, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	accepted := make([]*StoreTx, 0, len(packets))
	hopsSeen := make(map[string]bool)
	changed := false
	for _, packet := range packets {
		tx := packet.tx
		existing := s.byHash[tx.Hash]
		observations := tx.Observations
		charge := packet.bytes
		if existing != nil {
			observations = make([]*StoreObs, 0, len(tx.Observations))
			charge = 0
			for _, obs := range tx.Observations {
				if !existing.obsKeys[obs.ObserverID+"|"+obs.PathJSON] {
					observations = append(observations, obs)
					charge += estimateStoreObsBytes(obs)
				}
			}
			// Estimate the winning path before mutating the live pointer. Copy
			// only the fields the estimator needs, never the decode sync.Once.
			candidate := StoreTx{RawHex: existing.RawHex, Hash: existing.Hash,
				DecodedJSON: existing.DecodedJSON, PathJSON: existing.PathJSON}
			if (len(existing.Observations) == 0 && len(observations) > 0) || packet.hops > pathLen(existing.PathJSON) {
				candidate.PathJSON = tx.PathJSON
				candidate.pathParsed = false
			}
			charge += estimateStoreTxBytes(&candidate) - existing.accountedBytes
		}
		// Live observations can consume budget while the SQL cursor is open.
		if s.maxMemoryMB > 0 && s.trackedBytes+charge > int64(s.maxMemoryMB)*1048576 {
			limited = true
			currentCursor = packet.cursor
			break
		}
		if existing == nil {
			s.byHash[tx.Hash], s.byTxID[tx.ID] = tx, tx
			accepted = append(accepted, tx)
			if tx.PayloadType != nil {
				s.byPayloadType[*tx.PayloadType] = append(s.byPayloadType[*tx.PayloadType], tx)
			}
			s.trackAdvertPubkey(tx)
		} else {
			tx = existing
			for _, obs := range observations {
				tx.Observations = append(tx.Observations, obs)
				tx.obsKeys[obs.ObserverID+"|"+obs.PathJSON] = true
				if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
					tx.observerSet[obs.ObserverID] = true
					tx.UniqueObserverCount++
				}
				tx.ObservationCount++
				if obs.Timestamp > tx.LatestSeen {
					tx.LatestSeen = obs.Timestamp
				}
			}
			pickBestObservation(tx)
			// The preflight charge above includes this same path delta.
			rechargeTx(tx)
		}
		changed = unionAdvertEvidence(tx, masks[tx.ID]) || changed || len(observations) > 0
		for _, obs := range observations {
			s.byObsID[obs.ID] = obs
			if obs.ObserverID != "" {
				s.byObserver[obs.ObserverID] = append(s.byObserver[obs.ObserverID], obs)
			}
			if s.maxMemoryMB <= 0 && obs.ID > s.maxObsID {
				s.maxObsID = obs.ID
			}
		}
		if s.maxMemoryMB <= 0 && tx.ID > s.maxTxID {
			s.maxTxID = tx.ID
		}
		s.indexByNode(tx)
		pks := make([]string, 0, len(packet.resolved))
		for pk := range packet.resolved {
			pks = append(pks, pk)
		}
		s.indexResolvedPathHops(tx, pks, hopsSeen)
		for pk := range packet.nodes {
			s.addToByNode(tx, pk)
		}
		s.totalObs += len(observations)
		s.trackedBytes += charge
	}
	if len(accepted) > 0 {
		s.packets = mergeChunkIntoPackets(accepted, s.packets)
	}
	if changed {
		s.invalidateAdvertEvidence()
	}
	if limited {
		s.startupMemoryLimited = true
		// A partial timestamp group is not complete coverage. Use the next
		// second as a conservative boundary, including last_seen's second
		// precision when first_seen itself contains fractional seconds.
		var omitted time.Time
		if s.db.hasLastSeen {
			unix, err := strconv.ParseInt(currentCursor.recency, 10, 64)
			if err != nil {
				return 0, cursor, fmt.Errorf("load cursor timestamp: %w", err)
			}
			omitted = time.Unix(unix, 0)
		} else {
			omitted, err = time.Parse(time.RFC3339Nano, currentCursor.recency)
			if err != nil {
				return 0, cursor, fmt.Errorf("load cursor timestamp: %w", err)
			}
		}
		s.oldestLoaded = omitted.Add(time.Second).UTC().Format(time.RFC3339)
	}
	return seen, cursor, nil
}
