package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const settingsPath = "/api/account/settings"

func settingsDocWith(kv ...string) *settingsDoc {
	d := &settingsDoc{V: 1, Keys: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		d.Keys[kv[i]] = kv[i+1]
	}
	return d
}

// rawJSON encodes like a browser's JSON.stringify: no HTML escaping.
func rawJSON(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// doRaw is f.do with a body sent byte for byte (f.do's json.Marshal
// escapes <, > and &, which JSON.stringify does not).
func (f *authFixture) doRaw(method, path, body string, mods ...reqMod) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testBase)
	for _, m := range mods {
		m(req)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func TestSettingsGetWithoutDocument(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync1@example.org", "Sync One", pw)
	w := f.do("GET", settingsPath, nil, as(c))
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"doc":null`) {
		t.Fatalf("doc not null at revision 0: %s", w.Body.String())
	}
	got := decode[settingsGetResponse](t, w)
	if got.Revision != 0 || got.Generation != "" || got.Doc != nil || len(got.Allowlist) != len(settingsAllowlist) {
		t.Fatalf("GET = rev %d generation %q doc %v allowlist %d", got.Revision, got.Generation, got.Doc, len(got.Allowlist))
	}
}

func TestSettingsPutGetAndConflict(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync2@example.org", "Sync Two", pw)
	w := f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("meshcore-favorites", `["aa"]`)}, as(c))
	expectStatus(t, w, 200)
	first := decode[settingsPutResponse](t, w)
	if first.Revision != 1 || len(first.Generation) != 32 {
		t.Fatalf("first write = %+v; want revision 1 and a generation", first)
	}
	// A second device still at revision 0 gets 409 with the current document.
	w = f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("meshcore-theme", "dark")}, as(c))
	expectStatus(t, w, 409)
	conf := decode[settingsConflictResponse](t, w)
	if conf.Revision != 1 || conf.Generation != first.Generation || conf.Doc == nil || conf.Doc.Keys["meshcore-favorites"] != `["aa"]` {
		t.Fatalf("409 body = %+v", conf)
	}
	w = f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 1, BaseGeneration: first.Generation, Doc: settingsDocWith("meshcore-favorites", `["aa"]`, "meshcore-theme", "dark")}, as(c))
	expectStatus(t, w, 200)
	if second := decode[settingsPutResponse](t, w); second.Revision != 2 || second.Generation != first.Generation {
		t.Fatalf("second write = %+v; want revision 2 in generation %s", second, first.Generation)
	}
	got := decode[settingsGetResponse](t, f.do("GET", settingsPath, nil, as(c)))
	if got.Revision != 2 || got.Generation != first.Generation || got.Doc.Keys["meshcore-theme"] != "dark" || got.Doc.V != 1 {
		t.Fatalf("GET after two writes = %+v", got)
	}
}

// After DELETE the revisions restart at 1. A device that synced the old
// document must get 409 even when its revision matches the new document's.
func TestSettingsStaleGenerationConflicts(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync11@example.org", "Sync Eleven", pw)
	old := decode[settingsPutResponse](t, f.do("PUT", settingsPath, settingsPutRequest{Doc: settingsDocWith("meshcore-theme", "dark")}, as(c)))
	expectStatus(t, f.do("DELETE", settingsPath, nil, as(c)), 200)
	cur := decode[settingsPutResponse](t, f.do("PUT", settingsPath, settingsPutRequest{Doc: settingsDocWith("meshcore-theme", "light")}, as(c)))
	if cur.Revision != old.Revision || cur.Generation == old.Generation {
		t.Fatalf("new document %+v; old %+v", cur, old)
	}
	w := f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: old.Revision, BaseGeneration: old.Generation, Doc: settingsDocWith("meshcore-theme", "stale")}, as(c))
	expectStatus(t, w, 409)
	conf := decode[settingsConflictResponse](t, w)
	if conf.Revision != cur.Revision || conf.Generation != cur.Generation || conf.Doc.Keys["meshcore-theme"] != "light" {
		t.Fatalf("409 body = %+v", conf)
	}
}

func TestSettingsValuesStoredVerbatim(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync3@example.org", "Sync Three", pw)
	val := `{"name":"<b>A&B</b> \"quoted\" é ✓"}`
	body := rawJSON(t, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("cs-theme-overrides", val)})
	expectStatus(t, f.doRaw("PUT", settingsPath, body, as(c)), 200)
	got := decode[settingsGetResponse](t, f.do("GET", settingsPath, nil, as(c)))
	if got.Doc.Keys["cs-theme-overrides"] != val {
		t.Fatalf("value = %q; want %q", got.Doc.Keys["cs-theme-overrides"], val)
	}
}

func TestSettingsRejectsBadShapeAndKeys(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync4@example.org", "Sync Four", pw)
	type extraFieldReq struct {
		BaseRevision   int64        `json:"baseRevision"`
		BaseGeneration string       `json:"baseGeneration"`
		Doc            *settingsDoc `json:"doc"`
		Extra          int          `json:"extra"`
	}
	type numberDoc struct {
		V    int            `json:"v"`
		Keys map[string]int `json:"keys"`
	}
	type numberValueReq struct {
		BaseRevision int64     `json:"baseRevision"`
		Doc          numberDoc `json:"doc"`
	}
	bad := []any{
		settingsPutRequest{BaseRevision: 0},
		settingsPutRequest{BaseRevision: 0, Doc: &settingsDoc{V: 2, Keys: map[string]string{}}},
		settingsPutRequest{BaseRevision: 0, Doc: &settingsDoc{V: 1}},
		settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("panel-drag-packets", "1")},
		settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("cs-settings-sync-base", "{}")},
		extraFieldReq{BaseRevision: 0, Doc: settingsDocWith("meshcore-theme", "dark"), Extra: 1},
		numberValueReq{BaseRevision: 0, Doc: numberDoc{V: 1, Keys: map[string]int{"meshcore-theme": 1}}},
	}
	for i, b := range bad {
		if w := f.do("PUT", settingsPath, b, as(c)); w.Code != 400 {
			t.Errorf("case %d: status %d; want 400; body %s", i, w.Code, w.Body.String())
		}
	}
	if got := decode[settingsGetResponse](t, f.do("GET", settingsPath, nil, as(c))); got.Revision != 0 {
		t.Fatalf("a refused PUT stored something: rev %d", got.Revision)
	}
}

func TestSettingsDenylistWinsOverAllowlist(t *testing.T) {
	saved := settingsAllowlist
	t.Cleanup(func() { settingsAllowlist = saved })
	settingsAllowlist = append(append([]settingsKey{}, saved...),
		settingsKey{Key: "corescope_channel_keys", Kind: settingsKindScalar},
		settingsKey{Key: "corescope_channel_other", Kind: settingsKindScalar},
		settingsKey{Key: "meshcore-api-key", Kind: settingsKindScalar})
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync5@example.org", "Sync Five", pw)
	for _, k := range []string{"corescope_channel_keys", "corescope_channel_labels", "corescope_channel_other", "meshcore-api-key"} {
		w := f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith(k, "secret")}, as(c))
		expectStatus(t, w, 400)
		if !strings.Contains(w.Body.String(), "never synced") {
			t.Errorf("%s: body %s", k, w.Body.String())
		}
	}
	for _, k := range decode[settingsGetResponse](t, f.do("GET", settingsPath, nil, as(c))).Allowlist {
		if settingsDenied(k.Key) {
			t.Errorf("GET advertises denied key %q", k.Key)
		}
	}
}

func TestSettingsSizeCap(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync6@example.org", "Sync Six", pw)
	envelope := len(`{"v":1,"keys":{"cs-theme-overrides":""}}`)
	put := func(base int64, val string) *httptest.ResponseRecorder {
		return f.doRaw("PUT", settingsPath, rawJSON(t, settingsPutRequest{BaseRevision: base, Doc: settingsDocWith("cs-theme-overrides", val)}), as(c))
	}
	expectStatus(t, put(0, strings.Repeat("x", settingsDocMaxBytes-envelope+1)), 413)
	expectStatus(t, put(0, strings.Repeat("x", settingsBodyMax)), 413)
	// Exactly at the cap is accepted. '<' is 1 byte for JSON.stringify but 6
	// for Go's default encoder: the cap must be measured like the browser.
	expectStatus(t, put(0, strings.Repeat("<", settingsDocMaxBytes-envelope)), 200)
}

func TestSettingsRateLimitPerUser(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync7@example.org", "Sync Seven", pw)
	d := f.registerAndActivate(t, "sync8@example.org", "Sync Eight", pw)
	f.srv.auth.settingsPut = newRateLimiter(2, time.Hour)
	v := decode[settingsPutResponse](t, f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith()}, as(c)))
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 1, BaseGeneration: v.Generation, Doc: settingsDocWith()}, as(c)), 200)
	w := f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 2, BaseGeneration: v.Generation, Doc: settingsDocWith()}, as(c))
	expectStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Same IP, other user: not limited.
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith()}, as(d)), 200)
}

func TestSettingsNeedSessionAndCSRF(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync9@example.org", "Sync Nine", pw)
	noCSRF := &client{cookie: c.cookie}
	expectStatus(t, f.do("GET", settingsPath, nil), 401)
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{Doc: settingsDocWith()}, as(noCSRF)), 403)
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{Doc: settingsDocWith()}, as(c), header("Origin", "https://evil.example")), 403)
	expectStatus(t, f.do("DELETE", settingsPath, nil, as(noCSRF)), 403)
}

func TestSettingsDeleteAndAccountDelete(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync10@example.org", "Sync Ten", pw)
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{Doc: settingsDocWith("meshcore-theme", "dark")}, as(c)), 200)
	expectStatus(t, f.do("DELETE", settingsPath, nil, as(c)), 200)
	if got := decode[settingsGetResponse](t, f.do("GET", settingsPath, nil, as(c))); got.Revision != 0 || got.Doc != nil {
		t.Fatalf("after DELETE: %+v", got)
	}
	// The next change starts a new document.
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{Doc: settingsDocWith("meshcore-theme", "light")}, as(c)), 200)
	expectStatus(t, f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: pw}, as(c)), 200)
	if _, v, err := f.st.GetSettings(c.me.ID); err != nil || v.Revision != 0 {
		t.Fatalf("settings after account delete: %+v, %v", v, err)
	}
}
