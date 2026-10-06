# User Settings Sync (Sub-project B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A logged-in user's allowlisted `localStorage` settings follow them across devices, stored as one JSON document per user in `users.db`, with nothing changing when user management is off or the user is logged out.

**Architecture:** `internal/users` gets schema v2 (`user_settings`) and three store methods. `cmd/server` owns the allowlist and a hard denylist and serves `GET/PUT/DELETE /api/account/settings` behind `withUser`. A new `public/settings-sync.js` wraps `Storage.prototype.setItem/removeItem` for allowlisted keys while active, runs a three-way merge (local, profile, per-device baseline), pushes debounced and pulls on load, focus and every minute. Logout asks keep or remove through a dialog built on the app's `.modal` pattern.

**Tech Stack:** Go 1.22 (`modernc.org/sqlite` in `internal/users`, gorilla/mux in `cmd/server`), vanilla ES5 JavaScript (no build step, no npm deps), Node `vm` unit tests, Playwright E2E.

**Spec:** `docs/specs/2026-10-06-user-settings-sync-design.md` (binding). Sub-project A: `docs/specs/2026-10-06-user-management-design.md`. Executors read both.

## Resolved open points (spec "Open points for the implementation plan")

1. **Dialog component.** There is no shared JS dialog helper in `public/`: `channels.js:826` and `channels.js:886` inline their modal markup, `packets.js:4171-4210` (`showBYOP`) builds one by hand with a focus trap and Escape, and `account.js:297` uses the native `confirm()`. The shared part is the CSS: `.modal-overlay` and `.modal` (`style.css:1624-1640`). The logout and delete dialogs use those two classes with `role="dialog"`, `aria-modal="true"`, `aria-labelledby`, a Tab focus trap and Escape, following `showBYOP`. The builder is `showDialog(opts)` in `settings-sync.js` (Task 6).
2. **Re-render without reload.** `navigate()` is a top-level function declaration in `app.js:1155`, so it is `window.navigate`. It destroys the current page and re-runs its `init` from `location.hash` (`app.js:1238-1265`). The sync module calls `window.navigate()` after applying remote values.
3. **Mid-edit.** Following the spec's starting point: the re-render is skipped while the route is an account page (`#/account`, `#/account/...`; every form there can hold unsaved input, `account.js`) or the geofilter editor is open (`#cv2-gf-modal-overlay`, `customize-v2.js:1785`). Skipping is the deferral: the next page the user opens runs its `init` and reads the new values. The account page's sync status updates in place.

## Deviations and gaps for the controller

- **Allowlist re-check.** All 62 keys in the spec table occur as quoted string literals in `public/*.js` (checked with grep on 2026-10-06, including keys built from constants such as `FAV_KEY` `app.js:579`, `LS_KEY` `filter-ux.js:16`, `storageKey` options `nodes.js:1469`, `observers.js:465`, `packets.js:2490`, `scope-audit.js:365`). The spec says "about 55"; the table holds 62. No per-page preference key is missing: every other `localStorage` key in `public/` is layout or device state (`meshcore-panel-width`, `meshcore-pkt-col-px`, `meshcore-*-col-widths`, `channels-sidebar-width`, `mc-rt-sidebar-width`, `live-feed-width`, `live-controls-expanded`, `live-feed-hidden`, `live-legend-hidden`, `ch-*-collapsed`, `live-fullscreen`, `live-nav-pinned`, `map-view`, `live-map-view`, `rx-coverage-view`, `geofilter-draft`, `panel-drag-*`, `panel-corner-*`, `packets-*-cols`, `meshcore-gesture-hints-*`, `meshcore-affinity-debug`), a legacy v1 customizer key (`meshcore-user-theme`, `meshcore-timestamp-*`, migrated into `cs-theme-overrides` by `customize-v2.js:29-34`), or denylisted.
- **`updated_at` is `INTEGER` (unix seconds), not `TEXT`.** Every other timestamp in `users.db` is an integer written through `unix()` (`internal/users/store.go`, `schema.go`). The spec gives `TEXT`; the column is never read by the API.
- **Two existing modules change despite decision 5 ("existing modules stay unchanged").** Interception needs no change to any storage module, but: `auth.js` gets a logout hook (`CSAuth.setLogoutHandler`) because the dialog must run before the logout POST, and `customize-v2.js` exports its existing `_runPipeline` as `runPipeline` because `cs-theme-overrides` has no storage listener to trigger. `account.js` mounts the new section and ignores a cancelled logout.
- **The "every listed key occurs in `public/`" test is a Go test** (`cmd/server/settings_allowlist_test.go`), because the list lives in Go. It skips `public/settings-sync.js` so the sync module cannot keep a dead key alive.
- **Spec gaps resolved here:**
  - *Account copy deleted while other devices are active.* Under the spec's first-login rule (profile revision 0 uploads this device), any active device would re-upload within a minute after "Delete synced settings from my account". This plan adds a hold: a device whose baseline revision is above 0 (or already held) that sees revision 0 keeps its values, clears its baseline, and uploads only on its next allowlisted change. That matches the spec's "the next change starts a new document" on every device.
  - *429* is missing from the client error table: treated like a network error (backoff).
  - *Baseline ownership:* the baseline stores the user id; another user's baseline counts as none (union merge), so user B logging in after user A on one browser never removes anything.
  - *"Remove my settings" when the final push fails:* local data stays and a toast says so, because removing would lose changes that exist nowhere else.
  - *Logout dialog dismissal:* the dialog has a third button "Cancel", and Escape or a click on the backdrop also cancels (no logout).
  - *413:* pushing stops until "Sync now" or a reload; 400 stops until a reload (spec).

## Global Constraints

- Branch `feat/user-settings-sync` in worktree `C:\dev\meshcore\CoreScope\.worktrees\user-mgmt`. Do not create another worktree or branch.
- Commit author `efiten <erwin.fiten@gmail.com>`. Every commit message ends with these two lines:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71`
- Explicit `git add <file> ...` only. Never `git add -A` or `git add .`. Check `git diff --cached --stat` before each commit.
- No em dashes in docs, comments or commit text. Use a comma, colon, brackets or split the sentence.
- Feature off (no `userManagement` block, or `enabled: false`), or logged out: nothing visible changes and the browser makes no settings request and wraps nothing.
- Values in the document are raw `localStorage` strings; the server never parses them. Document shape `{"v": 1, "keys": {"<key>": "<raw string>"}}`.
- Maximum serialized document 256 KiB (`settingsDocMaxBytes = 256 << 10`). PUT body cap: 256 KiB + 8 KiB envelope.
- Per-user PUT limit: 60 per hour (`newRateLimiter(60, time.Hour)`), keyed `user:<id>`, not per IP.
- Denylist, checked before the allowlist: `meshcore-api-key` and every key starting with `corescope_channel_`.
- Client timing: push debounce 2 s; pull on login, page load while logged in, tab visible, every 60 s while visible, "Sync now"; 409 retries at most 3; backoff 2 s doubling to 5 min.
- Per-device baseline keys (never synced): `cs-settings-sync-base`, `cs-settings-sync-rev`.
- User-facing strings (verbatim): "Settings updated from another device", "Your settings are now saved to your account.", "Keep my settings on this device", "Remove my settings from this device", "Last synced <time>", "Not synced: retrying", "Sync now", "Delete synced settings from my account".
- No audit rows for settings reads and writes. Server log lines carry `#<id>` and byte sizes only, never document content.
- AGENTS.md: tests for every logic change; CSS colors only through variables; every dynamic string in HTML through `escapeHtml`; no new `map[string]interface{}` (typed structs); no npm deps; no build step; `cmd/server` writes only `users.db` through `internal/users`; every new `/api/` route gets an OpenAPI entry (`TestOpenAPICompleteness`); new unit test files are listed in `test-all.sh`.
- Cache busters are automatic (`__BUST__`); never edit them.
- Go toolchain (Windows git-bash), before any `go` command:
  `export PATH="/c/Users/efite/AppData/Local/Microsoft/WinGet/Packages/BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe/mingw64/bin:$PATH"; export CGO_ENABLED=1`
- Frontend suite: `GITHUB_REF_NAME=v3.13.1 PYTHONIOENCODING=utf-8 sh test-all.sh`
- Run Go tests per module (`internal/users`, `cmd/server`); there is no `go.work`. Run `gofmt -w` on the Go files you touched, then `gofmt -l` on them must print nothing (do not reformat untouched files; `cmd/server` has pre-existing gofmt drift).

## Review Focus

1. **The account copy is deleted (from the account page, possibly on another device) while this device holds an older baseline:** this device keeps every local value and does not re-upload until its next change. Pinned in Task 5 ("account copy deleted elsewhere").
2. **Two users on one browser** (A logs out with "Keep", B logs in): B's merge must not remove anything because of A's baseline. Pinned in Task 5 ("another user's baseline counts as none").
3. **Values with `<`, `>`, `&`, quotes and non-ASCII** come back byte for byte, and the 256 KiB cap is measured without Go's HTML escaping (a browser's `JSON.stringify` does not escape `<`), so a document the browser measures under the cap is accepted. Pinned in Task 3 ("values stored verbatim", "cap measured like JSON.stringify").
4. **Pages that write the same value again** (`live.js:3921` restores the theme, map toggles re-store booleans): no PUT, so the 60 per hour limit is not burned. Pinned in Task 5 ("writing the same value again does not push").
5. **"Remove my settings from this device" while the final push fails (offline):** local data stays and the user is told. Pinned in Task 6 ("remove after a failed push keeps the data").

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/users/schema.go` | migration v2: `user_settings` | 1 |
| `internal/users/settings.go` (new) | `GetSettings`, `PutSettings`, `DeleteSettings`, `ErrSettingsConflict` | 1 |
| `internal/users/settings_test.go` (new) | store and migration tests | 1 |
| `cmd/server/settings_allowlist.go` (new) | `settingsKey`, `settingsAllowlist`, `settingsDenied`, `syncedSettingsKeys`, size constant | 2 |
| `cmd/server/settings_allowlist_test.go` (new) | allowlist shape, every key occurs in `public/` | 2 |
| `cmd/server/settings_handlers.go` (new) | wire types, validation, encoding, the three handlers, `writeJSONStatus` | 3 |
| `cmd/server/settings_test.go` (new) | HTTP tests | 3 |
| `cmd/server/auth_types.go` | `decodeJSONMax` (decodeJSON delegates) | 3 |
| `cmd/server/auth_ratelimit.go` | `writeTooManyRequests` extracted from `allow` | 3 |
| `cmd/server/auth_service.go` | `settingsPut` limiter + gc | 3 |
| `cmd/server/auth_routes.go` | three routes | 3 |
| `cmd/server/openapi.go` | three route entries | 3 |
| `cmd/server/user_mgmt_off_test.go` | settings route absent when off | 3 |
| `docs/api-spec.md` | three rows, body cap note | 3 |
| `public/settings-sync.js` (new) | merge (4), engine (5), dialog, logout, section (6) | 4, 5, 6 |
| `tests/unit/test-settings-sync.js` (new) | vm tests of the real module | 4, 5, 6 |
| `test-all.sh` | list the new unit test | 4 |
| `public/index.html` | load `settings-sync.js` after `auth.js` | 5 |
| `public/customize-v2.js` | export `runPipeline` | 5 |
| `public/auth.js` | `setLogoutHandler`, cancellable logout | 6 |
| `public/account.js` | mount the section, ignore cancelled logout | 6 |
| `public/account.css` | dialog layout | 6 |
| `tests/unit/test-user-management-ui.js` | auth.js and account.js tests for the hook and the section | 6 |
| `tests/e2e/test-user-management-e2e.js` | existing logout step answers the dialog (6); two-device steps, axe (7) | 6, 7 |
| `docs/user-guide/accounts.md` | "Settings sync" for users | 7 |

---

### Task 1: `users.db` schema v2 and settings store methods

**Files:**
- Modify: `internal/users/schema.go` (append to `migrations`, after the v1 entry that ends at line 78)
- Create: `internal/users/settings.go`
- Test: `internal/users/settings_test.go`

**Interfaces:**
- Consumes: `Store` (`store.go`), `unix(time.Time) int64` (`store.go`), test helpers `newTestStore`, `mustCreate` (`helpers_test.go`, `users_test.go`).
- Produces:
  - `var ErrSettingsConflict error`
  - `func (s *Store) GetSettings(userID int64) (doc string, revision int64, err error)`: `"", 0, nil` when no row.
  - `func (s *Store) PutSettings(userID, baseRevision int64, doc string) (int64, error)`: new revision `baseRevision+1`; on stale base returns `(storedRevision, ErrSettingsConflict)`.
  - `func (s *Store) DeleteSettings(userID int64) error`: no row is not an error.

- [ ] **Step 1: Write the failing tests**

Create `internal/users/settings_test.go`:

```go
package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsGetWithoutRow(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	doc, rev, err := st.GetSettings(u.ID)
	if err != nil || doc != "" || rev != 0 {
		t.Fatalf("GetSettings = %q, %d, %v; want \"\", 0, nil", doc, rev, err)
	}
}

func TestSettingsPutRevisions(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")

	rev, err := st.PutSettings(u.ID, 0, `{"v":1,"keys":{"a":"1"}}`)
	if err != nil || rev != 1 {
		t.Fatalf("first write = %d, %v; want 1, nil", rev, err)
	}
	// A second device still at revision 0 is stale.
	rev, err = st.PutSettings(u.ID, 0, `{"v":1,"keys":{"b":"2"}}`)
	if !errors.Is(err, ErrSettingsConflict) || rev != 1 {
		t.Fatalf("stale write = %d, %v; want 1, ErrSettingsConflict", rev, err)
	}
	// A base ahead of the stored revision is stale too.
	if rev, err = st.PutSettings(u.ID, 5, `x`); !errors.Is(err, ErrSettingsConflict) || rev != 1 {
		t.Fatalf("future base = %d, %v; want 1, ErrSettingsConflict", rev, err)
	}
	clk.Advance(time.Minute)
	rev, err = st.PutSettings(u.ID, 1, `{"v":1,"keys":{"c":"3"}}`)
	if err != nil || rev != 2 {
		t.Fatalf("matching write = %d, %v; want 2, nil", rev, err)
	}
	doc, got, err := st.GetSettings(u.ID)
	if err != nil || got != 2 || doc != `{"v":1,"keys":{"c":"3"}}` {
		t.Fatalf("GetSettings = %q, %d, %v", doc, got, err)
	}
	var at int64
	if err := st.db.QueryRow(`SELECT updated_at FROM user_settings WHERE user_id = ?`, u.ID).Scan(&at); err != nil || at != unix(clk.Now()) {
		t.Fatalf("updated_at = %d, %v; want %d", at, err, unix(clk.Now()))
	}
}

func TestSettingsWithoutRowNeedBaseZero(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if rev, err := st.PutSettings(u.ID, 3, `{}`); !errors.Is(err, ErrSettingsConflict) || rev != 0 {
		t.Fatalf("PutSettings(base 3, no row) = %d, %v; want 0, ErrSettingsConflict", rev, err)
	}
}

func TestSettingsDelete(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, 0, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatal(err)
	}
	if doc, rev, err := st.GetSettings(u.ID); err != nil || doc != "" || rev != 0 {
		t.Fatalf("after delete = %q, %d, %v", doc, rev, err)
	}
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	// The next write starts a new document.
	if rev, err := st.PutSettings(u.ID, 0, `{}`); err != nil || rev != 1 {
		t.Fatalf("write after delete = %d, %v; want 1, nil", rev, err)
	}
}

func TestSettingsGoWithTheAccount(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, 0, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM user_settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("user_settings rows after account delete = %d, %v; want 0", n, err)
	}
}

func TestSettingsSurviveDisable(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, 0, `{"v":1,"keys":{}}`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(u.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, rev, err := st.GetSettings(u.ID); err != nil || rev != 1 {
		t.Fatalf("settings after disable: rev %d, %v; want 1", rev, err)
	}
}

// A users.db written by a v1 binary gains user_settings and keeps its rows.
func TestMigrateV1DatabaseToV2(t *testing.T) {
	if len(migrations) != 2 {
		t.Fatalf("len(migrations) = %d; this test pins the v1 to v2 step", len(migrations))
	}
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
	}, migrations[0]...)
	stmts = append(stmts, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v1 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 db: %v", err)
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err != nil || v != 2 {
		t.Fatalf("SchemaVersion = %d, %v; want 2", v, err)
	}
	u, err := st.GetByEmail("old@example.org")
	if err != nil {
		t.Fatalf("v1 user lost: %v", err)
	}
	if rev, err := st.PutSettings(u.ID, 0, `{}`); err != nil || rev != 1 {
		t.Fatalf("PutSettings on migrated db = %d, %v", rev, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (Go toolchain exports from Global Constraints first):
```bash
cd internal/users && go test -count=1 -run 'Settings|MigrateV1' ./...
```
Expected: FAIL to compile with `st.GetSettings undefined` (and `PutSettings`, `DeleteSettings`, `ErrSettingsConflict`).

- [ ] **Step 3: Add migration v2**

In `internal/users/schema.go`, the `migrations` slice currently ends with the v1 entry's closing `},` followed by `}`. Insert the v2 entry between them:

```go
	{ // v2: sub-project B, settings sync (one document per user)
		`CREATE TABLE user_settings (
			user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			doc TEXT NOT NULL,
			revision INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
```

- [ ] **Step 4: Implement the store methods**

Create `internal/users/settings.go`:

```go
package users

import (
	"database/sql"
	"errors"
)

// ErrSettingsConflict: PutSettings was given a base revision that is not
// the stored one (another device wrote first).
var ErrSettingsConflict = errors.New("users: settings revision conflict")

// GetSettings returns the user's synced settings document and its
// revision; "" and 0 when the user has none.
func (s *Store) GetSettings(userID int64) (string, int64, error) {
	var doc string
	var rev int64
	err := s.db.QueryRow(`SELECT doc, revision FROM user_settings WHERE user_id = ?`, userID).Scan(&doc, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	return doc, rev, nil
}

// PutSettings stores doc when the stored revision equals baseRevision (0 =
// no row yet) and returns the new revision, baseRevision+1. Otherwise it
// returns the stored revision and ErrSettingsConflict.
func (s *Store) PutSettings(userID, baseRevision int64, doc string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var cur int64
	err = tx.QueryRow(`SELECT revision FROM user_settings WHERE user_id = ?`, userID).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if cur != baseRevision {
		return cur, ErrSettingsConflict
	}
	next := baseRevision + 1
	if _, err := tx.Exec(`INSERT INTO user_settings (user_id, doc, revision, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET doc = excluded.doc, revision = excluded.revision, updated_at = excluded.updated_at`,
		userID, doc, next, unix(s.now())); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

// DeleteSettings removes the user's synced settings. No row is not an error.
func (s *Store) DeleteSettings(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM user_settings WHERE user_id = ?`, userID)
	return err
}
```

`Store.Delete` needs no change: `users.db` is opened with `foreign_keys(1)` (`store.go`), so the row cascades; `TestSettingsGoWithTheAccount` pins it.

- [ ] **Step 5: Run the tests to verify they pass**

Run:
```bash
cd internal/users && gofmt -w schema.go settings.go settings_test.go && go test -count=1 ./... && gofmt -l schema.go settings.go settings_test.go
```
Expected: `ok  github.com/meshcore-analyzer/users`, and `gofmt -l` prints nothing. `TestOpenCreatesSchemaAndIsIdempotent` now checks version 2 on a fresh database.

- [ ] **Step 6: Commit**

```bash
git add internal/users/schema.go internal/users/settings.go internal/users/settings_test.go
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
feat(users): store one settings document per user (schema v2)

Migration v2 adds user_settings (one JSON document per user plus a
revision). PutSettings writes only when the caller's base revision is
the stored one, so two devices cannot overwrite each other silently.
The row goes with the account through the existing cascade.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

### Task 2: Allowlist and denylist for synced keys

**Files:**
- Create: `cmd/server/settings_allowlist.go`
- Test: `cmd/server/settings_allowlist_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `const settingsDocMaxBytes = 256 << 10`
  - `type settingsKey struct { Key string `json:"key"`; Kind string `json:"kind"`; ID string `json:"id,omitempty"` }`
  - `const settingsKindSet = "set"`, `const settingsKindScalar = "scalar"`
  - `var settingsAllowlist []settingsKey` (a var so tests can extend it)
  - `func settingsDenied(key string) bool`
  - `func syncedSettingsKeys() []settingsKey` (allowlist minus denied keys; this is what GET returns and what PUT accepts)

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/settings_allowlist_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsAllowlistShape(t *testing.T) {
	seen := map[string]bool{}
	sets := map[string]string{}
	for _, k := range settingsAllowlist {
		if seen[k.Key] {
			t.Errorf("duplicate allowlist key %q", k.Key)
		}
		seen[k.Key] = true
		switch k.Kind {
		case settingsKindSet:
			sets[k.Key] = k.ID
		case settingsKindScalar:
			if k.ID != "" {
				t.Errorf("scalar %q has an identity field", k.Key)
			}
		default:
			t.Errorf("key %q has kind %q", k.Key, k.Kind)
		}
		if settingsDenied(k.Key) {
			t.Errorf("allowlist contains denied key %q", k.Key)
		}
	}
	want := map[string]string{"meshcore-my-nodes": "pubkey", "meshcore-favorites": "", "corescope_saved_filters_v1": "name"}
	if len(sets) != len(want) {
		t.Fatalf("set keys = %v; want %v", sets, want)
	}
	for k, id := range want {
		if got, ok := sets[k]; !ok || got != id {
			t.Errorf("set %q identity = %q (present %v); want %q", k, got, ok, id)
		}
	}
	if len(settingsAllowlist) != 62 {
		t.Errorf("allowlist has %d keys; the spec table has 62", len(settingsAllowlist))
	}
}

func TestSettingsDenied(t *testing.T) {
	for _, k := range []string{"meshcore-api-key", "corescope_channel_keys", "corescope_channel_labels", "corescope_channel_cache", "corescope_channel_anything"} {
		if !settingsDenied(k) {
			t.Errorf("settingsDenied(%q) = false", k)
		}
	}
	for _, k := range []string{"meshcore-favorites", "corescope_saved_filters_v1", "meshcore-api-key-hint", "corescope_channel"} {
		if settingsDenied(k) {
			t.Errorf("settingsDenied(%q) = true", k)
		}
	}
}

func TestSyncedSettingsKeysDropsDeniedKeys(t *testing.T) {
	saved := settingsAllowlist
	t.Cleanup(func() { settingsAllowlist = saved })
	settingsAllowlist = append(append([]settingsKey{}, saved...),
		settingsKey{Key: "corescope_channel_keys", Kind: settingsKindScalar},
		settingsKey{Key: "meshcore-api-key", Kind: settingsKindScalar})
	got := syncedSettingsKeys()
	if len(got) != len(saved) {
		t.Fatalf("syncedSettingsKeys has %d keys; want %d", len(got), len(saved))
	}
	for _, k := range got {
		if settingsDenied(k.Key) {
			t.Errorf("denied key %q returned", k.Key)
		}
	}
}

// Every synced key must still be used by the frontend: a renamed key would
// otherwise sync a value nothing reads. Keys are matched as quoted string
// literals, so a key that is a prefix of another does not pass by accident.
func TestSettingsAllowlistKeysOccurInPublic(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "public", "*.js"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no public/*.js found (%v)", err)
	}
	var all strings.Builder
	for _, f := range files {
		if filepath.Base(f) == "settings-sync.js" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	src := all.String()
	for _, k := range settingsAllowlist {
		if !strings.Contains(src, "'"+k.Key+"'") && !strings.Contains(src, `"`+k.Key+`"`) {
			t.Errorf("allowlisted key %q no longer occurs as a string literal in public/*.js; remove it or follow the rename", k.Key)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd cmd/server && go test -count=1 -run 'SettingsAllowlist|SettingsDenied|SyncedSettings' .
```
Expected: FAIL to compile with `undefined: settingsAllowlist` (and `settingsDenied`, `settingsKey`).

- [ ] **Step 3: Implement the allowlist**

Create `cmd/server/settings_allowlist.go`:

```go
package main

import "strings"

// settingsDocMaxBytes caps one user's serialized settings document.
const settingsDocMaxBytes = 256 << 10

// settingsKey is one localStorage key the settings sync accepts.
type settingsKey struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`         // settingsKindSet or settingsKindScalar
	ID   string `json:"id,omitempty"` // set only: the item field that identifies an item; "" = the item itself
}

const (
	settingsKindSet    = "set"    // a JSON array, merged per item
	settingsKindScalar = "scalar" // any string, merged as a whole
)

func scalarSettings(keys ...string) []settingsKey {
	out := make([]settingsKey, len(keys))
	for i, k := range keys {
		out[i] = settingsKey{Key: k, Kind: settingsKindScalar}
	}
	return out
}

// settingsAllowlist is the single source of the synced keys
// (docs/specs/2026-10-06-user-settings-sync-design.md). GET
// /api/account/settings hands it to the browser. Device-specific state
// (panel and column sizes, collapsed panels, map positions) stays out on
// purpose. A var so tests can extend it.
var settingsAllowlist = append([]settingsKey{
	{Key: "meshcore-my-nodes", Kind: settingsKindSet, ID: "pubkey"},
	{Key: "meshcore-favorites", Kind: settingsKindSet},
	{Key: "corescope_saved_filters_v1", Kind: settingsKindSet, ID: "name"},
}, scalarSettings(
	// customizer, theme, units
	"cs-theme-overrides", "meshcore-theme", "meshcore-cb-preset", "mc-dark-tile-provider", "mc-light-tile-provider",
	"meshcore-distance-unit", "meshcore-heatmap-opacity", "meshcore-live-heatmap-opacity", "live-channel-colors",
	// packets
	"meshcore-observer-filter", "meshcore-type-filter", "meshcore-time-window", "meshcore-hex-hashes",
	"meshcore-full-names", "meshcore-obs-sort", "meshcore-packets-sort",
	// tables
	"meshcore-nodes-sort", "meshcore-observers-sort", "meshcore-scope-audit-sort", "meshcore-channel-sort",
	// nodes
	"meshcore-nodes-last-heard", "meshcore-nodes-status-filter", "meshcore-nodes-silent-for",
	// region and area
	"meshcore-area-filter", "meshcore-region-filter", "mc-region-show-all-nodes", "meshcore-hide-1byte-hops",
	"channels-show-encrypted",
	// map
	"meshcore-map-clustering", "meshcore-map-heatmap", "meshcore-map-hash-labels", "meshcore-map-multibyte-overlay",
	"meshcore-map-scope-overlay", "meshcore-map-status-filter", "meshcore-map-scope-filter", "meshcore-map-byte-filter",
	"meshcore-map-region-filter", "meshcore-map-geo-filter", "meshcore-top-routes-axis", "meshcore-top-routes-n",
	// analytics
	"subpath-hide-collisions", "meshcore-repeater-scatter-x", "meshcore-repeater-scatter-y", "ng-min-score",
	// home
	"meshcore-user-level",
	// live
	"meshcore-live-heatmap", "live-ghost-hops", "live-realistic-propagation", "live-favorites-only",
	"live-multibyte-only", "live-matrix-mode", "live-matrix-rain", "meshcore-color-packets-by-hash",
	"live-node-filter", "live-vcr-speed", "live-audio-voice", "live-audio-enabled", "live-audio-bpm",
	"live-audio-volume",
)...)

// settingsDenied reports keys that must never leave the browser: channel
// keys, labels and decrypted-message caches (#725) and the admin API key.
// It is checked before the allowlist, so adding one there cannot sync it.
func settingsDenied(key string) bool {
	return key == "meshcore-api-key" || strings.HasPrefix(key, "corescope_channel_")
}

// syncedSettingsKeys is the allowlist minus denied keys: what GET returns
// and what PUT accepts.
func syncedSettingsKeys() []settingsKey {
	out := make([]settingsKey, 0, len(settingsAllowlist))
	for _, k := range settingsAllowlist {
		if !settingsDenied(k.Key) {
			out = append(out, k)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd cmd/server && go test -count=1 -run 'SettingsAllowlist|SettingsDenied|SyncedSettings' . && gofmt -w settings_allowlist.go settings_allowlist_test.go && gofmt -l settings_allowlist.go settings_allowlist_test.go
```
Expected: `ok`, no gofmt output. If `TestSettingsAllowlistKeysOccurInPublic` names a key, stop and report it to the controller (scope question), do not edit the list.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/settings_allowlist.go cmd/server/settings_allowlist_test.go
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
feat(server): allowlist and denylist for synced settings

The server owns the list of localStorage keys that settings sync may
store (62 keys from the spec table) and a denylist that wins over it:
corescope_channel_* and meshcore-api-key never leave the browser
(#725). A test fails when an allowlisted key no longer occurs in
public/*.js.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

### Task 3: `GET/PUT/DELETE /api/account/settings`

**Files:**
- Create: `cmd/server/settings_handlers.go`
- Test: `cmd/server/settings_test.go`
- Modify: `cmd/server/auth_types.go` (`decodeJSON`, near line 175)
- Modify: `cmd/server/auth_ratelimit.go` (the 429 block inside `allow`)
- Modify: `cmd/server/auth_service.go` (`authService` struct, `newAuthService`, `prune`)
- Modify: `cmd/server/auth_routes.go` (after the `/api/account/sessions/{id}` route)
- Modify: `cmd/server/openapi.go` (after the `"DELETE /api/account/sessions/{id}"` entry, line 75)
- Modify: `cmd/server/user_mgmt_off_test.go` (path list in `TestUserManagementOffIsUnchanged`)
- Modify: `docs/api-spec.md` (user-management table and its intro paragraph)

**Interfaces:**
- Consumes: Task 1 `GetSettings`, `PutSettings`, `DeleteSettings`, `users.ErrSettingsConflict`; Task 2 `settingsKey`, `settingsAllowlist`, `settingsDenied`, `syncedSettingsKeys`, `settingsDocMaxBytes`, `settingsKindScalar`. Existing: `withUser`, `authedHandler`, `writeJSON`, `writeError`, `okResponse`, `rateLimiter.take`, test fixture `newAuthFixture`, `registerAndActivate`, `as`, `header`, `decode`, `expectStatus`, `pw`.
- Produces (JSON contract the browser relies on in Tasks 5 and 6):
  - `GET` 200 `{"revision": <int>, "doc": {"v":1,"keys":{...}} | null, "allowlist": [{"key","kind","id"?}]}`
  - `PUT` body `{"baseRevision": <int>, "doc": {"v":1,"keys":{...}}}`: 200 `{"revision"}`; 409 `{"revision","doc"}`; 400; 413; 429 (+`Retry-After`)
  - `DELETE` 200 `{"ok": true}`
  - Go: `settingsDoc`, `settingsGetResponse`, `settingsPutRequest`, `settingsRevisionResponse`, `settingsConflictResponse`, `decodeJSONMax`, `writeTooManyRequests`, `writeJSONStatus`, `authService.settingsPut`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/settings_test.go`:

```go
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
	if got.Revision != 0 || got.Doc != nil || len(got.Allowlist) != len(settingsAllowlist) {
		t.Fatalf("GET = rev %d doc %v allowlist %d", got.Revision, got.Doc, len(got.Allowlist))
	}
}

func TestSettingsPutGetAndConflict(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sync2@example.org", "Sync Two", pw)
	w := f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("meshcore-favorites", `["aa"]`)}, as(c))
	expectStatus(t, w, 200)
	if rev := decode[settingsRevisionResponse](t, w).Revision; rev != 1 {
		t.Fatalf("first revision = %d", rev)
	}
	// A second device still at revision 0 gets 409 with the current document.
	w = f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith("meshcore-theme", "dark")}, as(c))
	expectStatus(t, w, 409)
	conf := decode[settingsConflictResponse](t, w)
	if conf.Revision != 1 || conf.Doc == nil || conf.Doc.Keys["meshcore-favorites"] != `["aa"]` {
		t.Fatalf("409 body = %+v", conf)
	}
	w = f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 1, Doc: settingsDocWith("meshcore-favorites", `["aa"]`, "meshcore-theme", "dark")}, as(c))
	expectStatus(t, w, 200)
	got := decode[settingsGetResponse](t, f.do("GET", settingsPath, nil, as(c)))
	if got.Revision != 2 || got.Doc.Keys["meshcore-theme"] != "dark" || got.Doc.V != 1 {
		t.Fatalf("GET after two writes = %+v", got)
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
		BaseRevision int64        `json:"baseRevision"`
		Doc          *settingsDoc `json:"doc"`
		Extra        int          `json:"extra"`
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
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 0, Doc: settingsDocWith()}, as(c)), 200)
	expectStatus(t, f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 1, Doc: settingsDocWith()}, as(c)), 200)
	w := f.do("PUT", settingsPath, settingsPutRequest{BaseRevision: 2, Doc: settingsDocWith()}, as(c))
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
	if _, rev, err := f.st.GetSettings(c.me.ID); err != nil || rev != 0 {
		t.Fatalf("settings after account delete: rev %d, %v", rev, err)
	}
}
```

In `cmd/server/user_mgmt_off_test.go`, extend the path list in `TestUserManagementOffIsUnchanged`:

```go
	for _, p := range []string{"/api/auth/me", "/api/admin/users", "/api/account/sessions", "/api/account/settings"} {
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd cmd/server && go test -count=1 -run 'Settings|UserManagementOff' .
```
Expected: FAIL to compile with `undefined: settingsDoc` (and the other response types, `settingsBodyMax`, `auth.settingsPut`).

- [ ] **Step 3: Extract `writeTooManyRequests`**

In `cmd/server/auth_ratelimit.go`, add after `sweepLocked`:

```go
// writeTooManyRequests answers 429 with a Retry-After of at least 1 s.
func writeTooManyRequests(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
}
```

and replace the loop at the end of `allow` with:

```go
	for _, k := range keys {
		if ok, wait := l.take(k); !ok {
			writeTooManyRequests(w, wait)
			return false
		}
	}
	return true
```

- [ ] **Step 4: Add `decodeJSONMax`**

In `cmd/server/auth_types.go`, replace `decodeJSON` with:

```go
// decodeJSON reads a small JSON body into dst, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeJSONMax(w, r, dst, 16<<10, http.StatusBadRequest)
}

// decodeJSONMax is decodeJSON with a body cap of limit bytes. A body over
// the cap answers tooLarge (decodeJSON keeps its historical 400).
func decodeJSONMax(w http.ResponseWriter, r *http.Request, dst any, limit int64, tooLarge int) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) && tooLarge != http.StatusBadRequest {
			writeError(w, tooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
```

- [ ] **Step 5: Add the limiter**

In `cmd/server/auth_service.go`:
- in `authService`, after `hook   *rateLimiter`, add `settingsPut *rateLimiter // PUT /api/account/settings, per user`
- in `newAuthService`, after `hook:   newRateLimiter(600, time.Minute),`, add `settingsPut: newRateLimiter(60, time.Hour),`
- in `prune`, after `a.hook.gc()`, add `a.settingsPut.gc()`

- [ ] **Step 6: Implement the handlers**

Create `cmd/server/settings_handlers.go`:

```go
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

type settingsGetResponse struct {
	Revision  int64         `json:"revision"`
	Doc       *settingsDoc  `json:"doc"`
	Allowlist []settingsKey `json:"allowlist"`
}

type settingsPutRequest struct {
	BaseRevision int64        `json:"baseRevision"`
	Doc          *settingsDoc `json:"doc"`
}

type settingsRevisionResponse struct {
	Revision int64 `json:"revision"`
}

type settingsConflictResponse struct {
	Revision int64        `json:"revision"`
	Doc      *settingsDoc `json:"doc"`
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[routes] JSON encode error: %v", err)
	}
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
func (s *Server) loadSettings(w http.ResponseWriter, uid int64) (int64, *settingsDoc, bool) {
	raw, rev, err := s.auth.st.GetSettings(uid)
	if err != nil {
		log.Printf("[users] settings read for user #%d: %v", uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return 0, nil, false
	}
	if rev == 0 {
		return 0, nil, true
	}
	var doc settingsDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		log.Printf("[users] settings for user #%d do not decode (%d bytes): %v", uid, len(raw), err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return 0, nil, false
	}
	return rev, &doc, true
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	rev, doc, ok := s.loadSettings(w, u.ID)
	if !ok {
		return
	}
	writeJSON(w, settingsGetResponse{Revision: rev, Doc: doc, Allowlist: syncedSettingsKeys()})
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
	rev, err := a.st.PutSettings(u.ID, req.BaseRevision, raw)
	if errors.Is(err, users.ErrSettingsConflict) {
		cur, doc, ok := s.loadSettings(w, u.ID)
		if !ok {
			return
		}
		writeJSONStatus(w, http.StatusConflict, settingsConflictResponse{Revision: cur, Doc: doc})
		return
	}
	if err != nil {
		log.Printf("[users] settings write for user #%d (%d bytes): %v", u.ID, len(raw), err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, settingsRevisionResponse{Revision: rev})
}

func (s *Server) handleSettingsDelete(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	if err := s.auth.st.DeleteSettings(u.ID); err != nil {
		log.Printf("[users] settings delete for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, okResponse{OK: true})
}
```

- [ ] **Step 7: Register the routes and the OpenAPI entries**

In `cmd/server/auth_routes.go`, after the `/api/account/sessions/{id}` line:

```go
	r.HandleFunc("/api/account/settings", s.withUser(s.handleSettingsGet)).Methods("GET")
	r.HandleFunc("/api/account/settings", s.withUser(s.handleSettingsPut)).Methods("PUT")
	r.HandleFunc("/api/account/settings", s.withUser(s.handleSettingsDelete)).Methods("DELETE")
```

In `cmd/server/openapi.go`, after the `"DELETE /api/account/sessions/{id}"` entry:

```go
		"GET /api/account/settings":    {Summary: "Get own synced settings", Description: "Returns {revision, doc, allowlist}. doc is {v: 1, keys: {<localStorage key>: <raw string>}} or null at revision 0. allowlist lists the keys the server accepts: {key, kind: set|scalar, id?}, where id names the field that identifies an item of a set (absent: the item itself).", Tag: "users", Session: true},
		"PUT /api/account/settings":    {Summary: "Replace own synced settings", Description: "Body {baseRevision, doc}. 200 {revision}. 409 {revision, doc} when baseRevision is not the stored revision. 400 when doc is not {v: 1, keys} or holds a key that is never synced or not allowlisted. 413 above 256 KiB. 429 above 60 writes per hour per user.", Tag: "users", Session: true},
		"DELETE /api/account/settings": {Summary: "Delete own synced settings", Description: "Removes the stored document; the next PUT (baseRevision 0) starts a new one.", Tag: "users", Session: true},
```

- [ ] **Step 8: Document the API**

In `docs/api-spec.md`, in the user-management intro paragraph, change "On every route except the webhook they are limited to 16 KiB" to "On every route except the webhook and `PUT /api/account/settings` they are limited to 16 KiB", and after the `DELETE /api/account/sessions/{id}` row add:

```markdown
| `GET /api/account/settings` | session | -> `{revision, doc, allowlist}`. `doc` is `{v: 1, keys: {<localStorage key>: <raw string>}}`, or `null` at revision 0. `allowlist` is `[{key, kind, id?}]`: `kind` is `set` (JSON array merged per item; `id` names the field that identifies an item, absent means the item itself) or `scalar` |
| `PUT /api/account/settings` | session | `{baseRevision, doc}` -> `{revision}`. `409` `{revision, doc}` when `baseRevision` is not the stored revision; `400` for another shape, a key that is never synced (`corescope_channel_*`, `meshcore-api-key`) or a key not in the allowlist; `413` when `doc` is over 256 KiB (body cap 264 KiB); `429` above 60 writes per hour per user |
| `DELETE /api/account/settings` | session | -> `{ok}`. The next `PUT` with `baseRevision` 0 starts a new document |
```

- [ ] **Step 9: Run the tests to verify they pass**

Run:
```bash
cd cmd/server && go test -count=1 -run 'Settings|UserManagementOff|OpenAPI|RateLimit|Auth|Account' . && gofmt -w settings_handlers.go settings_test.go auth_types.go auth_ratelimit.go auth_service.go auth_routes.go openapi.go user_mgmt_off_test.go && gofmt -l settings_handlers.go settings_test.go auth_types.go auth_ratelimit.go auth_service.go auth_routes.go openapi.go user_mgmt_off_test.go
```
Expected: `ok`, no gofmt output. Then the whole module once:
```bash
cd cmd/server && go test -count=1 ./...
```
Expected: `ok`.

- [ ] **Step 10: Commit**

```bash
git add cmd/server/settings_handlers.go cmd/server/settings_test.go cmd/server/auth_types.go cmd/server/auth_ratelimit.go cmd/server/auth_service.go cmd/server/auth_routes.go cmd/server/openapi.go cmd/server/user_mgmt_off_test.go docs/api-spec.md
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
feat(server): GET/PUT/DELETE /api/account/settings

One settings document per user behind withUser (session, Origin and
CSRF). PUT writes only on a matching base revision and answers 409 with
the current document, so the browser can merge without another GET.
Keys are checked against the denylist and then the allowlist; the
document is capped at 256 KiB, measured without HTML escaping; PUT is
limited to 60 per hour per user. Registered only with user management
on. No audit rows; the log carries the user id and sizes only.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

### Task 4: Three-way merge (`public/settings-sync.js`, part 1)

**Files:**
- Create: `public/settings-sync.js`
- Test: `tests/unit/test-settings-sync.js`
- Modify: `test-all.sh` (insert after `node tests/unit/test-scope-audit-styles-linked.js`, line 187)

**Interfaces:**
- Consumes: nothing (pure functions).
- Produces (inside the IIFE, used by Task 5):
  - `own(o, k)`: own property or `undefined`
  - `mergeDocs(local, profile, base, allowlist) -> {keys, localChanges, differsFromProfile}` where `local`, `profile`, `base` are `{key: rawString}`, `allowlist` is `[{key, kind: 'set'|'scalar', id?}]`, `keys` is the merged `{key: rawString}` (absent = unset), `localChanges` the keys whose merged value differs from local, `differsFromProfile` whether the merged result differs from the profile.
  - `window.CSSettingsSync = { _test: { mergeDocs } }`
- Test harness (in `tests/unit/test-settings-sync.js`, reused by Tasks 5 and 6): `makeEnv(opts)`, `fakeServer()`, `fakeTimers()`, `settle()`, `plain(x)`, `J`, `ALLOW`.

- [ ] **Step 1: Write the failing tests (with the full harness)**

Create `tests/unit/test-settings-sync.js`:

```js
/* Unit tests for public/settings-sync.js (settings sync, sub-project B).
 * The real file runs in a vm sandbox with a fake Storage (a real
 * prototype, so the module's Storage.prototype wrap is exercised), a fake
 * CSAuth backed by a fake server, and fake timers. Tests run one by one. */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..', '..');
const SRC = fs.readFileSync(path.join(ROOT, 'public/settings-sync.js'), 'utf8');
const J = JSON.stringify;
// Objects made inside the vm have another Object.prototype; compare plain copies.
const plain = (x) => JSON.parse(JSON.stringify(x));
const settle = () => new Promise((r) => setImmediate(r));

const tests = [];
function test(name, fn) { tests.push({ name, fn }); }

// escapeHtml comes from the real app.js, not a copy.
function loadEscapeHtml() {
  const src = fs.readFileSync(path.join(ROOT, 'public/app.js'), 'utf8');
  const m = src.match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

function fakeTimers() {
  let now = 0, seq = 0;
  const list = [];
  const api = {
    setTimeout(fn, ms) { list.push({ id: ++seq, at: now + (ms || 0), fn }); return seq; },
    clearTimeout(id) { const i = list.findIndex((t) => t.id === id); if (i >= 0) list.splice(i, 1); },
    setInterval(fn, ms) { list.push({ id: ++seq, at: now + ms, fn, every: ms }); return seq; },
    clearInterval(id) { api.clearTimeout(id); },
    async advance(ms) {
      const end = now + ms;
      for (;;) {
        await settle();
        list.sort((a, b) => a.at - b.at);
        const t = list[0];
        if (!t || t.at > end) break;
        now = t.at;
        if (t.every) t.at += t.every; else list.shift();
        t.fn();
      }
      now = end;
      await settle();
    },
    // Pending one-shot timers, as delays from now.
    delays() { return list.filter((t) => !t.every).map((t) => t.at - now).sort((a, b) => a - b); },
  };
  return api;
}

const ALLOW = [
  { key: 'meshcore-favorites', kind: 'set' },
  { key: 'meshcore-my-nodes', kind: 'set', id: 'pubkey' },
  { key: 'meshcore-time-window', kind: 'scalar' },
  { key: 'meshcore-theme', kind: 'scalar' },
  { key: 'cs-theme-overrides', kind: 'scalar' },
];

// fakeServer keeps one account document with the semantics of
// cmd/server/settings_handlers.go. fail[METHOD] queues canned answers
// ('network' rejects the request).
function fakeServer() {
  const s = { rev: 0, doc: null, puts: [], gets: 0, deletes: 0, fail: { GET: [], PUT: [], DELETE: [] } };
  s.handle = (method, p, body) => {
    if (method === 'PUT') s.puts.push(body);
    if (method === 'GET') s.gets++;
    if (s.fail[method].length) return s.fail[method].shift();
    if (method === 'GET') return { status: 200, data: { revision: s.rev, doc: s.doc, allowlist: ALLOW } };
    if (method === 'PUT') {
      if (body.baseRevision !== s.rev) return { status: 409, data: { revision: s.rev, doc: s.doc } };
      s.rev++;
      s.doc = body.doc;
      return { status: 200, data: { revision: s.rev } };
    }
    s.deletes++;
    s.rev = 0;
    s.doc = null;
    return { status: 200, data: { ok: true } };
  };
  return s;
}

const AUTO_IDS = ['syncStatus', 'syncNow', 'syncDelete', 'syncMsg'];

// makeEnv loads the real module. opts: local {key: raw}, server,
// user (default {id: 7}; null = logged out), enabled (default true), hash.
function makeEnv(opts) {
  opts = opts || {};
  function Storage() { this.m = new Map(); }
  Storage.prototype.getItem = function (k) { return this.m.has(k) ? this.m.get(k) : null; };
  Storage.prototype.setItem = function (k, v) { this.m.set(k, String(v)); };
  Storage.prototype.removeItem = function (k) { this.m.delete(k); };
  const originalSet = Storage.prototype.setItem;
  const ls = new Storage();
  Object.keys(opts.local || {}).forEach((k) => ls.m.set(k, opts.local[k]));
  const server = opts.server || fakeServer();
  const timers = fakeTimers();
  const enabled = opts.enabled !== false;
  let user = opts.user === undefined ? { id: 7 } : opts.user;
  let logoutHandler = null;
  const winListeners = {}, docListeners = {}, els = {};
  const toasts = [], events = [], warnings = [], errors = [];
  const mkEl = (id) => {
    const el = { id, innerHTML: '', textContent: '', handlers: {}, cls: {} };
    el.classList = { toggle(c, on) { el.cls[c] = !!on; } };
    el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
    return el;
  };
  const auth = {
    isEnabled: () => enabled,
    user: () => user,
    ready: () => Promise.resolve(enabled ? user : null),
    request(method, p, body) {
      const r = server.handle(method, p, body === undefined ? undefined : JSON.parse(JSON.stringify(body)));
      if (r === 'network') return Promise.reject(new Error('offline'));
      return Promise.resolve({ ok: r.status < 400, status: r.status, data: r.data || {} });
    },
    notify(m) { toasts.push(m); },
    setLogoutHandler(fn) { logoutHandler = fn; },
  };
  const env = { navigations: 0, pipelines: 0 };
  const win = {
    localStorage: ls, Storage, CSAuth: auth, MC_USER_MGMT: enabled ? { enabled: true } : null,
    addEventListener(t, f) { (winListeners[t] = winListeners[t] || []).push(f); },
    dispatchEvent(e) { events.push(e); },
    navigate() { env.navigations++; },
    _customizerV2: { runPipeline() { env.pipelines++; } },
  };
  const doc = {
    visibilityState: 'visible',
    addEventListener(t, f) { docListeners[t] = f; },
    removeEventListener(t, f) { if (docListeners[t] === f) delete docListeners[t]; },
    getElementById(id) { return els[id] || (AUTO_IDS.indexOf(id) !== -1 ? (els[id] = mkEl(id)) : null); },
  };
  const ctx = {
    window: win, document: doc, location: { hash: opts.hash || '#/packets' },
    console: { warn: (m) => warnings.push(String(m)), error: (m) => errors.push(String(m)), log() {} },
    Promise,
    StorageEvent: function (type, init) { this.type = type; this.key = init.key; this.newValue = init.newValue; },
    escapeHtml: loadEscapeHtml(),
    setTimeout: timers.setTimeout, clearTimeout: timers.clearTimeout,
    setInterval: timers.setInterval, clearInterval: timers.clearInterval,
  };
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  Object.assign(env, {
    ls, server, timers, toasts, events, warnings, errors, els, doc, ctx, win, originalSet,
    api: win.CSSettingsSync, t: win.CSSettingsSync._test,
    get logoutHandler() { return logoutHandler; },
    login(u) { user = u; (winListeners['cs-auth-changed'] || []).forEach((f) => f({ detail: u })); return settle(); },
    logout() { user = null; (winListeners['cs-auth-changed'] || []).forEach((f) => f({ detail: null })); return settle(); },
    fireDoc(t) { if (docListeners[t]) docListeners[t](); return settle(); },
    el(id) { return els[id] || (els[id] = mkEl(id)); },
  });
  return env;
}

// ── mergeDocs ──
const M_ALLOW = [{ key: 'fav', kind: 'set' }, { key: 'nodes', kind: 'set', id: 'pubkey' }, { key: 'tw', kind: 'scalar' }];
const n = (pubkey, name) => ({ pubkey, name });
const MERGE_CASES = [
  // name, local, profile, base, keys, localChanges, differsFromProfile
  ['set: an item added here since the last sync is kept',
    { fav: J(['a', 'b']) }, { fav: J(['a']) }, { fav: J(['a']) }, { fav: J(['a', 'b']) }, [], true],
  ['set: an item removed here since the last sync is removed',
    { fav: J(['a']) }, { fav: J(['a', 'b']) }, { fav: J(['a', 'b']) }, { fav: J(['a']) }, [], true],
  ['set: an item added on another device arrives',
    { fav: J(['a']) }, { fav: J(['a', 'c']) }, { fav: J(['a']) }, { fav: J(['a', 'c']) }, ['fav'], false],
  ['set: an item removed on another device is not brought back',
    { fav: J(['a', 'b']) }, { fav: J(['a']) }, { fav: J(['a', 'b']) }, { fav: J(['a']) }, ['fav'], false],
  ['set: additions on both sides are both kept',
    { fav: J(['a', 'x']) }, { fav: J(['a', 'y']) }, { fav: J(['a']) }, { fav: J(['a', 'y', 'x']) }, ['fav'], true],
  ['set: without a baseline the lists merge by union',
    { fav: J(['x']) }, { fav: J(['y']) }, {}, { fav: J(['y', 'x']) }, ['fav'], true],
  ['set: same identity edited only here keeps the local version',
    { nodes: J([n('k1', 'new')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'new')]) }, [], true],
  ['set: same identity edited on both sides keeps the profile version',
    { nodes: J([n('k1', 'new')]) }, { nodes: J([n('k1', 'other')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'other')]) }, ['nodes'], false],
  ['set: same identity edited only in the profile arrives',
    { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'other')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'other')]) }, ['nodes'], false],
  ['scalar: changed only here wins',
    { tw: '60' }, { tw: '15' }, { tw: '15' }, { tw: '60' }, [], true],
  ['scalar: changed in the profile wins',
    { tw: '15' }, { tw: '180' }, { tw: '15' }, { tw: '180' }, ['tw'], false],
  ['scalar: changed on both sides, the profile wins',
    { tw: '60' }, { tw: '180' }, { tw: '15' }, { tw: '180' }, ['tw'], false],
  ['scalar: removed here wins',
    {}, { tw: '15' }, { tw: '15' }, {}, [], true],
  ['scalar: removed in the profile is removed here',
    { tw: '15' }, {}, { tw: '15' }, {}, ['tw'], false],
  ['set: the same list spelled differently is not a local change',
    { fav: '["a", "b"]' }, { fav: '["a","b"]' }, { fav: '["a","b"]' }, { fav: '["a", "b"]' }, [], true],
  ['keys outside the allowlist are ignored',
    { other: '1', tw: '15' }, { other: '2', tw: '15' }, {}, { tw: '15' }, [], false],
];

MERGE_CASES.forEach(([name, l, p, b, keys, changes, differs]) => {
  test('merge ' + name, () => {
    const env = makeEnv({ enabled: false });
    const r = plain(env.t.mergeDocs(l, p, b, M_ALLOW));
    assert.deepStrictEqual(r.keys, keys);
    assert.deepStrictEqual(r.localChanges, changes);
    assert.strictEqual(r.differsFromProfile, differs);
  });
});

test('merge: a set value that is not a JSON list merges as a scalar, with a warning', () => {
  const env = makeEnv({ enabled: false });
  const r = plain(env.t.mergeDocs({ fav: 'not json' }, { fav: J(['a']) }, { fav: J(['a']) }, M_ALLOW));
  assert.deepStrictEqual(r.keys, { fav: 'not json' });
  assert.strictEqual(env.warnings.length, 1);
  assert(env.warnings[0].indexOf('fav') !== -1, env.warnings[0]);
});

(async () => {
  let passed = 0, failed = 0;
  for (const t of tests) {
    try { await t.fn(); passed++; console.log('  ok   ' + t.name); }
    catch (e) { failed++; console.log('  FAIL ' + t.name + ': ' + (e && e.stack || e)); }
  }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})();
```

All later `test(...)` blocks (Tasks 5 and 6) go above the final `(async () => { ... })();` runner.

In `test-all.sh`, insert after line 187 (`node tests/unit/test-scope-audit-styles-linked.js`):

```sh
node tests/unit/test-settings-sync.js
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `node tests/unit/test-settings-sync.js`
Expected: FAIL with `ENOENT: no such file or directory, open '...public/settings-sync.js'`.

- [ ] **Step 3: Implement the merge**

Create `public/settings-sync.js`:

```js
/* Settings sync for optional user management
 * (docs/specs/2026-10-06-user-settings-sync-design.md). Inert unless
 * window.MC_USER_MGMT is on and CSAuth reports a logged-in user. While
 * active it wraps Storage.prototype.setItem/removeItem for the keys the
 * server allowlists, merges this device with the account's copy and
 * pushes changes. Exposes window.CSSettingsSync. */
(function () {
  'use strict';

  // ── Three-way merge (pure) ──
  // Values are raw localStorage strings; undefined means "not set".

  function own(o, k) { return o && Object.prototype.hasOwnProperty.call(o, k) ? o[k] : undefined; }

  // parseList returns the array behind raw ([] when unset), or null when
  // raw is not a JSON array.
  function parseList(raw) {
    if (raw === undefined) return [];
    try {
      var v = JSON.parse(raw);
      return Array.isArray(v) ? v : null;
    } catch (e) { return null; }
  }

  // identity of a set item: its idField when it is an object that has one,
  // else its JSON (a string item is compared as itself).
  function identity(item, idField) {
    if (idField && item && typeof item === 'object' && item[idField] != null) return 'f:' + String(item[idField]);
    return 'j:' + JSON.stringify(item);
  }

  function indexList(list, idField) {
    var map = Object.create(null), order = [];
    list.forEach(function (item) {
      var id = identity(item, idField);
      if (id in map) return;
      map[id] = item;
      order.push(id);
    });
    return { map: map, order: order };
  }

  function sameItem(a, b) { return JSON.stringify(a) === JSON.stringify(b); }

  // mergeSet computes P + (L - B) - (B - L) by identity. Returns the merged
  // raw string, undefined when neither side has the key, or null when a
  // side is not a JSON list (the caller then merges it as a scalar).
  function mergeSet(l, p, b, idField) {
    var al = parseList(l), ap = parseList(p), ab = parseList(b);
    if (!al || !ap || !ab) return null;
    var L = indexList(al, idField), P = indexList(ap, idField), B = indexList(ab, idField);
    var out = [];
    P.order.forEach(function (id) {
      var inL = id in L.map, inB = id in B.map;
      if (inB && !inL) return; // removed on this device since the last sync
      var onlyLocalChanged = inL && inB && !sameItem(L.map[id], B.map[id]) && sameItem(P.map[id], B.map[id]);
      out.push(onlyLocalChanged ? L.map[id] : P.map[id]);
    });
    L.order.forEach(function (id) {
      if (id in P.map || id in B.map) return; // in the profile, or removed on another device
      out.push(L.map[id]); // added on this device since the last sync
    });
    if (!out.length && l === undefined && p === undefined) return undefined;
    var s = JSON.stringify(out);
    // Keep an existing spelling of the same list: formatting alone is no change.
    if (l !== undefined && JSON.stringify(al) === s) return l;
    if (p !== undefined && JSON.stringify(ap) === s) return p;
    return s;
  }

  function mergeScalar(l, p, b) { return (l !== b && p === b) ? l : p; }

  // mergeDocs merges local, profile and baseline ({key: raw}) for every
  // allowlisted key. localChanges lists the keys this device must write;
  // differsFromProfile says the result must be pushed.
  function mergeDocs(local, profile, base, allowlist) {
    var keys = {}, localChanges = [], differs = false;
    allowlist.forEach(function (entry) {
      var k = entry.key, l = own(local, k), p = own(profile, k), b = own(base, k), v;
      if (entry.kind === 'set') {
        v = mergeSet(l, p, b, entry.id || '');
        if (v === null) {
          console.warn('[settings-sync] ' + k + ' is not a JSON list on this device or in the account; merged as one value');
          v = mergeScalar(l, p, b);
        }
      } else {
        v = mergeScalar(l, p, b);
      }
      if (v !== undefined) keys[k] = v;
      if (v !== l) localChanges.push(k);
      if (v !== p) differs = true;
    });
    return { keys: keys, localChanges: localChanges, differsFromProfile: differs };
  }

  window.CSSettingsSync = {
    _test: { mergeDocs: mergeDocs }
  };
})();
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `node tests/unit/test-settings-sync.js`
Expected: every line `ok`, last line `17 passed, 0 failed`. Then the suite: `GITHUB_REF_NAME=v3.13.1 PYTHONIOENCODING=utf-8 sh test-all.sh` ends with `All standalone frontend suites passed` (the inventory test sees the new file in `test-all.sh`).

- [ ] **Step 5: Commit**

```bash
git add public/settings-sync.js tests/unit/test-settings-sync.js test-all.sh
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
feat(ui): three-way merge for settings sync

Lists merge per item by identity: an item added on either side is kept,
an item removed on one side since the last sync stays removed. Single
values take the account's copy unless only this device changed them. A
list that does not parse merges as a single value, with a warning. Not
loaded by any page yet.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

### Task 5: Sync engine (interception, pull, push, apply)

**Files:**
- Modify: `public/settings-sync.js` (insert the engine between `mergeDocs` and `window.CSSettingsSync = {`, replace the export)
- Modify: `public/index.html` (after `<script src="auth.js?v=__BUST__"></script>`, line 190)
- Modify: `public/customize-v2.js` (`window._customizerV2` object, line 2962)
- Test: `tests/unit/test-settings-sync.js` (add tests above the runner)

**Interfaces:**
- Consumes: Task 4 `own`, `mergeDocs`; Task 3 JSON contract; `window.CSAuth.request(method, path, body) -> Promise<{ok, status, data}>` (rejects on network error), `CSAuth.notify(msg)`, `CSAuth.ready() -> Promise<user|null>`, `CSAuth.isEnabled()`, the `cs-auth-changed` window event (`detail` = user or null) (`public/auth.js`); `window.navigate()` (`app.js:1155`); `window._customizerV2.runPipeline()` (added here).
- Produces (inside the IIFE, used by Task 6): `state` (fields `active, userId, policy, base, rev, hold, firstUpload, dirty, seq, pushing, pushTimer, retryTimer, pullTimer, backoff, blocked, status, lastSyncedAt, tooLarge`), `rawGet`, `rawRemove`, `BASE_KEY`, `REV_KEY`, `saveBase(keys, rev, hold)`, `setStatus(s)`, `push() -> Promise<boolean>`, `pull() -> Promise`, `syncNow() -> Promise`, `activate(user)`, `deactivate()`, `midEdit() -> boolean`. Export: `window.CSSettingsSync = { syncNow, _test: { mergeDocs, state, activate, deactivate, pull, push, midEdit } }`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/unit/test-settings-sync.js`, above the runner:

```js
// ── engine ──
const BASE = (user, keys, rev, hold) => ({ 'cs-settings-sync-base': J({ user, keys, hold: !!hold }), 'cs-settings-sync-rev': String(rev) });
const synced = (keys, rev) => Object.assign({}, keys, BASE(7, keys, rev));
const serverWith = (rev, keys) => { const s = fakeServer(); s.rev = rev; s.doc = rev ? { v: 1, keys } : null; return s; };

test('feature off: no request and no interception', async () => {
  const env = makeEnv({ enabled: false });
  await env.timers.advance(120000);
  assert.strictEqual(env.server.gets + env.server.puts.length, 0);
  assert.strictEqual(env.win.Storage.prototype.setItem, env.originalSet);
});

test('logged out: no request and no interception', async () => {
  const env = makeEnv({ user: null });
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(120000);
  assert.strictEqual(env.server.gets + env.server.puts.length, 0);
  assert.strictEqual(env.win.Storage.prototype.setItem, env.originalSet);
});

test('first login with an empty profile uploads this device (never channel keys)', async () => {
  const env = makeEnv({ user: null, local: { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '60', corescope_channel_keys: '{"#x":"00"}', 'meshcore-api-key': 'k' } });
  await env.login({ id: 7 });
  assert.strictEqual(env.server.puts.length, 1);
  assert.deepStrictEqual(env.server.puts[0], { baseRevision: 0, doc: { v: 1, keys: { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '60' } } });
  assert.deepStrictEqual(env.toasts, ['Your settings are now saved to your account.']);
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '1');
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).user, 7);
});

test('first login with nothing to upload sends nothing', async () => {
  const env = makeEnv({ user: null });
  await env.login({ id: 7 });
  assert.strictEqual(env.server.puts.length, 0);
  assert.strictEqual(env.t.state.status, 'idle');
});

test('login on another device merges the profile, applies theme and customizer, re-renders', async () => {
  const server = serverWith(4, { 'meshcore-favorites': J(['a']), 'meshcore-theme': 'dark', 'cs-theme-overrides': '{"x":1}' });
  const env = makeEnv({ user: null, server, local: { 'meshcore-favorites': J(['b']) } });
  await env.login({ id: 7 });
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a', 'b']));
  assert.strictEqual(env.ls.getItem('meshcore-theme'), 'dark');
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'meshcore-theme' && e.newValue === 'dark'));
  assert.strictEqual(env.pipelines, 1);
  assert.strictEqual(env.navigations, 1);
  assert(env.toasts.indexOf('Settings updated from another device') !== -1);
  // b was only here: pushed on top of revision 4.
  assert.strictEqual(server.puts.length, 1);
  assert.strictEqual(server.puts[0].baseRevision, 4);
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a', 'b']));
});

test('interception: allowlisted writes push once, 2 s after the last; other keys never', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const env = makeEnv({ server: serverWith(1, keys), local: synced(keys, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-time-window', '60');
  env.ls.setItem('meshcore-time-window', '180');
  env.ls.setItem('corescope_channel_keys', '{"#x":"00"}');
  env.ls.setItem('meshcore-api-key', 'k');
  env.ls.setItem('panel-drag-packets', '1');
  await env.timers.advance(1999);
  assert.strictEqual(env.server.puts.length, 0);
  await env.timers.advance(1);
  assert.strictEqual(env.server.puts.length, 1);
  assert.deepStrictEqual(env.server.puts[0].doc.keys, { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '180' });
  env.ls.removeItem('meshcore-time-window');
  await env.timers.advance(2000);
  assert.strictEqual(env.server.puts.length, 2);
  env.ls.setItem('corescope_channel_keys', '{}');
  await env.timers.advance(10000);
  assert.strictEqual(env.server.puts.length, 2);
});

test('writing the same value again does not push', async () => {
  const keys = { 'meshcore-theme': 'dark' };
  const env = makeEnv({ server: serverWith(1, keys), local: synced(keys, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-theme', 'dark');
  env.ls.removeItem('meshcore-time-window');
  await env.timers.advance(10000);
  assert.strictEqual(env.server.puts.length, 0);
});

test('values applied from the account do not trigger a push', async () => {
  const env = makeEnv({ server: serverWith(2, { 'meshcore-time-window': '180' }), local: synced({ 'meshcore-time-window': '60' }, 1) });
  await env.timers.advance(10000);
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '180');
  assert.strictEqual(env.server.puts.length, 0);
});

test('409: merges the returned document and retries on top of it', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, local: synced(keys, 1) });
  await env.timers.advance(0);
  server.rev = 2;
  server.doc = { v: 1, keys: { 'meshcore-favorites': J(['a', 'c']) } }; // another device
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(server.puts.length, 2);
  assert.strictEqual(server.puts[1].baseRevision, 2);
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a', 'c', 'b']));
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a', 'c', 'b']));
});

test('409 more than three times falls back to the retry backoff', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, local: synced(keys, 1) });
  await env.timers.advance(0);
  const conflict = { status: 409, data: { revision: 1, doc: { v: 1, keys } } };
  server.fail.PUT = [conflict, conflict, conflict, conflict];
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(server.puts.length, 4);
  assert.strictEqual(env.t.state.status, 'retrying');
  assert.deepStrictEqual(env.timers.delays(), [2000]);
});

test('network errors, 5xx and 429 back off from 2 s doubling to 5 min; success resets', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, local: synced(keys, 1) });
  env.doc.visibilityState = 'hidden'; // no minute pulls in between
  await env.timers.advance(0);
  const expected = [2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 300000];
  server.fail.PUT = expected.map((_, i) => ['network', { status: 503, data: {} }, { status: 429, data: {} }][i % 3]);
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  for (const d of expected) {
    assert.deepStrictEqual(env.timers.delays(), [d]);
    assert.strictEqual(env.t.state.status, 'retrying');
    await env.timers.advance(d);
  }
  assert.strictEqual(env.t.state.status, 'ok');
  assert.strictEqual(env.t.state.backoff, 2000);
  assert.strictEqual(server.doc.keys['meshcore-time-window'], '60');
});

test('tab focus pulls; the minute pull runs only while visible', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  const g0 = env.server.gets;
  await env.fireDoc('visibilitychange');
  assert.strictEqual(env.server.gets, g0 + 1);
  await env.timers.advance(60000);
  assert.strictEqual(env.server.gets, g0 + 2);
  env.doc.visibilityState = 'hidden';
  await env.timers.advance(180000);
  assert.strictEqual(env.server.gets, g0 + 2);
});

test('413 stops pushing and names the largest keys; Sync now tries again', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 413, data: { error: 'too large' } }];
  env.ls.setItem('cs-theme-overrides', 'x'.repeat(100));
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.t.state.status, 'too-large');
  assert.strictEqual(env.t.state.tooLarge[0], 'cs-theme-overrides');
  env.ls.setItem('meshcore-time-window', '15');
  await env.timers.advance(10000);
  assert.strictEqual(server.puts.length, 1);
  await env.api.syncNow();
  await settle();
  assert.strictEqual(server.puts.length, 2);
  assert.strictEqual(env.t.state.status, 'ok');
});

test('400 stops pushing until reload and logs the reason', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 400, data: { error: 'key "x" is not a synced setting' } }];
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.t.state.status, 'rejected');
  assert.strictEqual(env.errors.length, 1);
  env.ls.setItem('meshcore-time-window', '15');
  await env.api.syncNow();
  await env.timers.advance(10000);
  assert.strictEqual(server.puts.length, 1);
});

test('account copy deleted elsewhere: values stay, no upload until the next change', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const env = makeEnv({ server: serverWith(0, null), local: synced(keys, 3) });
  await env.timers.advance(60000);
  assert.strictEqual(env.server.puts.length, 0);
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).hold, true);
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '0');
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(env.server.puts.length, 1);
  assert.deepStrictEqual(env.server.puts[0], { baseRevision: 0, doc: { v: 1, keys: { 'meshcore-favorites': J(['a', 'b']) } } });
});

test("another user's baseline counts as none: nothing is removed", async () => {
  const local = Object.assign({ 'meshcore-favorites': J(['a', 'b']) }, BASE(99, { 'meshcore-favorites': J(['a', 'b', 'c']) }, 5));
  const env = makeEnv({ server: serverWith(2, { 'meshcore-favorites': J(['c']) }), local });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['c', 'a', 'b']));
});

test('mid-edit: on an account page or with the geofilter editor open, no re-render', async () => {
  const env = makeEnv({ hash: '#/account', server: serverWith(2, { 'meshcore-time-window': '180' }), local: synced({ 'meshcore-time-window': '60' }, 1) });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '180');
  assert.strictEqual(env.navigations, 0);
  assert(env.toasts.indexOf('Settings updated from another device') !== -1);
  env.ctx.location.hash = '#/packets';
  assert.strictEqual(env.t.midEdit(), false);
  env.el('cv2-gf-modal-overlay');
  assert.strictEqual(env.t.midEdit(), true);
  env.ctx.location.hash = '#/accounts-other';
  delete env.els['cv2-gf-modal-overlay'];
  assert.strictEqual(env.t.midEdit(), false);
});

test('logout makes the module inert: wrap removed, no timers, no requests', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  assert.notStrictEqual(env.win.Storage.prototype.setItem, env.originalSet);
  await env.logout();
  assert.strictEqual(env.win.Storage.prototype.setItem, env.originalSet);
  const before = env.server.gets + env.server.puts.length;
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(180000);
  assert.strictEqual(env.server.gets + env.server.puts.length, before);
  assert.deepStrictEqual(env.timers.delays(), []);
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `node tests/unit/test-settings-sync.js`
Expected: the Task 4 merge tests pass; the new engine tests FAIL (for example `first login ...: Expected values to be strictly equal: 0 !== 1`, `env.api.syncNow is not a function`).

- [ ] **Step 3: Implement the engine**

In `public/settings-sync.js`, insert this block after `mergeDocs` and before `window.CSSettingsSync = {`:

```js
  // ── Sync engine ──

  var BASE_KEY = 'cs-settings-sync-base';
  var REV_KEY = 'cs-settings-sync-rev';
  var PUSH_DELAY_MS = 2000;
  var PULL_EVERY_MS = 60000;
  var BACKOFF_MIN_MS = 2000;
  var BACKOFF_MAX_MS = 300000;
  var MAX_CONFLICT_RETRIES = 3;
  // Keys whose change an existing storage listener applies (app.js, cb-presets.js).
  var LISTENER_KEYS = { 'meshcore-theme': true, 'meshcore-cb-preset': true };

  // Assigning localStorage.setItem would store a key named "setItem"
  // (Storage has a named-property setter), so the wrap goes on the prototype.
  var proto = window.Storage.prototype;
  var origGet = proto.getItem, origSet = proto.setItem, origRemove = proto.removeItem;

  var state = {
    active: false, userId: null, policy: null,
    base: {}, rev: 0, hold: false, firstUpload: false,
    dirty: false, seq: 0, pushing: null, pushTimer: null, retryTimer: null, pullTimer: null,
    backoff: BACKOFF_MIN_MS, blocked: null, status: 'idle', lastSyncedAt: null, tooLarge: []
  };

  function rawGet(k) { var v = origGet.call(window.localStorage, k); return v === null ? undefined : v; }
  function rawSet(k, v) { origSet.call(window.localStorage, k, v); }
  function rawRemove(k) { origRemove.call(window.localStorage, k); }

  function setPolicy(list) {
    var byKey = Object.create(null);
    list.forEach(function (e) { byKey[e.key] = e; });
    state.policy = { list: list, byKey: byKey };
  }

  // pick keeps the allowlisted keys of o.
  function pick(o) {
    var out = {};
    state.policy.list.forEach(function (e) { var v = own(o, e.key); if (v !== undefined) out[e.key] = v; });
    return out;
  }

  function snapshot() {
    var out = {};
    state.policy.list.forEach(function (e) { var v = rawGet(e.key); if (v !== undefined) out[e.key] = v; });
    return out;
  }

  function sameKeys(a, b) {
    var ka = Object.keys(a);
    return ka.length === Object.keys(b).length && ka.every(function (k) { return own(b, k) === a[k]; });
  }

  // The baseline is the last document this device and the account agreed
  // on. It belongs to one user: another user's baseline counts as none.
  function loadBase(userId) {
    try {
      var b = JSON.parse(rawGet(BASE_KEY) || 'null');
      if (b && b.user === userId && b.keys && typeof b.keys === 'object') {
        return { keys: b.keys, rev: Number(rawGet(REV_KEY)) || 0, hold: !!b.hold };
      }
    } catch (e) { /* a damaged baseline counts as none */ }
    return { keys: {}, rev: 0, hold: false };
  }

  function saveBase(keys, rev, hold) {
    state.base = keys;
    state.rev = rev;
    state.hold = hold;
    rawSet(BASE_KEY, JSON.stringify({ user: state.userId, keys: keys, hold: hold }));
    rawSet(REV_KEY, String(rev));
  }

  function setStatus(s) { state.status = s; }

  function watched(storage, k) {
    return state.active && !!state.policy && storage === window.localStorage && !!state.policy.byKey[k];
  }

  function install() {
    proto.setItem = function (k, v) {
      var watch = watched(this, k), before = watch ? origGet.call(this, k) : null;
      origSet.call(this, k, v);
      if (watch && before !== String(v)) markDirty();
    };
    proto.removeItem = function (k) {
      var watch = watched(this, k), before = watch ? origGet.call(this, k) : null;
      origRemove.call(this, k);
      if (watch && before !== null) markDirty();
    };
  }

  function uninstall() {
    proto.setItem = origSet;
    proto.removeItem = origRemove;
  }

  function markDirty() {
    state.dirty = true;
    state.seq++;
    if (state.hold) saveBase(state.base, state.rev, false); // the next change starts a new document
    schedulePush(PUSH_DELAY_MS);
  }

  function schedulePush(ms) {
    clearTimeout(state.pushTimer);
    state.pushTimer = setTimeout(function () { state.pushTimer = null; push(); }, ms);
  }

  function retryLater() {
    setStatus('retrying');
    clearTimeout(state.retryTimer);
    var wait = state.backoff;
    state.backoff = Math.min(state.backoff * 2, BACKOFF_MAX_MS);
    state.retryTimer = setTimeout(function () { state.retryTimer = null; push(); }, wait);
  }

  function synced(seq) {
    state.backoff = BACKOFF_MIN_MS;
    state.lastSyncedAt = new Date();
    clearTimeout(state.retryTimer);
    state.retryTimer = null;
    if (state.seq === seq) state.dirty = false;
    else schedulePush(PUSH_DELAY_MS); // written again while the request was out
    setStatus('ok');
  }

  function largestKeys(keys) {
    return Object.keys(keys).sort(function (a, b) { return keys[b].length - keys[a].length; }).slice(0, 3);
  }

  // midEdit: re-rendering now would throw away unsaved input. Account
  // pages hold forms; the geofilter editor (customize-v2.js) is a modal
  // over the page. The page the user opens next reads the new values.
  function midEdit() {
    return /^#\/account(\/|\?|$)/.test(location.hash) || !!document.getElementById('cv2-gf-modal-overlay');
  }

  // afterRemoteChange makes the running page show values that arrived from
  // the account: storage listeners for theme and colour-blind preset, the
  // customizer pipeline for its overrides, and a router re-render for
  // everything a page reads at init.
  function afterRemoteChange(changed) {
    changed.forEach(function (k) {
      if (!LISTENER_KEYS[k]) return;
      var v = rawGet(k);
      window.dispatchEvent(new StorageEvent('storage', { key: k, newValue: v === undefined ? null : v }));
    });
    if (changed.indexOf('cs-theme-overrides') !== -1 && window._customizerV2) window._customizerV2.runPipeline();
    window.CSAuth.notify('Settings updated from another device');
    if (!midEdit()) window.navigate();
  }

  function writeLocal(keys, changed) {
    changed.forEach(function (k) {
      var v = own(keys, k);
      try {
        if (v === undefined) rawRemove(k); else rawSet(k, v);
      } catch (e) { console.warn('[settings-sync] could not store ' + k + ': ' + e.message); }
    });
  }

  // applyProfile brings the account's document into this device: merge,
  // write what changed, make it the new baseline. Returns true when this
  // device holds values the account lacks.
  function applyProfile(rev, doc) {
    var local = snapshot();
    if (!rev) {
      if (state.rev > 0 || state.hold) {
        // The account's copy was deleted, here or on another device. Keep
        // this device as it is; its next change starts a new document.
        saveBase({}, 0, true);
        return false;
      }
      state.firstUpload = true; // first login: this device's values form the first document
      return Object.keys(local).length > 0;
    }
    var profile = pick((doc && doc.keys) || {});
    var m = mergeDocs(local, profile, state.base, state.policy.list);
    writeLocal(m.keys, m.localChanges);
    saveBase(profile, rev, false);
    if (m.localChanges.length) afterRemoteChange(m.localChanges);
    return m.differsFromProfile;
  }

  // push sends this device's allowlisted values. A 409 merges the returned
  // document and retries (at most MAX_CONFLICT_RETRIES); network errors,
  // 5xx and 429 retry with backoff. Resolves true when the account holds
  // this device's values.
  function push(attempt) {
    if (!state.active || state.blocked || !state.policy) return Promise.resolve(false);
    if (state.pushing) return state.pushing.then(function () { return state.dirty ? push() : true; });
    attempt = attempt || 0;
    var keys = snapshot(), seq = state.seq, uid = state.userId;
    if (state.rev > 0 && sameKeys(keys, state.base)) { synced(seq); return Promise.resolve(true); }
    setStatus('syncing');
    var p = window.CSAuth.request('PUT', '/api/account/settings', { baseRevision: state.rev, doc: { v: 1, keys: keys } })
      .then(function (r) {
        state.pushing = null;
        if (!state.active || state.userId !== uid) return false; // logged out, or another user, meanwhile
        if (r.ok) {
          saveBase(keys, r.data.revision, false);
          synced(seq);
          if (state.firstUpload) {
            state.firstUpload = false;
            window.CSAuth.notify('Your settings are now saved to your account.');
          }
          return true;
        }
        if (r.status === 409 && attempt < MAX_CONFLICT_RETRIES) {
          applyProfile(r.data.revision, r.data.doc);
          return push(attempt + 1);
        }
        if (r.status === 413) {
          state.blocked = 'too-large';
          state.tooLarge = largestKeys(keys);
          setStatus('too-large');
          return false;
        }
        if (r.status === 400) {
          console.error('[settings-sync] the server refused the settings: ' + (r.data && r.data.error));
          state.blocked = 'rejected';
          setStatus('rejected');
          return false;
        }
        if (r.status !== 401) retryLater(); // 401: auth.js logged out, which deactivates this module
        return false;
      }, function () {
        state.pushing = null;
        if (state.active && state.userId === uid) retryLater();
        return false;
      });
    state.pushing = p;
    return p;
  }

  function pull() {
    if (!state.active) return Promise.resolve();
    if (state.pushing) return state.pushing.then(pull);
    var uid = state.userId;
    return window.CSAuth.request('GET', '/api/account/settings').then(function (r) {
      if (!state.active || state.userId !== uid) return;
      if (!r.ok) { if (r.status !== 401) setStatus('retrying'); return; }
      setPolicy(r.data.allowlist || []);
      if (applyProfile(r.data.revision, r.data.doc)) {
        state.dirty = true;
        state.seq++;
        return push();
      }
      if (state.dirty) return push();
      if (r.data.revision) { state.lastSyncedAt = new Date(); setStatus('ok'); }
      else setStatus(state.hold ? 'held' : 'idle');
    }, function () { if (state.active) setStatus('retrying'); });
  }

  // syncNow: the account page button. Retries after a 413 (the user may
  // have trimmed), never after a 400 (that needs a reload).
  function syncNow() {
    if (!state.active) return Promise.resolve();
    if (state.blocked === 'too-large') state.blocked = null;
    state.backoff = BACKOFF_MIN_MS;
    clearTimeout(state.retryTimer);
    state.retryTimer = null;
    return pull();
  }

  function onVisible() {
    if (document.visibilityState !== 'visible') return;
    clearTimeout(state.retryTimer);
    state.retryTimer = null;
    pull();
  }

  function activate(user) {
    if (state.active && state.userId === user.id) return Promise.resolve();
    if (state.active) deactivate();
    var b = loadBase(user.id);
    state.active = true;
    state.userId = user.id;
    state.policy = null;
    state.base = b.keys;
    state.rev = b.rev;
    state.hold = b.hold;
    state.firstUpload = false;
    state.dirty = false;
    state.blocked = null;
    state.backoff = BACKOFF_MIN_MS;
    install();
    document.addEventListener('visibilitychange', onVisible);
    state.pullTimer = setInterval(function () { if (document.visibilityState === 'visible') pull(); }, PULL_EVERY_MS);
    return pull();
  }

  function deactivate() {
    state.active = false;
    clearTimeout(state.pushTimer);
    clearTimeout(state.retryTimer);
    clearInterval(state.pullTimer);
    state.pushTimer = state.retryTimer = state.pullTimer = null;
    document.removeEventListener('visibilitychange', onVisible);
    uninstall();
    state.policy = null;
    state.userId = null;
    state.pushing = null;
    setStatus('idle');
  }

  window.addEventListener('cs-auth-changed', function (e) {
    if (!window.CSAuth.isEnabled()) return;
    if (e.detail) activate(e.detail);
    else if (state.active) deactivate();
  });
  window.CSAuth.ready().then(function (u) { if (u && window.CSAuth.isEnabled()) activate(u); });
```

Replace the export at the end with:

```js
  window.CSSettingsSync = {
    syncNow: syncNow,
    _test: { mergeDocs: mergeDocs, state: state, activate: activate, deactivate: deactivate, pull: pull, push: push, midEdit: midEdit }
  };
```

- [ ] **Step 4: Load the module and export the customizer pipeline**

In `public/index.html`, after line 190 (`<script src="auth.js?v=__BUST__"></script>`):

```html
  <script src="settings-sync.js?v=__BUST__"></script>
```

In `public/customize-v2.js`, inside `window._customizerV2 = {`, after `computeEffective: computeEffective,` add:

```js
    // Settings sync (settings-sync.js) re-applies overrides that arrived from the account.
    runPipeline: _runPipeline,
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `node tests/unit/test-settings-sync.js`
Expected: all `ok`, `35 passed, 0 failed`.
Run: `GITHUB_REF_NAME=v3.13.1 PYTHONIOENCODING=utf-8 sh test-all.sh`
Expected: `All standalone frontend suites passed` (customizer tests still pass with the extra export).

- [ ] **Step 6: Commit**

```bash
git add public/settings-sync.js public/index.html public/customize-v2.js tests/unit/test-settings-sync.js
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
feat(ui): sync allowlisted settings between devices

While a user is logged in (and only then), settings-sync.js wraps
Storage.prototype.setItem/removeItem for the keys the server allowlists
and pushes 2 s after the last change. It pulls on login, page load, tab
focus and every minute while visible, merges with the per-device
baseline, writes what changed without pushing it back, applies theme
and colour-blind preset through their storage listeners, re-runs the
customizer pipeline and re-renders the page (not on account pages or
with the geofilter editor open). 409 merges and retries up to 3 times;
network errors, 5xx and 429 back off from 2 s to 5 min. When the
account's copy was deleted, a device keeps its values and uploads on
its next change. Feature off or logged out: no request, no wrap.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

### Task 6: Logout dialog and the "Settings sync" account section

**Files:**
- Modify: `public/auth.js` (`logout`, the header logout click handler at line 128-132, the `window.CSAuth` object)
- Modify: `public/account.js` (`profileHtml`, `views.profile`)
- Modify: `public/settings-sync.js` (dialog, logout, section; `setStatus`, `activate`, `deactivate`, export)
- Modify: `public/account.css` (append)
- Modify: `tests/unit/test-user-management-ui.js` (`loadAccount` signature, new tests)
- Modify: `tests/unit/test-settings-sync.js` (new tests above the runner)
- Modify: `tests/e2e/test-user-management-e2e.js` (the existing account-page logout step answers the dialog)

**Interfaces:**
- Consumes: Task 5 `state`, `rawRemove`, `BASE_KEY`, `REV_KEY`, `saveBase`, `push`, `syncNow`, `activate`, `deactivate`; global `escapeHtml` (`app.js:1087`); `.modal-overlay`, `.modal` (`style.css:1624-1640`); `.account-btn*`, `.account-msg`, `.account-hint`, `.account-actions` (`account.css`).
- Produces:
  - `CSAuth.setLogoutHandler(fn | null)`. `fn()` returns a Promise of `{cancel?: true, afterLogout?: function}`. `CSAuth.logout(next)` resolves `{ok: false, cancelled: true, status: 0, data: {}}` on cancel; `afterLogout` runs after a successful POST, before the user is cleared.
  - `CSSettingsSync.mountSection(el)`: renders `#syncStatus`, `#syncNow`, `#syncDelete`, `#syncMsg` into `el`.
  - `_test` adds `flush, onLogout, deleteRemote, statusText, useDialog(fn)`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/unit/test-settings-sync.js`, above the runner:

```js
// ── logout dialog and account section ──
const KEYS = { 'meshcore-favorites': J(['a']) };

test('the logout handler is registered only while active', async () => {
  const off = makeEnv({ enabled: false });
  await settle();
  assert.strictEqual(off.logoutHandler, null);
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  assert.strictEqual(typeof env.logoutHandler, 'function');
  await env.logout();
  assert.strictEqual(env.logoutHandler, null);
});

test('logout dialog: keep first and focused, channel-key text, three choices', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  let seen = null;
  env.t.useDialog((opts) => { seen = plain(opts); return Promise.resolve('keep'); });
  await env.logoutHandler();
  assert.deepStrictEqual(seen.choices.map((c) => c.id), ['keep', 'remove', 'cancel']);
  assert.strictEqual(seen.choices[0].label, 'Keep my settings on this device');
  assert.strictEqual(seen.choices[0].primary, true);
  assert.strictEqual(seen.choices[1].label, 'Remove my settings from this device');
  assert(seen.text.join(' ').indexOf('Channel keys are never synced') !== -1);
});

test('logout dialog: keep pushes pending changes first and leaves local data', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-time-window', '60'); // still in the 2 s debounce
  env.t.useDialog(() => Promise.resolve('keep'));
  const h = plain(await env.logoutHandler());
  assert.deepStrictEqual(h, {});
  assert.strictEqual(env.server.puts.length, 1);
  assert.strictEqual(env.server.doc.keys['meshcore-time-window'], '60');
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '60');
});

test('logout dialog: remove deletes synced keys and the baseline, never channel keys', async () => {
  const local = Object.assign({ 'meshcore-time-window': '60', corescope_channel_keys: '{"#x":"00"}', 'meshcore-api-key': 'k' }, synced(KEYS, 1));
  const env = makeEnv({ server: serverWith(1, KEYS), local });
  await env.timers.advance(0);
  env.t.useDialog(() => Promise.resolve('remove'));
  const h = await env.logoutHandler();
  assert.strictEqual(typeof h.afterLogout, 'function');
  h.afterLogout();
  for (const k of ['meshcore-favorites', 'meshcore-time-window', 'cs-settings-sync-base', 'cs-settings-sync-rev']) {
    assert.strictEqual(env.ls.getItem(k), null, k + ' left behind');
  }
  assert.strictEqual(env.ls.getItem('corescope_channel_keys'), '{"#x":"00"}');
  assert.strictEqual(env.ls.getItem('meshcore-api-key'), 'k');
});

test('logout dialog: remove after a failed push keeps the data and says so', async () => {
  const server = serverWith(1, KEYS);
  const env = makeEnv({ server, local: synced(KEYS, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-time-window', '60');
  server.fail.PUT = ['network'];
  env.t.useDialog(() => Promise.resolve('remove'));
  const h = await env.logoutHandler();
  h.afterLogout();
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '60');
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
  assert(env.toasts.indexOf('Your latest settings could not be saved to your account, so they stay on this device.') !== -1);
});

test('logout dialog: Cancel, Escape or the backdrop cancel the logout', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  for (const choice of ['cancel', null]) {
    env.t.useDialog(() => Promise.resolve(choice));
    assert.deepStrictEqual(plain(await env.logoutHandler()), { cancel: true });
  }
});

test('account section: status, Sync now, delete with confirmation', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  const el = env.el('syncSection');
  env.api.mountSection(el);
  assert(el.innerHTML.indexOf('id="syncNow"') !== -1 && el.innerHTML.indexOf('id="syncDelete"') !== -1);
  assert(el.innerHTML.indexOf('Delete synced settings from my account') !== -1);
  assert(env.els.syncStatus.textContent.indexOf('Last synced ') === 0, env.els.syncStatus.textContent);
  const g0 = env.server.gets;
  await env.els.syncNow.handlers.click();
  await settle();
  assert.strictEqual(env.server.gets, g0 + 1);
  env.t.useDialog(() => Promise.resolve('cancel'));
  await env.els.syncDelete.handlers.click();
  assert.strictEqual(env.server.deletes, 0);
  env.t.useDialog(() => Promise.resolve('delete'));
  await env.els.syncDelete.handlers.click();
  await settle();
  assert.strictEqual(env.server.deletes, 1);
  assert.strictEqual(env.t.state.hold, true);
  assert.strictEqual(env.els.syncMsg.textContent, 'Synced settings deleted from your account.');
  assert.strictEqual(env.els.syncStatus.textContent, 'No settings saved in your account. Your next change starts a new copy.');
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
});

test('status texts', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  const st = env.t.state;
  st.status = 'retrying';
  assert.strictEqual(env.t.statusText(), 'Not synced: retrying');
  st.status = 'too-large'; st.tooLarge = ['cs-theme-overrides', 'meshcore-my-nodes'];
  assert.strictEqual(env.t.statusText(), 'Not synced: your settings are larger than your account can hold. Largest: cs-theme-overrides, meshcore-my-nodes');
  st.status = 'idle';
  assert.strictEqual(env.t.statusText(), 'Not synced yet');
});
```

In `tests/unit/test-user-management-ui.js`:

1. Change the `loadAccount` signature and the `win` line so a test can add globals:

```js
function loadAccount(hash, routes, extraWin) {
```
```js
  const win = Object.assign({ CSAuth, addEventListener(t, fn) { listeners[t] = fn; } }, extraWin || {});
```

2. Add after the test `'a refused logout keeps the user and the view'`:

```js
test('logout handler: cancel keeps the session and posts nothing', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: { ok: true } });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  env.win.CSAuth.setLogoutHandler(() => Promise.resolve({ cancel: true }));
  const r = await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(r.cancelled, true);
  assert.strictEqual(r.ok, false);
  assert.strictEqual(env.calls.length, 0);
  assert.strictEqual(env.win.CS_USER.displayName, 'Ann');
  assert.strictEqual(env.loc.hash, '#/account');
});

test('logout handler: afterLogout runs after the POST, before the user is cleared', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: { ok: true } });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  let userAtAfter = 'not called', postedBefore = false;
  env.win.CSAuth.setLogoutHandler(() => Promise.resolve({ afterLogout() { userAtAfter = env.win.CS_USER; postedBefore = env.calls.length === 1; } }));
  const r = await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(r.ok, true);
  assert.strictEqual(postedBefore, true);
  assert.strictEqual(userAtAfter.displayName, 'Ann');
  assert.strictEqual(env.win.CS_USER, null);
});

test('logout handler: a refused POST does not run afterLogout', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 403, body: { error: 'no' } });
  await env.win.CSAuth.ready();
  let ran = false;
  env.win.CSAuth.setLogoutHandler(() => Promise.resolve({ afterLogout() { ran = true; } }));
  await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(ran, false);
});
```

3. Add after the test `'profile view: a refused logout shows the error next to the button'`:

```js
test('profile view: a cancelled logout shows no message', async () => {
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.setLogout({ ok: false, cancelled: true, status: 0, data: {} });
  env.t.views.profile({ set innerHTML(v) {} });
  await env.els.accountPageLogout.handlers.click();
  assert.strictEqual(env.els.logoutMsg.textContent, '');
});

test('profile view mounts the settings sync section only when the module is loaded', () => {
  const mounted = [];
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }), { CSSettingsSync: { mountSection(el) { mounted.push(el); } } });
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  const app = { innerHTML: '' };
  env.t.views.profile(app);
  assert(app.innerHTML.indexOf('<h3>Settings sync</h3><div id="syncSection"></div>') !== -1);
  assert.strictEqual(mounted.length, 1);
  assert.strictEqual(mounted[0], env.els.syncSection);
  const without = loadAccount('#/account', () => ({}));
  assert.strictEqual(without.t.profileHtml({ email: 'a', role: 'user', displayName: 'A' }).indexOf('syncSection'), -1);
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `node tests/unit/test-settings-sync.js && node tests/unit/test-user-management-ui.js`
Expected: FAIL, for example `env.logoutHandler` is `null` while active, `env.t.useDialog is not a function`, `env.win.CSAuth.setLogoutHandler is not a function`, and `syncSection` missing from the profile HTML.

- [ ] **Step 3: Make logout cancellable in `auth.js`**

In `public/auth.js`, replace the `logout` function and its comment with:

```js
  // An optional handler (settings-sync.js) runs first: it may cancel the
  // logout or hand back afterLogout, which runs once the server ended the
  // session and before the user is cleared.
  var logoutHandler = null;
  function setLogoutHandler(fn) { logoutHandler = fn; }

  // logout ends the session on the server. Only when that succeeded does it
  // move to next and then clear the user, so pages listening for
  // 'cs-auth-changed' already see the new view. A refusal keeps the user
  // and is returned for the caller to show; a cancel returns cancelled.
  function logout(next) {
    return Promise.resolve(logoutHandler ? logoutHandler() : null).then(function (h) {
      h = h || {};
      if (h.cancel) return { ok: false, cancelled: true, status: 0, data: {} };
      return request('POST', '/api/auth/logout').then(function (r) {
        if (r.ok) {
          if (h.afterLogout) h.afterLogout();
          location.hash = next;
          setUser(null);
        }
        return r;
      });
    });
  }
```

In the header logout click handler, change

```js
        if (!r.ok) notify((r.data && r.data.error) || ('Logout failed (HTTP ' + r.status + ')'));
```
to
```js
        if (!r.ok && !r.cancelled) notify((r.data && r.data.error) || ('Logout failed (HTTP ' + r.status + ')'));
```

In `window.CSAuth = {`, after `logout: logout,` add `setLogoutHandler: setLogoutHandler,`.

- [ ] **Step 4: Mount the section and ignore a cancel in `account.js`**

In `profileHtml`, change

```js
      '<h3>Devices</h3><ul class="account-sessions" id="sessList"></ul>' + msgBox('sessMsg') +
```
to
```js
      '<h3>Devices</h3><ul class="account-sessions" id="sessList"></ul>' + msgBox('sessMsg') +
      (window.CSSettingsSync ? '<h3>Settings sync</h3><div id="syncSection"></div>' : '') +
```

In `views.profile`, after `document.getElementById('profName').value = u.displayName;` add:

```js
      if (window.CSSettingsSync) window.CSSettingsSync.mountSection(document.getElementById('syncSection'));
```

In the `accountPageLogout` click handler, change `if (!r.ok) say(errText(r), false, 'logoutMsg');` to:

```js
          if (!r.ok && !r.cancelled) say(errText(r), false, 'logoutMsg');
```

- [ ] **Step 5: Add the dialog, logout and section to `settings-sync.js`**

Change `setStatus` in the engine to render:

```js
  function setStatus(s) { state.status = s; renderStatus(); }
```

In `activate`, after `install();` add `window.CSAuth.setLogoutHandler(onLogout);`. In `deactivate`, after `uninstall();` add `window.CSAuth.setLogoutHandler(null);`.

Insert after `deactivate` (before the `window.addEventListener('cs-auth-changed', ...` boot lines):

```js
  // ── Dialog, logout and the account-page section ──

  // showDialog uses the app's modal pattern (.modal-overlay + .modal, as
  // the BYOP dialog in packets.js): role=dialog, focus on the first choice,
  // Tab trapped, Escape or a backdrop click dismiss. Resolves with the
  // chosen id, or null when dismissed.
  function showDialog(opts) {
    return new Promise(function (resolve) {
      var prev = document.activeElement;
      var overlay = document.createElement('div');
      overlay.className = 'modal-overlay cs-dialog-overlay';
      overlay.innerHTML = '<div class="modal cs-dialog" role="dialog" aria-modal="true" aria-labelledby="csDialogTitle" aria-describedby="csDialogText">' +
        '<h3 id="csDialogTitle">' + escapeHtml(opts.title) + '</h3>' +
        '<div id="csDialogText">' + opts.text.map(function (t) { return '<p class="account-hint">' + escapeHtml(t) + '</p>'; }).join('') + '</div>' +
        '<div class="cs-dialog-actions">' + opts.choices.map(function (c) {
          return '<button type="button" class="account-btn ' + (c.primary ? 'account-btn-primary' : 'account-btn-secondary') +
            '" data-choice="' + escapeHtml(c.id) + '">' + escapeHtml(c.label) + '</button>';
        }).join('') + '</div></div>';
      document.body.appendChild(overlay);
      var buttons = overlay.querySelectorAll('button');
      function close(choice) {
        overlay.remove();
        if (prev && prev.focus) prev.focus();
        resolve(choice);
      }
      overlay.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(null); return; }
        if (e.key !== 'Tab') return;
        var first = buttons[0], last = buttons[buttons.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      });
      overlay.addEventListener('click', function (e) {
        var btn = e.target.closest && e.target.closest('[data-choice]');
        if (btn) close(btn.getAttribute('data-choice'));
        else if (e.target === overlay) close(null);
      });
      buttons[0].focus();
    });
  }
  var dialog = showDialog;

  // flush pushes pending changes now. Resolves true when the account holds
  // everything this device has.
  function flush() {
    clearTimeout(state.pushTimer);
    state.pushTimer = null;
    if (!state.dirty) return Promise.resolve(true);
    return push().then(function (ok) { return ok && !state.dirty; });
  }

  function removeLocal() {
    state.policy.list.forEach(function (e) { rawRemove(e.key); });
    rawRemove(BASE_KEY);
    rawRemove(REV_KEY);
  }

  var LOGOUT_TEXT = [
    'Your settings stay saved in your account.',
    'Channel keys are never synced and stay on this device. They are not removed, because no copy exists anywhere else.'
  ];

  // onLogout is CSAuth's logout handler while active (see auth.js logout).
  function onLogout() {
    return dialog({
      title: 'Log out',
      text: LOGOUT_TEXT,
      choices: [
        { id: 'keep', label: 'Keep my settings on this device', primary: true },
        { id: 'remove', label: 'Remove my settings from this device' },
        { id: 'cancel', label: 'Cancel' }
      ]
    }).then(function (choice) {
      if (choice !== 'keep' && choice !== 'remove') return { cancel: true };
      return flush().then(function (saved) {
        if (choice === 'keep') return {};
        if (!saved || !state.policy) {
          // Removing now would lose changes that exist nowhere else.
          return { afterLogout: function () { window.CSAuth.notify('Your latest settings could not be saved to your account, so they stay on this device.'); } };
        }
        return { afterLogout: removeLocal };
      });
    });
  }

  // deleteRemote removes the account's copy; this device keeps its values
  // and its next change starts a new document.
  function deleteRemote() {
    return window.CSAuth.request('DELETE', '/api/account/settings').then(function (r) {
      if (r.ok) {
        clearTimeout(state.pushTimer);
        state.pushTimer = null;
        state.dirty = false;
        saveBase({}, 0, true);
        setStatus('held');
      }
      return r;
    });
  }

  function statusText() {
    switch (state.status) {
      case 'ok': return 'Last synced ' + state.lastSyncedAt.toLocaleString();
      case 'syncing': return 'Syncing…';
      case 'retrying': return 'Not synced: retrying';
      case 'too-large': return 'Not synced: your settings are larger than your account can hold. Largest: ' + state.tooLarge.join(', ');
      case 'rejected': return 'Not synced: the server refused your settings. Reload the page to try again.';
      case 'held': return 'No settings saved in your account. Your next change starts a new copy.';
      default: return 'Not synced yet';
    }
  }

  function renderStatus() {
    var el = document.getElementById('syncStatus');
    if (!el) return;
    el.textContent = statusText();
    el.classList.toggle('ok', state.status === 'ok');
    el.classList.toggle('err', state.status === 'retrying' || state.status === 'too-large' || state.status === 'rejected');
  }

  function say(text, ok) {
    var el = document.getElementById('syncMsg');
    if (!el) return;
    el.textContent = text;
    el.classList.toggle('ok', ok);
    el.classList.toggle('err', !ok);
  }

  function mountSection(el) {
    el.innerHTML =
      '<p class="account-msg" id="syncStatus" role="status" aria-live="polite"></p>' +
      '<div class="account-actions">' +
      '<button type="button" id="syncNow" class="account-btn account-btn-secondary">Sync now</button>' +
      '<button type="button" id="syncDelete" class="account-btn account-btn-secondary">Delete synced settings from my account</button>' +
      '</div>' +
      '<p class="account-hint">Synced: your nodes, favorites, theme and customizer settings, saved packet filters, and the filter, sort and view choices of each page.</p>' +
      '<p class="account-hint">Not synced: channel keys and decrypted messages, the API key, panel and column sizes, collapsed panels and map positions.</p>' +
      '<p class="account-msg" id="syncMsg" role="status" aria-live="polite"></p>';
    renderStatus();
    document.getElementById('syncNow').addEventListener('click', function () { return syncNow(); });
    document.getElementById('syncDelete').addEventListener('click', function () {
      return dialog({
        title: 'Delete synced settings',
        text: ['This deletes the copy of your settings stored in your account. The settings on this device stay. Your next change starts a new copy.'],
        choices: [{ id: 'delete', label: 'Delete synced settings', primary: true }, { id: 'cancel', label: 'Cancel' }]
      }).then(function (choice) {
        if (choice !== 'delete') return;
        return deleteRemote().then(function (r) {
          say(r.ok ? 'Synced settings deleted from your account.' : ((r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')')), r.ok);
        }, function () { say('Network error, try again.', false); });
      });
    });
  }
```

Replace the export with:

```js
  window.CSSettingsSync = {
    mountSection: mountSection,
    syncNow: syncNow,
    _test: {
      mergeDocs: mergeDocs, state: state, activate: activate, deactivate: deactivate, pull: pull, push: push,
      midEdit: midEdit, flush: flush, onLogout: onLogout, deleteRemote: deleteRemote, statusText: statusText,
      useDialog: function (fn) { dialog = fn; }
    }
  };
```

- [ ] **Step 6: Dialog layout**

Append to `public/account.css`:

```css
/* Settings sync dialogs (settings-sync.js) on the shared .modal. */
.cs-dialog { display: flex; flex-direction: column; gap: 8px; color: var(--text); }
.cs-dialog h3 { margin: 0; }
.cs-dialog-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
```

- [ ] **Step 7: Answer the dialog in the existing E2E logout step**

In `tests/e2e/test-user-management-e2e.js`, step `'user logs out from the account page (phone width) and logs in again'`, change

```js
    await user.click('#accountPageLogout');
    await user.waitForSelector('#loginForm');
```
to
```js
    await user.click('#accountPageLogout');
    // Settings sync is active for a logged-in user: logout asks keep or remove.
    await user.click('.cs-dialog [data-choice="keep"]');
    await user.waitForSelector('#loginForm');
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `node tests/unit/test-settings-sync.js && node tests/unit/test-user-management-ui.js`
Expected: both end with `0 failed` (`43 passed` for the first).
Run: `GITHUB_REF_NAME=v3.13.1 PYTHONIOENCODING=utf-8 sh test-all.sh`
Expected: `All standalone frontend suites passed`.

- [ ] **Step 9: Commit**

```bash
git add public/auth.js public/account.js public/settings-sync.js public/account.css tests/unit/test-settings-sync.js tests/unit/test-user-management-ui.js tests/e2e/test-user-management-e2e.js
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
feat(ui): logout dialog and settings sync section on the account page

With settings sync active, logout first asks "Keep my settings on this
device" (default) or "Remove my settings from this device", pushes
pending changes, and only removes the synced keys and the baseline once
that push succeeded. Channel keys and the API key are never removed.
Cancel, Escape or the backdrop cancel the logout. auth.js gets a logout
hook for this; automatic logouts (401) stay silent.

The account page gains a "Settings sync" section: last synced time or
"Not synced: retrying", Sync now, what is and is not synced, and
"Delete synced settings from my account" behind a confirmation. The
dialogs use the shared .modal classes with a focus trap.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

### Task 7: Two-device E2E, axe, and the user guide

**Files:**
- Modify: `tests/e2e/test-user-management-e2e.js` (helpers after `registerAndActivate`; steps before `'admin disables the user; ...'`; the existing axe step uses the new helper)
- Modify: `docs/user-guide/accounts.md` ("For users" section)

**Interfaces:**
- Consumes: Task 3 API, Tasks 5 and 6 behaviour and DOM ids (`#syncSection`, `#syncStatus`, `.cs-dialog`, `[data-choice]`), `window.CSSettingsSync.syncNow()`, `window.CSAuth.request`; existing helpers `registerAndActivate`, `authReady`, `step`, `assert`, `PW`, `BASE`.
- Produces: nothing for later tasks. CI already runs this file against the e2etest server on port 13582 (`.github/workflows/deploy.yml:768-769`) and it is already classified in `scripts/non-unit-tests.json:118`; no workflow change.

- [ ] **Step 1: Add the helpers**

In `tests/e2e/test-user-management-e2e.js`, after `registerAndActivate`, add:

```js
// axeClean fails on serious or critical WCAG 2 A/AA violations inside sel.
async function axeClean(pg, sel) {
  const res = await new AxeBuilder({ page: pg }).include(sel).withTags(['wcag2a', 'wcag2aa']).analyze();
  const bad = res.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
  assert(bad.length === 0, sel + ': ' + bad.map((v) => v.id + ' ' + v.nodes.map((n) => n.target.join(' ') + ' ' + ((n.any[0] || {}).message || '')).join(' | ')).join(', '));
}

// The account's synced keys, read through the page's own session.
async function accountKeys(pg) {
  return pg.evaluate(() => window.CSAuth.request('GET', '/api/account/settings').then((r) => (r.data.doc && r.data.doc.keys) || {}));
}

async function until(fn, label) {
  const end = Date.now() + 8000;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error('timed out: ' + label);
    await new Promise((r) => setTimeout(r, 200));
  }
}
```

Change the body of the existing step `'axe: no serious or critical violations on the new views'` to use it:

```js
    for (const [pg, route, sel] of [[user, '/#/account/login', '#loginForm'], [admin, '/#/admin/users', '.um-table']]) {
      await pg.goto(BASE + route);
      await pg.waitForSelector(sel);
      await authReady(pg);
      await pg.waitForTimeout(1500);
      await axeClean(pg, '#app');
    }
```

- [ ] **Step 2: Add the two-device steps**

Insert before the step `'admin disables the user; the live session is logged out without a reload'`:

```js
  // Settings sync: two browser contexts are two devices on one account.
  const SYNC_FAV = 'e2e5e7c0000000000000000000000000000000000000000000000000000000a1';
  const d1 = await (await browser.newContext()).newPage();
  const d2 = await (await browser.newContext()).newPage();
  for (const [pg, tag] of [[d1, 'd1'], [d2, 'd2']]) {
    pg.setDefaultTimeout(8000);
    pg.on('pageerror', (e) => console.error('[pageerror ' + tag + ']', e.message));
  }

  await step('settings sync: device 1 saves a packet time window and a favorite to the account', async () => {
    await registerAndActivate(d1, 'sync@e2e.test', 'E2E Sync');
    await d1.goto(BASE + '/#/packets');
    await d1.waitForSelector('#fTimeWindow');
    await d1.selectOption('#fTimeWindow', '180');
    // No favorite star without node rows in view: write the key as nodes.js does.
    await d1.evaluate((pk) => localStorage.setItem('meshcore-favorites', JSON.stringify([pk])), SYNC_FAV);
    await until(async () => {
      const k = await accountKeys(d1);
      return k['meshcore-time-window'] === '180' && (k['meshcore-favorites'] || '').includes(SYNC_FAV);
    }, 'account holds the time window and the favorite');
  });

  await step('settings sync: device 2 logs in and gets both; its channel key stays local', async () => {
    await d2.goto(BASE + '/#/account/login', { waitUntil: 'domcontentloaded' });
    await d2.waitForSelector('#loginForm');
    await d2.evaluate(() => localStorage.setItem('corescope_channel_keys', JSON.stringify({ '#e2e': '00112233445566778899aabbccddeeff' })));
    await d2.fill('#loginEmail', 'sync@e2e.test');
    await d2.fill('#loginPassword', PW);
    await d2.click('#loginForm button[type="submit"]');
    await d2.waitForSelector('#profileForm');
    await d2.waitForFunction((pk) => (localStorage.getItem('meshcore-favorites') || '').includes(pk) &&
      localStorage.getItem('meshcore-time-window') === '180', SYNC_FAV);
    const k = await accountKeys(d2);
    assert(!('corescope_channel_keys' in k), 'channel key reached the account');
  });

  await step('settings sync: a favorite removed on device 1 is gone on device 2', async () => {
    await d1.evaluate(() => localStorage.setItem('meshcore-favorites', '[]'));
    await until(async () => !((await accountKeys(d1))['meshcore-favorites'] || '').includes(SYNC_FAV), 'removal reached the account');
    await d2.evaluate(() => window.CSSettingsSync.syncNow());
    await d2.waitForFunction((pk) => !(localStorage.getItem('meshcore-favorites') || '').includes(pk), SYNC_FAV);
  });

  await step('settings sync: axe on the section and the logout dialog; Remove keeps the channel key', async () => {
    await d2.waitForSelector('#syncStatus');
    await axeClean(d2, '#syncSection');
    await d2.click('#accountPageLogout');
    await d2.waitForSelector('.cs-dialog');
    assert(await d2.evaluate(() => document.activeElement && document.activeElement.getAttribute('data-choice') === 'keep'), 'Keep is not focused');
    await axeClean(d2, '.cs-dialog');
    await d2.click('.cs-dialog [data-choice="remove"]');
    await d2.waitForSelector('#loginForm');
    const left = await d2.evaluate(() => ({
      fav: localStorage.getItem('meshcore-favorites'), tw: localStorage.getItem('meshcore-time-window'),
      base: localStorage.getItem('cs-settings-sync-base'), ch: localStorage.getItem('corescope_channel_keys'),
    }));
    assert(left.fav === null && left.tw === null && left.base === null, 'synced keys left: ' + JSON.stringify(left));
    assert(left.ch && left.ch.includes('#e2e'), 'channel key removed');
  });
```

- [ ] **Step 3: Write the user guide section**

In `docs/user-guide/accounts.md`, under "## For users", after the "**My account:**" bullet, add:

```markdown
- **Settings sync:** while you are logged in, your settings follow you: your nodes,
  favorites, theme and customizer settings, saved packet filters, and the filter, sort
  and view choices of each page. Log in on another browser or phone and they are
  restored; later changes reach your other devices within about a minute, or when you
  return to the tab. A node or favorite added on one device is never dropped by another,
  and one you removed stays removed.
- **Never synced:** channel keys and decrypted messages, the API key, panel and column
  sizes, collapsed panels and map positions. They stay in the browser where you set them.
- **Logging out** asks whether to keep your settings on this device (the default) or
  remove them; your account keeps its copy either way, and channel keys are never
  removed. An automatic logout (expired session) keeps everything on the device.
- **My account, Settings sync** shows when your settings were last saved, has *Sync now*,
  and *Delete synced settings from my account*, which removes the account's copy only.
  The settings on your devices stay, and your next change starts a new copy.
```

- [ ] **Step 4: Run the E2E suite against local servers**

Follow the header of `tests/e2e/test-user-management-e2e.js` (never point a server at the tracked fixture; migrate copies). In git-bash, with the Go toolchain exports from Global Constraints:

```bash
TMP=$(mktemp -d); CFGDIR=$(mktemp -d)
cp test-fixtures/e2e-fixture.db "$TMP/on.db"; cp test-fixtures/e2e-fixture.db "$TMP/off.db"
(cd cmd/migrate && go build -o ../../corescope-migrate .)
./corescope-migrate -db "$TMP/on.db" && ./corescope-migrate -db "$TMP/off.db"
(cd cmd/server && go build -o ../../corescope-server . && go build -tags e2etest -o ../../corescope-server-e2e .)
cat > "$CFGDIR/config.json" <<'EOF'
{"port": 13582, "userManagement": {"enabled": true, "dbPath": "users.db", "adminEmails": ["admin@e2e.test"],
 "publicBaseUrl": "http://localhost:13582", "mail": {"provider": "fake", "fromEmail": "noreply@e2e.test"}}}
EOF
./corescope-server -port 13581 -db "$TMP/off.db" -public public &
(cd "$CFGDIR" && "$OLDPWD/corescope-server-e2e" -config-dir . -port 13582 -db "$TMP/on.db" -public "$OLDPWD/public") &
BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js
```

Expected: last line `N/N tests passed` with N = 10 (6 existing steps + 4 new). Stop both servers afterwards (`kill %1 %2`). If Playwright or Chromium cannot run on this machine, set `CHROMIUM_PATH`; if it still cannot run, say so in the report and leave the E2E to CI. Do not claim it passed.

- [ ] **Step 5: Run every suite once more**

```bash
GITHUB_REF_NAME=v3.13.1 PYTHONIOENCODING=utf-8 sh test-all.sh
cd internal/users && go test -count=1 ./... && cd ../../cmd/server && go test -count=1 ./...
```
Expected: `All standalone frontend suites passed`, and `ok` for both Go modules.

- [ ] **Step 6: Commit**

```bash
git add tests/e2e/test-user-management-e2e.js docs/user-guide/accounts.md
git diff --cached --stat
git commit --author="efiten <erwin.fiten@gmail.com>" -F - <<'EOF'
test(e2e): settings sync across two devices; user guide

Two browser contexts on one account: a packet time window chosen in
the UI and a favorite reach the second device at login, a removal on
the first device reaches the second, and logout with "Remove" clears
the synced keys and the baseline but keeps a channel key. Axe checks
the Settings sync section and the logout dialog. The user guide
describes what is and is not synced.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FGYZDCxPrcM9FC6VHksY71
EOF
```

---

## Not in this plan

- Realtime push between devices (spec non-goal), operator defaults (sub-project C), sharing.
- Manual validation on staging with a desktop and a phone on one account (spec "Manual on staging"): for the controller after merge, with staging's own config (`config.staging.json`).
- Exposing the sync timings (2 s, 60 s, backoff) in the customizer (AGENTS.md rule 8): they are protocol constants between client and server limits, not display preferences; tracked here as a deliberate no.
