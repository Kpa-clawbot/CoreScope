package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Wire format is pinned to MeshCore a366955: Packet.cpp and Mesh.cpp::createAdvert;
// group encryption reuses this revision's firmware-derived test helper.
type benchFixture struct {
	c        benchConfig
	public   []string
	private  []ed25519.PrivateKey
	channels map[string]string
}

func benchFixtureFor(c benchConfig) *benchFixture {
	f := &benchFixture{c: c, channels: map[string]string{}}
	for i := 0; i < c.Shape.Nodes; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("corescope-bench/%d/node/%d", c.Seed, i)))
		key := ed25519.NewKeyFromSeed(seed[:])
		f.private = append(f.private, key)
		f.public = append(f.public, hex.EncodeToString(key.Public().(ed25519.PublicKey)))
	}
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("#bench-%02d", i)
		f.channels[name] = deriveHashtagChannelKey(name)
	}
	return f
}
func (f *benchFixture) packet(index, variant int, stamp int64) (*PacketData, error) {
	node := index % len(f.public)
	payloadType := 5
	var payload []byte
	if index%10 == 0 {
		payloadType = 4
		public := f.private[node].Public().(ed25519.PublicKey)
		app := []byte{0x92}
		location := make([]byte, 8)
		binary.LittleEndian.PutUint32(location, uint32(20_000_000+node*100))
		binary.LittleEndian.PutUint32(location[4:], uint32(30_000_000+node*100))
		app = append(app, location...)
		app = append(app, []byte(fmt.Sprintf("Bench%04d-%07d", node, index))...)
		signed := append([]byte{}, public...)
		clock := make([]byte, 4)
		binary.LittleEndian.PutUint32(clock, uint32(stamp))
		signed = append(signed, clock...)
		signed = append(signed, app...)
		payload = append(payload, public...)
		payload = append(payload, clock...)
		payload = append(payload, ed25519.Sign(f.private[node], signed)...)
		payload = append(payload, app...)
	} else {
		channelIndex := 0
		if index%10 >= 5 {
			channelIndex = 1 + index%19
		}
		channel := fmt.Sprintf("#bench-%02d", channelIndex)
		key := f.channels[channel]
		inner := make([]byte, 5)
		binary.LittleEndian.PutUint32(inner, uint32(stamp))
		inner = append(inner, []byte(fmt.Sprintf("Bench%04d: synthetic sequence %d seed %d %s", node, index, f.c.Seed, strings.Repeat("x", index%40)))...)
		cipher, mac := buildChannelEncrypted(key, inner)
		keyBytes, _ := hex.DecodeString(key)
		sum := sha256.Sum256(keyBytes)
		macBytes, _ := hex.DecodeString(mac)
		cipherBytes, _ := hex.DecodeString(cipher)
		payload = append([]byte{sum[0]}, macBytes...)
		payload = append(payload, cipherBytes...)
	}
	hashSize := 1 + index%3
	hopCount := 1 + index%3
	route := 1
	if index%5 == 0 && variant == 0 {
		hopCount = 0
	}
	if payloadType == 4 && variant == 1 {
		route = 2
		hopCount = 0
	}
	var path []byte
	for hop := 0; hop < hopCount; hop++ {
		key, _ := hex.DecodeString(f.public[(index+variant*7+hop+1)%len(f.public)])
		path = append(path, key[:hashSize]...)
	}
	if variant >= 1000 {
		hashSize = 3
		hopCount = 2
		route = 1
		path = []byte{0xfa, byte(variant >> 8), byte(variant), 0xfb, byte(index >> 8), byte(index)}
	}
	wire := append([]byte{byte(payloadType<<2 | route), byte((hashSize-1)<<6 | hopCount)}, path...)
	wire = append(wire, payload...)
	raw := hex.EncodeToString(wire)
	decoded, err := DecodePacket(raw, f.channels, true)
	if err != nil {
		return nil, err
	}
	score, snr, rssi := float64(index%5), float64(index%20)-10.5, -100.5+float64(index%25)
	msg := &MQTTPacketMessage{Raw: raw, SNR: &snr, RSSI: &rssi, Score: &score, Timestamp: time.Unix(stamp, 0).UTC().Format(time.RFC3339)}
	p := BuildPacketData(msg, decoded, f.public[0], "AAA", nil)
	p.Timestamp = msg.Timestamp
	if p.Hash == "" || p.PayloadType != payloadType {
		return nil, fmt.Errorf("generated frame failed native decoder")
	}
	return p, nil
}

type benchGeneratedObservation struct {
	Packet        *PacketData
	ObserverIndex any
	PathVariant   int
}

func (f *benchFixture) observation(index, ordinal int, stamp int64, used map[string]bool) (benchGeneratedObservation, error) {
	observerIndex := ordinal%f.c.Shape.Observers + 1
	result := benchGeneratedObservation{ObserverIndex: observerIndex}
	observerID := f.public[observerIndex-1]
	if ordinal%17 == 16 {
		result.ObserverIndex, observerID = nil, ""
	}
	// Keep every observation and NULL identity. A path-prefix collision for
	// the same identity needs a different valid wire path, not different JSON
	// spelling. The bound stays below packet()'s special replay variants.
	for attempt := 0; attempt < 64; attempt++ {
		result.PathVariant = ordinal + attempt
		packet, err := f.packet(index, result.PathVariant, stamp)
		if err != nil {
			return result, err
		}
		key := observerID + "|" + packet.PathJSON
		if !used[key] {
			used[key] = true
			result.Packet = packet
			return result, nil
		}
	}
	return result, fmt.Errorf("no unique observer/path identity after 64 valid path variants")
}

func TestCoreScopeBenchmarkObservationKeyspace(t *testing.T) {
	for _, shape := range []struct {
		name             string
		nodes, observers int
	}{{"B", 2000, 128}, {"L", 5000, 512}} {
		t.Run(shape.name, func(t *testing.T) {
			c := benchConfig{Corpus: shape.name, Seed: 20261008, Epoch: 1791451200, Shape: benchShape{Nodes: shape.nodes, Observers: shape.observers, Days: 8}}
			f := benchFixtureFor(c)
			// The path recipe depends on index modulo Nodes and modulo 3;
			// route/fanout periods 5/10 divide both node counts. Thus these
			// 6,000/15,000 positions cover the complete B/L path key space.
			corrections, missing := 0, 0
			for index := 0; index < 3*shape.nodes; index++ {
				used := make(map[string]bool)
				count := benchFanout(c, index)
				if count > shape.observers {
					t.Fatal("known observer identities would repeat within a transmission")
				}
				// Known observer IDs are distinct. Only NULL identities can
				// share a key, so exercise every such pair with real decoding.
				for ordinal := 16; ordinal < count; ordinal += 17 {
					original, err := f.packet(index, ordinal, c.Epoch)
					if err != nil {
						t.Fatal(err)
					}
					got, err := f.observation(index, ordinal, c.Epoch, used)
					if err != nil {
						t.Fatal(err)
					}
					if got.ObserverIndex != nil || got.Packet.Hash != original.Hash || got.Packet.RawHex[:4] != original.RawHex[:4] {
						t.Fatal("path correction changed observer, payload identity, route or hop/hash shape")
					}
					if got.PathVariant != ordinal {
						corrections++
						if got.Packet.PathJSON == original.PathJSON {
							t.Fatal("retry did not change the real decoded path")
						}
					}
					missing++
				}
			}
			if corrections == 0 || missing != 3*shape.nodes*6/10 {
				t.Fatalf("NULL collision coverage: corrections=%d NULLs=%d", corrections, missing)
			}
		})
	}
}

func TestCoreScopeBenchmarkObservationRetryBound(t *testing.T) {
	c := benchConfig{Corpus: "B", Seed: 20261008, Epoch: 1791451200, Shape: benchShape{Nodes: 2000, Observers: 128}}
	f := benchFixtureFor(c)
	used := make(map[string]bool)
	for prefix := 0; prefix < 256; prefix++ {
		used[fmt.Sprintf(`|["%02X"]`, prefix)] = true
	}
	if _, err := f.observation(9, 16, c.Epoch, used); err == nil {
		t.Fatal("exhausted path space must fail rather than drop an observation or loop forever")
	}
}

func TestCoreScopeBenchmarkProtocol(t *testing.T) {
	c := benchConfig{Seed: 20261008, Epoch: 1791451200, Shape: benchShape{Nodes: 80, Observers: 8, Transmissions: 100, Days: 8}}
	f := benchFixtureFor(c)
	a, _ := f.packet(0, 0, c.Epoch)
	repeated, _ := f.packet(18000, 0, c.Epoch)
	if a.Hash == repeated.Hash {
		t.Fatal("repeated advertiser and emission time collapsed distinct corpus transmissions")
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		a, e := f.packet(i, 0, c.Epoch-int64(i))
		if e != nil {
			t.Fatal(e)
		}
		b, e := f.packet(i, 1000, c.Epoch-int64(i))
		if e != nil {
			t.Fatal(e)
		}
		if a.Hash != b.Hash {
			t.Fatal("path change altered content identity")
		}
		if seen[a.Hash] {
			t.Fatal("generator accidentally deduplicated distinct transmissions")
		}
		if a.Score == nil || *a.Score != math.Trunc(*a.Score) {
			t.Fatal("timed score corpus must stay integral so the unmodified baseline loads the same rows")
		}
		seen[a.Hash] = true
		if i%10 != 0 && !strings.HasPrefix(a.ChannelHash, "#bench-") {
			t.Fatal("generated channel message did not decrypt")
		}
	}
	for _, name := range []string{"S", "B", "L"} {
		c.Corpus = name
		total := 0
		for i := 0; i < 100; i++ {
			total += benchFanout(c, i)
		}
		want := 1600
		if name == "S" {
			want = 300
		}
		if total != want {
			t.Fatalf("fanout %s=%d", name, total)
		}
	}
}

func TestCoreScopeBenchmarkEventStream(t *testing.T) {
	dir := t.TempDir()
	c := benchConfig{Corpus: "S", Seed: 20261008, Epoch: 1791454800, WireEpoch: 1791451200, Shape: benchShape{Nodes: 200, Observers: 16, Transmissions: 100, Observations: 300, Days: 2}, Output: dir, EventsFile: filepath.Join(dir, "one.jsonl"), Seconds: 4, IngestRate: 10}
	benchMakeEvents(t, c)
	first, e := os.ReadFile(c.EventsFile)
	if e != nil {
		t.Fatal(e)
	}
	c.EventsFile = filepath.Join(dir, "two.jsonl")
	benchMakeEvents(t, c)
	second, e := os.ReadFile(c.EventsFile)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same seed/epochs produced different event streams")
	}
	dec := json.NewDecoder(bytes.NewReader(first))
	counts := map[string]int{}
	var additional *PacketData
	for i := 0; i < 40; i++ {
		var input benchInput
		if e := dec.Decode(&input); e != nil {
			t.Fatal(e)
		}
		if input.Sequence != i {
			t.Fatal("sequence drift")
		}
		counts[input.Class]++
		if i == 10 {
			additional = input.Packet
		}
		if i == 16 && (input.Packet.Hash != additional.Hash || input.Packet.PathJSON != additional.PathJSON) {
			t.Fatal("duplicate event does not hit the same observation key")
		}
	}
	for class, want := range map[string]int{"new": 20, "additional": 12, "duplicate": 6, "late": 2} {
		if counts[class] != want {
			t.Fatalf("event class %s=%d want %d", class, counts[class], want)
		}
	}
}

func TestCoreScopeBenchmark(t *testing.T) {
	c := benchRead(t)
	if runtime.GOOS != "linux" {
		t.Fatal("measurement requires Linux")
	}
	if c.Mode == "prepare" {
		if err := benchPrepare(c); err != nil {
			t.Fatal(err)
		}
		return
	}
	if c.Mode == "events" {
		benchMakeEvents(t, c)
		return
	}
	s, err := benchOpen(c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	if c.Mode == "replay" {
		profiles := benchProfileWindow(c)
		pool := benchPoolWindow(t, c, s.db)
		defer func() {
			if err := <-profiles; err != nil {
				t.Error(err)
			}
			<-pool
		}()
	}
	settings, err := benchSettings(s)
	if err != nil {
		t.Fatal(err)
	}
	benchJSON(t, filepath.Join(c.Output, "database-settings.json"), settings)
	if err = s.RefreshPrefixIndex(); err != nil {
		t.Fatal(err)
	}
	switch c.Mode {
	case "replay":
		benchReplay(t, c, s)
	case "retention":
		benchRetention(t, c, s)
	case "handler":
		benchHandler(t, c, s, benchFixtureFor(c))
	case "plans":
		if e := os.MkdirAll(filepath.Join(c.Output, "plans"), 0700); e != nil {
			t.Fatal(e)
		}
		for _, query := range []struct {
			name, sql string
			args      []any
		}{
			{"retention-empty", pruneAgedTransmissionIDs, []any{time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339), pruneBatchTransmissions}},
			{"advert-preservation", legacyAdvertObservationSQL, []any{1, 1, "[]"}},
		} {
			plan, e := benchExplain(s, query.sql, query.args...)
			if e != nil {
				t.Fatal(e)
			}
			benchJSON(t, filepath.Join(c.Output, "plans", query.name+".json"), map[string]any{"sql": query.sql, "args": query.args, "plan": plan, "context": "separate diagnostic after replay and retention; excluded from headline timings"})
		}
	default:
		t.Fatal("unsupported benchmark mode")
	}
}

type benchEvent struct {
	Sequence  int    `json:"sequence"`
	Class     string `json:"class"`
	Hash      string `json:"hash"`
	Scheduled int64  `json:"scheduled_ns"`
	Started   int64  `json:"started_ns"`
	Completed int64  `json:"completed_ns"`
	Measured  bool   `json:"measured"`
	Dropped   bool   `json:"dropped"`
	New       bool   `json:"new_transmission"`
	Error     string `json:"error,omitempty"`
}

type benchInput struct {
	Sequence int         `json:"sequence"`
	Class    string      `json:"class"`
	Packet   *PacketData `json:"packet"`
}

func benchMakeEvents(t *testing.T, c benchConfig) {
	f := benchFixtureFor(c)
	file, e := os.OpenFile(c.EventsFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	for sequence := 0; sequence < (c.Warmup+c.Seconds)*c.IngestRate; sequence++ {
		class, index, variant := "new", c.Shape.Transmissions+sequence, 0
		part, block := sequence%20, sequence/20
		stamp := c.Epoch + int64(sequence/c.IngestRate)
		if part >= 10 {
			stride := c.Shape.Days * 10
			blocks := c.Shape.Transmissions / stride
			if blocks < 1 {
				blocks = 1
			}
			index = (block%blocks)*stride + block%10
			wire := c
			if c.WireEpoch != 0 {
				wire.Epoch = c.WireEpoch
			}
			stamp = benchStamp(wire, index)
			class = "additional"
			variant = 1000 + part
			if part >= 16 && part < 19 {
				class = "duplicate"
				variant = 1010
			}
			if part == 19 {
				class = "late"
				variant = 1019
			}
		}
		packet, e := f.packet(index, variant, stamp)
		if e != nil {
			t.Fatal(e)
		}
		packet.ObserverID = f.public[0]
		packet.Timestamp = time.Unix(c.Epoch+int64(sequence/c.IngestRate), 0).UTC().Format(time.RFC3339)
		if class == "late" {
			packet.Timestamp = time.Unix(benchStamp(c, index)-60, 0).UTC().Format(time.RFC3339)
		}
		if e := enc.Encode(benchInput{sequence, class, packet}); e != nil {
			t.Fatal(e)
		}
	}
}

func benchReplay(t *testing.T, c benchConfig, s *Store) {
	var initialTx, initialObs int64
	if err := s.db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&initialTx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&initialObs); err != nil {
		t.Fatal(err)
	}
	file := benchOutput(t, c, "ingest.jsonl")
	encoder := json.NewEncoder(file)
	var outputMu sync.Mutex
	emit := func(e benchEvent) {
		outputMu.Lock()
		defer outputMu.Unlock()
		if err := encoder.Encode(e); err != nil {
			t.Error(err)
		}
	}
	if err := os.WriteFile(filepath.Join(c.Output, "replay-ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	start, err := benchStart(c)
	if err != nil {
		t.Fatal(err)
	}
	benchSleep(start)
	queue := make(chan benchInput, 1024)
	done := make(chan struct{})
	var successful, newTx, newObs, dropped int64
	seenReplay := make(map[string]bool)
	var firstError error
	go func() {
		defer close(done)
		for input := range queue {
			sequence, class, p := input.Sequence, input.Class, input.Packet
			var e error
			event := benchEvent{Sequence: sequence, Class: class, Scheduled: start + int64(sequence)*int64(time.Second)/int64(c.IngestRate), Measured: sequence >= c.Warmup*c.IngestRate}
			if p != nil {
				event.Hash = p.Hash
				event.Started = benchMono()
				beforeErrors := s.Stats.WriteErrors.Load()
				event.New, e = s.InsertTransmission(p)
				if e == nil && s.Stats.WriteErrors.Load() != beforeErrors {
					e = fmt.Errorf("write-error counter advanced despite handler return")
				}
				event.Completed = benchMono()
				if e == nil && event.New != (class == "new") {
					e = fmt.Errorf("event class %s had unexpected transmission creation", class)
				}
			}
			if e != nil {
				event.Error = e.Error()
				if firstError == nil {
					firstError = e
				}
			} else {
				successful++
				if event.New {
					newTx++
				}
				key := p.Hash + "/" + p.ObserverID + "/" + p.PathJSON
				if !seenReplay[key] {
					newObs++
					seenReplay[key] = true
				}
			}
			emit(event)
		}
	}()
	retained := make(chan benchPruneResult, 1)
	go func() {
		benchSleep(start + int64(c.Warmup+c.Seconds*3/4)*int64(time.Second))
		_ = os.WriteFile(filepath.Join(c.Output, "retention-started"), []byte("started"), 0600)
		retained <- benchPrune(s)
	}()
	events := (c.Warmup + c.Seconds) * c.IngestRate
	inputFile, err := os.Open(c.EventsFile)
	if err != nil {
		close(queue)
		<-done
		t.Fatal(err)
	}
	defer inputFile.Close()
	inputDecoder := json.NewDecoder(inputFile)
	var producerError error
	for sequence := 0; sequence < events; sequence++ {
		var input benchInput
		if err := inputDecoder.Decode(&input); err != nil {
			producerError = err
			break
		}
		if input.Sequence != sequence || input.Packet == nil {
			producerError = fmt.Errorf("invalid pre-generated event sequence")
			break
		}
		scheduled := start + int64(sequence)*int64(time.Second)/int64(c.IngestRate)
		benchSleep(scheduled)
		select {
		case queue <- input:
		default:
			dropped++
			emit(benchEvent{Sequence: sequence, Class: "schedule", Scheduled: scheduled, Dropped: true, Measured: sequence >= c.Warmup*c.IngestRate})
		}
	}
	close(queue)
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("bounded ingestion queue did not drain in 120 seconds")
	}
	retention := <-retained
	benchJSON(t, filepath.Join(c.Output, "retention.json"), retention)
	if retention.Error != "" {
		t.Fatal(retention.Error)
	}
	var actualTx, actualObs int64
	if err := s.db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&actualTx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&actualObs); err != nil {
		t.Fatal(err)
	}
	verified := dropped == 0 && successful == int64(events) && producerError == nil && firstError == nil && s.Stats.WriteErrors.Load() == 0 && actualTx == initialTx+newTx-retention.Transmissions && actualObs == initialObs+newObs-retention.Observations
	benchJSON(t, filepath.Join(c.Output, "ingest-validation.json"), map[string]any{"verified": verified, "scheduled": events, "completed": successful, "dropped_schedules": dropped, "expected_transmissions": initialTx + newTx - retention.Transmissions, "actual_transmissions": actualTx, "expected_observations": initialObs + newObs - retention.Observations, "actual_observations": actualObs, "write_errors": s.Stats.WriteErrors.Load(), "writer_wait_hold": s.WriterStatsSnapshot(), "finished_ns": benchMono()})
	if !verified {
		t.Fatalf("durable ingest effects did not match: dropped=%d completed=%d/%d tx=%d/%d obs=%d/%d first error=%v", dropped, successful, events, actualTx, initialTx+newTx-retention.Transmissions, actualObs, initialObs+newObs-retention.Observations, firstError)
	}
}

type benchPruneResult struct {
	Elapsed       int64  `json:"elapsed_ns"`
	Transmissions int64  `json:"transmissions_deleted"`
	Observations  int64  `json:"observations_deleted"`
	Error         string `json:"error,omitempty"`
}

func benchPrune(s *Store) benchPruneResult {
	result := benchPruneResult{}
	cutoff := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	q := fmt.Sprintf("SELECT count(*) FROM observations o JOIN transmissions t ON t.id=o.transmission_id WHERE t.first_seen < '%s'", cutoff)
	if err := s.db.QueryRow(q).Scan(&result.Observations); err != nil {
		result.Error = err.Error()
		return result
	}
	started := benchMono()
	n, err := s.PruneOldPackets(7)
	result.Elapsed = benchMono() - started
	result.Transmissions = n
	if err != nil {
		result.Error = err.Error()
	}
	return result
}
func benchRetention(t *testing.T, c benchConfig, s *Store) {
	r := benchPrune(s)
	benchJSON(t, filepath.Join(c.Output, "retention-empty.json"), r)
	if r.Error != "" || r.Transmissions != 0 || r.Observations != 0 {
		t.Fatal("empty retention pass found unexpected work or failed")
	}
}

func benchHandler(t *testing.T, c benchConfig, s *Store, f *benchFixture) {
	var before, after int64
	if err := s.db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	samples := make([]int64, 0, 200)
	for i := 0; i < 200; i++ {
		p, err := f.packet(c.Shape.Transmissions+10000000+i, 0, c.Epoch+int64(i))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{"raw": p.RawHex, "origin": "Synthetic bench", "SNR": 6.5, "RSSI": -95.5})
		if err != nil {
			t.Fatal(err)
		}
		msg := &mockMessage{topic: "meshcore/AAA/" + f.public[0] + "/packets", payload: payload}
		started := benchMono()
		handleMessage(s, "bench", MQTTSource{Name: "bench"}, msg, f.channels, nil, &Config{})
		samples = append(samples, benchMono()-started)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	valid := after-before == 200 && s.Stats.WriteErrors.Load() == 0
	benchJSON(t, filepath.Join(c.Output, "handler.json"), map[string]any{"verified": valid, "samples_ns": samples, "new_transmissions": after - before, "classification": "separate 200-envelope mechanism control; not mixed-load throughput"})
	if !valid {
		t.Fatal("real MQTT handler dropped valid synthetic envelopes")
	}
}
