package main

import "github.com/gorilla/mux"

// e2eRoutes is set only by the e2etest build (auth_e2e.go).
var e2eRoutes func(s *Server, r *mux.Router)

// registerAuthRoutes adds every user-management route. Called by
// RegisterRoutes only when s.auth != nil, so with the feature off these
// paths are plain 404s.
func (s *Server) registerAuthRoutes(r *mux.Router) {
	r.HandleFunc("/api/auth/register", s.requireOrigin(s.handleRegister)).Methods("POST")
	r.HandleFunc("/api/auth/activate", s.requireOrigin(s.handleActivate)).Methods("POST")
	r.HandleFunc("/api/auth/login", s.requireOrigin(s.handleLogin)).Methods("POST")
	r.HandleFunc("/api/auth/logout", s.requireOrigin(s.handleLogout)).Methods("POST")
	r.HandleFunc("/api/auth/me", s.handleMe).Methods("GET")
	r.HandleFunc("/api/auth/forgot", s.requireOrigin(s.handleForgot)).Methods("POST")
	r.HandleFunc("/api/auth/reset", s.requireOrigin(s.handleReset)).Methods("POST")
	// Tasks 5–7 add routes here.
	if e2eRoutes != nil {
		e2eRoutes(s, r)
	}
}
