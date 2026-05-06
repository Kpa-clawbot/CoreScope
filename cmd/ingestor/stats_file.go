package main

import (
	"encoding/json"
	"log"
	"os"
	"time"
)

// IngestorStatsSnapshot mirrors the JSON shape consumed by the server's
// /api/perf/write-sources endpoint (see cmd/server/perf_io.go IngestorStats).
type IngestorStatsSnapshot struct {
	SampledAt          string           `json:"sampledAt"`
	TxInserted         int64            `json:"tx_inserted"`
	ObsInserted        int64            `json:"obs_inserted"`
	DuplicateTx        int64            `json:"tx_dupes"`
	NodeUpserts        int64            `json:"node_upserts"`
	ObserverUpserts    int64            `json:"observer_upserts"`
	WriteErrors        int64            `json:"write_errors"`
	SignatureDrops     int64            `json:"sig_drops"`
	WALCommits         int64            `json:"walCommits"`
	GroupCommitFlushes int64            `json:"groupCommitFlushes"`
	BackfillUpdates    map[string]int64 `json:"backfillUpdates"`
}

// statsFilePath returns the writable path the ingestor will publish stats to.
// Override via env CORESCOPE_INGESTOR_STATS for tests / non-default deploys.
func statsFilePath() string {
	if p := os.Getenv("CORESCOPE_INGESTOR_STATS"); p != "" {
		return p
	}
	return "/tmp/corescope-ingestor-stats.json"
}

// StartStatsFileWriter writes the current stats snapshot to disk every
// `interval` so the server can serve them at /api/perf/write-sources.
// Failures are logged once-per-interval and never fatal.
func StartStatsFileWriter(s *Store, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		path := statsFilePath()
		tmp := path + ".tmp"
		for range t.C {
			snap := IngestorStatsSnapshot{
				SampledAt:          time.Now().UTC().Format(time.RFC3339),
				TxInserted:         s.Stats.TransmissionsInserted.Load(),
				ObsInserted:        s.Stats.ObservationsInserted.Load(),
				DuplicateTx:       s.Stats.DuplicateTransmissions.Load(),
				NodeUpserts:        s.Stats.NodeUpserts.Load(),
				ObserverUpserts:    s.Stats.ObserverUpserts.Load(),
				WriteErrors:        s.Stats.WriteErrors.Load(),
				SignatureDrops:     s.Stats.SignatureDrops.Load(),
				WALCommits:         s.Stats.WALCommits.Load(),
				GroupCommitFlushes: s.Stats.GroupCommitFlushes.Load(),
				BackfillUpdates:    s.Stats.SnapshotBackfills(),
			}
			b, err := json.Marshal(snap)
			if err != nil {
				log.Printf("[stats-file] marshal: %v", err)
				continue
			}
			// Atomic-ish replace via tmp + rename.
			if err := os.WriteFile(tmp, b, 0o644); err != nil {
				log.Printf("[stats-file] write %s: %v", tmp, err)
				continue
			}
			if err := os.Rename(tmp, path); err != nil {
				log.Printf("[stats-file] rename %s -> %s: %v", tmp, path, err)
			}
		}
	}()
}
