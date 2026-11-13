package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// TestPgSchemaParity pins the Postgres baseline (internal/dbschema/pgschema)
// to the schema a freshly opened SQLite store ends up with: same tables,
// columns (name, order-independent; type class; nullability; default),
// primary keys, foreign keys, UNIQUE constraints, named indexes (columns,
// uniqueness, partial-ness) and packets_v columns. A schema change on the
// SQLite side that is not mirrored in a new pgschema migration fails here.
//
// Out of scope: the opt-in observation_hops* tables (SQLite-only for now) and
// Postgres's own schema_version bookkeeping table.
func TestPgSchemaParity(t *testing.T) {
	pg := dbxtest.OpenPostgres(t)
	if err := pgschema.Apply(context.Background(), pg, t.Logf); err != nil {
		t.Fatalf("pgschema.Apply: %v", err)
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	store.WaitForAsyncMigrations()

	lite := describeSQLite(t, store.db)
	post := describePostgres(t, pg)

	for _, d := range diffSchemas(lite, post) {
		t.Error(d)
	}

	// Postgres-only invariant: every text column compares bytewise.
	for _, c := range uncollatedTextColumns(t, pg) {
		t.Errorf("postgres column %s is text without COLLATE \"C\"", c)
	}
}

// parityExcluded reports tables deliberately absent from one side.
func parityExcluded(table string) bool {
	return strings.HasPrefix(table, "observation_hops") || table == "schema_version" ||
		strings.HasPrefix(table, "sqlite_")
}

type colDesc struct {
	typeClass string // int | real | text
	notNull   bool
	dflt      string // normalised; "" = no default
}

type idxDesc struct {
	unique  bool
	partial bool
	cols    string // comma-joined; "<expr>" for an expression
}

type tableDesc struct {
	cols    map[string]colDesc
	pk      string   // comma-joined, in key order
	uniques []string // UNIQUE-constraint column lists, sorted
	fks     []string // "col->table(col) ON DELETE x", sorted
}

type schemaDesc struct {
	tables  map[string]*tableDesc
	indexes map[string]string // index name -> "table" (explicit indexes only)
	idx     map[string]idxDesc
	viewCol []string // packets_v columns in order
}

var (
	pgNowText  = regexp.MustCompile(`(?i)^to_char\(\(?now\(\) AT TIME ZONE 'UTC'(::text)?\)?, 'YYYY-MM-DD HH24:MI:SS'(::text)?\)$`)
	pgNowEpoch = regexp.MustCompile(`(?i)^\(?EXTRACT\(epoch FROM now\(\)\)\)?::bigint$`)
	pgCast     = regexp.MustCompile(`::[a-z ]+$`)
)

// normSQLiteDefault and normPostgresDefault map each backend's default
// expression onto one vocabulary:
// literals verbatim, and the two "now" forms the translation rules allow.
func normSQLiteDefault(d sql.NullString) string {
	v := strings.TrimSpace(d.String)
	switch {
	case !d.Valid, strings.EqualFold(v, "NULL"):
		return ""
	case v == "datetime('now')", strings.EqualFold(v, "CURRENT_TIMESTAMP"):
		return "<now text>"
	case v == "CAST(strftime('%s','now') AS INTEGER)":
		return "<now epoch>"
	}
	return v
}

func normPostgresDefault(d sql.NullString) string {
	v := strings.TrimSpace(d.String)
	switch {
	case !d.Valid, strings.EqualFold(v, "NULL"), strings.HasPrefix(strings.ToUpper(v), "NULL::"):
		return ""
	case pgNowText.MatchString(v):
		return "<now text>"
	case pgNowEpoch.MatchString(v):
		return "<now epoch>"
	}
	return pgCast.ReplaceAllString(v, "")
}

func sqliteTypeClass(decl string) string {
	u := strings.ToUpper(decl)
	switch {
	case strings.Contains(u, "INT"):
		return "int"
	case strings.Contains(u, "REAL"), strings.Contains(u, "FLOA"), strings.Contains(u, "DOUB"):
		return "real"
	case strings.Contains(u, "TEXT"), strings.Contains(u, "CHAR"), strings.Contains(u, "CLOB"), u == "DATETIME":
		return "text"
	}
	return "unknown sqlite type " + decl
}

func postgresTypeClass(dataType string) string {
	switch dataType {
	case "bigint":
		return "int"
	case "double precision":
		return "real"
	case "text":
		return "text"
	}
	return "unmapped postgres type " + dataType
}

func queryStrings(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func describeSQLite(t *testing.T, db *sql.DB) *schemaDesc {
	t.Helper()
	sd := &schemaDesc{tables: map[string]*tableDesc{}, indexes: map[string]string{}, idx: map[string]idxDesc{}}
	for _, tbl := range queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`) {
		if parityExcluded(tbl) {
			continue
		}
		td := &tableDesc{cols: map[string]colDesc{}}
		sd.tables[tbl] = td

		rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		pk := map[int]string{}
		for rows.Next() {
			var cid, notNull, pkPos int
			var name, typ string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pkPos); err != nil {
				t.Fatal(err)
			}
			// A PRIMARY KEY column is NOT NULL in Postgres. In SQLite an
			// INTEGER PRIMARY KEY cannot hold NULL either, but a TEXT one
			// legally can (a historical quirk) and table_info reports it as
			// nullable; nothing in CoreScope relies on a NULL key.
			td.cols[name] = colDesc{typeClass: sqliteTypeClass(typ), notNull: notNull == 1 || pkPos > 0, dflt: normSQLiteDefault(dflt)}
			if pkPos > 0 {
				pk[pkPos] = name
			}
		}
		rows.Close()
		for i := 1; i <= len(pk); i++ {
			td.pk += sep(i > 1) + pk[i]
		}

		rows, err = db.Query(fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, seq int
			var ref, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &ref, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatal(err)
			}
			td.fks = append(td.fks, fmt.Sprintf("%s->%s(%s) ON DELETE %s", from, ref, to, onDelete))
		}
		rows.Close()
		sort.Strings(td.fks)

		type ixRow struct {
			name, origin    string
			unique, partial bool
		}
		var ixs []ixRow
		rows, err = db.Query(fmt.Sprintf(`PRAGMA index_list(%q)`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var seq, unique, partial int
			var r ixRow
			if err := rows.Scan(&seq, &r.name, &unique, &r.origin, &partial); err != nil {
				t.Fatal(err)
			}
			r.unique, r.partial = unique == 1, partial == 1
			ixs = append(ixs, r)
		}
		rows.Close()
		for _, ix := range ixs {
			cols := sqliteIndexCols(t, db, ix.name)
			switch ix.origin {
			case "pk": // compared via td.pk
			case "u":
				td.uniques = append(td.uniques, cols)
			default:
				sd.indexes[ix.name] = tbl
				sd.idx[ix.name] = idxDesc{unique: ix.unique, partial: ix.partial, cols: cols}
			}
		}
		sort.Strings(td.uniques)
	}

	rows, err := db.Query(`PRAGMA table_info(packets_v)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pkPos int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pkPos); err != nil {
			t.Fatal(err)
		}
		sd.viewCol = append(sd.viewCol, name)
	}
	return sd
}

func sqliteIndexCols(t *testing.T, db *sql.DB, index string) string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, index))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var seq, cid int
		var name sql.NullString
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			t.Fatal(err)
		}
		if name.Valid {
			cols = append(cols, name.String)
		} else {
			cols = append(cols, "<expr>")
		}
	}
	return strings.Join(cols, ",")
}

// pgKeyCols renders an attnum array column (conkey, indkey) as comma-joined
// names, "<expr>" for 0, in array order. Its two %s are the array and the
// oid of the relation whose attribute numbers it holds.
const pgKeyCols = `(SELECT string_agg(COALESCE(a.attname::text, '<expr>'), ',' ORDER BY k.ord)
	FROM unnest(%s) WITH ORDINALITY k(n, ord)
	LEFT JOIN pg_attribute a ON a.attrelid = %s AND a.attnum = k.n)`

func describePostgres(t *testing.T, db *sql.DB) *schemaDesc {
	t.Helper()
	sd := &schemaDesc{tables: map[string]*tableDesc{}, indexes: map[string]string{}, idx: map[string]idxDesc{}}
	for _, tbl := range queryStrings(t, db, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`) {
		if parityExcluded(tbl) {
			continue
		}
		td := &tableDesc{cols: map[string]colDesc{}}
		sd.tables[tbl] = td

		rows, err := db.Query(`SELECT column_name, data_type, is_nullable, column_default
			FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ, nullable string
			var dflt sql.NullString
			if err := rows.Scan(&name, &typ, &nullable, &dflt); err != nil {
				t.Fatal(err)
			}
			td.cols[name] = colDesc{typeClass: postgresTypeClass(typ), notNull: nullable == "NO", dflt: normPostgresDefault(dflt)}
		}
		rows.Close()

		// Constraints: p = primary key, u = unique, f = foreign key.
		rows, err = db.Query(fmt.Sprintf(`SELECT con.contype::text, `+pgKeyCols+`,
				COALESCE(cf.relname::text, ''), COALESCE(`+pgKeyCols+`, ''), con.confdeltype::text
			FROM pg_constraint con LEFT JOIN pg_class cf ON cf.oid = con.confrelid
			WHERE con.conrelid = ?::text::regclass AND con.contype IN ('p', 'u', 'f')`,
			"con.conkey", "con.conrelid", "con.confkey", "con.confrelid"), tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var typ, cols, ref, refCols, onDelete string
			if err := rows.Scan(&typ, &cols, &ref, &refCols, &onDelete); err != nil {
				t.Fatal(err)
			}
			switch typ {
			case "p":
				td.pk = cols
			case "u":
				td.uniques = append(td.uniques, cols)
			case "f":
				td.fks = append(td.fks, fmt.Sprintf("%s->%s(%s) ON DELETE %s", cols, ref, refCols, pgDeleteAction(onDelete)))
			}
		}
		rows.Close()
		sort.Strings(td.uniques)
		sort.Strings(td.fks)

		// Indexes not backing a constraint, i.e. the CREATE INDEX ones.
		rows, err = db.Query(fmt.Sprintf(`SELECT ic.relname::text, i.indisunique, i.indpred IS NOT NULL, `+pgKeyCols+`
			FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
			WHERE i.indrelid = ?::text::regclass
			  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid AND c.conrelid = i.indrelid AND c.contype IN ('p', 'u', 'x'))`,
			"i.indkey::int2[]", "i.indrelid"), tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, cols string
			var ix idxDesc
			if err := rows.Scan(&name, &ix.unique, &ix.partial, &cols); err != nil {
				t.Fatal(err)
			}
			ix.cols = cols
			sd.indexes[name] = tbl
			sd.idx[name] = ix
		}
		rows.Close()
	}
	sd.viewCol = queryStrings(t, db, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'packets_v' ORDER BY ordinal_position`)
	return sd
}

func pgDeleteAction(c string) string {
	switch c {
	case "a":
		return "NO ACTION"
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	}
	return "unknown action " + c
}

func uncollatedTextColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT c.table_name || '.' || c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables tb ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
		WHERE c.table_schema = current_schema() AND tb.table_type = 'BASE TABLE'
		  AND c.table_name <> 'schema_version' AND c.data_type = 'text'
		  AND COALESCE(c.collation_name, '') <> 'C'
		ORDER BY 1`)
}

func sep(b bool) string {
	if b {
		return ","
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// diffSchemas lists every difference between the SQLite and Postgres
// descriptions, one self-explanatory line each.
func diffSchemas(lite, post *schemaDesc) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	for _, tbl := range sortedKeys(lite.tables) {
		lt := lite.tables[tbl]
		pt, ok := post.tables[tbl]
		if !ok {
			add("table %s: in SQLite, missing in Postgres", tbl)
			continue
		}
		for _, col := range sortedKeys(lt.cols) {
			lc := lt.cols[col]
			pc, ok := pt.cols[col]
			if !ok {
				add("column %s.%s: in SQLite, missing in Postgres", tbl, col)
				continue
			}
			if lc.typeClass != pc.typeClass {
				add("column %s.%s: type class SQLite %s, Postgres %s", tbl, col, lc.typeClass, pc.typeClass)
			}
			if lc.notNull != pc.notNull {
				add("column %s.%s: NOT NULL SQLite %v, Postgres %v", tbl, col, lc.notNull, pc.notNull)
			}
			if lc.dflt != pc.dflt {
				add("column %s.%s: default SQLite %q, Postgres %q", tbl, col, lc.dflt, pc.dflt)
			}
		}
		for _, col := range sortedKeys(pt.cols) {
			if _, ok := lt.cols[col]; !ok {
				add("column %s.%s: in Postgres, missing in SQLite", tbl, col)
			}
		}
		if lt.pk != pt.pk {
			add("table %s: primary key SQLite (%s), Postgres (%s)", tbl, lt.pk, pt.pk)
		}
		if a, b := strings.Join(lt.uniques, " | "), strings.Join(pt.uniques, " | "); a != b {
			add("table %s: UNIQUE constraints SQLite [%s], Postgres [%s]", tbl, a, b)
		}
		if a, b := strings.Join(lt.fks, " | "), strings.Join(pt.fks, " | "); a != b {
			add("table %s: foreign keys SQLite [%s], Postgres [%s]", tbl, a, b)
		}
	}
	for _, tbl := range sortedKeys(post.tables) {
		if _, ok := lite.tables[tbl]; !ok {
			add("table %s: in Postgres, missing in SQLite", tbl)
		}
	}

	for _, name := range sortedKeys(lite.indexes) {
		li := lite.idx[name]
		pi, ok := post.idx[name]
		if !ok {
			add("index %s on %s: in SQLite, missing in Postgres", name, lite.indexes[name])
			continue
		}
		if lite.indexes[name] != post.indexes[name] {
			add("index %s: on table %s in SQLite, %s in Postgres", name, lite.indexes[name], post.indexes[name])
		}
		if li.unique != pi.unique {
			add("index %s: UNIQUE SQLite %v, Postgres %v", name, li.unique, pi.unique)
		}
		if li.partial != pi.partial {
			add("index %s: partial SQLite %v, Postgres %v", name, li.partial, pi.partial)
		}
		if li.cols != pi.cols {
			add("index %s: columns SQLite (%s), Postgres (%s)", name, li.cols, pi.cols)
		}
	}
	for _, name := range sortedKeys(post.indexes) {
		if _, ok := lite.indexes[name]; !ok {
			add("index %s on %s: in Postgres, missing in SQLite", name, post.indexes[name])
		}
	}

	if a, b := strings.Join(lite.viewCol, ","), strings.Join(post.viewCol, ","); a != b {
		add("view packets_v: columns SQLite (%s), Postgres (%s)", a, b)
	}
	if len(lite.tables) == 0 || len(lite.viewCol) == 0 {
		add("SQLite description is empty: introspection is broken")
	}
	return out
}
