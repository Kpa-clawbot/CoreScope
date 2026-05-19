package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
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
// Returns the observer directory (built from live MQTT traffic), Turnstile
// config, MQTT status, and default observer target — matching the source
// project's /api/bootstrap response shape.
func (s *Server) handleHealthBootstrap(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || s.cfg.HealthCheck == nil {
		http.Error(w, "health check not available", http.StatusServiceUnavailable)
		return
	}
	hcfg := s.cfg.HealthCheck

	// Build serialised observer directory from the live registry.
	var observerDirectory []map[string]interface{}
	var activeObservers []map[string]interface{}
	var defaultObserverKeys []string

	if s.healthObs != nil {
		recs := s.healthObs.Directory()
		observerDirectory = make([]map[string]interface{}, 0, len(recs))
		for _, rec := range recs {
			entry := s.healthObs.SerializeObserver(rec)
			observerDirectory = append(observerDirectory, entry)
			if rec.IsActive(int64(hcfg.ObserverActiveWindowSeconds) * 1000) {
				activeObservers = append(activeObservers, entry)
			}
		}

		// Default scoring target: KnownObservers list if configured, else active.
		if len(hcfg.KnownObservers) > 0 {
			defaultObserverKeys = hcfg.KnownObservers
		} else {
			defaultObserverKeys = s.healthObs.ActiveKeys()
		}
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
		},
		"turnstile": map[string]interface{}{
			"enabled": hcfg.Turnstile.Enabled,
			"siteKey": hcfg.Turnstile.SiteKey,
		},
		"testChannel": map[string]interface{}{
			"name":       hcfg.TestChannelName,
			"codePrefix": effectiveCodePrefix(hcfg),
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
