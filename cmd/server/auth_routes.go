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
	r.HandleFunc("/api/account", s.withUser(s.handleAccountPatch)).Methods("PATCH")
	r.HandleFunc("/api/account", s.withUser(s.handleAccountDelete)).Methods("DELETE")
	r.HandleFunc("/api/account/password", s.withUser(s.handleAccountPassword)).Methods("POST")
	r.HandleFunc("/api/account/email", s.withUser(s.handleAccountEmail)).Methods("POST")
	r.HandleFunc("/api/account/confirm-email", s.requireOrigin(s.handleConfirmEmail)).Methods("POST")
	r.HandleFunc("/api/account/sessions", s.withUser(s.handleAccountSessions)).Methods("GET")
	r.HandleFunc("/api/account/sessions/{id}", s.withUser(s.handleAccountSessionDelete)).Methods("DELETE")
	// Tasks 6–7 add routes here.
	if e2eRoutes != nil {
		e2eRoutes(s, r)
	}
}
