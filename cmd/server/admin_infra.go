package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/meshcore-analyzer/infraqueue"
)

type setInfrastructureRequest struct {
	Pubkey         string `json:"pubkey"`
	Infrastructure bool   `json:"infrastructure"`
}

// handleSetInfrastructureFlag enqueues a request for the ingestor (which
// holds the only writable DB handle) to set/clear a node's
// `infrastructure` flag. See internal/infraqueue for the request/result
// marker-file protocol this uses — the same pattern already used by
// handlePruneGeoFilter for the same reason (server opens SQLite mode=ro).
func (s *Server) handleSetInfrastructureFlag(w http.ResponseWriter, r *http.Request) {
	var req setInfrastructureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pubkey := strings.ToLower(strings.TrimSpace(req.Pubkey))
	if !isHexPubkey(pubkey) {
		// isHexPubkey (node_reach.go) requires a full 64-char lowercase-hex
		// key — the admin UI always supplies an exact key pulled from
		// /api/nodes/search or /api/nodes/infrastructure results, so this
		// rejects garbage input, not legitimate prefixes.
		writeError(w, http.StatusBadRequest, "pubkey must be a full 64-char hex public key")
		return
	}

	var actorID *int64
	actorUsername := ""
	if actor := adminFromContext(r.Context()); actor != nil {
		actorID = &actor.ID
		actorUsername = actor.Username
	}

	id := infraqueue.NewID()
	q := infraqueue.Request{
		ID:             id,
		RequestedAt:    time.Now().UTC(),
		PublicKey:      pubkey,
		Infrastructure: req.Infrastructure,
		ActorAdminID:   actorID,
		ActorUsername:  actorUsername,
	}
	if err := infraqueue.WriteRequest(s.db.path, q); err != nil {
		log.Printf("[infra-queue] failed to enqueue request %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue request")
		return
	}
	log.Printf("[infra-queue] enqueued request %s: pubkey=%s infrastructure=%v actor=%s",
		id, pubkey, req.Infrastructure, actorUsername)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted":  true,
		"requestId": id,
		"statusUrl": "/api/admin/nodes/infrastructure/status?id=" + id,
	})
}

// handleInfrastructureFlagStatus reports the state of a previously
// enqueued infra-flag request. Mirrors handlePruneGeoFilterStatus.
func (s *Server) handleInfrastructureFlagStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	res, err := infraqueue.ReadResult(s.db.path, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid infra request id") {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		writeError(w, http.StatusInternalServerError, "status read failed")
		return
	}
	if res != nil {
		status := "done"
		if res.Error != "" {
			status = "error"
		}
		writeJSON(w, map[string]interface{}{
			"requestId":   res.ID,
			"status":      status,
			"applied":     res.Applied,
			"requestedAt": res.RequestedAt,
			"completedAt": res.CompletedAt,
			"error":       res.Error,
		})
		return
	}

	pending, err := infraqueue.RequestExists(s.db.path, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid infra request id") {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		writeError(w, http.StatusInternalServerError, "status read failed")
		return
	}
	if pending {
		writeJSON(w, map[string]interface{}{
			"requestId": id,
			"status":    "pending",
		})
		return
	}
	writeError(w, http.StatusNotFound, "unknown request id")
}
