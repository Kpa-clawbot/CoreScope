package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type benchShape struct {
	Transmissions int `json:"transmissions"`
	Observations  int `json:"observations"`
	Nodes         int `json:"nodes"`
	Observers     int `json:"observers"`
	Days          int `json:"days"`
}
type benchConfig struct {
	EventsFile string     `json:"events_file"`
	CorpusInfo string     `json:"corpus_info"`
	Mode       string     `json:"mode"`
	Corpus     string     `json:"corpus"`
	Shape      benchShape `json:"shape"`
	Seed       int64      `json:"seed"`
	WireEpoch  int64      `json:"wire_epoch"`
	Epoch      int64      `json:"epoch"`
	SQLite     string     `json:"sqlite"`
	Output     string     `json:"output"`
	StateDir   string     `json:"state_dir"`
	BaseURL    string     `json:"base_url"`
	StartFile  string     `json:"start_file"`
	Warmup     int        `json:"warmup"`
	Seconds    int        `json:"seconds"`
	IngestRate int        `json:"ingest_rate"`
	HTTPRate   int        `json:"http_rate"`
}

func benchRead(t *testing.T) benchConfig {
	t.Helper()
	path := os.Getenv("CORESCOPE_BENCH_CONFIG")
	if path == "" {
		t.Skip("opt-in benchmark overlay")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var c benchConfig
	if e = json.Unmarshal(b, &c); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(filepath.Clean(c.Output), "corescope-bench-") {
		t.Fatal("output is not an allowlisted disposable benchmark directory")
	}
	if c.Output == "" || c.Shape.Transmissions <= 0 || c.Shape.Nodes <= 0 || c.Shape.Observers < 1 {
		t.Fatal("incomplete benchmark configuration")
	}
	return c
}
func benchJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}
func benchOutput(t *testing.T, c benchConfig, name string) *os.File {
	t.Helper()
	f, e := os.OpenFile(filepath.Join(c.Output, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func benchStart(c benchConfig) (int64, error) {
	for i := 0; i < 2400; i++ {
		if b, e := os.ReadFile(c.StartFile); e == nil {
			var start struct {
				Mono int64 `json:"mono_ns"`
			}
			if e = json.Unmarshal(b, &start); e != nil {
				return 0, e
			}
			return start.Mono, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("benchmark start barrier timed out")
}
func benchSleep(until int64) {
	if delay := until - benchMono(); delay > 0 {
		time.Sleep(time.Duration(delay))
	}
}
func benchFanout(c benchConfig, i int) int {
	if c.Corpus == "S" {
		return i%5 + 1
	}
	switch {
	case i%10 < 5:
		return 4
	case i%10 < 7:
		return 22
	case i%10 < 9:
		return 23
	default:
		return 50
	}
}
func benchStamp(c benchConfig, i int) int64 {
	day := (i / 10) % c.Shape.Days
	if day == 0 {
		return c.Epoch - 2700 + int64(i%1800)
	}
	return c.Epoch - int64(day)*86400 - 43200 + int64(i%3600)
}
