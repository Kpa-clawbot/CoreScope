package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// effectiveCodePrefix returns the code prefix that will be used for new sessions,
// applying the same "MHC" fallback logic as CreateSession.
func effectiveCodePrefix(cfg *HealthCheckConfig) string {
	if cfg.CodePrefix == "" {
		return "MHC"
	}
	return cfg.CodePrefix
}

func (s *Server) registerHealthRoutes(r *mux.Router) {
	r.HandleFunc("/api/health/bootstrap", s.handleHealthBootstrap).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/health/verify-turnstile", s.handleHealthVerifyTurnstile).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/health/sessions", s.handleHealthCreateSession).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/health/sessions/{id}", s.handleHealthGetSession).Methods("GET", "OPTIONS")
	r.HandleFunc("/share/{id}", s.handleHealthShare).Methods("GET")
}

// GET /api/health/bootstrap
// Returns the observer directory, Turnstile config, MQTT status, and default
// observer target. Observer data is sourced from the main CoreScope DB (names,
// coordinates, regions) and enriched with live-MQTT activity from the registry.
func (s *Server) handleHealthBootstrap(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || s.cfg.HealthCheck == nil {
		http.Error(w, "health check not available", http.StatusServiceUnavailable)
		return
	}
	hcfg := s.cfg.HealthCheck
	activeWindowMs := int64(hcfg.ObserverActiveWindowSeconds) * 1000

	var observerDirectory []map[string]interface{}
	var activeObservers []map[string]interface{}
	var defaultObserverKeys []string

	// Primary source: the CoreScope observer table — already has names, IATA
	// regions, and coordinates ingested from packet traffic.
	if s.db != nil {
		dbObservers, err := s.db.GetObservers()
		if err == nil && len(dbObservers) > 0 {
			ids := make([]string, len(dbObservers))
			for i, o := range dbObservers {
				ids[i] = o.ID
			}
			nodeLocations := s.db.GetNodeLocationsByKeys(ids)

			observerDirectory = make([]map[string]interface{}, 0, len(dbObservers))
			for _, o := range dbObservers {
				if s.cfg != nil && s.cfg.IsObserverBlacklisted(o.ID) {
					continue
				}
				var lat, lon *float64
				if nodeLoc, ok := nodeLocations[strings.ToLower(o.ID)]; ok {
					if v, _ := nodeLoc["lat"]; v != nil {
						if f, ok := v.(float64); ok {
							lat = &f
						}
					}
					if v, _ := nodeLoc["lon"]; v != nil {
						if f, ok := v.(float64); ok {
							lon = &f
						}
					}
				}
				name := ""
				if o.Name != nil {
					name = *o.Name
				}
				region := ""
				if o.IATA != nil {
					region = *o.IATA
				}
				hasLocation := lat != nil && lon != nil
				shortKey := observerShortKey(o.ID)

				// isActive: prefer registry (health-check MQTT activity); fall
				// back to CoreScope last_seen within the active window.
				isActive := false
				if s.healthObs != nil {
					rec := s.healthObs.Get(o.ID)
					if rec != nil {
						isActive = rec.IsActive(activeWindowMs)
					}
				}

				entry := map[string]interface{}{
					"key":         o.ID,
					"shortKey":    shortKey,
					"name":        name,
					"label":       name,
					"region":      region,
					"hasLocation": hasLocation,
					"lat":         nil,
					"lon":         nil,
					"packetCount": o.PacketCount,
					"isActive":    isActive,
				}
				if hasLocation {
					entry["lat"] = *lat
					entry["lon"] = *lon
				}
				if name == "" {
					entry["label"] = shortKey
				}
				observerDirectory = append(observerDirectory, entry)
				if isActive {
					activeObservers = append(activeObservers, entry)
				}
			}
		}
	}

	// Fallback / supplement: observers from the live registry that are not yet
	// in the DB (e.g. KnownObservers that haven't sent a packet through the
	// ingestor yet, or health-check-only deployments without a DB).
	if s.healthObs != nil {
		dbKeys := make(map[string]bool, len(observerDirectory))
		for _, e := range observerDirectory {
			if k, ok := e["key"].(string); ok {
				dbKeys[strings.ToLower(k)] = true
			}
		}
		for _, rec := range s.healthObs.Directory() {
			if dbKeys[strings.ToLower(rec.Key)] {
				continue // already present from DB
			}
			entry := s.healthObs.SerializeObserver(rec)
			observerDirectory = append(observerDirectory, entry)
			if rec.IsActive(activeWindowMs) {
				activeObservers = append(activeObservers, entry)
			}
		}
	}

	// Default scoring target: KnownObservers if configured, else active keys.
	if len(hcfg.KnownObservers) > 0 {
		defaultObserverKeys = hcfg.KnownObservers
	} else if s.healthObs != nil {
		defaultObserverKeys = s.healthObs.ActiveKeys()
	}

	if observerDirectory == nil {
		observerDirectory = []map[string]interface{}{}
	}
	if activeObservers == nil {
		activeObservers = []map[string]interface{}{}
	}
	if defaultObserverKeys == nil {
		defaultObserverKeys = []string{}
	}

	mqttConnected := s.healthMQTT != nil && s.healthMQTT.client != nil && s.healthMQTT.client.IsConnected()
	decryptionConfigured := s.healthMQTT != nil && s.healthMQTT.chanKey != nil

	resp := map[string]interface{}{
		"observerDirectory":   observerDirectory,
		"activeObservers":     activeObservers,
		"defaultObserverKeys": defaultObserverKeys,
		"observerStats": map[string]interface{}{
			"configuredCount":  len(hcfg.KnownObservers),
			"activeCount":      len(activeObservers),
			"windowSeconds":    hcfg.ObserverActiveWindowSeconds,
			"retentionSeconds": hcfg.ObserverRetentionSeconds,
		},
		"mqtt": map[string]interface{}{
			"connected": mqttConnected,
			"broker":    firstBrokerURL(hcfg),
		},
		"turnstile": map[string]interface{}{
			"enabled": hcfg.Turnstile.Enabled,
			"siteKey": hcfg.Turnstile.SiteKey,
		},
		"testChannel": map[string]interface{}{
			"name":                 hcfg.TestChannelName,
			"codePrefix":           effectiveCodePrefix(hcfg),
			"decryptionConfigured": decryptionConfigured,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// POST /api/health/verify-turnstile
func (s *Server) handleHealthVerifyTurnstile(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || s.cfg.HealthCheck == nil {
		http.Error(w, "health check not available", http.StatusServiceUnavailable)
		return
	}

	if !s.cfg.HealthCheck.Turnstile.Enabled {
		http.Error(w, "turnstile not enabled", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	remoteIP := clientIP(r)
	if err := verifyTurnstileToken(s.cfg.HealthCheck.Turnstile.SecretKey, body.Token, remoteIP); err != nil {
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}

	token, err := generateAuthToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	expiry := time.Now().Add(time.Hour)
	s.healthTokens.Add(token, expiry)

	http.SetCookie(w, &http.Cookie{
		Name:     "hc_auth",
		Value:    token,
		Expires:  expiry,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// POST /api/health/sessions
func (s *Server) handleHealthCreateSession(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || s.cfg.HealthCheck == nil || s.healthDB == nil || s.healthRL == nil {
		http.Error(w, "health check not available", http.StatusServiceUnavailable)
		return
	}

	if s.cfg.HealthCheck.Turnstile.Enabled {
		cookie, err := r.Cookie("hc_auth")
		if err != nil || !s.healthTokens.Valid(cookie.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	remoteIP := clientIP(r)
	if !s.healthRL.Allow(remoteIP) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)

	var body struct {
		AllowlistEnabled     bool     `json:"allowlistEnabled"`
		ExpectedObserverKeys []string `json:"expectedObserverKeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	const maxExpectedObservers = 200
	if len(body.ExpectedObserverKeys) > maxExpectedObservers {
		http.Error(w, "too many expected observers", http.StatusBadRequest)
		return
	}

	// When no explicit allowlist is provided, snapshot the current active
	// observers as the expected set (same behaviour as the source project).
	expectedKeys := body.ExpectedObserverKeys
	allowlist := body.AllowlistEnabled
	if !allowlist && len(expectedKeys) == 0 && s.healthObs != nil {
		if len(s.cfg.HealthCheck.KnownObservers) > 0 {
			expectedKeys = s.cfg.HealthCheck.KnownObservers
		} else {
			expectedKeys = s.healthObs.ActiveKeys()
		}
	}

	sess, err := s.healthDB.CreateSession(s.cfg.HealthCheck, allowlist, expectedKeys)
	if err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	if s.healthMQTT != nil {
		s.healthMQTT.RegisterSession(sess)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(sess)
}

// GET /api/health/sessions/{id}
func (s *Server) handleHealthGetSession(w http.ResponseWriter, r *http.Request) {
	if s.healthDB == nil {
		http.Error(w, "health check not available", http.StatusServiceUnavailable)
		return
	}

	id := mux.Vars(r)["id"]
	sess, err := s.healthDB.GetSession(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if time.Now().Unix() > sess.ResultRetainedUntil {
		http.Error(w, "results expired", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sess)
}

// GET /share/{id}
func (s *Server) handleHealthShare(w http.ResponseWriter, r *http.Request) {
	sharePath := "public/share.html"
	if s.publicDir != "" {
		sharePath = filepath.Join(s.publicDir, "share.html")
	}
	http.ServeFile(w, r, sharePath)
}
