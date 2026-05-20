package main

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// PR #1216 round-2 review fixes. Tests RED before the fix lands.
//
// r1 closed the cold-start blind spot but introduced three new failure
// modes that r2 must eliminate:
//
//   r2 #1 — checkSourceLiveness returns LivenessOK for BOTH "messages
//           flowing" AND "disconnected/never-connected". A stalled source
//           whose TCP eventually RSTs trips processLivenessTransition's
//           recovery branch and emits "messages flowing again (recovered)"
//           while going from silently broken to overtly broken. Fix: a
//           distinct LivenessDisconnected kind that the transition
//           function treats as a silent (no-emit) state, so the alert
//           cooldown does not collapse on a non-event.
//
//   r2 #2 — MarkReconnected re-stamps StartedAt on every reconnect, so
//           the cold-start grace clock restarts forever under a broker
//           flap (CONNECT ok, SUBSCRIBE ACL-denied — the exact #1212
//           shape). The headline "NEVER received" alarm never fires.
//           Fix: separate FirstConnectedAt (set once at registration,
//           never reset) from StartedAt (free to reset on reconnect for
//           transient-stall tracking). Cold-start grace must use
//           FirstConnectedAt.
//
//   r2 #3 — main.go calls log.Fatalf on a tag collision in the liveness
//           registry, killing the entire ingestor over one config typo.
//           That recreates the #1212 total-ingest-stop failure class
//           this PR exists to prevent. Fix: log an ERROR and skip
//           liveness registration for the duplicate — the MQTT source
//           still attempts to connect, just isn't tracked by the
//           watchdog (the first registration remains authoritative).

// r2 #1 RED: a stalled source whose connection then drops must NOT emit
// "recovered". The current code does — checkSourceLiveness returns
// LivenessOK for both genuine recovery and disconnection, so
// processLivenessTransition sees lastAlert!=0 + kind==LivenessOK and
// fires the recovery INFO. Operators reading the log think the source
// healed when it actually died.
func TestMQTTStallWatchdog_NoFalseRecoveryOnDisconnect(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	var connected atomic.Bool
	connected.Store(true)

	s := &SourceLivenessState{
		Tag:           "drops-after-stall",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return connected.Load() },
	}
	atomic.StoreInt64(&s.LastMessageUnix, now.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, now.Add(-20*time.Minute).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: registerLivenessState: %v", err)
	}

	var mu sync.Mutex
	var emits []string
	emit := func(args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			if str, ok := args[0].(string); ok {
				emits = append(emits, str)
			}
		}
	}

	tick := make(chan time.Time, 2)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		runLivenessWatchdogLoop(tick, done, 5*time.Minute, emit)
		close(exited)
	}()

	// Tick 1: source connected + 10m silent → WARN edge.
	tick <- now
	waitFor(t, &mu, &emits, 1, 2*time.Second)

	// The TCP socket RSTs — paho flips IsConnected to false. The watchdog
	// must NOT interpret this as recovery; the source went from silently
	// broken to overtly broken.
	connected.Store(false)
	tick <- now.Add(60 * time.Second)

	// Settle so any (incorrect) extra emits land before we count.
	time.Sleep(150 * time.Millisecond)
	close(done)
	<-exited

	mu.Lock()
	got := append([]string(nil), emits...)
	mu.Unlock()

	for _, e := range got {
		upper := strings.ToUpper(e)
		if strings.Contains(upper, "RECOVER") || strings.Contains(upper, "FLOWING AGAIN") {
			t.Fatalf("watchdog must NOT emit recovery INFO when a stalled source disconnects; got %q (all=%v)", e, got)
		}
	}
}

// r2 #2 RED: a broker that ACKs CONNECT but denies SUBSCRIBE causes paho
// to loop CONNECT → drop → CONNECT → drop. Each reconnect calls
// MarkReconnected, which re-stamps StartedAt=now and resets the
// cold-start grace clock. After 30 minutes of flapping, the source has
// still NEVER received a message, but the "NEVER received" alarm never
// fires because sinceStart is always sub-threshold. Fix: track
// FirstConnectedAt separately from StartedAt; the cold-start check must
// use the former.
func TestMQTTStallWatchdog_ColdStartSurvivesBrokerFlap(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	t0 := time.Now()
	s := &SourceLivenessState{
		Tag:           "flapping-acl-deny",
		Broker:        "tcp://acl-denied:1883",
		IsConnectedFn: func() bool { return true },
	}
	// First registration stamps FirstConnectedAt (and StartedAt) at t0.
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: registerLivenessState: %v", err)
	}

	// Paho keeps re-establishing the TCP/MQTT session every minute. No
	// message ever arrives because SUBSCRIBE is denied. Each reconnect
	// resets StartedAt.
	for i := 1; i <= 6; i++ {
		s.MarkReconnected(t0.Add(time.Duration(i) * time.Minute))
	}

	// 6m after the very first connection — well past the 5m cold-start
	// threshold. The headline alarm must fire.
	now := t0.Add(6*time.Minute + 30*time.Second)
	_, kind := checkSourceLiveness(s, 5*time.Minute, now)
	if kind != LivenessNeverReceived {
		t.Fatalf("under broker flap (#1212 ACL-deny class), cold-start alarm must fire based on FirstConnectedAt, not the most recent reconnect; got kind=%v", kind)
	}
}

// Sanity check: a single transient reconnect WITHIN the cold-start window
// must NOT prematurely trip the NeverReceived alarm — the grace was
// designed for that. This guards against an over-correction where r2
// switches blindly to FirstConnectedAt and ignores legitimate startup
// jitter.
func TestMQTTStallWatchdog_TransientReconnectDuringGraceStaysQuiet(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	t0 := time.Now()
	s := &SourceLivenessState{
		Tag:           "transient-reconnect",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: registerLivenessState: %v", err)
	}

	// 30s in, one transient reconnect.
	s.MarkReconnected(t0.Add(30 * time.Second))

	// 1m after registration — still inside the 5m grace.
	_, kind := checkSourceLiveness(s, 5*time.Minute, t0.Add(1*time.Minute))
	if kind != LivenessOK {
		t.Fatalf("during cold-start grace, transient reconnects must stay quiet; got kind=%v", kind)
	}
}

// r2 #3 RED: tag collision must not kill the ingestor. main.go currently
// log.Fatalf's, which recreates the #1212 total-ingest-stop class this
// PR exists to prevent. registerLivenessOrSkip is the small helper main
// will call instead: log an ERROR + skip liveness registration for the
// duplicate, return false so the caller knows the source is connecting
// untracked. The first registration remains authoritative.
func TestRegisterLivenessOrSkip_LogsErrorAndDoesNotExitOnCollision(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	}()

	a := &SourceLivenessState{Tag: "dup", Broker: "tcp://a:1883"}
	b := &SourceLivenessState{Tag: "dup", Broker: "tcp://b:1883"}

	if ok := registerLivenessOrSkip(a); !ok {
		t.Fatalf("first registration must succeed; helper returned false (log=%q)", buf.String())
	}
	if ok := registerLivenessOrSkip(b); ok {
		t.Fatalf("second registration with same tag must return false (skip); helper returned true (log=%q)", buf.String())
	}

	logOut := buf.String()
	if !strings.Contains(logOut, "ERROR") {
		t.Errorf("collision must be logged at ERROR severity so operators see it without it crashing the process; got %q", logOut)
	}
	if !strings.Contains(logOut, "dup") {
		t.Errorf("collision log must include the offending tag; got %q", logOut)
	}
	if !strings.Contains(strings.ToLower(logOut), "skip") {
		t.Errorf("collision log must say the duplicate is being skipped so operators know the source is untracked; got %q", logOut)
	}

	// And the registry still holds the FIRST registration.
	livenessRegistryMu.RLock()
	got := livenessRegistry["dup"]
	livenessRegistryMu.RUnlock()
	if got != a {
		t.Errorf("first registration must remain authoritative after collision-skip; got pointer for broker=%s", got.Broker)
	}
}
