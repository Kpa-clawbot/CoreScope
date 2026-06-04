package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/gorilla/mux"
)

// TestConfigClientExposesCustomizerDisabledTabs verifies that the
// /api/config/client endpoint surfaces the operator-set list of customizer
// tabs to hide, so the customize-v2 frontend can filter them out of
// _renderTabs(). Issue #1508.
func TestConfigClientExposesCustomizerDisabledTabs(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{
		Port: 3000,
		Customizer: &CustomizerConfig{
			DisabledTabs: []string{"branding", "geofilter", "export"},
		},
	}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	custRaw, ok := body["customizer"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected body.customizer object, got %T (body=%s)", body["customizer"], w.Body.String())
	}
	tabsRaw, ok := custRaw["disabledTabs"].([]interface{})
	if !ok {
		t.Fatalf("expected body.customizer.disabledTabs array, got %T", custRaw["disabledTabs"])
	}
	got := make([]string, 0, len(tabsRaw))
	for _, v := range tabsRaw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("disabledTabs element not a string: %T", v)
		}
		got = append(got, s)
	}
	want := []string{"branding", "export", "geofilter"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("disabledTabs: got %v, want %v", got, want)
	}
}

// TestConfigClientDefaultsCustomizerDisabledTabsEmpty verifies the backward-
// compat default: when no customizer block is configured, the field is still
// present and is an empty array (so the frontend can blindly call .includes()).
func TestConfigClientDefaultsCustomizerDisabledTabsEmpty(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	custRaw, ok := body["customizer"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected body.customizer object, got %T", body["customizer"])
	}
	tabsRaw, ok := custRaw["disabledTabs"].([]interface{})
	if !ok {
		t.Fatalf("expected body.customizer.disabledTabs array, got %T", custRaw["disabledTabs"])
	}
	if len(tabsRaw) != 0 {
		t.Errorf("default disabledTabs should be empty, got %v", tabsRaw)
	}
}
