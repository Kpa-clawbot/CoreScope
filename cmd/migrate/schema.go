package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// queryer is the read surface shared by *sql.DB, *sql.Conn and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// colClass is the type class every value is normalised to before it is
// written or hashed. Both schemas map onto the same classes: SQLite
// INTEGER/REAL/TEXT/DATETIME become int/float/text/text, Postgres
// BIGINT/DOUBLE PRECISION/TEXT the same (see pgschema's translation rules).
type colClass byte

const (
	clsInt   colClass = 'I'
	clsFloat colClass = 'F'
	clsText  colClass = 'T'
	clsBlob  colClass = 'B'
)

func (c colClass) String() string {
	switch c {
	case clsInt:
		return "integer"
	case clsFloat:
		return "float"
	case clsText:
		return "text"
	case clsBlob:
		return "blob"
	}
	return "?"
}

type column struct {
	name    string
	class   colClass
	notNull bool
}

type table struct {
	name    string
	cols    []column
	pk      []int    // indexes into cols, in primary-key order
	parents []string // tables this one references by foreign key
	serial  string   // identity (Postgres) / AUTOINCREMENT (SQLite) column, or ""
}

func (t *table) colIndex(name string) int {
	for i, c := range t.cols {
		if c.name == name {
			return i
		}
	}
	return -1
}

func (t *table) colNames() []string {
	out := make([]string, len(t.cols))
	for i, c := range t.cols {
		out[i] = c.name
	}
	return out
}

type schema struct {
	tables map[string]*table
}

// sortedNames returns the table names in byte order.
func (s *schema) sortedNames() []string {
	out := make([]string, 0, len(s.tables))
	for n := range s.tables {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// skipTables are never copied or compared.
var skipTables = map[string]string{
	"schema_version":            "schema bookkeeping, written by the target's own schema setup",
	"sqlite_sequence":           "SQLite-internal; rebuilt from MAX(id) after the copy",
	"observation_hops":          "opt-in SQLite-only index; the ingestor rebuilds it",
	"observation_hops_coverage": "opt-in SQLite-only index; the ingestor rebuilds it",
}

// bookkeepingTables are seeded by schema creation (pgschema's baseline
// inserts every migration name as done), so they are exempt from the
// empty-target rule, are upserted rather than bulk-loaded, and may hold extra
// seeded rows on the target side in verify.
var bookkeepingTables = map[string]bool{
	"_migrations":       true,
	"_async_migrations": true,
}

// sqliteReadOnlyDSN opens path read-only as a URI filename. Escaping the path
// keeps '?', '#' and '%' in file names from being read as URI syntax.
func sqliteReadOnlyDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	return u.String() + "?mode=ro&_busy_timeout=5000"
}

// loadSchema discovers tables, columns, type classes, primary keys, foreign
// keys and identity columns.
func loadSchema(ctx context.Context, q queryer, kind backend) (*schema, error) {
	if kind == postgresBackend {
		return loadPostgresSchema(ctx, q)
	}
	return loadSQLiteSchema(ctx, q)
}

func loadSQLiteSchema(ctx context.Context, q queryer) (*schema, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	type tdef struct{ name, sql string }
	var defs []tdef
	for rows.Next() {
		var d tdef
		if err := rows.Scan(&d.name, &d.sql); err != nil {
			rows.Close()
			return nil, err
		}
		defs = append(defs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s := &schema{tables: map[string]*table{}}
	for _, d := range defs {
		t := &table{name: d.name}
		if skipReason(d.name) != "" {
			// Never read; sqlite_sequence's columns are not even typed.
			s.tables[t.name] = t
			continue
		}
		type pkcol struct{ pos, idx int }
		var pks []pkcol
		cr, err := q.QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, d.name)
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %w", d.name, err)
		}
		for cr.Next() {
			var name, decl string
			var notNull, pk int
			if err := cr.Scan(&name, &decl, &notNull, &pk); err != nil {
				cr.Close()
				return nil, err
			}
			cls, err := sqliteClass(decl)
			if err != nil {
				cr.Close()
				return nil, fmt.Errorf("%s.%s: %w", d.name, name, err)
			}
			if pk > 0 {
				pks = append(pks, pkcol{pk, len(t.cols)})
			}
			t.cols = append(t.cols, column{name: name, class: cls, notNull: notNull != 0 || pk > 0})
		}
		cr.Close()
		if err := cr.Err(); err != nil {
			return nil, err
		}
		sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
		for _, p := range pks {
			t.pk = append(t.pk, p.idx)
		}
		if len(t.pk) == 1 && t.cols[t.pk[0]].class == clsInt && strings.Contains(strings.ToUpper(d.sql), "AUTOINCREMENT") {
			t.serial = t.cols[t.pk[0]].name
		}

		fr, err := q.QueryContext(ctx, `SELECT DISTINCT "table" FROM pragma_foreign_key_list(?)`, d.name)
		if err != nil {
			return nil, fmt.Errorf("foreign keys of %s: %w", d.name, err)
		}
		for fr.Next() {
			var p string
			if err := fr.Scan(&p); err != nil {
				fr.Close()
				return nil, err
			}
			t.parents = append(t.parents, p)
		}
		fr.Close()
		if err := fr.Err(); err != nil {
			return nil, err
		}
		s.tables[t.name] = t
	}
	return s, nil
}

// sqliteClass maps a declared SQLite column type onto a class, following
// SQLite's affinity rules except that DATE/TIME types (NUMERIC affinity) are
// text, as pgschema translates them.
func sqliteClass(decl string) (colClass, error) {
	u := strings.ToUpper(decl)
	switch {
	case strings.Contains(u, "INT"):
		return clsInt, nil
	case strings.Contains(u, "CHAR"), strings.Contains(u, "CLOB"), strings.Contains(u, "TEXT"),
		strings.Contains(u, "DATE"), strings.Contains(u, "TIME"):
		return clsText, nil
	case strings.Contains(u, "BLOB"):
		return clsBlob, nil
	case strings.Contains(u, "REAL"), strings.Contains(u, "FLOA"), strings.Contains(u, "DOUB"):
		return clsFloat, nil
	}
	return 0, fmt.Errorf("unsupported SQLite column type %q", decl)
}

func pgClass(dataType string) (colClass, error) {
	switch dataType {
	case "bigint", "integer", "smallint":
		return clsInt, nil
	case "double precision", "real":
		return clsFloat, nil
	case "text", "character varying", "character":
		return clsText, nil
	case "bytea":
		return clsBlob, nil
	}
	return 0, fmt.Errorf("unsupported Postgres column type %q", dataType)
}

func loadPostgresSchema(ctx context.Context, q queryer) (*schema, error) {
	s := &schema{tables: map[string]*table{}}

	tr, err := q.QueryContext(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	for tr.Next() {
		var n string
		if err := tr.Scan(&n); err != nil {
			tr.Close()
			return nil, err
		}
		s.tables[n] = &table{name: n}
	}
	tr.Close()
	if err := tr.Err(); err != nil {
		return nil, err
	}

	cr, err := q.QueryContext(ctx, `SELECT table_name, column_name, data_type, is_identity, is_nullable
		FROM information_schema.columns WHERE table_schema = current_schema()
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	for cr.Next() {
		var tn, cn, dt, ident, nullable string
		if err := cr.Scan(&tn, &cn, &dt, &ident, &nullable); err != nil {
			cr.Close()
			return nil, err
		}
		t := s.tables[tn]
		if t == nil {
			continue // a view
		}
		cls, err := pgClass(dt)
		if err != nil {
			cr.Close()
			return nil, fmt.Errorf("%s.%s: %w", tn, cn, err)
		}
		if ident == "YES" {
			t.serial = cn
		}
		t.cols = append(t.cols, column{name: cn, class: cls, notNull: nullable == "NO"})
	}
	cr.Close()
	if err := cr.Err(); err != nil {
		return nil, err
	}

	pr, err := q.QueryContext(ctx, `SELECT tc.table_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_schema = tc.constraint_schema
		 AND kcu.constraint_name = tc.constraint_name
		 AND kcu.table_name = tc.table_name
		WHERE tc.table_schema = current_schema() AND tc.constraint_type = 'PRIMARY KEY'
		ORDER BY tc.table_name, kcu.ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("list primary keys: %w", err)
	}
	for pr.Next() {
		var tn, cn string
		if err := pr.Scan(&tn, &cn); err != nil {
			pr.Close()
			return nil, err
		}
		if t := s.tables[tn]; t != nil {
			if i := t.colIndex(cn); i >= 0 {
				t.pk = append(t.pk, i)
			}
		}
	}
	pr.Close()
	if err := pr.Err(); err != nil {
		return nil, err
	}

	fr, err := q.QueryContext(ctx, `SELECT DISTINCT tc.table_name, ccu.table_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
		  ON ccu.constraint_schema = tc.constraint_schema
		 AND ccu.constraint_name = tc.constraint_name
		WHERE tc.table_schema = current_schema() AND tc.constraint_type = 'FOREIGN KEY'`)
	if err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	for fr.Next() {
		var child, parent string
		if err := fr.Scan(&child, &parent); err != nil {
			fr.Close()
			return nil, err
		}
		if t := s.tables[child]; t != nil {
			t.parents = append(t.parents, parent)
		}
	}
	fr.Close()
	return s, fr.Err()
}

// fkOrder returns names with every table after the tables it references
// (ties in byte order). References to tables outside names are ignored.
func fkOrder(s *schema, names []string) ([]string, error) {
	in := map[string]bool{}
	for _, n := range names {
		in[n] = true
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	done := map[string]bool{}
	var out []string
	for len(out) < len(sorted) {
		progressed := false
		for _, n := range sorted {
			if done[n] {
				continue
			}
			ready := true
			for _, p := range s.tables[n].parents {
				if p != n && in[p] && !done[p] {
					ready = false
					break
				}
			}
			if ready {
				done[n] = true
				out = append(out, n)
				progressed = true
			}
		}
		if !progressed {
			return nil, fmt.Errorf("foreign-key cycle among tables %v", sorted)
		}
	}
	return out, nil
}
