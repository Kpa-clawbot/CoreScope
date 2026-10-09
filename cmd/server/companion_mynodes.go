package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

const myNodesKey = "meshcore-my-nodes"

// Results of addToMyNodes, reported as myNodes by POST /api/account/companions.
const (
	myNodesAdded   = "added"
	myNodesPresent = "present"
	myNodesFull    = "full"   // strictly: the document would exceed settingsDocMaxBytes
	myNodesFailed  = "failed" // any other merge error (the handler maps a returned error to this)
)

// myNodeItem is the item shape public/home.js writes: {pubkey, name, addedAt}.
type myNodeItem struct {
	Pubkey  string `json:"pubkey"`
	Name    string `json:"name"`
	AddedAt string `json:"addedAt"`
}

// addToMyNodes adds pubkey to uid's synced meshcore-my-nodes unless it is
// listed already (any case). It is a read-modify-write of the settings
// document through PutSettings' revision check, so the revision is bumped
// and open browsers pick the change up through the normal conflict flow;
// a concurrent write is retried. Existing items are kept as they are
// (compacted). A document that would exceed settingsDocMaxBytes is left
// alone and the result is "full". Any other failure is returned as an
// error; the link handler logs it and reports "failed".
func (a *authService) addToMyNodes(uid int64, pubkey, name string, now time.Time) (string, error) {
	if name == "" {
		name = pubkey[:12] // what home.js shows for an unnamed node
	}
	item, err := marshalNoEscape(myNodeItem{Pubkey: pubkey, Name: name, AddedAt: now.UTC().Format("2006-01-02T15:04:05.000Z")})
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 3; attempt++ {
		raw, v, err := a.st.GetSettings(uid)
		if err != nil {
			return "", err
		}
		doc := &settingsDoc{V: 1, Keys: map[string]string{}}
		if v.Revision != 0 {
			if err := json.Unmarshal([]byte(raw), doc); err != nil {
				return "", fmt.Errorf("stored settings do not decode: %w", err)
			}
			if doc.Keys == nil {
				doc.Keys = map[string]string{}
			}
		}
		var items []json.RawMessage
		if cur := doc.Keys[myNodesKey]; cur != "" {
			if err := json.Unmarshal([]byte(cur), &items); err != nil {
				return "", fmt.Errorf("%s is not a JSON array: %w", myNodesKey, err)
			}
		}
		for _, it := range items {
			var x struct {
				Pubkey string `json:"pubkey"`
			}
			if json.Unmarshal(it, &x) == nil && strings.EqualFold(strings.TrimSpace(x.Pubkey), pubkey) {
				return myNodesPresent, nil
			}
		}
		list, err := marshalNoEscape(append(items, json.RawMessage(item)))
		if err != nil {
			return "", err
		}
		doc.Keys[myNodesKey] = string(list)
		out, err := encodeSettingsDoc(doc)
		if err != nil {
			return "", err
		}
		if len(out) > settingsDocMaxBytes {
			return myNodesFull, nil
		}
		_, err = a.st.PutSettings(uid, v, out)
		if errors.Is(err, users.ErrSettingsConflict) {
			continue
		}
		if err != nil {
			return "", err
		}
		return myNodesAdded, nil
	}
	return "", errors.New("settings kept changing during three attempts")
}

// marshalNoEscape is json.Marshal without HTML escaping, like the
// browser's JSON.stringify (see encodeSettingsDoc).
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
