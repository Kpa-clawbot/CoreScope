package main

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// mqttEnvelope is the JSON wrapper that the MeshCore→MQTT bridge publishes.
// Format: {"raw":"aabbcc...","rssi":-95.5,"snr":4.5,"hash":"abcd1234"}
// The "raw" field is the hex-encoded raw packet bytes.
type mqttEnvelope struct {
	Raw  string   `json:"raw"`
	RSSI *float64 `json:"rssi,omitempty"`
	SNR  *float64 `json:"snr,omitempty"`
	Hash string   `json:"hash,omitempty"` // optional pre-computed message hash
}

// parseEnvelope attempts to unmarshal the MQTT payload as a JSON envelope.
// If that fails it tries to treat the raw bytes directly as hex.
func parseEnvelope(payload []byte) (*mqttEnvelope, bool) {
	// Try JSON envelope first (most common format from meshcoretomqtt bridge).
	if len(payload) > 0 && payload[0] == '{' {
		var env mqttEnvelope
		if err := json.Unmarshal(payload, &env); err == nil && env.Raw != "" {
			return &env, true
		}
	}
	// Fallback: entire payload is the raw hex string.
	s := strings.TrimSpace(string(payload))
	if s != "" {
		return &mqttEnvelope{Raw: s}, true
	}
	return nil, false
}

// HealthMQTTClient subscribes to the MeshCore channel and matches incoming
// group-text packets against active health-check sessions. It also maintains
// the ObserverRegistry by processing every MQTT packet (not just health-check
// ones), so the bootstrap observer directory stays current.
//
// It connects to every source in the sources list (typically all mqttSources
// from config.json) so that packets seen by any observer on any broker are
// captured.
type HealthMQTTClient struct {
	cfg     *HealthCheckConfig
	hdb     *HealthDB
	hub     *Hub
	obs     *ObserverRegistry
	chanKey *ChannelKey // nil if secret not configured

	clientsMu sync.Mutex
	clients   []mqtt.Client
	sources   []MQTTSource // stored at Start() time for introspection

	mu         sync.Mutex
	sessions   map[string]*HealthSession // code → session (active cache)
	hashToCode map[string]string         // messageHash → session code (dedup)
	done       chan struct{}
}

// NewHealthMQTTClient creates a client. Call Start to connect.
// obs is the shared ObserverRegistry; chanKey may be nil if secret is absent.
func NewHealthMQTTClient(cfg *HealthCheckConfig, hdb *HealthDB, hub *Hub, obs *ObserverRegistry, chanKey *ChannelKey) *HealthMQTTClient {
	return &HealthMQTTClient{
		cfg:        cfg,
		hdb:        hdb,
		hub:        hub,
		obs:        obs,
		chanKey:    chanKey,
		sessions:   make(map[string]*HealthSession),
		hashToCode: make(map[string]string),
		done:       make(chan struct{}),
	}
}

// IsAnyConnected returns true if at least one broker connection is established.
func (h *HealthMQTTClient) IsAnyConnected() bool {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	for _, c := range h.clients {
		if c.IsConnected() {
			return true
		}
	}
	return false
}

// BrokerURLs returns the broker URL of every configured source (in order).
func (h *HealthMQTTClient) BrokerURLs() []string {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	urls := make([]string, 0, len(h.sources))
	for _, src := range h.sources {
		urls = append(urls, src.Broker)
	}
	return urls
}

// RegisterSession adds (or refreshes) a session in the cache immediately after
// creation so packets can be matched before the next 30 s refresh tick.
func (h *HealthMQTTClient) RegisterSession(sess *HealthSession) {
	h.mu.Lock()
	h.sessions[sess.Code] = sess
	h.mu.Unlock()
}

// Start connects to every source in sources and starts subscriptions. It spawns
// one goroutine per source, waits for each initial connect attempt, then returns.
// Pass a single-element slice for backward-compatible single-broker operation.
func (h *HealthMQTTClient) Start(sources []MQTTSource) {
	h.clientsMu.Lock()
	h.sources = sources
	h.clientsMu.Unlock()

	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(s MQTTSource) {
			defer wg.Done()
			h.connectSource(s)
		}(src)
	}
	wg.Wait()

	h.refreshSessions()
	go h.expiryLoop()
}

// connectSource creates an MQTT client for src, registers it, and connects.
// It blocks until the initial connect attempt completes (success or failure).
func (h *HealthMQTTClient) connectSource(src MQTTSource) {
	topics := src.Topics
	if len(topics) == 0 {
		topics = []string{"meshcore/#"}
	}
	brokerURL := src.Broker
	name := src.Name
	if name == "" {
		name = brokerURL
	}

	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("corescope-health-%s-%d", name, time.Now().UnixNano())).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("[health-mqtt] connected to %s (%s)", brokerURL, name)
			for _, topic := range topics {
				tok := c.Subscribe(topic, 0, h.handleMessage)
				tok.Wait()
				if err := tok.Error(); err != nil {
					log.Printf("[health-mqtt] subscribe error topic=%s source=%s: %v", topic, name, err)
				} else {
					log.Printf("[health-mqtt] subscribed to %s on %s", topic, name)
				}
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("[health-mqtt] connection lost to %s (%s): %v — will reconnect", brokerURL, name, err)
		})

	if src.Username != "" {
		opts.SetUsername(src.Username)
	}
	if src.Password != "" {
		opts.SetPassword(src.Password)
	}
	// When rejectUnauthorized is explicitly false, skip TLS certificate verification.
	// This mirrors the ingestor behaviour and lets self-signed certs work.
	if src.RejectUnauthorized != nil && !*src.RejectUnauthorized {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // operator opt-in
	}

	client := mqtt.NewClient(opts)

	h.clientsMu.Lock()
	h.clients = append(h.clients, client)
	h.clientsMu.Unlock()

	log.Printf("[health-mqtt] connecting to %s (%s)", brokerURL, name)
	tok := client.Connect()
	tok.Wait()
	if err := tok.Error(); err != nil {
		log.Printf("[health-mqtt] initial connect to %s (%s) failed: %v — will keep retrying", brokerURL, name, err)
	}
}

// Disconnect stops background goroutines and disconnects all MQTT clients.
func (h *HealthMQTTClient) Disconnect() {
	select {
	case <-h.done:
	default:
		close(h.done)
	}
	h.clientsMu.Lock()
	clients := make([]mqtt.Client, len(h.clients))
	copy(clients, h.clients)
	h.clientsMu.Unlock()
	for _, c := range clients {
		if c != nil && c.IsConnected() {
			c.Disconnect(500)
		}
	}
}

// expiryLoop periodically expires stale sessions and refreshes the cache.
func (h *HealthMQTTClient) expiryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			if err := h.hdb.ExpireStale(); err != nil {
				log.Printf("[health-mqtt] ExpireStale: %v", err)
			}
			h.refreshSessions()
		}
	}
}

func (h *HealthMQTTClient) refreshSessions() {
	sessions, err := h.hdb.LoadActiveSessions()
	if err != nil {
		log.Printf("[health-mqtt] LoadActiveSessions: %v", err)
		return
	}
	h.mu.Lock()
	h.sessions = make(map[string]*HealthSession, len(sessions))
	for _, s := range sessions {
		h.sessions[s.Code] = s
	}
	h.mu.Unlock()
}

// handleMessage is the paho callback for every meshcore/# MQTT message.
func (h *HealthMQTTClient) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	observerKey := observerKeyFromTopic(topic)

	env, ok := parseEnvelope(msg.Payload())
	if !ok || env.Raw == "" {
		// No usable packet — may be a pure-JSON metadata message.
		h.handleMetadataMessage(topic, observerKey, msg.Payload())
		return
	}

	// Touch the observer (creates record if first time seen).
	h.obs.Touch(observerKey)

	rawHex := strings.TrimSpace(env.Raw)

	// Decode packet using the existing CoreScope decoder.
	pkt, err := DecodePacket(rawHex, false)
	if err != nil {
		return
	}

	// Learn observer location/name from packet if it carries a public key
	// matching the observer (ADVERT, etc.).  We do this regardless of payload
	// type so the directory stays up to date.
	h.learnFromPacket(pkt, observerKey)

	// Only group-text packets (payload type 5) carry health-check codes.
	if pkt.Header.PayloadType != PayloadGRP_TXT {
		return
	}

	log.Printf("[health-mqtt] GRP_TXT from observer=%s channelHash=%d mac=%s encDataLen=%d chanKeyConfigured=%v",
		observerShortKey(observerKey), pkt.Payload.ChannelHash, pkt.Payload.MAC,
		len(pkt.Payload.EncryptedData)/2, h.chanKey != nil)

	// Channel hash filter — only process messages for our test channel.
	if h.chanKey != nil && !h.chanKey.MatchesChannelHash(pkt.Payload.ChannelHash) {
		log.Printf("[health-mqtt] GRP_TXT dropped: channelHash=%d does not match test channel (expected %d) — check testChannelSecret config",
			pkt.Payload.ChannelHash, h.chanKey.ChannelHashByte)
		return
	}

	// Need ciphertext to proceed.
	if pkt.Payload.EncryptedData == "" {
		log.Printf("[health-mqtt] GRP_TXT dropped: no encrypted data in packet")
		return
	}
	cipherBytes, err := hex.DecodeString(pkt.Payload.EncryptedData)
	if err != nil || len(cipherBytes) == 0 {
		log.Printf("[health-mqtt] GRP_TXT dropped: could not decode encrypted data: %v", err)
		return
	}

	// HMAC validation — reject corrupted / wrong-channel packets.
	if h.chanKey != nil && !h.chanKey.ValidateMAC(pkt.Payload.MAC, cipherBytes) {
		log.Printf("[health-mqtt] GRP_TXT dropped: MAC validation failed (mac=%s) — wrong testChannelSecret or corrupted packet",
			pkt.Payload.MAC)
		return
	}

	// Decrypt.
	if h.chanKey == nil {
		log.Printf("[health-mqtt] GRP_TXT dropped: testChannelSecret not configured — cannot decrypt; set testChannelSecret in health check config")
		return
	}
	sender, messageBody, valid := h.chanKey.ParseGroupTextMessage(cipherBytes)
	if !valid || messageBody == "" {
		log.Printf("[health-mqtt] GRP_TXT dropped: decryption/parsing failed (valid=%v messageBody=%q) — check testChannelSecret matches the channel used",
			valid, messageBody)
		return
	}

	log.Printf("[health-mqtt] GRP_TXT decrypted: observer=%s sender=%q body=%q",
		observerShortKey(observerKey), sender, messageBody)

	// Determine message hash (for deduplication and re-use detection).
	msgHash := env.Hash
	if msgHash == "" {
		msgHash = ComputeContentHash(strings.ToUpper(rawHex))
	}

	// Signal metrics from the JSON envelope (zero when absent).
	var rssi, snr float64
	if env.RSSI != nil {
		rssi = *env.RSSI
	}
	if env.SNR != nil {
		snr = *env.SNR
	}

	// Extract relay path from the decoded packet.
	var path []string
	if len(pkt.Path.Hops) > 0 {
		path = append(path, pkt.Path.Hops...)
	}

	h.matchAndRecord(observerKey, messageBody, sender, msgHash, rssi, snr, path)
}

// handleMetadataMessage processes observer status / metadata JSON messages
// (those without a "raw" packet field) to learn names and coordinates.
func (h *HealthMQTTClient) handleMetadataMessage(topic, observerKey string, payload []byte) {
	var meta map[string]interface{}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return
	}

	// Name fields used by various MeshCore bridges.
	for _, field := range []string{"name", "device_name", "deviceName", "node_name", "nodeName", "callsign", "label"} {
		if v, ok := meta[field].(string); ok && strings.TrimSpace(v) != "" {
			h.obs.UpdateName(observerKey, strings.TrimSpace(v))
			break
		}
	}

	// Coordinates — try direct fields then nested objects.
	lat, lon, found := extractCoordinates(meta)
	if found {
		h.obs.UpdateLocation(observerKey, lat, lon)
	}

	_ = topic // reserved for future IATA/region extraction
}

// learnFromPacket extracts name/coordinate hints from decoded ADVERT packets.
func (h *HealthMQTTClient) learnFromPacket(pkt *DecodedPacket, observerKey string) {
	if pkt == nil || pkt.Payload.Type != "ADVERT" {
		return
	}
	if pkt.Payload.Name != "" {
		h.obs.UpdateName(observerKey, pkt.Payload.Name)
	}
}

// matchAndRecord finds the session matching messageBody and upserts a receipt.
func (h *HealthMQTTClient) matchAndRecord(observerKey, messageBody, sender, msgHash string, rssi, snr float64, path []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Fast path: already know which session this hash belongs to.
	var matched *HealthSession
	if msgHash != "" {
		if code, ok := h.hashToCode[msgHash]; ok {
			matched = h.sessions[code]
		}
	}

	// Slow path: scan session codes against message body.
	if matched == nil {
		for code, sess := range h.sessions {
			if matchesCode(messageBody, code) {
				matched = sess
				break
			}
		}
		if matched == nil {
			if len(h.sessions) == 0 {
				log.Printf("[health-mqtt] GRP_TXT body=%q: no active sessions to match against", messageBody)
			} else {
				codes := make([]string, 0, len(h.sessions))
				for c := range h.sessions {
					codes = append(codes, c)
				}
				log.Printf("[health-mqtt] GRP_TXT body=%q: no session code match (active sessions: %v)", messageBody, codes)
			}
			return
		}

		// Determine if this is a new use or same transmission.
		isNewUse := matched.MessageHash != "" && matched.MessageHash != msgHash
		if isNewUse {
			// New broadcast of the same code — reset receipts, increment use.
			if err := h.hdb.ClearReceipts(matched.ID); err != nil {
				log.Printf("[health-mqtt] ClearReceipts: %v", err)
			}
			if err := h.hdb.IncrementUseCount(matched.ID); err != nil {
				log.Printf("[health-mqtt] IncrementUseCount: %v", err)
			}
			// Remove stale hash mappings for this session.
			for h2, c := range h.hashToCode {
				if c == matched.Code {
					delete(h.hashToCode, h2)
				}
			}
		}

		// Link this hash to the session.
		if msgHash != "" {
			h.hashToCode[msgHash] = matched.Code
		}
		if matched.MessageHash == "" || isNewUse {
			if err := h.hdb.SetMessageHash(matched.ID, msgHash, sender); err != nil {
				log.Printf("[health-mqtt] SetMessageHash: %v", err)
			}
			matched.MessageHash = msgHash
			matched.Sender = sender
		}
	}

	if matched == nil {
		return
	}

	receipt := HealthReceipt{
		SessionID:   matched.ID,
		ObserverKey: observerKey,
		ObserverName: h.obs.Label(observerKey),
		MessageHash: msgHash,
		RSSI:        rssi,
		SNR:         snr,
		Path:        path,
	}

	if err := h.hdb.UpsertReceipt(matched.ID, receipt, msgHash); err != nil {
		log.Printf("[health-mqtt] UpsertReceipt: %v", err)
		return
	}

	// Re-read for accurate status and receipts.
	updated, err := h.hdb.GetSession(matched.ID)
	if err != nil || updated == nil {
		updated = matched
	}
	h.sessions[matched.Code] = updated

	// Broadcast update with current score.
	score := ScoreSession(updated, updated.Receipts, h.obs.ActiveKeys())
	h.hub.Broadcast(WSMessage{
		Type: "health_receipt",
		Data: map[string]interface{}{
			"sessionId": matched.ID,
			"receipt":   receipt,
			"score":     score,
			"status":    updated.Status,
		},
	})

	log.Printf("[health-mqtt] session %s (%s) receipt from %s hash=%s status=%s",
		matched.Code, matched.ID[:8], observerShortKey(observerKey), msgHash, updated.Status)
}

// matchesCode returns true when body contains the session code as a whole word.
func matchesCode(body, code string) bool {
	upper := strings.ToUpper(body)
	code = strings.ToUpper(code)
	idx := strings.Index(upper, code)
	if idx < 0 {
		return false
	}
	// Ensure it's a word boundary (not surrounded by alphanumeric).
	before := idx > 0 && isAlphanumeric(upper[idx-1])
	after := idx+len(code) < len(upper) && isAlphanumeric(upper[idx+len(code)])
	return !before && !after
}

func isAlphanumeric(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// extractCoordinates tries to find lat/lon from a generic JSON map.
func extractCoordinates(m map[string]interface{}) (lat, lon float64, ok bool) {
	lat, ok1 := toCoord(m, "lat", "latitude")
	lon, ok2 := toCoord(m, "lon", "lng", "longitude")
	if ok1 && ok2 && !(lat == 0 && lon == 0) {
		return lat, lon, true
	}
	// Recurse into nested objects one level deep.
	for _, v := range m {
		if nested, ok := v.(map[string]interface{}); ok {
			if lat, lon, found := extractCoordinates(nested); found {
				return lat, lon, true
			}
		}
	}
	return 0, 0, false
}

func toCoord(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch fv := v.(type) {
			case float64:
				return fv, true
			case int:
				return float64(fv), true
			}
		}
	}
	return 0, false
}

// observerKeyFromTopic extracts the observer public key from an MQTT topic.
// Expected formats:
//
//	meshcore/<region>/<observerKey>/packets
//	meshcore/<observerKey>/packets
//
// The observer key is the segment immediately before the last path segment.
func observerKeyFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return topic
	}
	return parts[len(parts)-2]
}

// firstBrokerURL returns the MQTT broker URL for the health subscription.
// Priority:
//  1. hcfg.MQTTBroker (explicit config — recommended for wss:// brokers)
//  2. MQTT_BROKER environment variable
//  3. tcp://localhost:1883 fallback
//
// Supported schemes: tcp://, ssl://, ws://, wss://, mqtt://, mqtts://.
// Paho MQTT natively handles ws:// and wss:// (WebSocket), so they are passed
// through unchanged. mqtt:// and mqtts:// are normalised to tcp:// and ssl://.
func firstBrokerURL(hcfg *HealthCheckConfig) string {
	url := ""
	if hcfg != nil && hcfg.MQTTBroker != "" {
		url = hcfg.MQTTBroker
	} else if v := os.Getenv("MQTT_BROKER"); v != "" {
		url = v
	}
	if url == "" {
		return "tcp://localhost:1883"
	}
	return normaliseBrokerURL(url)
}

// normaliseBrokerURL converts mqtt:// → tcp:// and mqtts:// → ssl://.
// ws:// and wss:// are passed through as-is (paho supports WebSocket natively).
func normaliseBrokerURL(url string) string {
	switch {
	case strings.HasPrefix(url, "mqtt://"):
		return "tcp://" + url[7:]
	case strings.HasPrefix(url, "mqtts://"):
		return "ssl://" + url[8:]
	default:
		return url // tcp://, ssl://, ws://, wss:// — all valid for paho
	}
}
