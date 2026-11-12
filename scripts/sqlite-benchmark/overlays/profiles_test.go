package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

// File capture only. The application's optional HTTP profiler stays disabled.
// This helper is used exclusively by separate diagnostic invocations.
func benchBeginProfiles(directory string) (func() error, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	cpu, err := os.Create(filepath.Join(directory, "cpu.pprof"))
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		cpu.Close()
		return nil, err
	}
	previous := runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(10000)
	return func() error {
		pprof.StopCPUProfile()
		runtime.SetMutexProfileFraction(previous)
		runtime.SetBlockProfileRate(0)
		if err := cpu.Close(); err != nil {
			return err
		}
		runtime.GC()
		for _, kind := range []string{"heap", "mutex", "block"} {
			file, err := os.Create(filepath.Join(directory, kind+".pprof"))
			if err != nil {
				return err
			}
			err = pprof.Lookup(kind).WriteTo(file, 0)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}, nil
}

func benchProfileWindow(c benchConfig) <-chan error {
	done := make(chan error, 1)
	if c.ProfileDir == "" {
		done <- nil
		close(done)
		return done
	}
	go func() {
		start, err := benchStart(c)
		var began, stopRequested int64
		if err == nil {
			benchSleep(start + int64(c.Warmup)*int64(time.Second))
			var finish func() error
			finish, err = benchBeginProfiles(c.ProfileDir)
			if err == nil {
				began = benchMono()
				benchSleep(start + int64(c.Warmup+c.Seconds)*int64(time.Second))
				stopRequested = benchMono()
				err = finish()
			}
		}
		result := struct {
			Complete       bool   `json:"complete"`
			Error          string `json:"error,omitempty"`
			RequestedStart int64  `json:"requested_start_ns"`
			RequestedEnd   int64  `json:"requested_end_ns"`
			CaptureStart   int64  `json:"capture_start_ns"`
			StopRequested  int64  `json:"stop_requested_ns"`
			Finished       int64  `json:"finished_ns"`
		}{Complete: err == nil, RequestedStart: start + int64(c.Warmup)*int64(time.Second), RequestedEnd: start + int64(c.Warmup+c.Seconds)*int64(time.Second), CaptureStart: began, StopRequested: stopRequested, Finished: benchMono()}
		if err != nil {
			result.Error = err.Error()
		}
		data, _ := json.Marshal(result)
		if writeErr := os.WriteFile(filepath.Join(c.ProfileDir, "done.json"), data, 0600); err == nil {
			err = writeErr
		}
		done <- err
		close(done)
	}()
	return done
}

// database/sql exposes cumulative pool counters. Report their window deltas,
// separately from request and writer-lock distributions; never subtract p95s.
func benchPoolWindow(t *testing.T, c benchConfig, db *sql.DB) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		start, err := benchStart(c)
		if err != nil {
			t.Error(err)
			return
		}
		benchSleep(start + int64(c.Warmup)*int64(time.Second))
		before, first := db.Stats(), benchMono()
		benchSleep(start + int64(c.Warmup+c.Seconds)*int64(time.Second))
		after, last := db.Stats(), benchMono()
		result := struct {
			Scope        string `json:"scope"`
			First        int64  `json:"first_ns"`
			Last         int64  `json:"last_ns"`
			WaitCount    int64  `json:"wait_count_delta"`
			WaitDuration int64  `json:"wait_duration_ns_delta"`
			MaxOpen      int    `json:"max_open_connections"`
		}{"ingestor database/sql pool", first, last, after.WaitCount - before.WaitCount, int64(after.WaitDuration - before.WaitDuration), after.MaxOpenConnections}
		benchJSON(t, filepath.Join(c.Output, "db-pool.json"), result)
	}()
	return done
}

func TestCoreScopeBenchmarkControlProfilesWritePrivateFiles(t *testing.T) {
	dir := t.TempDir()
	finish, err := benchBeginProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = fmt.Sprint(time.Now().UnixNano())
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cpu", "heap", "mutex", "block"} {
		info, err := os.Stat(filepath.Join(dir, kind+".pprof"))
		if err != nil || info.Size() == 0 {
			t.Fatalf("missing %s profile: %v", kind, err)
		}
	}
	if err := <-benchProfileWindow(benchConfig{}); err != nil {
		t.Fatal(err)
	}
}
