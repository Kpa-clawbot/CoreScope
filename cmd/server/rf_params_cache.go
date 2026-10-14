package main

// LoRa RF parameters per node from the MeshCore map (issue #1268).
//
// Frequency, spreading factor, bandwidth and coding rate are per-device
// settings that never travel in a packet; the public map
// (map.meshcore.io/api/v1/nodes) is the only source. OPT-IN: an empty
// rfParams.nodesUrl starts no fetch and /api/rf-params serves an empty,
// disabled response. Same shape as the known-channels cache (#1323):
// in-memory atomic snapshot, background refresh, fail-soft to the last good
// snapshot, no DB and no disk.
//
// The map payload is ~50 MB of JSON for ~66k nodes, so it is decoded as a
// stream, capped, validated field by field, and only entries whose advert
// is within maxAgeDays are kept.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultRfRefresh = 6 * time.Hour
	defaultRfMaxAge  = 7 * 24 * time.Hour
	// maxRfNodesBytes caps the decoded map payload (about 50 MB in
	// October 2026), so a runaway or hostile upstream cannot exhaust memory.
	maxRfNodesBytes   = 128 << 20
	maxRfPresetsBytes = 1 << 20
	// rfStartupJitter spreads the first fetch of many instances that start
	// together (a fleet redeploy) over a minute.
	rfStartupJitter = time.Minute
	// rfAdvertFutureSkew is how far ahead of now an advert may be dated.
	// last_advert comes from the node's own clock: a few minutes fast is
	// drift and stays, a wrong time zone (whole hours) or a wrong year
	// (about 20% of the map in October 2026, some dated 2096) does not.
	rfAdvertFutureSkew = 10 * time.Minute
	// rfRetryBase and rfRetryMax bound the retry after a failed fetch while
	// there is no data yet: 1, 2, 4, 8, 16 min, then every 30 min.
	rfRetryBase = time.Minute
	rfRetryMax  = 30 * time.Minute
)

// RfParamsConfig is the rfParams block in config.json.
type RfParamsConfig struct {
	NodesURL     string `json:"nodesUrl"`
	PresetsURL   string `json:"presetsUrl"`
	RefreshHours int    `json:"refreshHours"`
	MaxAgeDays   int    `json:"maxAgeDays"`
}

func (c *RfParamsConfig) enabled() bool { return c != nil && c.NodesURL != "" }

func (c *RfParamsConfig) refresh() time.Duration {
	if c != nil && c.RefreshHours > 0 {
		return time.Duration(c.RefreshHours) * time.Hour
	}
	return defaultRfRefresh
}

func (c *RfParamsConfig) maxAge() time.Duration {
	if c != nil && c.MaxAgeDays > 0 {
		return time.Duration(c.MaxAgeDays) * 24 * time.Hour
	}
	return defaultRfMaxAge
}

// RfParams is one node's radio settings as last advertised to the map.
type RfParams struct {
	Freq       float64 `json:"freq"`
	SF         int     `json:"sf"`
	BW         float64 `json:"bw"`
	CR         int     `json:"cr"`
	LastAdvert string  `json:"last_advert"`
}

// RfPreset is a named radio setting. Presets that share all four values
// are merged into one entry with their titles joined.
type RfPreset struct {
	Title string  `json:"title"`
	Freq  float64 `json:"freq"`
	SF    int     `json:"sf"`
	BW    float64 `json:"bw"`
	CR    int     `json:"cr"`
}

type rfParamsSnapshot struct {
	FetchedAt time.Time
	Source    string
	Nodes     map[string]RfParams // lower-case pubkey
	Presets   []RfPreset
}

// RfParamsResponse is the body of GET /api/rf-params.
type RfParamsResponse struct {
	Enabled   bool                `json:"enabled"`
	FetchedAt *time.Time          `json:"fetched_at"`
	Source    string              `json:"source,omitempty"`
	Presets   []RfPreset          `json:"presets"`
	Nodes     map[string]RfParams `json:"nodes"`
}

// loraBandwidthsKHz are the bandwidths SX126x/SX127x radios support.
var loraBandwidthsKHz = []float64{7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250, 500}

var pubkeyHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// rfAdvertFresh reports whether an advert dated at is no older than maxAge
// and not dated more than rfAdvertFutureSkew ahead of now.
func rfAdvertFresh(at, now time.Time, maxAge time.Duration) bool {
	return !at.After(now.Add(rfAdvertFutureSkew)) && now.Sub(at) <= maxAge
}

func validLoRa(freq float64, sf int, bw float64, cr int) bool {
	if freq < 100 || freq > 1100 || sf < 5 || sf > 12 || cr < 5 || cr > 8 {
		return false
	}
	for _, b := range loraBandwidthsKHz {
		if math.Abs(bw-b) < 0.01 {
			return true
		}
	}
	return false
}

var errRfTooLarge = errors.New("rf params payload exceeds size cap")

// capReader hands out at most max bytes and fails with errRfTooLarge when
// asked for more while the source still has data, so an oversize payload
// is refused instead of being cut into something that might still parse.
type capReader struct {
	r       io.Reader
	left    int64
	tripped bool
}

func newCapReader(r io.Reader, max int64) *capReader { return &capReader{r: r, left: max} }

func (c *capReader) Read(p []byte) (int, error) {
	if c.tripped {
		return 0, errRfTooLarge
	}
	if c.left <= 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			c.tripped = true
			return 0, errRfTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

type mapNode struct {
	PublicKey  string `json:"public_key"`
	LastAdvert string `json:"last_advert"`
	Params     *struct {
		Freq float64 `json:"freq"`
		BW   float64 `json:"bw"`
		CR   int     `json:"cr"`
		SF   int     `json:"sf"`
	} `json:"params"`
}

// parseRfNodes stream-decodes the map's node array. Entries that do not
// decode, lack params, have a malformed key, out-of-range values or an
// advert that is not fresh (rfAdvertFresh) are skipped; a payload that is
// not a well-formed array is an error.
func parseRfNodes(r io.Reader, now time.Time, maxAge time.Duration) (map[string]RfParams, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("decode map nodes: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, errors.New("decode map nodes: not a JSON array")
	}
	out := make(map[string]RfParams)
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode map node: %w", err)
		}
		var n mapNode
		if err := json.Unmarshal(raw, &n); err != nil || n.Params == nil {
			continue
		}
		pk := strings.ToLower(n.PublicKey)
		if !pubkeyHex.MatchString(pk) {
			continue
		}
		p := n.Params
		if !validLoRa(p.Freq, p.SF, p.BW, p.CR) {
			continue
		}
		at, err := time.Parse(time.RFC3339, n.LastAdvert)
		if err != nil || !rfAdvertFresh(at, now, maxAge) {
			continue
		}
		out[pk] = RfParams{Freq: p.Freq, SF: p.SF, BW: p.BW, CR: p.CR, LastAdvert: at.UTC().Format(time.RFC3339)}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("decode map nodes: %w", err)
	}
	return out, nil
}

type presetsPayload struct {
	Config struct {
		SuggestedRadioSettings struct {
			Entries []struct {
				Title           string `json:"title"`
				Frequency       string `json:"frequency"`
				SpreadingFactor string `json:"spreading_factor"`
				Bandwidth       string `json:"bandwidth"`
				CodingRate      string `json:"coding_rate"`
			} `json:"entries"`
		} `json:"suggested_radio_settings"`
	} `json:"config"`
}

// parseRfPresets reads the suggested radio settings from the MeshCore app
// config (api.meshcore.nz/api/v1/config). Invalid or untitled entries are
// dropped; entries with identical values are merged, first title first.
func parseRfPresets(r io.Reader) ([]RfPreset, error) {
	var p presetsPayload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode presets: %w", err)
	}
	out := []RfPreset{}
	index := map[string]int{}
	for _, e := range p.Config.SuggestedRadioSettings.Entries {
		title := strings.TrimSpace(e.Title)
		freq, err1 := strconv.ParseFloat(e.Frequency, 64)
		sf, err2 := strconv.Atoi(e.SpreadingFactor)
		bw, err3 := strconv.ParseFloat(e.Bandwidth, 64)
		cr, err4 := strconv.Atoi(e.CodingRate)
		if title == "" || err1 != nil || err2 != nil || err3 != nil || err4 != nil || !validLoRa(freq, sf, bw, cr) {
			continue
		}
		key := fmt.Sprintf("%.3f/%d/%g/%d", freq, sf, bw, cr)
		if i, ok := index[key]; ok {
			out[i].Title += " / " + title
			continue
		}
		index[key] = len(out)
		out = append(out, RfPreset{Title: title, Freq: freq, SF: sf, BW: bw, CR: cr})
	}
	return out, nil
}

type rfParamsCache struct {
	ptr    atomic.Pointer[rfParamsSnapshot]
	cfg    RfParamsConfig
	client *http.Client
	now    func() time.Time
	// lastLogged is only touched by the run goroutine.
	lastLogged time.Time
}

func newRfParamsCache(cfg *RfParamsConfig) *rfParamsCache {
	return &rfParamsCache{
		cfg:    *cfg,
		client: &http.Client{Timeout: 2 * time.Minute},
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (c *rfParamsCache) load() *rfParamsSnapshot { return c.ptr.Load() }

func (c *rfParamsCache) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CoreScope-RfParams/1.0 (+https://github.com/Kpa-clawbot/CoreScope)")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: upstream status %s", url, resp.Status)
	}
	return resp, nil
}

func (c *rfParamsCache) fetchNodes(ctx context.Context, now time.Time) (map[string]RfParams, error) {
	resp, err := c.get(ctx, c.cfg.NodesURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseRfNodes(newCapReader(resp.Body, maxRfNodesBytes), now, c.cfg.maxAge())
}

func (c *rfParamsCache) fetchPresets(ctx context.Context) ([]RfPreset, error) {
	resp, err := c.get(ctx, c.cfg.PresetsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseRfPresets(newCapReader(resp.Body, maxRfPresetsBytes))
}

// fetchOnce refreshes the snapshot. A failed node fetch keeps the previous
// snapshot whole; a failed preset fetch keeps the previous presets.
func (c *rfParamsCache) fetchOnce(ctx context.Context) error {
	now := c.now()
	nodes, err := c.fetchNodes(ctx, now)
	if err != nil {
		return fmt.Errorf("nodes fetch failed, keeping previous data: %w", err)
	}
	prev := c.load()
	var presets []RfPreset
	if prev != nil {
		presets = prev.Presets
	}
	var presetErr error
	if c.cfg.PresetsURL != "" {
		if p, err := c.fetchPresets(ctx); err == nil {
			presets = p
		} else {
			presetErr = fmt.Errorf("presets fetch failed, keeping previous presets: %w", err)
		}
	}
	c.ptr.Store(&rfParamsSnapshot{FetchedAt: now, Source: c.cfg.NodesURL, Nodes: nodes, Presets: presets})
	return presetErr
}

// rfNextWait is the wait before the next fetch: the refresh interval once
// there is data, otherwise a backoff after the failures-th failed attempt in
// a row, from rfRetryBase doubling up to rfRetryMax and never past refresh.
func rfNextWait(hasData bool, failures int, refresh time.Duration) time.Duration {
	if hasData || failures < 1 {
		return refresh
	}
	wait := rfRetryMax
	if failures <= 5 {
		wait = rfRetryBase << (failures - 1)
	}
	if wait > refresh {
		wait = refresh
	}
	return wait
}

// run starts the background refresh loop after a random startup delay.
// Until the first fetch succeeds it retries with a backoff (rfNextWait).
// Never blocks the caller.
func (c *rfParamsCache) run(ctx context.Context, logf func(string, ...interface{})) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(rand.Int63n(int64(rfStartupJitter)))):
		}
		failures := 0
		for {
			err := c.fetchOnce(ctx)
			if err != nil {
				logf("[rf-params] %v", err)
			}
			if c.load() == nil {
				failures++
			} else {
				failures = 0
			}
			if s := c.load(); s != nil && !s.FetchedAt.Equal(c.lastLogged) {
				c.lastLogged = s.FetchedAt
				logf("[rf-params] %d nodes within max age, %d presets", len(s.Nodes), len(s.Presets))
			}
			t := time.NewTimer(rfNextWait(c.load() != nil, failures, c.cfg.refresh()))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
}
