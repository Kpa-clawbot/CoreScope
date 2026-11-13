package main

import (
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestServerSourceHasNoCachedRWCalls enforces issue #1287: after the
// follow-up to #1283, cmd/server/ must contain ZERO writer call sites.
// Specifically, no `cachedRW(`, no `mode=rw`, and no `sql.Open(...rw...)`
// in non-test source files. All schema migrations, backfills, and
// neighbor-edge persistence must live in cmd/ingestor or a shared
// package — the server is the read path.
func TestServerSourceHasNoCachedRWCalls(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/server dir: %v", err)
	}
	// Patterns that indicate write-side DB usage on the server.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bcachedRW\s*\(`),
		regexp.MustCompile(`mode=rw`),
		regexp.MustCompile(`sql\.Open\([^)]*\?[^)]*_journal_mode=WAL[^)]*\)`),
		// The node directory is ingestor-owned (#1283/#1287); the
		// server opens mode=ro since #1289 and may never write it.
		//
		// This deliberately matches ANY column rather than naming
		// them. The column-specific form is what let #1598 through:
		// #1324 added `UPDATE nodes SET multibyte_` after relocating
		// PR #903's writer, then touchRelayLastSeen introduced a
		// second write shape (`SET last_seen`) that the grep did not
		// cover. It failed on every call for months with the error
		// discarded at the call site, so nodes.last_seen silently
		// degraded into an advert-age proxy. Enumerating shapes does
		// not scale — forbid the table instead.
		//
		// Writers live in cmd/ingestor: Store.TouchRelayNodes (#1598)
		// and RunMultibyteCapPersist (#1324, fed by a snapshot the
		// server publishes via internal/mbcapqueue).
		// Shapes are normalised before matching (see nodeTableWritePattern):
		// optional OR-conflict clause, optional quoting, optional alias.
		nodeTableWritePattern(`UPDATE(\s+OR\s+\w+)?`, `SET`),
		nodeTableWritePattern(`INSERT\s+(OR\s+\w+\s+)?INTO`, ``),
		nodeTableWritePattern(`REPLACE\s+INTO`, ``),
		nodeTableWritePattern(`DELETE\s+FROM`, ``),
		regexp.MustCompile(`\bpersistMultibyteCapability\s*\(`),
		regexp.MustCompile(`\bmaybePersistMultibyteCapability\s*\(`),
	}
	violations := []string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, p := range patterns {
			if loc := p.FindIndex(b); loc != nil {
				// Get line number
				line := 1 + strings.Count(string(b[:loc[0]]), "\n")
				violations = append(violations, fmt.Sprintf("%s:%d: %s", name, line, p.String()))
			}
		}
	}
	if len(violations) > 0 {
		t.Errorf("cmd/server/ contains forbidden writer call sites (#1287):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestServerDBHasNoWriteMethods enforces the architectural invariant from
// issue #1283: cmd/server is the read path. All write/maintenance methods
// (PruneOldPackets, PruneOldMetrics, RemoveStaleObservers) MUST live on
// the ingestor's *Store, not on the server's *DB.
//
// Before the fix, these methods existed on cmd/server/*DB and used
// cachedRW(db.path) to acquire a write lock, racing with the ingestor's
// concurrent INSERTs and producing SQLITE_BUSY (the bug in #1283).
// After the fix, this test passes because the methods are gone.
func TestServerDBHasNoWriteMethods(t *testing.T) {
	forbidden := []string{
		"PruneOldPackets",
		"PruneOldMetrics",
		"RemoveStaleObservers",
		"PurgeStaleObservers",
		// #738 / one-click geo-prune: the DELETE must live on the
		// ingestor's *Store. The server's HTTP handler now enqueues a
		// marker file (see internal/prunequeue); it does not write.
		"DeleteNodesByPubkeys",
		// #1324 follow-up: PR #903 originally added these to *PacketStore
		// (not *DB), and they UPDATEd nodes/inactive_nodes from a
		// mode=ro handle. After relocation, the methods live in the
		// ingestor's *Store (cmd/ingestor/multibyte_persist.go). Server
		// must expose neither on *DB nor on *PacketStore — see the
		// dedicated test below for *PacketStore.
	}
	typ := reflect.TypeOf((*DB)(nil))
	for _, name := range forbidden {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("server *DB exposes forbidden write method %q — must be relocated to ingestor (#1283)", name)
		}
	}
}

// TestServerDBConnIsReadOnly asserts that the *sql.DB the server opens
// cannot acquire a write lock. The server has always opened mode=ro, but
// before #1283 it routed around that by calling cachedRW(path) to get a
// second RW handle. After the fix, server-side writes are impossible
// because there is no helper to open a writable connection.
func TestServerDBConnIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ro_invariant.db"

	// Bootstrap a minimal DB with the ingestor-style WAL opener so the
	// server can attach in read-only mode.
	if err := bootstrapMinimalDB(t, path); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer d.conn.Close()

	_, err = d.conn.Exec(`INSERT INTO nodes (public_key, name) VALUES ('x','y')`)
	if err == nil {
		t.Fatalf("expected INSERT via server *DB to fail (read-only invariant)")
	}
}

// bootstrapMinimalDB creates a tiny DB with the columns these tests
// need, opened with WAL so the read-only opener in OpenDB can attach.
// Kept in *_test.go so it does NOT add any write capability to the
// production server binary.
func bootstrapMinimalDB(tb testing.TB, path string) error {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)
	rw, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	defer rw.Close()
	if _, err := rw.Exec(`CREATE TABLE IF NOT EXISTS nodes (public_key TEXT PRIMARY KEY, name TEXT)`); err != nil {
		return err
	}
	// prepareStatements compiles eagerly under mattn; give it the rest of the
	// surface it references so OpenDB gets far enough to test read-onlyness.
	ensurePreparable(tb, rw)
	return nil
}

// TestPacketStoreHasNoMultibytePersistMethods enforces the #1324 follow-up:
// PR #903 wired persistMultibyteCapability + maybePersistMultibyteCapability
// onto *PacketStore in cmd/server. Both executed UPDATEs on
// nodes/inactive_nodes from a mode=ro DB handle — impossible since #1289.
// After relocation the persistence lives in cmd/ingestor/*Store; the
// server only publishes a snapshot via internal/mbcapqueue. This test
// fails if a future change re-introduces these methods on *PacketStore.
func TestPacketStoreHasNoMultibytePersistMethods(t *testing.T) {
	forbidden := []string{
		"persistMultibyteCapability",
		"maybePersistMultibyteCapability",
	}
	typ := reflect.TypeOf((*PacketStore)(nil))
	for _, name := range forbidden {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("server *PacketStore exposes forbidden write method %q — must be relocated to ingestor (#1324)", name)
		}
	}
}

// nodeTableWritePattern builds a matcher for DML against the
// ingestor-owned node directory. verb is the leading keyword(s); trailer
// is what must follow the table name (e.g. SET for UPDATE), or empty.
//
// Covers the shapes a plain `UPDATE nodes SET` regex misses:
//
//	UPDATE OR REPLACE nodes SET ...
//	UPDATE "nodes" SET ... / `nodes` / [nodes]
//	UPDATE nodes AS n SET ...
//	REPLACE INTO nodes ...
//
// Residual gap, stated rather than papered over: SQL assembled at
// runtime (fmt.Sprintf("UPDATE %s SET ...", tbl)) cannot be caught by
// source grepping. That is what TestServerDBConnIsReadOnly and
// TestServerDBHasNoWriteMethods are for — the handle physically cannot
// write and the write helpers do not exist on the type. This test is the
// cheap first line that names the offending file and line; those two are
// the structural backstop.
func nodeTableWritePattern(verb, trailer string) *regexp.Regexp {
	const table = "[\"`\\[]?(nodes|inactive_nodes)[\"`\\]]?"
	const alias = `(\s+(AS\s+)?[a-z]\w*)?`
	expr := `(?i)` + verb + `\s+` + table + alias
	if trailer != "" {
		expr += `\s+` + trailer
	} else {
		expr += `\b`
	}
	return regexp.MustCompile(expr)
}

// TestOpenDBRefusesMissingDatabase pins the behaviour that makes mode=ro
// meaningful: OpenDB must fail on a path that does not exist rather than
// creating an empty database there.
//
// This is worth a test of its own because the guarantee is not obvious from the
// call site. github.com/mattn/go-sqlite3 always passes
// SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE to sqlite3_open_v2; what restricts it
// is the mode=ro parameter in the URI, which only takes effect because mattn's
// C wrapper ORs SQLITE_OPEN_URI into the flags (sqlite3.go _sqlite3_open_v2).
// Lose the file: prefix, or the URI flag, and this silently degrades into a
// read-write open that manufactures a fresh empty database — which the server
// would then happily serve. cmd/decrypt shipped exactly that bug for a while by
// building its DSN without the file: prefix.
func TestOpenDBRefusesMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.db")

	db, err := OpenDB(missing)
	if err == nil {
		db.Close()
		t.Fatal("OpenDB succeeded against a nonexistent database; mode=ro is not in effect")
	}

	// Neither the database itself nor a file literally named after the DSN
	// (what happens when URI handling is off) may have been created.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("read temp dir: %v", rerr)
	}
	for _, e := range entries {
		t.Errorf("OpenDB created %q; a read-only open must not write anything", e.Name())
	}
}

// TestUsersOpenIsTheOnlyServerWritePath pins the single exception to the
// read-only invariant (user management, opt-in): cmd/server may open a
// writable store only via users.Open, exactly once, and always passing the
// measurement DB path as forbidden.
func TestUsersOpenIsTheOnlyServerWritePath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`users\.Open\(`)
	want := regexp.MustCompile(`users\.Open\(set\.dbPath, measurementDBPath\)`)
	calls, good := 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		calls += len(call.FindAll(src, -1))
		good += len(want.FindAll(src, -1))
	}
	if calls != 1 || good != 1 {
		t.Fatalf("users.Open calls in cmd/server: %d total, %d with the measurement DB forbidden; want exactly 1 and 1", calls, good)
	}
}

// analyzerReadTxAllowed lists the functions allowed to open a transaction on
// the analyzer connection, as "file:func". Each is a read snapshot: several
// SELECTs that must see one consistent state. A transaction is never needed
// to read one statement, and the server has no business holding one to write.
// An entry that no longer begins a transaction fails the test, so the list
// cannot go stale and silently cover a later write.
var analyzerReadTxAllowed = map[string]bool{
	"scope_audit.go:ScopeAuditForwardingHeardIn": true,
}

// analyzerSQLCallArgs maps database/sql methods that take SQL to the index of
// the SQL argument.
var analyzerSQLCallArgs = map[string]int{
	"Exec": 0, "ExecContext": 1,
	"Query": 0, "QueryContext": 1,
	"QueryRow": 0, "QueryRowContext": 1,
	"Prepare": 0, "PrepareContext": 1,
}

var analyzerWriteSQL = regexp.MustCompile(`(?i)^(INSERT|UPDATE|DELETE|REPLACE)\b`)

// analyzerWriteViolations parses Go sources and reports, as "file:line: what",
// every place that
//
//   - calls Begin / BeginTx on the analyzer connection (the conn field of *DB,
//     or a *sql.DB passed around as conn) outside analyzerReadTxAllowed, or
//   - passes SQL starting with INSERT, UPDATE, DELETE or REPLACE to Exec,
//     Query*, or Prepare* on any receiver. In cmd/server every such receiver
//     is the analyzer handle or a Tx/Stmt made from it (users.db is written
//     only inside internal/users), so the receiver is not narrowed.
//
// The SQL is resolved from a string literal, a package-level string constant
// or variable, or the leading operand of a + concatenation, after leading
// whitespace and -- comments. Residual gap: SQL built at runtime
// (fmt.Sprintf, strings.Builder) cannot be resolved; TestServerDBConnIsReadOnly
// is the structural backstop for that.
func analyzerWriteViolations(t *testing.T, fset *token.FileSet, files map[string]*ast.File) []string {
	t.Helper()
	consts := map[string]string{}
	var resolve func(e ast.Expr) (string, bool)
	resolve = func(e ast.Expr) (string, bool) {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(v.Value)
			return s, err == nil
		case *ast.Ident:
			s, ok := consts[v.Name]
			return s, ok
		case *ast.ParenExpr:
			return resolve(v.X)
		case *ast.BinaryExpr:
			if v.Op == token.ADD {
				return resolve(v.X)
			}
		}
		return "", false
	}
	// Package-level string constants and variables, in two passes so a
	// value built from another (const a = b + "...") resolves either way.
	for pass := 0; pass < 2; pass++ {
		for _, f := range files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					if len(vs.Values) != len(vs.Names) {
						continue
					}
					for i, n := range vs.Names {
						if s, ok := resolve(vs.Values[i]); ok {
							consts[n.Name] = s
						}
					}
				}
			}
		}
	}

	isAnalyzerConn := func(e ast.Expr) bool {
		switch v := e.(type) {
		case *ast.Ident:
			return v.Name == "conn"
		case *ast.SelectorExpr:
			return v.Sel.Name == "conn"
		}
		return false
	}
	leadingSQL := func(s string) string {
		for {
			s = strings.TrimSpace(s)
			if !strings.HasPrefix(s, "--") {
				return s
			}
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return ""
			}
			s = s[nl+1:]
		}
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	seenAllowed := map[string]bool{}
	for _, name := range names {
		for _, d := range files[name].Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := name + ":" + fd.Name.Name
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pos := fset.Position(call.Pos())
				where := fmt.Sprintf("%s:%d", name, pos.Line)
				method := sel.Sel.Name
				if (method == "Begin" || method == "BeginTx") && isAnalyzerConn(sel.X) {
					if analyzerReadTxAllowed[fn] {
						seenAllowed[fn] = true
					} else {
						out = append(out, where+": "+method+" on the analyzer connection in "+fd.Name.Name)
					}
				}
				if idx, ok := analyzerSQLCallArgs[method]; ok && idx < len(call.Args) {
					if q, ok := resolve(call.Args[idx]); ok {
						if m := analyzerWriteSQL.FindString(leadingSQL(q)); m != "" {
							out = append(out, where+": "+strings.ToUpper(m)+" passed to "+method+" in "+fd.Name.Name)
						}
					}
				}
				return true
			})
		}
	}
	return append(out, analyzerAllowlistStale(files, seenAllowed)...)
}

// analyzerAllowlistStale reports allowlisted read snapshots that no longer
// open a transaction, but only for files present in this parse (the self-test
// below parses fixtures, not the package).
func analyzerAllowlistStale(files map[string]*ast.File, seen map[string]bool) []string {
	var out []string
	for fn := range analyzerReadTxAllowed {
		file := fn[:strings.IndexByte(fn, ':')]
		if _, ok := files[file]; ok && !seen[fn] {
			out = append(out, fn+": allowlisted read transaction no longer found; remove it from analyzerReadTxAllowed")
		}
	}
	sort.Strings(out)
	return out
}

func parseServerSources(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
	}
	return fset, files
}

// TestServerSourceHasNoAnalyzerWrites enforces #1856: cmd/server never opens a
// write transaction on, or sends write SQL to, the analyzer DB. The server's
// content-hash migration did both on the mode=ro handle (#1283), so it could
// never commit; it now lives in cmd/ingestor/hash_migrate.go.
func TestServerSourceHasNoAnalyzerWrites(t *testing.T) {
	fset, files := parseServerSources(t)
	if v := analyzerWriteViolations(t, fset, files); len(v) > 0 {
		t.Errorf("cmd/server writes the analyzer DB (#1856); writes belong in cmd/ingestor:\n  %s",
			strings.Join(v, "\n  "))
	}
}

// TestAnalyzerWriteViolationsDetects proves the checker bites on the shapes
// the removed server hash migration used, and leaves reads alone.
func TestAnalyzerWriteViolationsDetects(t *testing.T) {
	const src = `package main

const updateHash = "UPDATE transmissions SET hash = ? WHERE id = ?"
const pruneSQL = ` + "`" + `
	-- leading comment
	DELETE FROM observations WHERE id IN (` + "`" + ` + sel + ` + "`" + `)` + "`" + `

func migrate(store *PacketStore) {
	dbTx, _ := store.db.conn.Begin()
	stmt, _ := dbTx.Prepare(updateHash)
	dbTx.Exec("UPDATE observations SET transmission_id = ? WHERE transmission_id = ?", 1, 2)
	dbTx.ExecContext(ctx, "delete from transmissions where id = ?", 1)
	dbTx.Exec(pruneSQL)
	conn.BeginTx(ctx, nil)
	_ = stmt
}

func read(db *DB) {
	db.conn.Query("SELECT hash FROM transmissions WHERE note = 'UPDATE'")
	db.conn.QueryRow(` + "`" + `
		SELECT status FROM _async_migrations` + "`" + `)
	ws.conn.Close()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := analyzerWriteViolations(t, fset, map[string]*ast.File{"fixture.go": f})
	want := []string{
		"fixture.go:9: Begin on the analyzer connection in migrate",
		"fixture.go:10: UPDATE passed to Prepare in migrate",
		"fixture.go:11: UPDATE passed to Exec in migrate",
		"fixture.go:12: DELETE passed to ExecContext in migrate",
		"fixture.go:13: DELETE passed to Exec in migrate",
		"fixture.go:14: BeginTx on the analyzer connection in migrate",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestPostgresOpenIsOnlyInOpenPostgresDB pins the Postgres side of the
// read-only invariant: the analyzer database is opened on Postgres in exactly
// one place, OpenPostgresDB, from the DSN pgDSNWithReadOnly produced — so every
// connection starts with default_transaction_read_only=on and the role's
// write privileges are checked before the handle is used.
func TestPostgresOpenIsOnlyInOpenPostgresDB(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	driverUse := regexp.MustCompile(`\bPostgresDriverName\b`)
	want := regexp.MustCompile(`sql\.Open\(dbx\.PostgresDriverName, roDSN\)`)
	var uses []string
	good := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for range driverUse.FindAll(src, -1) {
			uses = append(uses, name)
		}
		if name == "db_postgres.go" {
			good += len(want.FindAll(src, -1))
			if !strings.Contains(string(src), "roDSN, password, err := pgDSNWithReadOnly(dsn)") {
				t.Error("db_postgres.go no longer derives roDSN from pgDSNWithReadOnly")
			}
		}
	}
	if len(uses) != 1 || uses[0] != "db_postgres.go" || good != 1 {
		t.Fatalf("Postgres driver uses in cmd/server: %v (read-only opens: %d); want exactly one, sql.Open(dbx.PostgresDriverName, roDSN) in db_postgres.go", uses, good)
	}
}
