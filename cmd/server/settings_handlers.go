package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/meshcore-analyzer/users"
)

// settingsBodyMax leaves room for the request envelope around a document
// at the cap.
const settingsBodyMax = settingsDocMaxBytes + 8<<10

// settingsDoc is one user's synced settings: raw localStorage strings by
// key. The server never parses the values.
type settingsDoc struct {
	V    int               `json:"v"`
	Keys map[string]string `json:"keys"`
}

// Generation identifies one document: revisions restart at 1 after a
// DELETE, and the generation tells the new document from the old one.
type settingsGetResponse struct {
	Revision   int64         `json:"revision"`
	Generation string        `json:"generation"`
	Doc        *settingsDoc  `json:"doc"`
	Allowlist  []settingsKey `json:"allowlist"`
}

type settingsPutRequest struct {
	BaseRevision   int64        `json:"baseRevision"`
	BaseGeneration string       `json:"baseGeneration"`
	Doc            *settingsDoc `json:"doc"`
}

type settingsPutResponse struct {
	Revision   int64  `json:"revision"`
	Generation string `json:"generation"`
}

type settingsConflictResponse struct {
	Revision   int64        `json:"revision"`
	Generation string       `json:"generation"`
	Doc        *settingsDoc `json:"doc"`
}

// validateSettingsDoc checks the documented shape, then every key: denied
// keys first (#725), then the allowlist.
func validateSettingsDoc(d *settingsDoc) error {
	if d == nil || d.V != 1 || d.Keys == nil {
		return errors.New(`doc must be {"v": 1, "keys": {...}}`)
	}
	allowed := map[string]bool{}
	for _, k := range syncedSettingsKeys() {
		allowed[k.Key] = true
	}
	for key := range d.Keys {
		if settingsDenied(key) {
			return fmt.Errorf("key %q is never synced", key)
		}
		if !allowed[key] {
			return fmt.Errorf("key %q is not a synced setting", key)
		}
	}
	return nil
}

// encodeSettingsDoc serializes without HTML escaping, like the browser's
// JSON.stringify, so the size cap matches what the client sends.
func encodeSettingsDoc(d *settingsDoc) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// loadSettings reads the stored document (nil at revision 0). On failure
// it writes 500 and returns ok=false.
func (s *Server) loadSettings(w http.ResponseWriter, uid int64) (users.SettingsVersion, *settingsDoc, bool) {
	raw, v, err := s.auth.st.GetSettings(uid)
	if err != nil {
		log.Printf("[users] settings read for user #%d: %v", uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return users.SettingsVersion{}, nil, false
	}
	if v.Revision == 0 {
		return users.SettingsVersion{}, nil, true
	}
	var doc settingsDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		log.Printf("[users] settings for user #%d do not decode (%d bytes): %v", uid, len(raw), err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return users.SettingsVersion{}, nil, false
	}
	return v, &doc, true
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	v, doc, ok := s.loadSettings(w, u.ID)
	if !ok {
		return
	}
	writeJSON(w, settingsGetResponse{Revision: v.Revision, Generation: v.Generation, Doc: doc, Allowlist: syncedSettingsKeys()})
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if ok, wait := a.settingsPut.take("user:" + strconv.FormatInt(u.ID, 10)); !ok {
		writeTooManyRequests(w, wait)
		return
	}
	var req settingsPutRequest
	if !decodeJSONMax(w, r, &req, settingsBodyMax, http.StatusRequestEntityTooLarge) {
		return
	}
	if err := validateSettingsDoc(req.Doc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := encodeSettingsDoc(req.Doc)
	if err != nil {
		log.Printf("[users] settings encode for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(raw) > settingsDocMaxBytes {
		log.Printf("[users] settings for user #%d refused: %d bytes", u.ID, len(raw))
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("settings are larger than %d KiB", settingsDocMaxBytes>>10))
		return
	}
	v, err := a.st.PutSettings(u.ID, users.SettingsVersion{Revision: req.BaseRevision, Generation: req.BaseGeneration}, raw)
	if errors.Is(err, users.ErrSettingsConflict) {
		cur, doc, ok := s.loadSettings(w, u.ID)
		if !ok {
			return
		}
		writeJSONStatus(w, http.StatusConflict, settingsConflictResponse{Revision: cur.Revision, Generation: cur.Generation, Doc: doc})
		return
	}
	if err != nil {
		log.Printf("[users] settings write for user #%d (%d bytes): %v", u.ID, len(raw), err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, settingsPutResponse{Revision: v.Revision, Generation: v.Generation})
}

func (s *Server) handleSettingsDelete(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	if err := s.auth.st.DeleteSettings(u.ID); err != nil {
		log.Printf("[users] settings delete for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, okResponse{OK: true})
}
