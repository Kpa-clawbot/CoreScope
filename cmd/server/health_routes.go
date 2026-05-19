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

func (s *Server) registerHealthRoutes(r *mux.Router) {
	r.HandleFunc("/api/health/bootstrap", s.handleHealthBootstrap).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/health/verify-turnstile", s.handleHealthVerifyTurnstile).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/health/sessions", s.handleHealthCreateSession).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/health/sessions/{id}", s.handleHealthGetSession).Methods("GET", "OPTIONS")
	r.HandleFunc("/share/{id}", s.handleHealthShare).Methods("GET")
}

// GET /api/health/bootstrap
func (s *Server) handleHealthBootstrap(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || s.cfg.HealthCheck == nil {
		http.Error(w, "health check not available", http.StatusServiceUnavailable)
		return
	}

	type observerEntry struct {
		Key    string  `json:"key"`
		Name   string  `json:"name,omitempty"`
		Lat    float64 `json:"lat,omitempty"`
		Lon    float64 `json:"lon,omitempty"`
		Region string  `json:"region,omitempty"`
	}

	var observers []observerEntry
	if s.db != nil {
		dbObservers, err := s.db.GetObservers()
		if err == nil && len(dbObservers) > 0 {
			// Batch lookup of lat/lon via node locations table
			ids := make([]string, len(dbObservers))
			for i, o := range dbObservers {
				ids[i] = o.ID
			}
			nodeLocations := s.db.GetNodeLocationsByKeys(ids)

			for _, o := range dbObservers {
				if s.cfg != nil && s.cfg.IsObserverBlacklisted(o.ID) {
					continue
				}
				var lat, lon float64
				if nodeLoc, ok := nodeLocations[strings.ToLower(o.ID)]; ok {
					if v, ok := nodeLoc["lat"]; ok && v != nil {
						if f, ok := v.(float64); ok {
							lat = f
						}
					}
					if v, ok := nodeLoc["lon"]; ok && v != nil {
						if f, ok := v.(float64); ok {
							lon = f
						}
					}
				}
				// Only include observers that have coordinates
				if lat == 0 && lon == 0 {
					continue
				}
				name := ""
				if o.Name != nil {
					name = *o.Name
				}
				region := ""
				if o.IATA != nil {
					region = *o.IATA
				}
				observers = append(observers, observerEntry{
					Key:    o.ID,
					Name:   name,
					Lat:    lat,
					Lon:    lon,
					Region: region,
				})
			}
		}
	}
	if observers == nil {
		observers = []observerEntry{}
	}

	type bootstrapResp struct {
		Observers           []observerEntry        `json:"observers"`
		Turnstile           map[string]interface{} `json:"turnstile"`
		ActiveWindowSeconds int                    `json:"activeWindowSeconds"`
		TestChannelName     string                 `json:"testChannelName"`
	}

	resp := bootstrapResp{
		Observers: observers,
		Turnstile: map[string]interface{}{
			"enabled": s.cfg.HealthCheck.Turnstile.Enabled,
			"siteKey": s.cfg.HealthCheck.Turnstile.SiteKey,
		},
		ActiveWindowSeconds: s.cfg.HealthCheck.ObserverRetentionSeconds,
		TestChannelName:     s.cfg.HealthCheck.TestChannelName,
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

	sess, err := s.healthDB.CreateSession(s.cfg.HealthCheck, body.AllowlistEnabled, body.ExpectedObserverKeys)
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
