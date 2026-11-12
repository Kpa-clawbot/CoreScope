package main

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientLogID(t *testing.T) {
	a, b := "198.51.100.20", "198.51.100.21"
	ida, idb := clientLogID(a), clientLogID(b)
	if ida != clientLogID(a) {
		t.Error("ID must be stable within a process")
	}
	if ida == idb {
		t.Error("different IPs must get different IDs")
	}
	if len(ida) != 12 {
		t.Errorf("len = %d, want 12 hex chars", len(ida))
	}
	for ip, id := range map[string]string{a: ida, b: idb, "2001:db8::7": clientLogID("2001:db8::7")} {
		if strings.Contains(id, ip) || strings.Trim(id, "0123456789abcdef") != "" {
			t.Errorf("ID %q for %s must be bare hex with no IP in it", id, ip)
		}
	}
	if clientLogID(clientSharedKey) != "shared" {
		t.Error("the shared bucket must log as shared")
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestPlannerJobLogHasNoClientIP(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	var buf lockedBuf
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	ip := "198.51.100.77"
	if w := h.get("/api/planner/profile?pts=30.1,-97.1,30.2,-97.0", ip); w.Code != http.StatusOK {
		t.Fatalf("job: %d %s", w.Code, w.Body.String())
	}
	var line string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, l := range strings.Split(buf.String(), "\n") {
			if strings.Contains(l, "[planner]") && strings.Contains(l, "client=") && strings.Contains(l, "outcome=") {
				line = l
			}
		}
		if line != "" {
			break
		}
	}
	if line == "" {
		t.Fatalf("no planner job log line; log: %q", buf.String())
	}
	if strings.Contains(buf.String(), ip) {
		t.Errorf("log contains the client IP: %q", buf.String())
	}
	if !strings.Contains(line, "client="+clientLogID(ip)) {
		t.Errorf("line %q lacks client=%s", line, clientLogID(ip))
	}
}
