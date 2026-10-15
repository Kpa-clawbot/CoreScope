package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type benchCorpusInfo struct {
	Node      string `json:"node"`
	Dense     string `json:"dense_observer"`
	Sparse    string `json:"sparse_observer"`
	NewPacket string `json:"new_packet"`
	OldPacket string `json:"old_packet"`
}

func benchInfo(c benchConfig) (benchCorpusInfo, error) {
	var info benchCorpusInfo
	b, e := os.ReadFile(c.CorpusInfo)
	if e == nil {
		e = json.Unmarshal(b, &info)
	}
	return info, e
}

func TestCoreScopeBenchmark(t *testing.T) {
	c := benchRead(t)
	if runtime.GOOS != "linux" {
		t.Fatal("measurement requires Linux")
	}
	switch c.Mode {
	case "queries":
		benchQueries(t, c)
	case "client":
		benchClient(t, c)
	default:
		t.Fatal("unsupported server benchmark mode")
	}
}

type benchRequest struct {
	Class          string `json:"class"`
	Scheduled      int64  `json:"scheduled_ns"`
	Dispatched     int64  `json:"dispatched_ns"`
	Completed      int64  `json:"completed_ns"`
	Status         int    `json:"status"`
	ExpectedStatus int    `json:"expected_status"`
	Bytes          int    `json:"bytes"`
	SHA256         string `json:"response_sha256,omitempty"`
	Error          string `json:"error,omitempty"`
	Measured       bool   `json:"measured"`
	Cache          string `json:"cache"`
}

func benchQueries(t *testing.T, c benchConfig) {
	target := os.Getenv("CORESCOPE_READER_DATABASE_URL")
	if target == "" {
		target = c.SQLite
	}
	db, e := OpenDB(target)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	info, e := benchInfo(c)
	if e != nil {
		t.Fatal(e)
	}
	out := json.NewEncoder(benchOutput(t, c, "sql-queries.jsonl"))
	for i := 0; i < 200; i++ {
		sample := benchRequest{Measured: true, Status: 200, Cache: "explicit SQL miss"}
		var value any
		if i%2 == 0 {
			db.msgCacheMu.Lock()
			db.msgCache = nil
			db.msgCacheMu.Unlock()
			sample.Class = "channel_messages_sql"
			sample.Dispatched = benchMono()
			rows, total, err := db.GetChannelMessages("#bench-00", 50, (i%4)*500)
			sample.Completed = benchMono()
			e = err
			value = rows
			if e == nil && (len(rows) == 0 || total < 50) {
				e = fmt.Errorf("channel SQL query returned no meaningful data")
			}
		} else {
			sample.Class = "observer_metrics_sql"
			sample.Dispatched = benchMono()
			rows, _, err := db.GetObserverMetrics(info.Dense, time.Unix(c.Epoch-86400, 0).UTC().Format(time.RFC3339), time.Unix(c.Epoch, 0).UTC().Format(time.RFC3339), "5m", 300)
			sample.Completed = benchMono()
			e = err
			value = rows
			if e == nil && len(rows) == 0 {
				e = fmt.Errorf("metrics SQL query returned no meaningful data")
			}
		}
		if e != nil {
			sample.Error = e.Error()
		}
		b, _ := json.Marshal(value)
		sum := sha256.Sum256(b)
		sample.SHA256 = hex.EncodeToString(sum[:])
		sample.Bytes = len(b)
		if e := out.Encode(sample); e != nil {
			t.Fatal(e)
		}
		if sample.Error != "" {
			t.Fatal(sample.Error)
		}
	}
}

type benchEndpoint struct {
	Class string `json:"class"`
	Path  string `json:"path"`
}

func benchEndpoints(c benchConfig, info benchCorpusInfo) []benchEndpoint {
	since24 := url.QueryEscape(time.Unix(c.Epoch-86400, 0).UTC().Format(time.RFC3339))
	since7 := url.QueryEscape(time.Unix(c.Epoch-7*86400, 0).UTC().Format(time.RFC3339))
	until := url.QueryEscape(time.Unix(c.Epoch, 0).UTC().Format(time.RFC3339))
	groups := []struct {
		weight int
		items  []benchEndpoint
	}{
		{20, []benchEndpoint{{"nodes_50", "/api/nodes?limit=50"}, {"nodes_2000", "/api/nodes?limit=2000"}, {"nodes_region", "/api/nodes?limit=50&region=AAA&search=Synthetic"}}},
		{20, []benchEndpoint{{"channels", "/api/channels"}, {"messages_0", "/api/channels/%23bench-00/messages?limit=50&offset=0"}, {"messages_1000", "/api/channels/%23bench-00/messages?limit=50&offset=1000"}}},
		{10, []benchEndpoint{{"observers", "/api/observers"}}},
		{10, []benchEndpoint{{"metrics_dense_24h", "/api/observers/" + info.Dense + "/metrics?since=" + since24 + "&until=" + until + "&resolution=5m"}, {"metrics_sparse_7d", "/api/observers/" + info.Sparse + "/metrics?since=" + since7 + "&until=" + until + "&resolution=1h"}}},
		{10, []benchEndpoint{{"reach", "/api/nodes/" + info.Node + "/reach"}, {"rx_coverage", "/api/nodes/" + info.Node + "/rx-coverage?bbox=19,29,22,32&z=14"}}},
		{15, []benchEndpoint{{"packets_memory", "/api/packets?limit=50"}, {"packet_detail_memory", "/api/packets/" + info.NewPacket}, {"packet_detail_sql", "/api/packets/" + info.OldPacket}}},
		{10, []benchEndpoint{{"analytics_rf", "/api/analytics/rf"}, {"analytics_topology", "/api/analytics/topology"}, {"analytics_channels", "/api/analytics/channels"}, {"analytics_rf_filtered", "/api/analytics/rf?region=AAA"}}},
		{5, []benchEndpoint{{"stats", "/api/stats"}}},
	}
	var result []benchEndpoint
	for _, group := range groups {
		for i := 0; i < group.weight; i++ {
			result = append(result, group.items[i%len(group.items)])
		}
	}
	return result
}

// Ignore timestamp/version metadata when deciding whether a response carries
// useful data. A successful empty response must not masquerade as fast SQL.
func benchNonempty(value any) bool {
	switch v := value.(type) {
	case []any:
		return len(v) > 0
	case map[string]any:
		if _, bad := v["error"]; bad {
			return false
		}
		if hash, ok := v["hash"].(string); ok && hash != "" {
			return true
		}
		for key, child := range v {
			switch key {
			case "total", "totalNodes", "nodeCount", "packetCount", "totalPackets", "totalTransmissions", "totalMessages", "count", "mobile_receptions", "totalLoaded":
				if n, ok := child.(float64); ok && n > 0 {
					return true
				}
			}
			switch child.(type) {
			case []any, map[string]any:
				if benchNonempty(child) {
					return true
				}
			}
		}
	}
	return false
}

func benchArrayCount(v any) int {
	if rows, ok := v.([]any); ok {
		return len(rows)
	}
	if obj, ok := v.(map[string]any); ok {
		for _, key := range []string{"nodes", "packets", "messages", "metrics", "samples", "observers", "features", "data"} {
			if child, found := obj[key]; found {
				if n := benchArrayCount(child); n > 0 {
					return n
				}
			}
		}
	}
	return 0
}

func benchClient(t *testing.T, c benchConfig) {
	info, e := benchInfo(c)
	if e != nil {
		t.Fatal(e)
	}
	endpoints := benchEndpoints(c, info)
	benchJSON(t, filepath.Join(c.Output, "http-workload.json"), endpoints)
	requests := json.NewEncoder(benchOutput(t, c, "requests.jsonl"))
	messages := json.NewEncoder(benchOutput(t, c, "websocket.jsonl"))
	conn, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(c.BaseURL, "http")+"/ws", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetReadLimit(8 << 20)
	var closing atomic.Bool
	var wsErr atomic.Pointer[string]
	wsDone := make(chan struct{})
	go func() {
		defer close(wsDone)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				if !closing.Load() {
					value := err.Error()
					wsErr.Store(&value)
				}
				return
			}
			var frame struct {
				Type string `json:"type"`
				Data struct {
					Hash string `json:"hash"`
					ID   int64  `json:"id"`
				} `json:"data"`
			}
			if err = json.Unmarshal(raw, &frame); err != nil {
				value := err.Error()
				wsErr.Store(&value)
				return
			}
			if frame.Type == "packet" && frame.Data.Hash != "" {
				if err := messages.Encode(map[string]any{"hash": frame.Data.Hash, "id": frame.Data.ID, "received_ns": benchMono(), "bytes": len(raw)}); err != nil {
					value := err.Error()
					wsErr.Store(&value)
					return
				}
			}
		}
	}()
	if e := os.WriteFile(filepath.Join(c.Output, "client-ready"), []byte("ready"), 0600); e != nil {
		t.Fatal(e)
	}
	start, e := benchStart(c)
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	limit := make(chan struct{}, 64)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var measuredErrors int64
	emit := func(sample benchRequest) {
		mu.Lock()
		defer mu.Unlock()
		if sample.Measured && sample.Error != "" {
			measuredErrors++
		}
		if e := requests.Encode(sample); e != nil {
			t.Error(e)
		}
	}
	for i := 0; i < (c.Warmup+c.Seconds)*c.HTTPRate; i++ {
		endpoint := endpoints[i%len(endpoints)]
		sample := benchRequest{Class: endpoint.Class, Scheduled: start + int64(i)*int64(time.Second)/int64(c.HTTPRate), Measured: i >= c.Warmup*c.HTTPRate, Cache: "production cache; individual hit/miss unobserved"}
		benchSleep(sample.Scheduled)
		select {
		case limit <- struct{}{}:
		default:
			sample.Error = "load generator concurrency cap reached"
			sample.Completed = benchMono()
			emit(sample)
			continue
		}
		wg.Add(1)
		go func(endpoint benchEndpoint, sample benchRequest) {
			defer wg.Done()
			defer func() { <-limit }()
			sample.Dispatched = benchMono()
			response, err := client.Get(c.BaseURL + endpoint.Path)
			if err != nil {
				sample.Error = err.Error()
			} else {
				sample.Status = response.StatusCode
				raw, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
				response.Body.Close()
				sample.Bytes = len(raw)
				sum := sha256.Sum256(raw)
				sample.SHA256 = hex.EncodeToString(sum[:])
				var value any
				switch {
				case readErr != nil:
					sample.Error = readErr.Error()
				case len(raw) > 8<<20:
					sample.Error = "response exceeded bounded client limit"
				case sample.Status == 404 && endpoint.Class == "packet_detail_sql":
					if _, err := os.Stat(filepath.Join(c.Output, "retention-started")); err != nil {
						sample.Error = "history packet missing before retention"
					} else {
						sample.Class = "packet_detail_pruned"
						sample.ExpectedStatus = 404
					}
				case sample.Status != 200:
					sample.Error = fmt.Sprintf("HTTP %d", sample.Status)
				case json.Unmarshal(raw, &value) != nil:
					sample.Error = "invalid JSON response"
				case (endpoint.Class == "nodes_50" || endpoint.Class == "messages_0" || endpoint.Class == "messages_1000" || endpoint.Class == "packets_memory") && benchArrayCount(value) != 50:
					sample.Error = "bounded page did not return the expected 50 real records"
				case endpoint.Class == "nodes_2000" && benchArrayCount(value) != 2000:
					sample.Error = "node directory did not return the expected 2000 records"
				case !benchNonempty(value):
					sample.Error = "empty or invalid benchmark result"
				}
			}
			sample.Completed = benchMono()
			emit(sample)
		}(endpoint, sample)
	}
	wg.Wait()
	// The writer may still be draining its bounded queue. The completion marker
	// is authoritative; allow the unchanged poller to catch up afterward.
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Stat(filepath.Join(c.Output, "ingest-validation.json")); e == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(10 * time.Second)
	closing.Store(true)
	conn.Close()
	<-wsDone
	benchJSON(t, filepath.Join(c.Output, "client-validation.json"), map[string]any{"verified": measuredErrors == 0 && wsErr.Load() == nil, "measured_errors": measuredErrors, "websocket_error": wsErr.Load(), "finished_ns": benchMono()})
	if measuredErrors > 0 {
		t.Fatalf("%d measured HTTP requests failed validation", measuredErrors)
	}
	if e := wsErr.Load(); e != nil {
		t.Fatal(*e)
	}
}
