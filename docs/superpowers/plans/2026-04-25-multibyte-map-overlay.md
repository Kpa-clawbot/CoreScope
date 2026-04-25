# Multibyte Map Overlay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a map overlay that colors repeater markers by multibyte-capability status (confirmed / suspected / unknown), backed by a persisted DB column populated from the server's existing analytics computation.

**Architecture:** The ingestor adds `multibyte_sup` + `multibyte_evidence` columns to the `nodes` table via a migration. The server's `PacketStore.persistMultiByteCapability()` upserts results from the already-running `computeMultiByteCapability()` analytics cycle into those columns (no-downgrade guard). `/api/nodes` passes the columns through to the frontend, which applies marker coloring when the new toggle is enabled.

**Tech Stack:** Go (server + ingestor), SQLite (shared DB), vanilla JS (map.js / Leaflet)

---

## File Map

| File | Change |
|---|---|
| `cmd/ingestor/db.go` | Add `multibyte_sup_v1` migration (ALTER TABLE nodes + inactive_nodes) |
| `cmd/ingestor/db_test.go` | Add schema test for new columns |
| `cmd/server/db.go` | Add `hasMultibyteSupCols` flag, update `detectSchema()`, convert `scanNodeRow` to DB method with conditional scanning, update three SELECT queries |
| `cmd/server/store.go` | Add `persistMultiByteCapability()`, wire into `GetHashSizes()` |
| `cmd/server/multibyte_capability_test.go` | Add tests for `persistMultiByteCapability()` |
| `public/map.js` | Add toggle to filters + UI, update `makeMarkerIcon` + `makeRepeaterLabelIcon` + `buildPopup` |

---

## Task 1: Ingestor migration — add multibyte_sup columns

**Files:**
- Modify: `cmd/ingestor/db.go` (after the `scope_name_v1` migration, around line 428)
- Modify: `cmd/ingestor/db_test.go` (add test after `TestSchemaNoiseFloorIsReal`)

- [ ] **Step 1: Write failing test**

Add to `cmd/ingestor/db_test.go` after the `TestSchemaNoiseFloorIsReal` function:

```go
func TestSchemaMultibyteSupColumns(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cols := map[string]string{}
	rows, err := s.db.Query("PRAGMA table_info(nodes)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dflt interface{}
		if rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil {
			cols[colName] = colType
		}
	}

	if ct, ok := cols["multibyte_sup"]; !ok {
		t.Error("nodes.multibyte_sup column missing")
	} else if ct != "INTEGER" {
		t.Errorf("nodes.multibyte_sup type=%s, want INTEGER", ct)
	}
	if _, ok := cols["multibyte_evidence"]; !ok {
		t.Error("nodes.multibyte_evidence column missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd cmd/ingestor && go test -run TestSchemaMultibyteSupColumns -v
```

Expected: FAIL — columns missing.

- [ ] **Step 3: Add migration to `cmd/ingestor/db.go`**

Locate the `scope_name_v1` migration block (around line 421). Add the following block immediately after it (after the closing `}`):

```go
// Migration: add multibyte capability columns to nodes/inactive_nodes (#903)
row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'multibyte_sup_v1'")
if row.Scan(&migDone) != nil {
    log.Println("[migration] Adding multibyte_sup columns to nodes/inactive_nodes...")
    db.Exec(`ALTER TABLE nodes ADD COLUMN multibyte_sup INTEGER NOT NULL DEFAULT 0`)
    db.Exec(`ALTER TABLE nodes ADD COLUMN multibyte_evidence TEXT`)
    db.Exec(`ALTER TABLE inactive_nodes ADD COLUMN multibyte_sup INTEGER NOT NULL DEFAULT 0`)
    db.Exec(`ALTER TABLE inactive_nodes ADD COLUMN multibyte_evidence TEXT`)
    db.Exec(`INSERT INTO _migrations (name) VALUES ('multibyte_sup_v1')`)
    log.Println("[migration] multibyte_sup columns added")
}
```

- [ ] **Step 4: Run test to verify it passes**

```
cd cmd/ingestor && go test -run TestSchemaMultibyteSupColumns -v
```

Expected: PASS.

- [ ] **Step 5: Run full ingestor test suite**

```
cd cmd/ingestor && go test ./... 2>&1 | tail -5
```

Expected: `ok` with no failures.

- [ ] **Step 6: Commit**

```bash
git add cmd/ingestor/db.go cmd/ingestor/db_test.go
git commit -m "feat(ingestor/db): add multibyte_sup migration to nodes table (#903)"
```

---

## Task 2: Server schema detection + node row enrichment

**Files:**
- Modify: `cmd/server/db.go`

The server opens the DB read-only and uses `detectSchema()` to discover columns. The `scanNodeRow` standalone function must become a method so it can check the `hasMultibyteSupCols` flag and conditionally scan.

- [ ] **Step 1: Write failing test**

Add to `cmd/server/db_test.go`. Find an existing test that calls `GetNodes` and add a new one that asserts `multibyte_sup` is present in the returned map:

```go
func TestGetNodesReturnsMultibyteSupField(t *testing.T) {
	conn, _ := sql.Open("sqlite", ":memory:")
	conn.SetMaxOpenConns(1)
	conn.Exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
		lat REAL, lon REAL, last_seen TEXT, first_seen TEXT,
		advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL,
		multibyte_sup INTEGER NOT NULL DEFAULT 0, multibyte_evidence TEXT
	)`)
	conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen)
		VALUES ('aabb1122', 'TestRep', 'repeater', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	db := &DB{conn: conn, hasMultibyteSupCols: true}

	nodes, _, _, err := db.GetNodes(10, 0, "", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected 1 node")
	}
	if _, ok := nodes[0]["multibyte_sup"]; !ok {
		t.Error("multibyte_sup missing from GetNodes response")
	}
	if nodes[0]["multibyte_sup"] != 0 {
		t.Errorf("multibyte_sup = %v, want 0", nodes[0]["multibyte_sup"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd cmd/server && go test -run TestGetNodesReturnsMultibyteSupField -v
```

Expected: FAIL — `hasMultibyteSupCols` field doesn't exist yet.

- [ ] **Step 3: Add `hasMultibyteSupCols` to `DB` struct**

In `cmd/server/db.go`, add the field to the `DB` struct (around line 24):

```go
type DB struct {
    conn             *sql.DB
    path             string
    isV3             bool
    hasResolvedPath  bool
    hasObsRawHex     bool
    hasScopeName     bool
    hasMultibyteSupCols bool // nodes.multibyte_sup column exists (#903)

    channelsCacheMu  sync.Mutex
    channelsCacheKey string
    channelsCacheRes []map[string]interface{}
    channelsCacheExp time.Time
}
```

- [ ] **Step 4: Add nodes PRAGMA check to `detectSchema()`**

In `cmd/server/db.go`, at the end of `detectSchema()` (after the `txRows` block that ends around line 103), add:

```go
nodeRows, err := db.conn.Query("PRAGMA table_info(nodes)")
if err != nil {
    return
}
defer nodeRows.Close()
for nodeRows.Next() {
    var cid int
    var colName string
    var colType sql.NullString
    var notNull, pk int
    var dflt sql.NullString
    if nodeRows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil {
        if colName == "multibyte_sup" {
            db.hasMultibyteSupCols = true
        }
    }
}
```

- [ ] **Step 5: Convert `scanNodeRow` to a DB method with conditional scanning**

Find `func scanNodeRow(rows *sql.Rows)` (around line 1829). Replace it entirely with:

```go
func (db *DB) scanNodeRow(rows *sql.Rows) map[string]interface{} {
    var pk string
    var name, role, lastSeen, firstSeen sql.NullString
    var lat, lon sql.NullFloat64
    var advertCount int
    var batteryMv sql.NullInt64
    var temperatureC sql.NullFloat64
    var multibyteSup sql.NullInt64
    var multibyteEvidence sql.NullString

    scanArgs := []interface{}{&pk, &name, &role, &lat, &lon, &lastSeen, &firstSeen, &advertCount, &batteryMv, &temperatureC}
    if db.hasMultibyteSupCols {
        scanArgs = append(scanArgs, &multibyteSup, &multibyteEvidence)
    }
    if err := rows.Scan(scanArgs...); err != nil {
        return nil
    }
    m := map[string]interface{}{
        "public_key":             pk,
        "name":                   nullStr(name),
        "role":                   nullStr(role),
        "lat":                    nullFloat(lat),
        "lon":                    nullFloat(lon),
        "last_seen":              nullStr(lastSeen),
        "first_seen":             nullStr(firstSeen),
        "advert_count":           advertCount,
        "last_heard":             nullStr(lastSeen),
        "hash_size":              nil,
        "hash_size_inconsistent": false,
        "multibyte_sup":          int(multibyteSup.Int64), // 0 when not scanned
    }
    if multibyteEvidence.Valid {
        m["multibyte_evidence"] = multibyteEvidence.String
    } else {
        m["multibyte_evidence"] = nil
    }
    if batteryMv.Valid {
        m["battery_mv"] = int(batteryMv.Int64)
    } else {
        m["battery_mv"] = nil
    }
    if temperatureC.Valid {
        m["temperature_c"] = temperatureC.Float64
    } else {
        m["temperature_c"] = nil
    }
    return m
}
```

- [ ] **Step 6: Update SELECT queries and call sites**

In `cmd/server/db.go`, make three changes:

**A. `GetNodes`** (around line 820) — replace the `querySQL` assignment:

```go
nodeColList := "public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c"
if db.hasMultibyteSupCols {
    nodeColList += ", multibyte_sup, multibyte_evidence"
}
querySQL := fmt.Sprintf("SELECT %s FROM nodes %s ORDER BY %s LIMIT ? OFFSET ?", nodeColList, w, order)
```

Then change `n := scanNodeRow(rows)` to `n := db.scanNodeRow(rows)`.

**B. `SearchNodes`** (around line 846) — replace the `rows` query and call:

```go
colList := "public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c"
if db.hasMultibyteSupCols {
    colList += ", multibyte_sup, multibyte_evidence"
}
rows, err := db.conn.Query(
    fmt.Sprintf("SELECT %s FROM nodes WHERE name LIKE ? OR public_key LIKE ? ORDER BY last_seen DESC LIMIT ?", colList),
    "%"+query+"%", query+"%", limit)
```

Change `n := scanNodeRow(rows)` to `n := db.scanNodeRow(rows)`.

**C. `GetNodeByPubkey`** (around line 866):

```go
colList := "public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c"
if db.hasMultibyteSupCols {
    colList += ", multibyte_sup, multibyte_evidence"
}
rows, err := db.conn.Query(
    fmt.Sprintf("SELECT %s FROM nodes WHERE public_key = ?", colList), pubkey)
```

Change `return scanNodeRow(rows), nil` to `return db.scanNodeRow(rows), nil`.

- [ ] **Step 7: Run test to verify it passes**

```
cd cmd/server && go test -run TestGetNodesReturnsMultibyteSupField -v
```

Expected: PASS.

- [ ] **Step 8: Run full server test suite**

```
cd cmd/server && go test ./... 2>&1 | tail -10
```

Expected: all pass. If `scanNodeRow` was referenced somewhere else as a standalone function, the compiler will catch it — fix those call sites to `db.scanNodeRow(rows)`.

- [ ] **Step 9: Commit**

```bash
git add cmd/server/db.go cmd/server/db_test.go
git commit -m "feat(server/db): expose multibyte_sup in node API response (#903)"
```

---

## Task 3: persistMultiByteCapability + wire into analytics

**Files:**
- Modify: `cmd/server/store.go`
- Modify: `cmd/server/multibyte_capability_test.go`

- [ ] **Step 1: Write failing test**

Add to `cmd/server/multibyte_capability_test.go`:

```go
// setupCapabilityTestDBWithMultibyteCols returns a DB with multibyte columns.
func setupCapabilityTestDBWithMultibyteCols(t *testing.T) *DB {
	t.Helper()
	db := setupCapabilityTestDB(t)
	db.conn.Exec(`ALTER TABLE nodes ADD COLUMN multibyte_sup INTEGER NOT NULL DEFAULT 0`)
	db.conn.Exec(`ALTER TABLE nodes ADD COLUMN multibyte_evidence TEXT`)
	db.hasMultibyteSupCols = true
	return db
}

func TestPersistMultiByteCapability_Confirmed(t *testing.T) {
	db := setupCapabilityTestDBWithMultibyteCols(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)
	entries := []MultiByteCapEntry{
		{PublicKey: "aabbccdd11223344", Status: "confirmed", Evidence: "advert"},
	}
	store.persistMultiByteCapability(entries)

	var sup int
	var evidence sql.NullString
	db.conn.QueryRow("SELECT multibyte_sup, multibyte_evidence FROM nodes WHERE public_key = ?",
		"aabbccdd11223344").Scan(&sup, &evidence)

	if sup != 2 {
		t.Errorf("multibyte_sup = %d, want 2", sup)
	}
	if !evidence.Valid || evidence.String != "advert" {
		t.Errorf("multibyte_evidence = %v, want 'advert'", evidence)
	}
}

func TestPersistMultiByteCapability_Suspected(t *testing.T) {
	db := setupCapabilityTestDBWithMultibyteCols(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)
	entries := []MultiByteCapEntry{
		{PublicKey: "aabbccdd11223344", Status: "suspected", Evidence: "path"},
	}
	store.persistMultiByteCapability(entries)

	var sup int
	db.conn.QueryRow("SELECT multibyte_sup FROM nodes WHERE public_key = ?",
		"aabbccdd11223344").Scan(&sup)

	if sup != 1 {
		t.Errorf("multibyte_sup = %d, want 1", sup)
	}
}

func TestPersistMultiByteCapability_NoDowngrade(t *testing.T) {
	db := setupCapabilityTestDBWithMultibyteCols(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence) VALUES (?, ?, ?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", recentTS(1), 2, "advert")

	store := NewPacketStore(db, nil)
	// Attempt to downgrade confirmed → suspected
	entries := []MultiByteCapEntry{
		{PublicKey: "aabbccdd11223344", Status: "suspected", Evidence: "path"},
	}
	store.persistMultiByteCapability(entries)

	var sup int
	var evidence sql.NullString
	db.conn.QueryRow("SELECT multibyte_sup, multibyte_evidence FROM nodes WHERE public_key = ?",
		"aabbccdd11223344").Scan(&sup, &evidence)

	if sup != 2 {
		t.Errorf("multibyte_sup = %d after downgrade attempt, want 2 (no downgrade)", sup)
	}
	if !evidence.Valid || evidence.String != "advert" {
		t.Errorf("multibyte_evidence = %v after downgrade attempt, want 'advert'", evidence)
	}
}

func TestPersistMultiByteCapability_UnknownSkipped(t *testing.T) {
	db := setupCapabilityTestDBWithMultibyteCols(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)
	entries := []MultiByteCapEntry{
		{PublicKey: "aabbccdd11223344", Status: "unknown", Evidence: ""},
	}
	store.persistMultiByteCapability(entries)

	var sup int
	db.conn.QueryRow("SELECT multibyte_sup FROM nodes WHERE public_key = ?",
		"aabbccdd11223344").Scan(&sup)

	if sup != 0 {
		t.Errorf("multibyte_sup = %d after unknown entry, want 0 (unchanged)", sup)
	}
}

func TestPersistMultiByteCapability_NoOpWhenColsMissing(t *testing.T) {
	db := setupCapabilityTestDB(t) // no multibyte cols, hasMultibyteSupCols = false
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)
	entries := []MultiByteCapEntry{
		{PublicKey: "aabbccdd11223344", Status: "confirmed", Evidence: "advert"},
	}
	// Must not panic or error when columns don't exist
	store.persistMultiByteCapability(entries)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
cd cmd/server && go test -run TestPersistMultiByteCapability -v
```

Expected: FAIL — `persistMultiByteCapability` undefined.

- [ ] **Step 3: Add `persistMultiByteCapability` to `cmd/server/store.go`**

Add the function directly after `computeMultiByteCapability` (after line 6322, before `// --- Bulk Health`):

```go
// persistMultiByteCapability upserts confirmed/suspected capability status into
// the nodes table. Status only moves forward (0→1→2); confirmed is never
// overwritten by suspected or unknown. Unknown entries are skipped entirely.
// No-op when hasMultibyteSupCols is false (DB not yet migrated).
func (s *PacketStore) persistMultiByteCapability(entries []MultiByteCapEntry) {
    if !s.db.hasMultibyteSupCols {
        return
    }
    for _, e := range entries {
        var sup int
        switch e.Status {
        case "confirmed":
            sup = 2
        case "suspected":
            sup = 1
        default:
            continue // unknown — nothing to write
        }
        var evidence interface{}
        if e.Evidence != "" {
            evidence = e.Evidence
        }
        s.db.conn.Exec(
            "UPDATE nodes SET multibyte_sup = ?, multibyte_evidence = ? WHERE public_key = ? AND multibyte_sup < ?",
            sup, evidence, e.PublicKey, sup,
        )
    }
}
```

- [ ] **Step 4: Wire into `GetHashSizes()` in `cmd/server/store.go`**

Find the block around line 5419:

```go
result["multiByteCapability"] = s.computeMultiByteCapability(adopterHS)
```

Replace with:

```go
entries := s.computeMultiByteCapability(adopterHS)
result["multiByteCapability"] = entries
s.persistMultiByteCapability(entries)
```

- [ ] **Step 5: Run tests to verify they pass**

```
cd cmd/server && go test -run TestPersistMultiByteCapability -v
```

Expected: all 5 PASS.

- [ ] **Step 6: Run full server test suite**

```
cd cmd/server && go test ./... 2>&1 | tail -10
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/store.go cmd/server/multibyte_capability_test.go
git commit -m "feat(server): persist multibyte capability status to nodes table (#903)"
```

---

## Task 4: Frontend — toggle + marker styling

**Files:**
- Modify: `public/map.js`

- [ ] **Step 1: Add `multibyteOverlay` to filters state**

In `public/map.js`, find the `filters` declaration (line 12). Add `multibyteOverlay` to it:

```js
let filters = { repeater: true, companion: true, room: true, sensor: true, observer: true, lastHeard: '30d', neighbors: false, clusters: false, hashLabels: localStorage.getItem('meshcore-map-hash-labels') !== 'false', statusFilter: localStorage.getItem('meshcore-map-status-filter') || 'all', byteSize: localStorage.getItem('meshcore-map-byte-filter') || 'all', multibyteOverlay: localStorage.getItem('meshcore-map-multibyte') === 'true' };
```

- [ ] **Step 2: Add checkbox to map controls HTML**

In `public/map.js`, find the Byte Size fieldset (around line 114). Add the checkbox line immediately after the `</div>` that closes the `mcByteFilter` div (after line 121):

```js
          <fieldset class="mc-section">
            <legend class="mc-label">Byte Size</legend>
            <div class="filter-group" id="mcByteFilter">
              <button class="btn ${filters.byteSize==='all'?'active':''}" data-byte="all">All</button>
              <button class="btn ${filters.byteSize==='1'?'active':''}" data-byte="1">1-byte</button>
              <button class="btn ${filters.byteSize==='2'?'active':''}" data-byte="2">2-byte</button>
              <button class="btn ${filters.byteSize==='3'?'active':''}" data-byte="3">3-byte</button>
            </div>
            <label for="mcMultibyte" style="display:block;margin-top:6px;font-size:12px;cursor:pointer"><input type="checkbox" id="mcMultibyte" ${filters.multibyteOverlay?'checked':''}> Show multibyte capability</label>
          </fieldset>
```

- [ ] **Step 3: Wire the change event listener**

Find where the existing filter event listeners are registered (around line 285, near `mcLastHeard`). Add:

```js
document.getElementById('mcMultibyte').addEventListener('change', function(e) {
  filters.multibyteOverlay = e.target.checked;
  localStorage.setItem('meshcore-map-multibyte', e.target.checked);
  renderMarkers();
});
```

- [ ] **Step 4: Update `makeMarkerIcon` to accept and apply multibyte styling**

In `public/map.js`, find `function makeMarkerIcon(role, isStale, isAlsoObserver)` (line 28). Replace it with:

```js
function makeMarkerIcon(role, isStale, isAlsoObserver, mbSup) {
    const s = ROLE_STYLE[role] || ROLE_STYLE.companion;
    const size = s.radius * 2 + 4;
    const c = size / 2;

    // Multibyte overlay color overrides (only when mbSup is a number, not null/undefined)
    let fill = s.color;
    let stroke = '#fff';
    let strokeExtra = '';
    let svgOpacity = 1;
    if (mbSup !== null && mbSup !== undefined) {
      if (mbSup >= 2) {
        fill = '#22c55e'; stroke = '#16a34a';
      } else if (mbSup >= 1) {
        fill = '#86efac'; stroke = '#22c55e'; strokeExtra = ' stroke-dasharray="3,2"';
      } else {
        svgOpacity = 0.45;
      }
    }

    let path;
    switch (s.shape) {
      case 'diamond':
        path = `<polygon points="${c},2 ${size-2},${c} ${c},${size-2} 2,${c}" fill="${fill}" stroke="${stroke}" stroke-width="2"${strokeExtra}/>`;
        break;
      case 'square':
        path = `<rect x="3" y="3" width="${size-6}" height="${size-6}" fill="${fill}" stroke="${stroke}" stroke-width="2"${strokeExtra}/>`;
        break;
      case 'triangle':
        path = `<polygon points="${c},2 ${size-2},${size-2} 2,${size-2}" fill="${fill}" stroke="${stroke}" stroke-width="2"${strokeExtra}/>`;
        break;
      case 'star': {
        const cx = c, cy = c, outer = c - 1, inner = outer * 0.4;
        let pts = '';
        for (let i = 0; i < 5; i++) {
          const aOuter = (i * 72 - 90) * Math.PI / 180;
          const aInner = ((i * 72) + 36 - 90) * Math.PI / 180;
          pts += `${cx + outer * Math.cos(aOuter)},${cy + outer * Math.sin(aOuter)} `;
          pts += `${cx + inner * Math.cos(aInner)},${cy + inner * Math.sin(aInner)} `;
        }
        path = `<polygon points="${pts.trim()}" fill="${fill}" stroke="${stroke}" stroke-width="1.5"${strokeExtra}/>`;
        break;
      }
      default:
        path = `<circle cx="${c}" cy="${c}" r="${c-2}" fill="${fill}" stroke="${stroke}" stroke-width="2"${strokeExtra}/>`;
    }

    let obsOverlay = '';
    if (isAlsoObserver) {
      const starSize = 8;
      const sx = size - starSize, sy = 0;
      const scx = starSize / 2, scy = starSize / 2, so = starSize / 2 - 0.5, si = so * 0.4;
      let starPts = '';
      for (let i = 0; i < 5; i++) {
        const aO = (i * 72 - 90) * Math.PI / 180;
        const aI = ((i * 72) + 36 - 90) * Math.PI / 180;
        starPts += `${scx + so * Math.cos(aO)},${scy + so * Math.sin(aO)} `;
        starPts += `${scx + si * Math.cos(aI)},${scy + si * Math.sin(aI)} `;
      }
      obsOverlay = `<g transform="translate(${sx},${sy})"><polygon points="${starPts.trim()}" fill="${ROLE_COLORS.observer || '#f1c40f'}" stroke="#fff" stroke-width="0.8"/></g>`;
    }
    const innerSvg = `${path}${obsOverlay}`;
    const svg = svgOpacity < 1
      ? `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" xmlns="http://www.w3.org/2000/svg" opacity="${svgOpacity}">${innerSvg}</svg>`
      : `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" xmlns="http://www.w3.org/2000/svg">${innerSvg}</svg>`;
    return L.divIcon({
      html: svg,
      className: 'meshcore-marker' + (isStale ? ' marker-stale' : ''),
      iconSize: [size, size],
      iconAnchor: [c, c],
      popupAnchor: [0, -c],
    });
  }
```

- [ ] **Step 5: Update `makeRepeaterLabelIcon` to accept and apply multibyte styling**

In `public/map.js`, find `function makeRepeaterLabelIcon(node, isStale, isAlsoObserver)` (line 84). Replace with:

```js
  function makeRepeaterLabelIcon(node, isStale, isAlsoObserver, mbSup) {
    var s = ROLE_STYLE['repeater'] || ROLE_STYLE.companion;
    var hs = node.hash_size || 1;
    var shortHash = node.public_key ? node.public_key.slice(0, hs * 2).toUpperCase() : '??';

    var bgColor = s.color;
    var textColor = '#fff';
    var border = '2px solid #fff';
    var extraStyle = '';
    if (mbSup !== null && mbSup !== undefined) {
      if (mbSup >= 2) {
        bgColor = '#22c55e'; border = '2px solid #16a34a';
      } else if (mbSup >= 1) {
        bgColor = '#86efac'; textColor = '#14532d'; border = '2px dashed #22c55e';
      } else {
        extraStyle = 'opacity:0.45;';
      }
    }

    var obsIndicator = isAlsoObserver ? ' <span style="color:' + (ROLE_COLORS.observer || '#f1c40f') + ';font-size:13px;line-height:1;" title="Also an observer">★</span>' : '';
    var html = '<div style="background:' + bgColor + ';color:' + textColor + ';font-weight:bold;font-size:11px;padding:2px 5px;border-radius:3px;border:' + border + ';box-shadow:0 1px 3px rgba(0,0,0,0.4);text-align:center;line-height:1.2;white-space:nowrap;' + extraStyle + '">' +
      shortHash + obsIndicator + '</div>';
    return L.divIcon({
      html: html,
      className: 'meshcore-marker meshcore-label-marker' + (isStale ? ' marker-stale' : ''),
      iconSize: null,
      iconAnchor: [14, 12],
      popupAnchor: [0, -12],
    });
  }
```

- [ ] **Step 6: Pass `mbSup` to icon functions at the marker creation call site**

In `public/map.js`, find the marker creation loop (around line 808). Replace the icon creation line (line 814):

```js
      const mbSup = (filters.multibyteOverlay && node.role === 'repeater')
        ? (typeof node.multibyte_sup === 'number' ? node.multibyte_sup : 0)
        : null;
      const icon = useLabel ? makeRepeaterLabelIcon(node, isStale, isAlsoObserver, mbSup) : makeMarkerIcon(node.role || 'companion', isStale, isAlsoObserver, mbSup);
```

- [ ] **Step 7: Add multibyte row to `buildPopup`**

In `public/map.js`, find `function buildPopup(node)` (line 938). After the `hashPrefixRow` definition (after line 949), add:

```js
    const mbSup = typeof node.multibyte_sup === 'number' ? node.multibyte_sup : 0;
    const mbEvidence = node.multibyte_evidence || null;
    const mbLabel = mbSup >= 2 ? 'confirmed (advert)' : mbSup >= 1 ? 'suspected (path)' : 'not detected';
    const mbColor = mbSup >= 2 ? '#22c55e' : mbSup >= 1 ? '#86efac' : '#9ca3af';
    const mbRow = (filters.multibyteOverlay && node.role === 'repeater')
      ? `<dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Multibyte</dt>
          <dd style="color:${mbColor};margin-left:88px;padding:2px 0;">${safeEsc(mbLabel)}</dd>`
      : '';
```

Then in the `return` template, add `${mbRow}` after `${hashPrefixRow}`:

```js
        <dl style="margin-top:8px;font-size:12px;">
          ${hashPrefixRow}
          ${mbRow}
          <dt ...>Key</dt>
          ...
```

- [ ] **Step 8: Verify no JS errors in browser**

Start the dev server and open the map page. Check browser console for errors. Toggle "Show multibyte capability" on and off. Confirm:
- Toggle state persists on page reload
- Repeater markers change color when toggle is ON
- Non-repeater nodes are unaffected
- Popup shows "Multibyte" row only when toggle is ON and node is a repeater

- [ ] **Step 9: Commit**

```bash
git add public/map.js
git commit -m "feat(frontend): add multibyte capability overlay to map (#903)"
```

---

## Task 5: Update API spec

**Files:**
- Modify: `docs/api-spec.md`

- [ ] **Step 1: Add new fields to the `/api/nodes` response schema**

Find the `GET /api/nodes` section in `docs/api-spec.md`. In the node object properties, add:

```markdown
| `multibyte_sup` | integer | `0` = unknown, `1` = suspected, `2` = confirmed multibyte capability |
| `multibyte_evidence` | string \| null | `"advert"` (confirmed via advert), `"path"` (suspected via hop path), or `null` |
```

- [ ] **Step 2: Commit**

```bash
git add docs/api-spec.md
git commit -m "docs(api-spec): add multibyte_sup and multibyte_evidence to node response (#903)"
```
