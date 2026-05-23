package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// perfHistoryFilePath derives the sidecar JSON path from the main DB path.
// e.g. "/data/cornmeister.db" → "/data/cornmeister-perf-history.json"
func perfHistoryFilePath(dbPath string) string {
	if dbPath == "" || dbPath == ":memory:" {
		return ""
	}
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + "-perf-history.json"
}

// loadPerfHistoryFromFile reads a previously saved ring buffer from disk.
// Returns nil (not an error) when the file doesn't exist yet.
func loadPerfHistoryFromFile(path string) []PerfSample {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[perf-history] could not read %s: %v", path, err)
		}
		return nil
	}
	var samples []PerfSample
	if err := json.Unmarshal(data, &samples); err != nil {
		log.Printf("[perf-history] could not parse %s: %v (starting fresh)", path, err)
		return nil
	}
	return samples
}

// savePerfHistoryToFile atomically writes samples to path as a JSON array.
// Uses a temp file + rename so a crash mid-write never corrupts the file.
func savePerfHistoryToFile(path string, samples []PerfSample) {
	if path == "" {
		return
	}
	data, err := json.Marshal(samples)
	if err != nil {
		log.Printf("[perf-history] marshal error: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("[perf-history] write error: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[perf-history] rename error: %v", err)
		_ = os.Remove(tmp)
	}
}
