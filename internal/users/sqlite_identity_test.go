package users

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func legacySQLiteFixture(t *testing.T, version int, auto bool) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	mustSQL(t, db, `CREATE TABLE schema_version(version INTEGER NOT NULL)`, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, version))
	for _, migration := range LegacyMigrations()[:version] {
		for _, statement := range migration {
			if auto {
				statement = strings.Replace(statement, "id INTEGER PRIMARY KEY,", "id INTEGER PRIMARY KEY AUTOINCREMENT,", 1)
			}
			mustSQL(t, db, statement)
		}
	}
	return path, db
}
func mustSQL(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture SQL: %v", err)
		}
	}
}
func sqliteRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
func seedSQLiteAccounts(t *testing.T, db *sql.DB) {
	mustSQL(t, db,
		`INSERT INTO users VALUES(0,'zero@example.invalid','Zero','opaque-hash','admin','active',1,NULL,123,NULL,0)`,
		`INSERT INTO users VALUES(-7,'negative@example.invalid','Negative','opaque-hash-2','user','pending',-1,NULL,NULL,2,1)`,
		`INSERT INTO sessions VALUES(0,'session-hash',0,'csrf',1,9999999999,2,'agent')`,
		`INSERT INTO tokens VALUES('live-token',0,'reset',NULL,9999999999,NULL),('used-token',0,'email_change','next@example.invalid',9999999999,3)`,
		`INSERT INTO audit_log VALUES(-1,1,77,'user.old',89,' { "raw": true } ')`,
		`INSERT INTO mail_log VALUES(0,0,'zero@example.invalid','reset','provider-id',1,'sent',2,'')`,
		`INSERT INTO mail_events(rowid,mail_id,event,at,reason) VALUES(0,0,'sent',2,''),(14,0,'delivered',3,'delivery')`,
		`INSERT INTO user_settings VALUES(0,' { "saved": 1 } ',2,'generation',3)`,
		`INSERT INTO proposals VALUES(0,'hashtag_channel','#test','approved',0,NULL,'note',1,3)`,
		`INSERT INTO notification_prefs VALUES(0,1,'node.offline','unsubscribe',3)`,
		`INSERT INTO notification_watches VALUES(0,'public-key',3)`,
		`INSERT INTO notification_state VALUES(0,'node.offline','public-key','bad',3)`)
}

func TestSQLiteIdentityUpgradePreservesAllAccountRows(t *testing.T) {
	path, db := legacySQLiteFixture(t, 5, false)
	seedSQLiteAccounts(t, db)
	tables := Tables()
	before := make(map[string][][]any)
	for _, table := range tables {
		projection := "*"
		if table == "mail_events" {
			projection = "rowid,*"
		}
		before[table] = sqliteRows(t, db, `SELECT `+projection+` FROM `+sqliteQuote(table)+` ORDER BY 1`)
	}
	mustSQL(t, db, `PRAGMA foreign_keys=ON`)
	if err := ApplySQLite(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if got := sqliteRows(t, db, `SELECT * FROM `+sqliteQuote(table)+` ORDER BY 1`); !reflect.DeepEqual(got, before[table]) {
			t.Fatalf("%s rows changed: got=%v want=%v", table, got, before[table])
		}
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d,%v", fk, err)
	}
	if err := AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
	// Historical references to deleted users remain meaningful after the upgrade.
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	next := mustCreate(t, st, "next@example.invalid", "Next")
	if next.ID != 124 {
		t.Fatalf("next user id=%d; want123+1", next.ID)
	}
	// Existing rowid=0 becomes an explicit ID; next mail-event allocation uses14+1.
	mustSQL(t, db, `INSERT INTO mail_events(mail_id,event,at) VALUES(0,'opened',4)`)
	var eventID int64
	if err := db.QueryRow(`SELECT id FROM mail_events WHERE event='opened'`).Scan(&eventID); err != nil || eventID != 15 {
		t.Fatalf("mail event id=%d,%v", eventID, err)
	}
	// Original FK actions and uniqueness survive the rebuild.
	mustSQL(t, db, `DELETE FROM users WHERE id=0`)
	for _, table := range []string{"sessions", "tokens", "user_settings", "notification_prefs", "notification_watches", "notification_state"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + sqliteQuote(table)).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s cascade count=%d,%v", table, n, err)
		}
	}
	var mailUser, proposer sql.NullInt64
	if err := db.QueryRow(`SELECT user_id FROM mail_log WHERE id=0`).Scan(&mailUser); err != nil || mailUser.Valid {
		t.Fatal("mail SET NULL failed")
	}
	if err := db.QueryRow(`SELECT proposer_id FROM proposals WHERE id=0`).Scan(&proposer); err != nil || proposer.Valid {
		t.Fatal("proposal SET NULL failed")
	}
	if _, err := db.Exec(`INSERT INTO mail_events(mail_id,event,at) VALUES(0,'opened',4)`); err == nil {
		t.Fatal("mail event uniqueness lost")
	}
	if _, err := db.Exec(`INSERT INTO users(email,display_name,password_hash,role,created_at) VALUES('invalid','x','x','invalid',1)`); err == nil {
		t.Fatal("role constraint lost")
	}
	mustSQL(t, db, `DELETE FROM mail_log WHERE id=0`)
	var n int
	db.QueryRow(`SELECT count(*) FROM mail_events`).Scan(&n)
	if n != 0 {
		t.Fatal("mail-event cascade lost")
	}
}

func TestSQLiteIdentityMigrationEveryLegacyVersion(t *testing.T) {
	for version := 1; version <= 5; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			_, db := legacySQLiteFixture(t, version, false)
			mustSQL(t, db, `INSERT INTO users(id,email,display_name,password_hash,created_at) VALUES(0,'legacy','Legacy','hash',1)`)
			if err := ApplySQLite(db); err != nil {
				t.Fatal(err)
			}
			if err := ApplySQLite(db); err != nil {
				t.Fatal("idempotence:", err)
			}
			var id int64
			if err := db.QueryRow(`SELECT id FROM users WHERE email='legacy'`).Scan(&id); err != nil || id != 0 {
				t.Fatal("legacy zero ID lost")
			}
		})
	}
}

func TestSQLiteIdentityMigrationPreservesDeletedSequenceHighWater(t *testing.T) {
	path, db := legacySQLiteFixture(t, 5, true)
	seedSQLiteAccounts(t, db)
	for _, table := range sqliteIdentities {
		if table.name == "mail_events" {
			continue
		}
		mustSQL(t, db, `UPDATE sqlite_sequence SET seq=900 WHERE name='`+table.name+`'`)
	}
	if err := ApplySQLite(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range sqliteIdentities {
		if table.name == "mail_events" {
			continue
		}
		var seq int64
		if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name=?1`, table.name).Scan(&seq); err != nil || seq != 900 {
			t.Fatalf("%s seq=%d,%v", table.name, seq, err)
		}
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u := mustCreate(t, st, "allocate@example.invalid", "Allocate")
	if u.ID != 901 {
		t.Fatalf("next id=%d; want901", u.ID)
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "copy.db")
	if err := st.Snapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := mustCreate(t, restored, "later@example.invalid", "Later"); got.ID != 902 {
		t.Fatalf("snapshot reused deleted id: %d", got.ID)
	}
}

func TestSQLiteIdentityMigrationRefusesExtensionsWithoutMutation(t *testing.T) {
	for _, ddl := range []string{`CREATE TRIGGER extension AFTER INSERT ON users BEGIN UPDATE users SET display_name='changed' WHERE id=NEW.id; END`, `ALTER TABLE users ADD COLUMN generated TEXT GENERATED ALWAYS AS (email) VIRTUAL`, `CREATE UNIQUE INDEX custom_constraint ON users(display_name)`} {
		t.Run(strings.Fields(ddl)[1], func(t *testing.T) {
			_, db := legacySQLiteFixture(t, 5, false)
			mustSQL(t, db, ddl)
			before := sqliteRows(t, db, `SELECT type,name,sql FROM sqlite_schema ORDER BY type,name`)
			if err := ApplySQLite(db); err == nil {
				t.Fatal("unsupported extension accepted")
			}
			after := sqliteRows(t, db, `SELECT type,name,sql FROM sqlite_schema ORDER BY type,name`)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused migration changed source schema")
			}
			var version int
			db.QueryRow(`SELECT version FROM schema_version`).Scan(&version)
			if version != 5 {
				t.Fatal("refused migration changed version")
			}
		})
	}
}

func TestSQLiteIdentityMigrationFailureRollsBackAndRestoresPragma(t *testing.T) {
	_, db := legacySQLiteFixture(t, 5, false)
	mustSQL(t, db, `INSERT INTO tokens VALUES('orphan',999,'reset',NULL,1,NULL)`, `PRAGMA foreign_keys=ON`)
	before := sqliteRows(t, db, `SELECT type,name,sql FROM sqlite_schema ORDER BY type,name`)
	if err := ApplySQLite(db); err == nil || !strings.Contains(err.Error(), "foreign keys") {
		t.Fatalf("late migration error=%v", err)
	}
	if after := sqliteRows(t, db, `SELECT type,name,sql FROM sqlite_schema ORDER BY type,name`); !reflect.DeepEqual(before, after) {
		t.Fatal("failed table rebuild was not rolled back")
	}
	var fk, version int
	db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	db.QueryRow(`SELECT version FROM schema_version`).Scan(&version)
	if fk != 1 || version != 5 {
		t.Fatalf("rollback state FK=%d version=%d", fk, version)
	}
	if rows := sqliteRows(t, db, `SELECT token_hash,user_id FROM tokens`); len(rows) != 1 || rows[0][0] != "orphan" {
		t.Fatal("source row changed on rollback")
	}
}

func TestSQLiteReadyRejectsMissingAccountObjects(t *testing.T) {
	for _, statement := range []string{`DROP TABLE tokens`, `DROP INDEX proposals_kind_subject`, `DROP TABLE notification_state`} {
		t.Run(statement, func(t *testing.T) {
			st, err := Open(filepath.Join(t.TempDir(), "accounts.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			mustSQL(t, st.db, statement)
			if err := AssertSQLiteReady(st.db); err == nil {
				t.Fatal("incomplete current account schema accepted")
			}
		})
	}
}

func TestSQLiteImmediateTransactionsAcrossIndependentPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Begin itself must reserve the writer before either caller reads a quota.
	tx, err := first.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A native NOWAIT connection deterministically detects the reserved writer;
	// no goroutine sleeps or scheduling order are needed to prove IMMEDIATE.
	conn, err := second.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.BeginTx(context.Background(), nil); err == nil {
		t.Fatal("second write transaction began before first committed")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	next.Rollback()
	conn.Close()
	// Explicit read-only snapshot transactions are not promoted to writers.
	read, err := first.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	if _, err := second.CreatePending("writer@example.invalid", "Writer", "hash"); err != nil {
		t.Fatal(err)
	}
	if first.db.Stats().MaxOpenConnections != 1 || second.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("account pool grew")
	}

}

func TestSQLiteEmptyVersionTableFromInterruptedLegacyInitialization(t *testing.T) {
	_, db := legacySQLiteFixture(t, 0, false)
	mustSQL(t, db, `DELETE FROM schema_version`)
	if err := ApplySQLite(db); err != nil {
		t.Fatal("legacy initialization could stop between CREATE and INSERT:", err)
	}
	if err := AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteAccountBootstrapAcrossPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.db")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st, err := Open(path)
			if err != nil {
				t.Errorf("bootstrap: %v", err)
				return
			}
			defer st.Close()
			if _, err := st.CreatePending(fmt.Sprintf("worker%d@example.invalid", i), "Concurrent", "hash"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("accounts=%d,%v", n, err)
	}
	if err := AssertSQLiteReady(st.db); err != nil {
		t.Fatal(err)
	}
}
