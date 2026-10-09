package users

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/meshcore-analyzer/dbconfig"
)

// Version 6 adds non-reusing physical identities. PostgreSQL retains its
// existing version 5 layout; the five historical SQLite migrations are intact.
const SQLiteSchemaVersion = 6

var sqliteIdentities = []struct{ name, columns, body string }{
	{"users", "id,email,display_name,password_hash,role,status,created_at,activated_at,activated_by,last_login_at,email_bouncing", `(
		id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','disabled')),
		created_at INTEGER NOT NULL, activated_at INTEGER, activated_by INTEGER, last_login_at INTEGER,
		email_bouncing INTEGER NOT NULL DEFAULT 0)`},
	{"sessions", "id,token_hash,user_id,csrf_token,created_at,expires_at,last_seen_at,user_agent", `(
		id INTEGER PRIMARY KEY AUTOINCREMENT, token_hash TEXT NOT NULL UNIQUE,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, csrf_token TEXT NOT NULL,
		created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL,
		user_agent TEXT NOT NULL DEFAULT '')`},
	{"audit_log", "id,at,actor_user_id,action,target_user_id,detail", `(
		id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, actor_user_id INTEGER,
		action TEXT NOT NULL, target_user_id INTEGER, detail TEXT NOT NULL DEFAULT '{}')`},
	{"mail_log", "id,user_id,to_email,purpose,provider_message_id,sent_at,last_event,last_event_at,last_reason", `(
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
		to_email TEXT NOT NULL, purpose TEXT NOT NULL, provider_message_id TEXT, sent_at INTEGER NOT NULL,
		last_event TEXT NOT NULL DEFAULT 'sent', last_event_at INTEGER NOT NULL, last_reason TEXT NOT NULL DEFAULT '')`},
	{"mail_events", "id,mail_id,event,at,reason", `(
		id INTEGER PRIMARY KEY AUTOINCREMENT, mail_id INTEGER NOT NULL REFERENCES mail_log(id) ON DELETE CASCADE,
		event TEXT NOT NULL, at INTEGER NOT NULL, reason TEXT NOT NULL DEFAULT '', UNIQUE (mail_id,event,at))`},
	{"proposals", "id,kind,subject,status,proposer_id,reviewer_id,note,created_at,decided_at", `(
		id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, subject TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','revoked')),
		proposer_id INTEGER REFERENCES users(id) ON DELETE SET NULL, reviewer_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
		note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, decided_at INTEGER)`},
}

type sqliteSchemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sqliteQuote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Normalize only for schema comparison, never for execution or query rewriting.
// Keep quoted content intact so different defaults/check literals cannot match.
func normalizedSQLiteDDL(text string) string {
	var out strings.Builder
	var quote rune
	for _, r := range text {
		if quote != 0 {
			out.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			quote = r
			out.WriteRune(r)
			continue
		}
		if !unicode.IsSpace(r) {
			out.WriteRune(unicode.ToUpper(r))
		}
	}
	return out.String()
}

func sqliteObjects(q sqliteSchemaQueryer) (map[string]string, error) {
	rows, err := q.QueryContext(context.Background(), `SELECT type,name,sql FROM sqlite_schema WHERE sql IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			return nil, err
		}
		if kind == "table" && (name == "schema_version" || name == "sqlite_sequence" || name == "sqlite_stat1" || name == "sqlite_stat2" || name == "sqlite_stat3" || name == "sqlite_stat4") {
			continue
		}
		out[kind+":"+name] = normalizedSQLiteDDL(ddl)
	}
	return out, rows.Err()
}

// Legacy account schemas are defined by the original append-only migrations.
// Refuse unsupported extensions rather than lose their columns or constraints
// during a table rebuild. Current version 6 never repeats this migration.
func validateSQLiteLayout(q sqliteSchemaQueryer, version int) error {
	expected, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer expected.Close()
	expected.SetMaxOpenConns(1)
	legacyVersion := version
	if legacyVersion > len(legacyMigrations) {
		legacyVersion = len(legacyMigrations)
	}
	for _, migration := range legacyMigrations[:legacyVersion] {
		for _, statement := range migration {
			if _, err := expected.Exec(statement); err != nil {
				return err
			}
		}
	}
	want, err := sqliteObjects(expected)
	if err != nil {
		return err
	}
	if version == SQLiteSchemaVersion {
		for _, table := range sqliteIdentities {
			want["table:"+table.name] = normalizedSQLiteDDL(`CREATE TABLE ` + table.name + table.body)
		}
	}
	got, err := sqliteObjects(q)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return errors.New("users: unsupported SQLite account schema; preserve the database and review its extra or missing objects before upgrading")
	}
	for name, ddl := range want {
		actual, ok := got[name]
		if !ok {
			return errors.New("users: incomplete SQLite account schema")
		}
		if version == SQLiteSchemaVersion && strings.HasPrefix(name, "table:") {
			tableName := strings.TrimPrefix(name, "table:")
			quoted := strings.Replace(ddl, "CREATETABLE"+strings.ToUpper(tableName)+"(", "CREATETABLE"+sqliteQuote(tableName)+"(", 1)
			if actual == quoted {
				continue
			}
		}
		if actual == ddl {
			continue
		}
		// Accept an already non-reusing allocator for a known INTEGER PK, and
		// preserve its sequence high water. Hidden mail-event rowids are v1-v5.
		safeIdentity := name == "table:users" || name == "table:sessions" || name == "table:audit_log" || name == "table:mail_log" || name == "table:proposals"
		if safeIdentity && actual == strings.Replace(ddl, "IDINTEGERPRIMARYKEY", "IDINTEGERPRIMARYKEYAUTOINCREMENT", 1) {
			continue
		}
		return errors.New("users: unsupported SQLite account columns or constraints; the original database was not changed")
	}
	return nil
}

// ApplySQLite atomically upgrades known SQLite account layouts through version 6.
// A reserved connection keeps FK settings local; BEGIN IMMEDIATE serializes
// competing migration/writer processes before any read-modify-write work.
func ApplySQLite(db *sql.DB) (err error) {
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	var v int
	if e := db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); e == nil && v == SQLiteSchemaVersion {
		return AssertSQLiteReady(db)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if _, restore := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA foreign_keys=%d`, foreignKeys)); restore != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, errors.New("users: cannot restore SQLite connection foreign-key enforcement"))
		}
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	var hasVersion int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_version'`).Scan(&hasVersion); err != nil {
		return err
	}
	if hasVersion == 0 {
		objects, err := sqliteObjects(conn)
		if err != nil {
			return err
		}
		if len(objects) != 0 {
			return errors.New("users: refusing to initialize accounts in an existing unrelated SQLite database")
		}
		if _, err := conn.ExecContext(ctx, `CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(0)`); err != nil {
			return err
		}
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(version),0) FROM schema_version`).Scan(&count, &v); err != nil {
		return err
	}
	// The historical initializer committed CREATE before the version-0 row.
	// Recover only its empty layout, never reinterpret occupied data as fresh.
	if count == 0 {
		if err := validateSQLiteLayout(conn, 0); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES(0)`); err != nil {
			return err
		}
		count = 1
	}
	if count != 1 || v < 0 || v > SQLiteSchemaVersion {
		return errors.New("users: invalid or unsupported SQLite account schema version")
	}
	if v == SQLiteSchemaVersion {
		if err := assertSQLiteReady(conn); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `COMMIT`)
		return err
	}
	if err := validateSQLiteLayout(conn, v); err != nil {
		return err
	}
	for i := v; i < len(legacyMigrations); i++ {
		for _, statement := range legacyMigrations[i] {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("users: SQLite migration %d: %w", i+1, err)
			}
		}
	}
	high, err := sqliteAccountHighWater(conn)
	if err != nil {
		return err
	}
	indexRows, err := conn.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL
		AND tbl_name IN ('users','sessions','audit_log','mail_log','mail_events','proposals') ORDER BY name`)
	if err != nil {
		return err
	}
	var indexes []string
	for indexRows.Next() {
		var statement string
		if err := indexRows.Scan(&statement); err != nil {
			indexRows.Close()
			return err
		}
		indexes = append(indexes, statement)
	}
	indexErr := indexRows.Err()
	indexRows.Close()
	if indexErr != nil {
		return indexErr
	}
	for _, table := range sqliteIdentities {
		temporary := "corescope_identity_" + table.name
		if _, err := conn.ExecContext(ctx, `CREATE TABLE `+sqliteQuote(temporary)+table.body); err != nil {
			return err
		}
		columns := table.columns
		sourceColumns := columns
		if table.name == "mail_events" {
			sourceColumns = "rowid," + strings.TrimPrefix(columns, "id,")
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+sqliteQuote(temporary)+` (`+columns+`) SELECT `+sourceColumns+` FROM `+sqliteQuote(table.name)); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DROP TABLE `+sqliteQuote(table.name)); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `ALTER TABLE `+sqliteQuote(temporary)+` RENAME TO `+sqliteQuote(table.name)); err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx, `UPDATE sqlite_sequence SET seq=?2 WHERE name=?1`, table.name, high[table.name])
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := conn.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES(?1,?2)`, table.name, high[table.name]); err != nil {
				return err
			}
		}
	}
	// Recreate the exact declared indexes lost when their owning tables changed.
	for _, statement := range indexes {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return rowErr
	}
	if invalid {
		return errors.New("users: SQLite account identity migration found invalid foreign keys; all changes were rolled back")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE schema_version SET version=?1`, SQLiteSchemaVersion); err != nil {
		return err
	}
	if err := assertSQLiteReady(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func sqliteAccountHighWater(q sqliteSchemaQueryer) (map[string]int64, error) {
	ctx := context.Background()
	high := map[string]int64{}
	var hasSequence int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='sqlite_sequence' AND type='table'`).Scan(&hasSequence); err != nil {
		return nil, err
	}
	if hasSequence != 0 {
		rows, err := q.QueryContext(ctx, `SELECT name,MAX(seq),count(*) FROM sqlite_sequence GROUP BY name`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			var seq int64
			var count int
			if err := rows.Scan(&name, &seq, &count); err != nil {
				rows.Close()
				return nil, err
			}
			if count != 1 || seq < 0 {
				rows.Close()
				return nil, errors.New("users: invalid SQLite account sequence state")
			}
			high[name] = seq
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, table := range sqliteIdentities {
		column := "id"
		if table.name == "mail_events" {
			column = "rowid"
		}
		var n int64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(`+sqliteQuote(column)+`),0) FROM `+sqliteQuote(table.name)).Scan(&n); err != nil {
			return nil, err
		}
		if n > high[table.name] {
			high[table.name] = n
		}
	}
	for _, ref := range []struct{ target, table, column string }{
		{"users", "audit_log", "actor_user_id"}, {"users", "audit_log", "target_user_id"}, {"users", "users", "activated_by"},
		{"users", "sessions", "user_id"}, {"users", "tokens", "user_id"}, {"users", "mail_log", "user_id"},
		{"users", "proposals", "proposer_id"}, {"users", "proposals", "reviewer_id"},
		{"users", "user_settings", "user_id"}, {"users", "notification_prefs", "user_id"},
		{"users", "notification_watches", "user_id"}, {"users", "notification_state", "user_id"}, {"mail_log", "mail_events", "mail_id"},
	} {
		var n int64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(`+sqliteQuote(ref.column)+`),0) FROM `+sqliteQuote(ref.table)).Scan(&n); err != nil {
			return nil, err
		}
		if n > high[ref.target] {
			high[ref.target] = n
		}
	}
	return high, nil
}

func AssertSQLiteReady(db *sql.DB) error {
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	return assertSQLiteReady(db)
}

func assertSQLiteReady(q sqliteSchemaQueryer) error {
	ctx := context.Background()
	var count, version int
	if err := q.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(version),0) FROM schema_version`).Scan(&count, &version); err != nil || count != 1 || version != SQLiteSchemaVersion {
		return errors.New("users: SQLite account schema needs a supported upgrade")
	}
	return validateSQLiteLayout(q, SQLiteSchemaVersion)
}
