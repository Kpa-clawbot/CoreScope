package main

import (
	"encoding/json"
	"log"
	"os"
	"syscall"
	"time"
)

// IngestorStatsSnapshot mirrors the JSON shape consumed by the server's
// /api/perf/write-sources endpoint (see cmd/server/perf_io.go IngestorStats).
//
// NOTE: each field below is sampled with an independent atomic.Load(), so the
// snapshot is EVENTUALLY-CONSISTENT — invariants like
// `walCommits >= tx_inserted` may be momentarily violated
// in a single sample. Consumers MUST NOT derive ratios on the assumption these
// counters were captured at the same instant; treat each field as an
// independent monotonically-increasing counter and look at deltas across
// multiple samples instead.
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
	GroupCommitFlushes int64            `json:"groupCommitFlushes"` // always 0 — group commit reverted (refs #1129)
	BackfillUpdates    map[string]int64 `json:"backfillUpdates"`
}

// statsFilePath returns the writable path the ingestor will publish stats to.
// Override via env CORESCOPE_INGESTOR_STATS for tests / non-default deploys.
//
// SECURITY: the default lives in /tmp which is world-writable. The writer uses
// O_NOFOLLOW + 0o600 so a pre-planted symlink cannot be used to clobber an
// arbitrary file via this path. Operators who want stronger guarantees should
// point CORESCOPE_INGESTOR_STATS at a private directory (e.g. /var/lib/corescope/).
func statsFilePath() string {
	if p := os.Getenv("CORESCOPE_INGESTOR_STATS"); p != "" {
		return p
	}
	return "/tmp/corescope-ingestor-stats.json"
}

// writeStatsAtomic writes b to path via a tmp-then-rename, refusing to follow
// symlinks on the tmp file. Returns nil on success, an error otherwise.
func writeStatsAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	// O_NOFOLLOW: if tmp is a pre-existing symlink, openat fails with ELOOP
	// instead of clobbering the symlink target. O_TRUNC zeroes existing
	// regular-file content. 0o600 — no need for world-readable.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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
		for range t.C {
			snap := IngestorStatsSnapshot{
				SampledAt:          time.Now().UTC().Format(time.RFC3339),
				TxInserted:         s.Stats.TransmissionsInserted.Load(),
				ObsInserted:        s.Stats.ObservationsInserted.Load(),
				DuplicateTx:        s.Stats.DuplicateTransmissions.Load(),
				NodeUpserts:        s.Stats.NodeUpserts.Load(),
				ObserverUpserts:    s.Stats.ObserverUpserts.Load(),
				WriteErrors:        s.Stats.WriteErrors.Load(),
				SignatureDrops:     s.Stats.SignatureDrops.Load(),
				WALCommits:         s.Stats.WALCommits.Load(),
				GroupCommitFlushes: 0, // group commit reverted (refs #1129)
				BackfillUpdates:    s.Stats.SnapshotBackfills(),
			}
			b, err := json.Marshal(snap)
			if err != nil {
				log.Printf("[stats-file] marshal: %v", err)
				continue
			}
			if err := writeStatsAtomic(path, b); err != nil {
				log.Printf("[stats-file] write %s: %v", path, err)
			}
		}
	}()
}
