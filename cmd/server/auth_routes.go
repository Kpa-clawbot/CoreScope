package main

import "github.com/gorilla/mux"

// e2eRoutes is set only by the e2etest build (auth_e2e.go).
var e2eRoutes func(s *Server, r *mux.Router)

// registerAuthRoutes adds every user-management route. Called by
// RegisterRoutes only when s.auth != nil, so with the feature off these
// paths are plain 404s.
func (s *Server) registerAuthRoutes(r *mux.Router) {
	// Tasks 4–7 add routes here.
	if e2eRoutes != nil {
		e2eRoutes(s, r)
	}
}
