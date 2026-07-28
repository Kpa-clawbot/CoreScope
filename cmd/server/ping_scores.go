// Package main: ping-score highscore/leaderboard feature.
//
// The ingestor writes one lightweight row per "ping"-triggering CHAN
// message to ping_triggers (tx_id, hash, channel_hash, sender, first_seen
// -- see cmd/ingestor/ping_triggers.go and internal/dbschema's
// ensurePingTriggersTable). This file periodically joins that detection
// index with the SAME GetPacketPath + airtime-annotation logic View Path
// already uses, deriving records (farthest, most hops, widest spread,
// fastest full spread, most airtime-efficient) and leaderboards (which
// relay appears most often, which observer hears pings first most often),
// caching the snapshot in memory -- matching the steady-state recomputer
// pattern used throughout cmd/server (analyticsRecomputer et al).
//
// Deliberately global, not scoped by region/area (per dborup, 2026-07-26).
package main

import (
	"database/sql"
	"log"
	"sort"
	"sync/atomic"
	"time"
)

// pingScoresRecomputeInterval: pings are rare relative to general channel
// traffic, so this doesn't need the 60s cadence of the hotter recomputers
// (neighbor graph, analytics) -- a couple of minutes keeps the highscore
// board fresh without adding needless periodic GetPacketPath-per-ping load.
const pingScoresRecomputeInterval = 2 * time.Minute

// PingScore is one ping's computed highscore-relevant stats.
type PingScore struct {
	Hash        string `json:"hash"`
	Sender      string `json:"sender,omitempty"`
	ChannelHash string `json:"channelHash,omitempty"`
	Timestamp   string `json:"timestamp"`

	StationCount int `json:"stationCount"`

	DeepestHops   int    `json:"deepestHops"`
	DeepestPubkey string `json:"deepestNodePubkey,omitempty"`
	DeepestName   string `json:"deepestNodeName,omitempty"`

	FarthestKm     *float64 `json:"farthestKm,omitempty"`
	FarthestPubkey string   `json:"farthestNodePubkey,omitempty"`
	FarthestName   string   `json:"farthestNodeName,omitempty"`

	// SpreadSeconds is the largest secondsAfterFirst across every branch
	// (not just deepest/farthest) -- how long the whole flood took to
	// finish reaching everyone it ever reached. nil when StationCount<2
	// (nothing to spread to) or no branch has timing data.
	SpreadSeconds *float64 `json:"spreadSeconds,omitempty"`

	AirtimeMs  *float64 `json:"airtimeMs,omitempty"`
	RelayCount int      `json:"relayCount,omitempty"`

	// KmPerSecondAirtime is FarthestKm / (AirtimeMs/1000) -- how much
	// geographic distance this ping covered per second of estimated RF
	// airtime spent relaying it. Only set when both FarthestKm and
	// AirtimeMs (with RelayCount>0) are available.
	KmPerSecondAirtime *float64 `json:"kmPerSecondAirtime,omitempty"`

	// relayPubkeys/firstPubkey/firstName/touchedAreaLabels feed the
	// leaderboards during computeAllPingScores -- never serialized on an
	// individual record.
	relayPubkeys      []string
	firstPubkey       string
	firstName         string
	touchedAreaLabels []string
}

// PingLeaderboardEntry is one row of a leaderboard ranking.
type PingLeaderboardEntry struct {
	Pubkey string `json:"pubkey,omitempty"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
}

// PingScoresSnapshot is the full cached ping-score board: current records
// plus leaderboards, global (not scoped by region/area).
type PingScoresSnapshot struct {
	GeneratedAt string `json:"generatedAt"`
	TotalPings  int    `json:"totalPings"`

	FarthestPing      *PingScore `json:"farthestPing,omitempty"`
	MostHopsPing      *PingScore `json:"mostHopsPing,omitempty"`
	WidestSpreadPing  *PingScore `json:"widestSpreadPing,omitempty"`
	FastestSpreadPing *PingScore `json:"fastestSpreadPing,omitempty"`
	MostEfficientPing *PingScore `json:"mostEfficientPing,omitempty"`

	// ThisWeek mirrors the 5 all-time records above but scoped to the
	// trailing 7 days -- an all-time record set once (e.g. a 364km
	// farthest ping) locks that slot forever, so this gives people an
	// achievable target that resets on its own (dborup, 2026-07-28).
	// nil when no ping in the last 7 days resolved to a usable score.
	ThisWeek *WeeklyPingRecords `json:"thisWeek,omitempty"`

	// RelayLeaderboard ranks nodes by how many DISTINCT pings they
	// appeared as a relay hop in (deduped per ping first, so one busy
	// ping's many branches can't over-credit a relay that appears in
	// several of them).
	RelayLeaderboard []PingLeaderboardEntry `json:"relayLeaderboard,omitempty"`

	// ObserverLeaderboard ranks observers by how many pings they were
	// the FIRST station to hear.
	ObserverLeaderboard []PingLeaderboardEntry `json:"observerLeaderboard,omitempty"`

	// SenderLeaderboard ranks senders by how many pings they've sent.
	// Keyed by the sender display NAME from the channel message itself
	// (ping_triggers.sender / pingTriggerSenderAndText in the ingestor) --
	// unlike RelayLeaderboard/ObserverLeaderboard there's no resolved
	// pubkey to link back to a node, so entries never carry one (matches
	// PingLeaderboardEntry.Pubkey's existing omitempty).
	SenderLeaderboard []PingLeaderboardEntry `json:"senderLeaderboard,omitempty"`

	// AreaLeaderboard ranks configured areas (cmd/server/config.go's
	// AreaEntry, same set View Path's touchedAreas draws from) by how
	// many DISTINCT pings had at least one relay hop or hearing station
	// inside them -- "which area is most active." Keyed by area Label
	// (matching SenderLeaderboard's no-pubkey precedent -- an area isn't a
	// resolvable node either). Omitted entirely when no areas are
	// configured (per-deployment optional feature, like the rest of the
	// Areas tooling).
	AreaLeaderboard []PingLeaderboardEntry `json:"areaLeaderboard,omitempty"`
}

// WeeklyPingRecords mirrors PingScoresSnapshot's 5 all-time record slots,
// scoped to the trailing 7 days.
type WeeklyPingRecords struct {
	FarthestPing      *PingScore `json:"farthestPing,omitempty"`
	MostHopsPing      *PingScore `json:"mostHopsPing,omitempty"`
	WidestSpreadPing  *PingScore `json:"widestSpreadPing,omitempty"`
	FastestSpreadPing *PingScore `json:"fastestSpreadPing,omitempty"`
	MostEfficientPing *PingScore `json:"mostEfficientPing,omitempty"`
}

type pingTriggerRow struct {
	txID        int64
	hash        string
	channelHash string
	sender      string
	firstSeen   string
}

// fetchPingTriggers reads every row from ping_triggers. Table absence
// (e.g. a fresh DB the ingestor hasn't migrated yet) is reported via err
// so the caller can skip this cycle rather than crash -- AssertReady
// guarantees the table exists once the server has started at all, but a
// recomputer's first tick can in principle race a test fixture that
// doesn't go through the normal startup path.
func (db *DB) fetchPingTriggers() ([]pingTriggerRow, error) {
	rows, err := db.conn.Query(`SELECT tx_id, hash, channel_hash, sender, first_seen FROM ping_triggers ORDER BY tx_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pingTriggerRow
	for rows.Next() {
		var r pingTriggerRow
		var channelHash, sender sql.NullString
		if err := rows.Scan(&r.txID, &r.hash, &channelHash, &sender, &r.firstSeen); err != nil {
			continue
		}
		r.channelHash = channelHash.String
		r.sender = sender.String
		out = append(out, r)
	}
	return out, nil
}

// computePingScore builds one ping's full stats via the same GetPacketPath
// + airtime/touched-areas annotation path View Path uses, so the numbers
// on the highscore board always match what "View path" shows for that
// packet.
func (s *Server) computePingScore(trigger pingTriggerRow) *PingScore {
	resp, err := s.db.GetPacketPath(trigger.hash)
	if err != nil || resp == nil || len(resp.Branches) == 0 {
		return nil
	}
	s.annotatePacketPathAirtime(resp)
	s.annotatePacketPathTouchedAreas(resp)

	score := &PingScore{
		Hash:         trigger.hash,
		Sender:       trigger.sender,
		ChannelHash:  trigger.channelHash,
		Timestamp:    trigger.firstSeen,
		StationCount: len(resp.Branches),
	}

	// Branches are sorted deepest-first by GetPacketPath.
	deepest := resp.Branches[0]
	score.DeepestHops = deepest.Hops
	if deepest.Observer != nil {
		score.DeepestPubkey = deepest.Observer.PublicKey
		score.DeepestName = deepest.Observer.Name
	}

	var maxSpread *float64
	relaySet := map[string]bool{}
	var farthestBranchIdx = -1
	for i := range resp.Branches {
		b := &resp.Branches[i]
		if b.DistanceFromFirstKm != nil && (farthestBranchIdx == -1 || *b.DistanceFromFirstKm > *resp.Branches[farthestBranchIdx].DistanceFromFirstKm) {
			farthestBranchIdx = i
		}
		if b.SecondsAfterFirst != nil && (maxSpread == nil || *b.SecondsAfterFirst > *maxSpread) {
			maxSpread = b.SecondsAfterFirst
		}
		for _, p := range b.Points {
			if p.PublicKey != "" {
				relaySet[p.PublicKey] = true
			}
		}
	}
	if farthestBranchIdx != -1 {
		fb := resp.Branches[farthestBranchIdx]
		score.FarthestKm = fb.DistanceFromFirstKm
		if fb.Observer != nil {
			score.FarthestPubkey = fb.Observer.PublicKey
			score.FarthestName = fb.Observer.Name
		}
	}
	if score.StationCount >= 2 {
		score.SpreadSeconds = maxSpread
	}
	if resp.EstimatedAirtimeMs != nil {
		score.AirtimeMs = resp.EstimatedAirtimeMs
		score.RelayCount = resp.AirtimeRelayCount
	}
	if score.FarthestKm != nil && score.AirtimeMs != nil && *score.AirtimeMs > 0 {
		kmPerSec := *score.FarthestKm / (*score.AirtimeMs / 1000.0)
		score.KmPerSecondAirtime = &kmPerSec
	}
	for pk := range relaySet {
		score.relayPubkeys = append(score.relayPubkeys, pk)
	}
	if resp.First != nil && resp.First.Observer != nil {
		score.firstPubkey = resp.First.Observer.PublicKey
		score.firstName = resp.First.Observer.Name
	}
	for _, area := range resp.TouchedAreas {
		score.touchedAreaLabels = append(score.touchedAreaLabels, area.Label)
	}
	return score
}

// pingRecordSet accumulates the same 5 "best of" record slots used by
// both the all-time and this-week views, so the selection rules only
// need to be written once.
type pingRecordSet struct {
	Farthest      *PingScore
	MostHops      *PingScore
	WidestSpread  *PingScore
	FastestSpread *PingScore
	MostEfficient *PingScore
}

func (rs *pingRecordSet) consider(score *PingScore) {
	if score.FarthestKm != nil && (rs.Farthest == nil || rs.Farthest.FarthestKm == nil || *score.FarthestKm > *rs.Farthest.FarthestKm) {
		rs.Farthest = score
	}
	if rs.MostHops == nil || score.DeepestHops > rs.MostHops.DeepestHops {
		rs.MostHops = score
	}
	if rs.WidestSpread == nil || score.StationCount > rs.WidestSpread.StationCount {
		rs.WidestSpread = score
	}
	// Fastest full spread only makes sense with a real multi-station
	// spread to measure -- a lone station is trivially "instant" and
	// would otherwise always win this record for nothing.
	if score.SpreadSeconds != nil && score.StationCount >= 2 &&
		(rs.FastestSpread == nil || rs.FastestSpread.SpreadSeconds == nil || *score.SpreadSeconds < *rs.FastestSpread.SpreadSeconds) {
		rs.FastestSpread = score
	}
	if score.KmPerSecondAirtime != nil &&
		(rs.MostEfficient == nil || rs.MostEfficient.KmPerSecondAirtime == nil || *score.KmPerSecondAirtime > *rs.MostEfficient.KmPerSecondAirtime) {
		rs.MostEfficient = score
	}
}

// computeAllPingScores computes the full snapshot: records + leaderboards.
func (s *Server) computeAllPingScores() *PingScoresSnapshot {
	triggers, err := s.db.fetchPingTriggers()
	if err != nil {
		log.Printf("[ping-scores] fetch ping_triggers (skipping this cycle): %v", err)
		return nil
	}

	snap := &PingScoresSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		TotalPings:  len(triggers),
	}
	relayCounts := map[string]*PingLeaderboardEntry{}
	observerCounts := map[string]*PingLeaderboardEntry{}
	// SenderLeaderboard is deliberately a rolling 30-day window, NOT
	// all-time like the records and the other two leaderboards (dborup,
	// after seeing it first ship all-time) -- "who pings the most" is
	// more interesting as an ongoing/current thing than a fixed
	// leaderboard someone can win once and hold forever. An unparseable
	// timestamp is treated as too old (excluded), same fail-toward-stale
	// convention as the active/degraded/silent health classification
	// elsewhere in this file's package.
	senderCutoff := time.Now().AddDate(0, 0, -30)
	senderCounts := map[string]*PingLeaderboardEntry{}
	areaCounts := map[string]*PingLeaderboardEntry{}

	// weekCutoff drives ThisWeek -- same fail-toward-stale rule as
	// senderCutoff above (an unparseable timestamp is excluded, not
	// defaulted to included).
	weekCutoff := time.Now().AddDate(0, 0, -7)
	allTime := &pingRecordSet{}
	week := &pingRecordSet{}

	for _, trigger := range triggers {
		score := s.computePingScore(trigger)
		if score == nil {
			continue
		}

		allTime.consider(score)
		ts, tsErr := time.Parse(time.RFC3339, score.Timestamp)
		if tsErr == nil && ts.After(weekCutoff) {
			week.consider(score)
		}

		for _, pk := range score.relayPubkeys {
			e := relayCounts[pk]
			if e == nil {
				e = &PingLeaderboardEntry{Pubkey: pk}
				relayCounts[pk] = e
			}
			e.Count++
		}
		if score.firstPubkey != "" {
			e := observerCounts[score.firstPubkey]
			if e == nil {
				e = &PingLeaderboardEntry{Pubkey: score.firstPubkey, Name: score.firstName}
				observerCounts[score.firstPubkey] = e
			}
			e.Count++
		}
		if score.Sender != "" && tsErr == nil && ts.After(senderCutoff) {
			e := senderCounts[score.Sender]
			if e == nil {
				e = &PingLeaderboardEntry{Name: score.Sender}
				senderCounts[score.Sender] = e
			}
			e.Count++
		}
		for _, label := range score.touchedAreaLabels {
			e := areaCounts[label]
			if e == nil {
				e = &PingLeaderboardEntry{Name: label}
				areaCounts[label] = e
			}
			e.Count++
		}
	}

	snap.FarthestPing = allTime.Farthest
	snap.MostHopsPing = allTime.MostHops
	snap.WidestSpreadPing = allTime.WidestSpread
	snap.FastestSpreadPing = allTime.FastestSpread
	snap.MostEfficientPing = allTime.MostEfficient
	if week.Farthest != nil || week.MostHops != nil || week.WidestSpread != nil || week.FastestSpread != nil || week.MostEfficient != nil {
		snap.ThisWeek = &WeeklyPingRecords{
			FarthestPing:      week.Farthest,
			MostHopsPing:      week.MostHops,
			WidestSpreadPing:  week.WidestSpread,
			FastestSpreadPing: week.FastestSpread,
			MostEfficientPing: week.MostEfficient,
		}
	}

	// Resolve relay names in one bulk query rather than N individual ones.
	if len(relayCounts) > 0 {
		pubkeys := make([]string, 0, len(relayCounts))
		for pk := range relayCounts {
			pubkeys = append(pubkeys, pk)
		}
		names, _ := s.db.namesAndRolesForPubkeys(pubkeys)
		for pk, e := range relayCounts {
			if name := names[pk]; name != "" {
				e.Name = name
			} else {
				e.Name = pk
			}
		}
	}

	snap.RelayLeaderboard = topPingLeaderboard(relayCounts, 10)
	snap.ObserverLeaderboard = topPingLeaderboard(observerCounts, 10)
	snap.SenderLeaderboard = topPingLeaderboard(senderCounts, 10)
	snap.AreaLeaderboard = topPingLeaderboard(areaCounts, 10)
	return snap
}

// topPingLeaderboard sorts entries by Count descending (ties broken by
// name for a stable, deterministic order) and returns the top N.
func topPingLeaderboard(counts map[string]*PingLeaderboardEntry, limit int) []PingLeaderboardEntry {
	if len(counts) == 0 {
		return nil
	}
	entries := make([]PingLeaderboardEntry, 0, len(counts))
	for _, e := range counts {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Name < entries[j].Name
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// pingScoresCache holds the latest snapshot, refreshed by
// StartPingScoresRecomputer. A nil-safe atomic.Value load: Load() before
// the first successful compute returns nil, meaning "not ready yet"
// (matches the fresh-DB / first-few-seconds-after-startup case).
type pingScoresCache struct {
	v atomic.Value // holds *PingScoresSnapshot
}

func (c *pingScoresCache) Load() *PingScoresSnapshot {
	v, _ := c.v.Load().(*PingScoresSnapshot)
	return v
}

func (c *pingScoresCache) Store(snap *PingScoresSnapshot) {
	if snap != nil {
		c.v.Store(snap)
	}
}

// StartPingScoresRecomputer runs an initial compute synchronously (so the
// first read after startup isn't empty once the ingestor has any ping
// history), then refreshes on a fixed ticker. Returns a stop func.
func (s *Server) StartPingScoresRecomputer(interval time.Duration) (stop func()) {
	s.pingScores.Store(s.computeAllPingScores())

	stopCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.pingScores.Store(s.computeAllPingScores())
			case <-stopCh:
				return
			}
		}
	}()
	return func() { close(stopCh) }
}
