package main

import (
	"bytes"
	"crypto/aes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// HealthMQTTClient subscribes to the MeshCore test channel and matches
// incoming group-text packets against active health-check sessions.
type HealthMQTTClient struct {
	cfg      *HealthCheckConfig
	hdb      *HealthDB
	hub      *Hub
	client   mqtt.Client
	mu       sync.RWMutex
	sessions map[string]*HealthSession // code → session (active cache)
	done     chan struct{}
}

// NewHealthMQTTClient creates a new client. Call Start to connect.
func NewHealthMQTTClient(cfg *HealthCheckConfig, hdb *HealthDB, hub *Hub) *HealthMQTTClient {
	return &HealthMQTTClient{
		cfg:      cfg,
		hdb:      hdb,
		hub:      hub,
		sessions: make(map[string]*HealthSession),
		done:     make(chan struct{}),
	}
}

// RegisterSession adds (or refreshes) a session in the in-memory cache so
// newly-created sessions are matched immediately without waiting for the
// next expiry/refresh tick.
func (h *HealthMQTTClient) RegisterSession(sess *HealthSession) {
	h.mu.Lock()
	h.sessions[sess.Code] = sess
	h.mu.Unlock()
}

// Start connects to the MQTT broker and begins listening. It blocks until the
// initial connection succeeds, then returns; the subscription handler and the
// expiry loop continue running in background goroutines.
func (h *HealthMQTTClient) Start(brokerURL string) {
	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID("corescope-health-" + fmt.Sprintf("%d", time.Now().UnixNano())).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("[health-mqtt] connected to %s", brokerURL)
			token := c.Subscribe("meshcore/#", 0, h.handleMessage)
			token.Wait()
			if err := token.Error(); err != nil {
				log.Printf("[health-mqtt] subscribe error: %v", err)
			} else {
				log.Printf("[health-mqtt] subscribed to meshcore/#")
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("[health-mqtt] connection lost: %v — will reconnect", err)
		})

	h.client = mqtt.NewClient(opts)

	token := h.client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		log.Printf("[health-mqtt] initial connect to %s failed: %v", brokerURL, err)
	}

	// Seed the session cache.
	h.refreshSessions()

	// Background: expire stale sessions and refresh the in-memory cache every 30s.
	go h.expiryLoop()
}

// expiryLoop periodically expires stale sessions and refreshes the in-memory
// cache. It stops when Disconnect closes h.done.
func (h *HealthMQTTClient) expiryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			if err := h.hdb.ExpireStale(); err != nil {
				log.Printf("[health-mqtt] ExpireStale error: %v", err)
			}
			h.refreshSessions()
		}
	}
}

// Disconnect stops the expiry goroutine and disconnects the MQTT client.
func (h *HealthMQTTClient) Disconnect() {
	select {
	case <-h.done:
		// already closed
	default:
		close(h.done)
	}
	if h.client != nil && h.client.IsConnected() {
		h.client.Disconnect(500) // 500ms quiesce
	}
}

// refreshSessions reloads active sessions from the DB into the in-memory cache.
func (h *HealthMQTTClient) refreshSessions() {
	sessions, err := h.hdb.LoadActiveSessions()
	if err != nil {
		log.Printf("[health-mqtt] LoadActiveSessions error: %v", err)
		return
	}
	h.mu.Lock()
	h.sessions = make(map[string]*HealthSession, len(sessions))
	for _, s := range sessions {
		h.sessions[s.Code] = s
	}
	h.mu.Unlock()
}

// handleMessage is the paho subscription callback for every meshcore/# message.
func (h *HealthMQTTClient) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	observerKey := observerKeyFromTopic(topic)

	// Convert raw bytes → hex → decode.
	raw := msg.Payload()
	hexStr := hex.EncodeToString(raw)
	pkt, err := DecodePacket(hexStr, false)
	if err != nil {
		return
	}

	// Only care about GRP_TXT packets (payload type 5).
	if pkt.Header.PayloadType != PayloadGRP_TXT {
		return
	}

	// EncryptedData holds the ciphertext (hex-encoded, without the channel-hash
	// and MAC prefix bytes that decodeGrpTxt strips off).
	cipherHex := pkt.Payload.EncryptedData
	if cipherHex == "" {
		return
	}
	cipherBytes, err := hex.DecodeString(cipherHex)
	if err != nil || len(cipherBytes) == 0 {
		return
	}

	// Decrypt with AES-128-ECB using key = first 16 bytes of SHA-256(testChannelSecret).
	plaintext, err := decryptGroupText(cipherBytes, h.cfg.TestChannelSecret)
	if err != nil {
		return
	}

	// Match against active sessions.
	h.mu.RLock()
	var matched *HealthSession
	for code, sess := range h.sessions {
		if strings.Contains(plaintext, code) {
			matched = sess
			break
		}
	}
	h.mu.RUnlock()

	if matched == nil {
		return
	}

	// Build a receipt and persist it.
	msgHash := ComputeContentHash(strings.ToUpper(hexStr))
	receipt := HealthReceipt{
		SessionID:   matched.ID,
		ObserverKey: observerKey,
		MessageHash: msgHash,
		// RSSI and SNR are not present in the raw packet bytes at this layer;
		// the ingestor attaches them from the MQTT observation envelope. We
		// leave them zero here — callers that have RSSI/SNR can call
		// UpsertReceipt directly with populated values.
	}

	if err := h.hdb.UpsertReceipt(matched.ID, receipt, msgHash); err != nil {
		log.Printf("[health-mqtt] UpsertReceipt error for session %s: %v", matched.ID, err)
		return
	}

	// Re-read the updated session for status.
	updated, err := h.hdb.GetSession(matched.ID)
	if err != nil || updated == nil {
		updated = matched
	}

	// Update the in-memory cache entry.
	h.mu.Lock()
	h.sessions[matched.Code] = updated
	h.mu.Unlock()

	// Broadcast to connected WebSocket clients.
	score := ScoreSession(updated, updated.Receipts, nil)
	h.hub.Broadcast(WSMessage{
		Type: "health_receipt",
		Data: map[string]interface{}{
			"sessionId": matched.ID,
			"receipt":   receipt,
			"score":     score,
			"status":    updated.Status,
		},
	})

	log.Printf("[health-mqtt] matched session %s (code %s) via observer %s, status=%s",
		matched.ID, matched.Code, observerKey, updated.Status)
}

// decryptGroupText decrypts an AES-128-ECB ciphertext using a key derived
// from channelSecret: key = SHA-256(channelSecret)[:16].
func decryptGroupText(ciphertext []byte, channelSecret string) (string, error) {
	h256 := sha256.Sum256([]byte(channelSecret))
	key := h256[:16]
	// AES-128-ECB is mandated by the MeshCore group-text protocol; this is not a design choice.
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length %d", len(ciphertext))
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return string(bytes.TrimRight(plaintext, "\x00")), nil
}

// observerKeyFromTopic extracts the observer key from an MQTT topic of the form:
//
//	meshcore/<region>/<observerKey>/packets
//	meshcore/<observerKey>/packets
//
// The observer key is the segment immediately before the last segment.
func observerKeyFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	// Need at least "meshcore", <observerKey>, <suffix> → 3 segments.
	if len(parts) < 3 {
		return topic
	}
	return parts[len(parts)-2]
}

// firstBrokerURL returns the MQTT broker URL to use for the health check
// subscription. The server Config struct has no MQTT fields (that lives in
// the ingestor config), so we check:
//  1. MQTT_BROKER environment variable
//  2. Fall back to mqtt://localhost:1883
func firstBrokerURL(_ *Config) string {
	if v := os.Getenv("MQTT_BROKER"); v != "" {
		// Normalise mqtt:// → tcp:// for paho compatibility.
		if strings.HasPrefix(v, "mqtt://") {
			return "tcp://" + v[7:]
		}
		if strings.HasPrefix(v, "mqtts://") {
			return "ssl://" + v[8:]
		}
		return v
	}
	return "tcp://localhost:1883"
}
