package main

import "github.com/gorilla/mux"

// e2eRoutes is set only by the e2etest build (auth_e2e.go).
var e2eRoutes func(s *Server, r *mux.Router)

// registerAuthRoutes adds every user-management route. Called by
// RegisterRoutes only when s.auth != nil, so with the feature off these
// paths are not registered and fall through to the SPA handler like any
// unknown path.
func (s *Server) registerAuthRoutes(r *mux.Router) {
	r.HandleFunc("/api/auth/register", s.requireOrigin(s.handleRegister)).Methods("POST")
	r.HandleFunc("/api/auth/activate", s.requireOrigin(s.handleActivate)).Methods("POST")
	r.HandleFunc("/api/auth/login", s.requireOrigin(s.handleLogin)).Methods("POST")
	r.HandleFunc("/api/auth/device-token", s.handleDeviceToken).Methods("POST")
	r.HandleFunc("/api/auth/logout", s.handleLogout).Methods("POST") // origin check inside (cookie path only)
	r.HandleFunc("/api/auth/me", s.withUser(s.handleMe)).Methods("GET")
	r.HandleFunc("/api/auth/forgot", s.requireOrigin(s.handleForgot)).Methods("POST")
	r.HandleFunc("/api/auth/reset", s.requireOrigin(s.handleReset)).Methods("POST")
	r.HandleFunc("/api/account", s.withUser(s.handleAccountPatch)).Methods("PATCH")
	r.HandleFunc("/api/account", s.withUser(s.handleAccountDelete)).Methods("DELETE")
	r.HandleFunc("/api/account/password", s.withUser(s.handleAccountPassword)).Methods("POST")
	r.HandleFunc("/api/account/email", s.withUser(s.handleAccountEmail)).Methods("POST")
	r.HandleFunc("/api/account/confirm-email", s.requireOrigin(s.handleConfirmEmail)).Methods("POST")
	r.HandleFunc("/api/account/sessions", s.withUser(s.handleAccountSessions)).Methods("GET")
	r.HandleFunc("/api/account/sessions/{id}", s.withUser(s.handleAccountSessionDelete)).Methods("DELETE")
	r.HandleFunc("/api/account/settings", s.withUser(s.handleSettingsGet)).Methods("GET")
	r.HandleFunc("/api/account/settings", s.withUser(s.handleSettingsPut)).Methods("PUT")
	r.HandleFunc("/api/account/settings", s.withUser(s.handleSettingsDelete)).Methods("DELETE")
	r.HandleFunc("/api/account/export", s.withUser(s.handleAccountExport)).Methods("GET")
	r.HandleFunc("/api/admin/users", s.withAdmin(s.handleAdminUsers)).Methods("GET")
	r.HandleFunc("/api/admin/users/{id}", s.withAdmin(s.handleAdminUserDetail)).Methods("GET")
	r.HandleFunc("/api/admin/users/{id}", s.withAdmin(s.handleAdminDelete)).Methods("DELETE")
	r.HandleFunc("/api/admin/users/{id}/disable", s.withAdmin(s.handleAdminDisable)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/enable", s.withAdmin(s.handleAdminEnable)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/role", s.withAdmin(s.handleAdminRole)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/resend-activation", s.withAdmin(s.handleAdminResendActivation)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/activate", s.withAdmin(s.handleAdminActivate)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/mail/{mailId}/refresh", s.withAdmin(s.handleAdminMailRefresh)).Methods("POST")
	r.HandleFunc("/api/admin/audit", s.withAdmin(s.handleAdminAudit)).Methods("GET")
	r.HandleFunc("/api/admin/stats", s.withAdmin(s.handleAdminStats)).Methods("GET")
	r.HandleFunc("/api/admin/users-backup", s.withAdmin(s.handleAdminUsersBackup)).Methods("GET")
	if s.auth.set.proposals.enabled {
		s.registerProposalRoutes(r)
	}
	if s.auth.notify != nil {
		s.registerNotifyRoutes(r)
	}
	// The webhook exists only when a secret is configured.
	if s.auth.set.webhookSecret != "" {
		r.HandleFunc("/api/mail/brevo/webhook", s.handleBrevoWebhook).Methods("POST")
	}
	if e2eRoutes != nil {
		e2eRoutes(s, r)
	}
}
