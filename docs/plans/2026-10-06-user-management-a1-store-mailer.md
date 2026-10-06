# User Management A1 — `internal/users` + `internal/mailer` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two self-contained, fully tested Go modules: an account store over a
separate `users.db`, and a mailer with a Brevo client, webhook parser and test fake.

**Architecture:** Both modules follow the existing `internal/<pkg>` pattern: their own
`go.mod`, module path `github.com/meshcore-analyzer/<pkg>`, and later consumed by
`cmd/server` through a `replace` directive (that wiring is A2). Times are stored as
unix seconds. The store's clock can be injected for tests. Neither module knows about
HTTP or config.

**Tech Stack:** Go 1.22, `modernc.org/sqlite v1.34.5` (already used by the server),
`golang.org/x/crypto v0.31.0` (argon2id; this version still declares `go 1.20`).

Spec: `docs/specs/2026-10-06-user-management-design.md`. Index:
`docs/plans/2026-10-06-user-management-a.md` (ground rules apply).

## File map

| File | Responsibility |
|---|---|
| `internal/users/go.mod` | Module definition |
| `internal/users/store.go` | `Open`, forbidden-path guard, clock, time helpers, errors |
| `internal/users/schema.go` | Forward-only migrations, `SchemaVersion` |
| `internal/users/validate.go` | Email / display name / password rules |
| `internal/users/password.go` | argon2id hashing, dummy check |
| `internal/users/token.go` | Random tokens, token hashing, hashed email |
| `internal/users/users.go` | User type and CRUD, list, admin count, pruning |
| `internal/users/sessions.go` | Sessions |
| `internal/users/onetime.go` | Activation / reset / email-change tokens |
| `internal/users/audit.go` | Audit log |
| `internal/users/mail.go` | Mail log and delivery events |
| `internal/users/*_test.go` | Tests per file, plus `helpers_test.go` |
| `internal/mailer/go.mod` | Module definition |
| `internal/mailer/mailer.go` | `Message`, `Event`, canonical event names, `Mailer` interface |
| `internal/mailer/fake.go` | In-memory `Fake` |
| `internal/mailer/brevo.go` | Brevo send + events API client, event-name normalization |
| `internal/mailer/brevo_webhook.go` | Brevo webhook payload parser |
| `internal/mailer/*_test.go` | Tests |

---

### Task 1: Scaffold `internal/users` with `Open` and the v1 schema

**Files:**
- Create: `internal/users/go.mod`, `internal/users/store.go`, `internal/users/schema.go`
- Test: `internal/users/helpers_test.go`, `internal/users/store_test.go`

- [ ] **Step 1: Create the module**

`internal/users/go.mod`:
```
module github.com/meshcore-analyzer/users

go 1.22

require modernc.org/sqlite v1.34.5
```

- [ ] **Step 2: Write the test helper and failing tests**

`internal/users/helpers_test.go`:
```go
package users

import (
	"path/filepath"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestStore opens a fresh users.db in a temp dir with a controllable clock.
func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	clk := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	st.SetClock(clk.Now)
	t.Cleanup(func() { st.Close() })
	return st, clk
}
```

`internal/users/store_test.go`:
```go
package users

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "users.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v, err := st.SchemaVersion()
	if err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer st2.Close()
	if v2, _ := st2.SchemaVersion(); v2 != len(migrations) {
		t.Fatalf("version after reopen = %d", v2)
	}
}

func TestOpenRefusesForbiddenPath(t *testing.T) {
	dir := t.TempDir()
	measurement := filepath.Join(dir, "meshcore.db")
	if err := os.WriteFile(measurement, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(measurement, measurement)
	if err == nil || !strings.Contains(err.Error(), "measurement database") {
		t.Fatalf("Open(measurement) err = %v; want refusal", err)
	}
	// A relative spelling of the same file is refused too.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	if _, err := Open("meshcore.db", measurement); err == nil {
		t.Fatal("relative path to the measurement DB was not refused")
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE schema_version SET version = 999`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open with newer schema err = %v", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd internal/users && go mod tidy && go test ./...`
Expected: FAIL to compile (`undefined: Open`, `migrations`).

- [ ] **Step 4: Implement `store.go`**

```go
// Package users is CoreScope's optional account store (user management,
// docs/specs/2026-10-06-user-management-design.md). It owns users.db, a
// SQLite file separate from the measurement database. cmd/server never
// writes measurement data (#1283); this package is the single, opt-in
// exception, and Open refuses to touch the measurement database by path.
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound     = errors.New("users: not found")
	ErrEmailTaken   = errors.New("users: email already registered")
	ErrTokenInvalid = errors.New("users: token invalid or already used")
	ErrTokenExpired = errors.New("users: token expired")
)

// Store is the users.db handle. Safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the users database at path and applies
// pending migrations. forbidden lists paths Open must refuse; the server
// passes the measurement DB path so a misconfigured dbPath can never turn
// this package into a writer of measurement data.
func Open(path string, forbidden ...string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("users: resolve %s: %w", path, err)
	}
	for _, f := range forbidden {
		if strings.TrimSpace(f) != "" && samePath(abs, f) {
			return nil, fmt.Errorf("users: refusing to open %s: it is the measurement database", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("users: create dir for %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", abs+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("users: open %s: %w", path, err)
	}
	// Account traffic is tiny; one connection removes SQLITE_BUSY between
	// our own writers and keeps the per-connection pragmas in force.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func samePath(abs, other string) bool {
	oa, err := filepath.Abs(other)
	if err != nil {
		return false
	}
	a, errA := os.Stat(abs)
	b, errB := os.Stat(oa)
	if errA == nil && errB == nil {
		return os.SameFile(a, b)
	}
	return strings.EqualFold(filepath.Clean(abs), filepath.Clean(oa))
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the time source. Tests only.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

type rowScanner interface{ Scan(dest ...any) error }

func unix(t time.Time) int64     { return t.Unix() }
func fromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

func fromNullUnix(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromUnix(v.Int64)
	return &t
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func expectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
```

- [ ] **Step 5: Implement `schema.go`**

```go
package users

import (
	"database/sql"
	"errors"
	"fmt"
)

// migrations[i] upgrades the schema from version i to i+1. Forward-only:
// never edit a shipped entry, append a new one.
var migrations = [][]string{
	{ // v1 — sub-project A
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','disabled')),
			created_at INTEGER NOT NULL,
			activated_at INTEGER,
			activated_by INTEGER,
			last_login_at INTEGER,
			email_bouncing INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE sessions (
			id INTEGER PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			csrf_token TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL,
			user_agent TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX sessions_user ON sessions(user_id)`,
		`CREATE TABLE tokens (
			token_hash TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			purpose TEXT NOT NULL CHECK (purpose IN ('activate','reset','email_change')),
			new_email TEXT,
			expires_at INTEGER NOT NULL,
			used_at INTEGER
		)`,
		`CREATE INDEX tokens_user ON tokens(user_id, purpose)`,
		`CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY,
			at INTEGER NOT NULL,
			actor_user_id INTEGER,
			action TEXT NOT NULL,
			target_user_id INTEGER,
			detail TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX audit_target ON audit_log(target_user_id)`,
		`CREATE INDEX audit_actor ON audit_log(actor_user_id)`,
		`CREATE TABLE mail_log (
			id INTEGER PRIMARY KEY,
			user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			to_email TEXT NOT NULL,
			purpose TEXT NOT NULL,
			provider_message_id TEXT,
			sent_at INTEGER NOT NULL,
			last_event TEXT NOT NULL DEFAULT 'sent',
			last_event_at INTEGER NOT NULL,
			last_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX mail_user ON mail_log(user_id)`,
		`CREATE UNIQUE INDEX mail_msgid ON mail_log(provider_message_id) WHERE provider_message_id IS NOT NULL`,
		`CREATE TABLE mail_events (
			mail_id INTEGER NOT NULL REFERENCES mail_log(id) ON DELETE CASCADE,
			event TEXT NOT NULL,
			at INTEGER NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			UNIQUE (mail_id, event, at)
		)`,
	},
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("users: schema_version: %w", err)
	}
	v, err := s.SchemaVersion()
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("users: init schema_version: %w", err)
		}
		v = 0
	} else if err != nil {
		return fmt.Errorf("users: read schema_version: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("users: database schema version %d is newer than this binary supports (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range migrations[i] {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("users: migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("users: migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("users: migration %d: %w", i+1, err)
		}
	}
	return nil
}

// SchemaVersion returns the applied schema version (sql.ErrNoRows on a
// database that has never been migrated).
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v)
	return v, err
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd internal/users && go mod tidy && go test ./...`
Expected: PASS (3 tests). Check that `go.mod` still says `go 1.22`.

- [ ] **Step 7: Commit**

```bash
git add internal/users
git commit -m "feat(users): add the users.db store with its v1 schema"
```

---

### Task 2: Validation rules

**Files:**
- Create: `internal/users/validate.go`
- Test: `internal/users/validate_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package users

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		"  Alice@Example.ORG ": "alice@example.org",
		"a.b+tag@sub.example.be": "a.b+tag@sub.example.be",
	}
	for in, want := range good {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "alice", "alice@", "@example.org", "alice@localhost", "Alice <alice@example.org>",
		"alice@example.org.", strings.Repeat("a", 250) + "@example.org"}
	for _, in := range bad {
		if _, err := NormalizeEmail(in); err == nil {
			t.Errorf("NormalizeEmail(%q) accepted", in)
		} else {
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("NormalizeEmail(%q) error is %T, want *ValidationError", in, err)
			}
		}
	}
}

func TestValidateDisplayName(t *testing.T) {
	if got, err := ValidateDisplayName("  ON8AR Erwin  "); err != nil || got != "ON8AR Erwin" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ValidateDisplayName("👩‍💻 dev"); err != nil { // ZWJ emoji sequence is allowed
		t.Fatalf("ZWJ sequence rejected: %v", err)
	}
	bad := []string{"a", strings.Repeat("x", 33), "evil‮eman", "tab\tname", "zero​width", "line sep"}
	for _, in := range bad {
		if _, err := ValidateDisplayName(in); err == nil {
			t.Errorf("ValidateDisplayName(%q) accepted", in)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("correct horse"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	for _, in := range []string{"short", strings.Repeat("p", 129)} {
		if err := ValidatePassword(in); err == nil {
			t.Errorf("ValidatePassword(len %d) accepted", len(in))
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/users && go test -run 'Normalize|Validate' ./...`
Expected: FAIL (`undefined: NormalizeEmail`).

- [ ] **Step 3: Implement `validate.go`**

```go
package users

import (
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidationError is a user-facing input problem; Msg is safe to show.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// NormalizeEmail trims and lowercases an address and checks it is a bare
// addr-spec with a dotted domain. The result is the stored identity.
func NormalizeEmail(raw string) (string, error) {
	invalid := &ValidationError{Msg: "enter a valid email address"}
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > 254 {
		return "", invalid
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Name != "" || addr.Address != e {
		return "", invalid
	}
	at := strings.LastIndexByte(e, '@')
	domain := e[at+1:]
	if at < 1 || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", invalid
	}
	return e, nil
}

// ValidateDisplayName trims a display name and enforces 2–32 characters
// without control, format (bidi, zero-width) or line/paragraph separator
// characters. ZWJ (U+200D) is allowed so emoji sequences survive.
func ValidateDisplayName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if !utf8.ValidString(n) {
		return "", &ValidationError{Msg: "display name is not valid text"}
	}
	if c := utf8.RuneCountInString(n); c < 2 || c > 32 {
		return "", &ValidationError{Msg: "display name must be 2 to 32 characters"}
	}
	for _, r := range n {
		if r == '‍' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return "", &ValidationError{Msg: "display name contains invisible or control characters"}
		}
	}
	return n, nil
}

// ValidatePassword enforces length only (10–128 characters), per NIST 800-63B.
func ValidatePassword(p string) error {
	if c := utf8.RuneCountInString(p); c < 10 || c > 128 {
		return &ValidationError{Msg: "password must be 10 to 128 characters"}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd internal/users && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/users/validate.go internal/users/validate_test.go
git commit -m "feat(users): add email, display name and password rules"
```

---

### Task 3: Password hashing and tokens

**Files:**
- Create: `internal/users/password.go`, `internal/users/token.go`
- Modify: `internal/users/go.mod` (adds `golang.org/x/crypto`)
- Test: `internal/users/password_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package users

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", h)
	}
	ok, err := VerifyPassword(h, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("verify correct = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(h, "wrong horse battery")
	if err != nil || ok {
		t.Fatalf("verify wrong = %v, %v", ok, err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("two hashes of the same password are identical (salt not random)")
	}
}

func TestVerifyPasswordRejectsForeignFormats(t *testing.T) {
	for _, enc := range []string{"", "plain", "$2a$10$abcdefghijklmnopqrstuv", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"} {
		if _, err := VerifyPassword(enc, "x"); err == nil {
			t.Errorf("VerifyPassword(%q) returned no error", enc)
		}
	}
}

func TestNewTokenAndHash(t *testing.T) {
	raw, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 43 { // 32 bytes, base64url without padding
		t.Fatalf("raw token length %d", len(raw))
	}
	if HashToken(raw) != hash || len(hash) != 64 {
		t.Fatalf("hash mismatch: %s vs %s", HashToken(raw), hash)
	}
	raw2, _, _ := NewToken()
	if raw == raw2 {
		t.Fatal("tokens repeat")
	}
}

func TestHashedEmailIsStableAndOpaque(t *testing.T) {
	a, b := HashedEmail("alice@example.org"), HashedEmail("alice@example.org")
	if a != b || !strings.HasPrefix(a, "sha256:") || strings.Contains(a, "alice") {
		t.Fatalf("HashedEmail = %q / %q", a, b)
	}
}

func TestBurnPasswordCheckDoesNotPanic(t *testing.T) {
	BurnPasswordCheck("anything at all")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/users && go test -run 'Password|Token|HashedEmail|Burn' ./...`
Expected: FAIL (`undefined: HashPassword`).

- [ ] **Step 3: Add the dependency**

Run: `cd internal/users && go get golang.org/x/crypto@v0.31.0`
Then confirm that `go.mod` still has `go 1.22`. If `go get` rewrote it, set it back
by hand and re-run `go mod tidy`.

- [ ] **Step 4: Implement `password.go`**

```go
package users

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: OWASP's minimum recommendation (19 MiB, 2 passes,
// 1 lane). They are encoded in every hash, so raising them later only
// affects new hashes.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 19 * 1024
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("users: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC argon2id hash in constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("users: unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("users: unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("users: bad argon2 parameters: %w", err)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("users: bad salt: %w", err)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("users: bad key")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// BurnPasswordCheck runs a full hash comparison against a fixed dummy hash,
// so a login for an unknown address costs as much as one for a real account.
func BurnPasswordCheck(password string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("corescope-dummy-password") })
	_, _ = VerifyPassword(dummyHash, password)
}
```

- [ ] **Step 5: Implement `token.go`**

```go
package users

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewToken returns a 256-bit random token (base64url, sent to the user) and
// its SHA-256 hex hash (the only form stored).
func NewToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("users: token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken is the stored form of a raw token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// HashedEmail replaces an address in records that outlive the account.
func HashedEmail(email string) string {
	sum := sha256.Sum256([]byte(email))
	return "sha256:" + hex.EncodeToString(sum[:8])
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd internal/users && go mod tidy && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/users
git commit -m "feat(users): hash passwords with argon2id and add random tokens"
```

---

### Task 4: Users CRUD, list, admin count, pending pruning

**Files:**
- Create: `internal/users/users.go`
- Test: `internal/users/users_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package users

import (
	"errors"
	"testing"
)

func mustCreate(t *testing.T, st *Store, email, name string) *User {
	t.Helper()
	u, err := st.CreatePending(email, name, "$argon2id$placeholder")
	if err != nil {
		t.Fatalf("CreatePending(%s): %v", email, err)
	}
	return u
}

func TestCreatePendingAndDuplicate(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "alice@example.org", "Alice")
	if u.ID == 0 || u.Status != StatusPending || u.Role != RoleUser || !u.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("unexpected user: %+v", u)
	}
	if _, err := st.CreatePending("alice@example.org", "Other", "x"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate err = %v; want ErrEmailTaken", err)
	}
	got, err := st.GetByEmail("alice@example.org")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetByEmail = %+v, %v", got, err)
	}
	if _, err := st.GetByID(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(missing) err = %v", err)
	}
}

func TestActivateOnlyPending(t *testing.T) {
	st, _ := newTestStore(t)
	admin := mustCreate(t, st, "admin@example.org", "Admin")
	u := mustCreate(t, st, "bob@example.org", "Bob")
	by := admin.ID
	if err := st.Activate(u.ID, RoleAdmin, &by); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetByID(u.ID)
	if got.Status != StatusActive || got.Role != RoleAdmin || got.ActivatedAt == nil || got.ActivatedBy == nil || *got.ActivatedBy != admin.ID {
		t.Fatalf("after Activate: %+v", got)
	}
	if err := st.Activate(u.ID, RoleUser, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Activate err = %v; want ErrNotFound", err)
	}
}

func TestSettersAndEmailUniqueness(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "Aa")
	mustCreate(t, st, "b@example.org", "Bb")
	if err := st.SetEmail(a.ID, "b@example.org"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("SetEmail to taken = %v", err)
	}
	if err := st.SetEmail(a.ID, "c@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDisplayName(a.ID, "New name"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(a.ID, "newhash"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(a.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole(a.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailBouncing(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchLogin(a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetByID(a.ID)
	if got.Email != "c@example.org" || got.DisplayName != "New name" || got.PasswordHash != "newhash" ||
		got.Status != StatusDisabled || got.Role != RoleAdmin || !got.EmailBouncing ||
		got.LastLoginAt == nil || !got.LastLoginAt.Equal(clk.Now()) {
		t.Fatalf("after setters: %+v", got)
	}
	if err := st.SetStatus(9999, StatusActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus(missing) = %v", err)
	}
}

func TestListFiltersAndAdminCount(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "alice@example.org", "Alice")
	b := mustCreate(t, st, "bob@example.org", "Bob_1")
	mustCreate(t, st, "carol@example.org", "Carol")
	st.Activate(a.ID, RoleAdmin, nil)
	st.Activate(b.ID, RoleUser, nil)

	all, err := st.List(ListFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("List all = %d, %v", len(all), err)
	}
	pending, _ := st.List(ListFilter{Status: StatusPending})
	if len(pending) != 1 || pending[0].Email != "carol@example.org" {
		t.Fatalf("pending = %+v", pending)
	}
	admins, _ := st.List(ListFilter{Role: RoleAdmin})
	if len(admins) != 1 || admins[0].ID != a.ID {
		t.Fatalf("admins = %+v", admins)
	}
	q, _ := st.List(ListFilter{Query: "BOB"})
	if len(q) != 1 || q[0].ID != b.ID {
		t.Fatalf("query BOB = %+v", q)
	}
	// LIKE wildcards in the query are literal.
	if w, _ := st.List(ListFilter{Query: "_"}); len(w) != 1 || w[0].ID != b.ID {
		t.Fatalf("query _ = %+v", w)
	}
	n, err := st.CountActiveAdmins()
	if err != nil || n != 1 {
		t.Fatalf("CountActiveAdmins = %d, %v", n, err)
	}
	st.SetStatus(a.ID, StatusDisabled)
	if n, _ := st.CountActiveAdmins(); n != 0 {
		t.Fatalf("disabled admin still counted: %d", n)
	}
}

```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/users && go test ./...`
Expected: FAIL (`undefined: User`, `CreatePending`).

- [ ] **Step 3: Implement `users.go`**

```go
package users

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// User is one account. PasswordHash never leaves the server.
type User struct {
	ID            int64
	Email         string
	DisplayName   string
	PasswordHash  string
	Role          Role
	Status        Status
	CreatedAt     time.Time
	ActivatedAt   *time.Time
	ActivatedBy   *int64 // admin who activated manually; nil = activated by link
	LastLoginAt   *time.Time
	EmailBouncing bool
}

const userCols = `id, email, display_name, password_hash, role, status, created_at, activated_at, activated_by, last_login_at, email_bouncing`

func scanUser(row rowScanner) (*User, error) {
	var u User
	var role, status string
	var created int64
	var activatedAt, activatedBy, lastLogin sql.NullInt64
	var bouncing int
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &role, &status,
		&created, &activatedAt, &activatedBy, &lastLogin, &bouncing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Role, u.Status = Role(role), Status(status)
	u.CreatedAt = fromUnix(created)
	u.ActivatedAt = fromNullUnix(activatedAt)
	if activatedBy.Valid {
		v := activatedBy.Int64
		u.ActivatedBy = &v
	}
	u.LastLoginAt = fromNullUnix(lastLogin)
	u.EmailBouncing = bouncing != 0
	return &u, nil
}

// CreatePending inserts a new pending user. email must already be normalized.
func (s *Store) CreatePending(email, displayName, passwordHash string) (*User, error) {
	res, err := s.db.Exec(`INSERT INTO users (email, display_name, password_hash, role, status, created_at)
		VALUES (?, ?, ?, 'user', 'pending', ?)`, email, displayName, passwordHash, unix(s.now()))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetByID(id)
}

func (s *Store) GetByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetByEmail looks up a normalized address.
func (s *Store) GetByEmail(email string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// Activate moves a pending user to active with role. by is the admin who
// activated manually, nil for link activation. ErrNotFound if not pending.
func (s *Store) Activate(id int64, role Role, by *int64) error {
	return expectOne(s.db.Exec(`UPDATE users SET status = 'active', role = ?, activated_at = ?, activated_by = ?
		WHERE id = ? AND status = 'pending'`, string(role), unix(s.now()), nullInt(by), id))
}

func (s *Store) SetStatus(id int64, st Status) error {
	return expectOne(s.db.Exec(`UPDATE users SET status = ? WHERE id = ?`, string(st), id))
}

func (s *Store) SetRole(id int64, r Role) error {
	return expectOne(s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, string(r), id))
}

func (s *Store) SetPassword(id int64, hash string) error {
	return expectOne(s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id))
}

func (s *Store) SetDisplayName(id int64, name string) error {
	return expectOne(s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, id))
}

// SetEmail changes the address (normalized) and clears the bounce flag.
func (s *Store) SetEmail(id int64, email string) error {
	err := expectOne(s.db.Exec(`UPDATE users SET email = ?, email_bouncing = 0 WHERE id = ?`, email, id))
	if isUniqueViolation(err) {
		return ErrEmailTaken
	}
	return err
}

func (s *Store) SetEmailBouncing(id int64, v bool) error {
	b := 0
	if v {
		b = 1
	}
	return expectOne(s.db.Exec(`UPDATE users SET email_bouncing = ? WHERE id = ?`, b, id))
}

func (s *Store) TouchLogin(id int64) error {
	return expectOne(s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, unix(s.now()), id))
}

// Delete removes a user; sessions and tokens cascade. Mail-log rows survive
// for delivery forensics, with the address replaced by HashedEmail.
func (s *Store) Delete(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var email string
	if err := tx.QueryRow(`SELECT email FROM users WHERE id = ?`, id).Scan(&email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(`UPDATE mail_log SET to_email = ? WHERE user_id = ?`, HashedEmail(email), id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ListFilter narrows List. Zero values mean "any".
type ListFilter struct {
	Status Status
	Role   Role
	Query  string // substring of email or display name, case-insensitive
}

// List returns at most 1000 users, newest first.
func (s *Store) List(f ListFilter) ([]User, error) {
	q := `SELECT ` + userCols + ` FROM users WHERE 1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if f.Role != "" {
		q += ` AND role = ?`
		args = append(args, string(f.Role))
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		like := "%" + escapeLike(strings.ToLower(t)) + "%"
		q += ` AND (email LIKE ? ESCAPE '\' OR lower(display_name) LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 1000`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// CountActiveAdmins counts admins whose status is active.
func (s *Store) CountActiveAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'`).Scan(&n)
	return n, err
}

// PruneStalePending deletes pending accounts older than maxAge that have no
// unused, unexpired activation token left.
func (s *Store) PruneStalePending(maxAge time.Duration) (int64, error) {
	now := unix(s.now())
	res, err := s.db.Exec(`DELETE FROM users WHERE status = 'pending' AND created_at < ?
		AND NOT EXISTS (SELECT 1 FROM tokens t WHERE t.user_id = users.id AND t.purpose = 'activate'
			AND t.used_at IS NULL AND t.expires_at > ?)`, now-int64(maxAge/time.Second), now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd internal/users && go test ./...`
Expected: PASS for the enabled tests.

- [ ] **Step 5: Commit**

```bash
git add internal/users/users.go internal/users/users_test.go
git commit -m "feat(users): add user CRUD, listing and pending-account pruning"
```

---

### Task 5: Sessions

**Files:**
- Create: `internal/users/sessions.go`
- Test: `internal/users/sessions_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package users

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "s@example.org", "Sess")
	raw, sess, err := st.CreateSession(u.ID, time.Hour, strings.Repeat("A", 300))
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || sess.CSRFToken == "" || len(sess.UserAgent) != 200 {
		t.Fatalf("CreateSession: raw=%q sess=%+v", raw, sess)
	}
	got, err := st.LookupSession(raw)
	if err != nil || got.ID != sess.ID || got.UserID != u.ID || got.CSRFToken != sess.CSRFToken {
		t.Fatalf("LookupSession = %+v, %v", got, err)
	}
	if _, err := st.LookupSession("not-a-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token err = %v", err)
	}

	clk.Advance(30 * time.Minute)
	if err := st.ExtendSession(sess.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	clk.Advance(45 * time.Minute) // 75 min after creation, 45 after extend
	if _, err := st.LookupSession(raw); err != nil {
		t.Fatalf("extended session expired early: %v", err)
	}
	clk.Advance(16 * time.Minute)
	if _, err := st.LookupSession(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session err = %v", err)
	}
	if list, _ := st.ListSessions(u.ID); len(list) != 0 {
		t.Fatalf("expired session not deleted on lookup: %+v", list)
	}
}

func TestDeleteSessions(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "d@example.org", "Del")
	other := mustCreate(t, st, "o@example.org", "Oth")
	r1, s1, _ := st.CreateSession(u.ID, time.Hour, "a")
	_, s2, _ := st.CreateSession(u.ID, time.Hour, "b")
	_, s3, _ := st.CreateSession(u.ID, time.Hour, "c")
	_, so, _ := st.CreateSession(other.ID, time.Hour, "x")

	if err := st.DeleteSession(other.ID, s2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting someone else's session = %v", err)
	}
	if err := st.DeleteSession(u.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUserSessions(u.ID, s1.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListSessions(u.ID)
	if len(list) != 1 || list[0].ID != s1.ID {
		t.Fatalf("after DeleteUserSessions except s1: %+v (s3=%d)", list, s3.ID)
	}
	if err := st.DeleteSessionByToken(r1); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListSessions(u.ID); len(list) != 0 {
		t.Fatalf("session survived DeleteSessionByToken: %+v", list)
	}
	if list, _ := st.ListSessions(other.ID); len(list) != 1 || list[0].ID != so.ID {
		t.Fatalf("other user's sessions touched: %+v", list)
	}
	// Deleting a user cascades to their sessions.
	st.Delete(other.ID)
	if list, _ := st.ListSessions(other.ID); len(list) != 0 {
		t.Fatal("sessions survived user delete")
	}
}

func TestPruneExpiredSessions(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "p@example.org", "Pru")
	st.CreateSession(u.ID, time.Hour, "")
	st.CreateSession(u.ID, 3*time.Hour, "")
	clk.Advance(2 * time.Hour)
	n, err := st.PruneExpiredSessions()
	if err != nil || n != 1 {
		t.Fatalf("PruneExpiredSessions = %d, %v", n, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/users && go test -run Session ./...`
Expected: FAIL (`undefined: CreateSession`).

- [ ] **Step 3: Implement `sessions.go`**

```go
package users

import (
	"strings"
	"time"
)

// Session is one logged-in device. The raw token exists only in the cookie.
type Session struct {
	ID         int64
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
}

const maxUserAgent = 200

const sessionCols = `id, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent`

func scanSession(row rowScanner) (*Session, error) {
	var s Session
	var created, expires, seen int64
	if err := row.Scan(&s.ID, &s.UserID, &s.CSRFToken, &created, &expires, &seen, &s.UserAgent); err != nil {
		return nil, err
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = fromUnix(created), fromUnix(expires), fromUnix(seen)
	return &s, nil
}

// CreateSession starts a session and returns the raw cookie token.
func (s *Store) CreateSession(userID int64, ttl time.Duration, userAgent string) (string, *Session, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	csrf, _, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	if len(userAgent) > maxUserAgent {
		userAgent = strings.ToValidUTF8(userAgent[:maxUserAgent], "")
	}
	now := s.now()
	res, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, hash, userID, csrf, unix(now), unix(now.Add(ttl)), unix(now), userAgent)
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	return raw, &Session{ID: id, UserID: userID, CSRFToken: csrf, CreatedAt: fromUnix(unix(now)),
		ExpiresAt: fromUnix(unix(now.Add(ttl))), LastSeenAt: fromUnix(unix(now)), UserAgent: userAgent}, nil
}

// LookupSession resolves a raw cookie token. Unknown and expired sessions
// return ErrNotFound; expired ones are deleted on the way.
func (s *Store) LookupSession(raw string) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE token_hash = ?`, HashToken(raw)))
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.now().Before(sess.ExpiresAt) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, ErrNotFound
	}
	return sess, nil
}

// ExtendSession marks the session seen now and moves its expiry to now+ttl.
func (s *Store) ExtendSession(id int64, ttl time.Duration) error {
	now := s.now()
	return expectOne(s.db.Exec(`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		unix(now), unix(now.Add(ttl)), id))
}

func (s *Store) DeleteSessionByToken(raw string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, HashToken(raw))
	return err
}

// DeleteSession revokes one session, only if it belongs to userID.
func (s *Store) DeleteSession(userID, sessionID int64) error {
	return expectOne(s.db.Exec(`DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID))
}

// DeleteUserSessions revokes all of a user's sessions except exceptID (0 = none kept).
func (s *Store) DeleteUserSessions(userID, exceptID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, exceptID)
	return err
}

// ListSessions returns a user's sessions, most recently seen first.
func (s *Store) ListSessions(userID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

func (s *Store) PruneExpiredSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, unix(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd internal/users && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/users/sessions.go internal/users/sessions_test.go
git commit -m "feat(users): add sessions with sliding expiry"
```

---

### Task 6: One-time tokens (activate, reset, email change)

**Files:**
- Create: `internal/users/onetime.go`
- Test: `internal/users/onetime_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package users

import (
	"errors"
	"testing"
	"time"
)

func TestTokenConsumeOnce(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "t@example.org", "Tok")
	raw, err := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	uid, newEmail, err := st.ConsumeToken(raw, PurposeActivate)
	if err != nil || uid != u.ID || newEmail != "" {
		t.Fatalf("ConsumeToken = %d, %q, %v", uid, newEmail, err)
	}
	if _, _, err := st.ConsumeToken(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second consume err = %v", err)
	}
}

func TestTokenPurposeMismatchDoesNotConsume(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "m@example.org", "Mis")
	raw, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	if _, _, err := st.ConsumeToken(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("mismatch err = %v", err)
	}
	if _, _, err := st.ConsumeToken(raw, PurposeReset); err != nil {
		t.Fatalf("token was consumed by the mismatched attempt: %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "e@example.org", "Exp")
	raw, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	clk.Advance(time.Hour)
	if _, _, err := st.ConsumeToken(raw, PurposeReset); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired err = %v", err)
	}
}

func TestNewTokenInvalidatesOlderSamePurpose(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "n@example.org", "New")
	old, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	keep, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	fresh, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if _, _, err := st.ConsumeToken(old, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("old activation link still works: %v", err)
	}
	if _, _, err := st.ConsumeToken(fresh, PurposeActivate); err != nil {
		t.Fatalf("fresh link: %v", err)
	}
	if _, _, err := st.ConsumeToken(keep, PurposeReset); err != nil {
		t.Fatalf("other purpose was invalidated: %v", err)
	}
}

func TestEmailChangeTokenCarriesNewEmail(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "c@example.org", "Chg")
	raw, _ := st.IssueToken(u.ID, PurposeEmailChange, time.Hour, "new@example.org")
	_, ne, err := st.ConsumeToken(raw, PurposeEmailChange)
	if err != nil || ne != "new@example.org" {
		t.Fatalf("ConsumeToken = %q, %v", ne, err)
	}
}

func TestInvalidateAndPruneTokens(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "i@example.org", "Inv")
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err := st.InvalidateTokens(u.ID, PurposeActivate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ConsumeToken(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("invalidated token still works: %v", err)
	}
	clk.Advance(8 * 24 * time.Hour)
	if n, err := st.PruneTokens(7 * 24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("PruneTokens = %d, %v", n, err)
	}
}

func TestPruneStalePending(t *testing.T) {
	st, clk := newTestStore(t)
	stale := mustCreate(t, st, "stale@example.org", "Stale")
	st.IssueToken(stale.ID, PurposeActivate, 48*time.Hour, "")
	fresh := mustCreate(t, st, "fresh@example.org", "Fresh")
	_ = fresh
	clk.Advance(49 * time.Hour)
	resent := mustCreate(t, st, "resent@example.org", "Resent")
	_ = resent
	n, err := st.PruneStalePending(48 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// stale: old + token expired → pruned. fresh: old, no token → pruned.
	// resent: created after the advance → kept.
	if n != 2 {
		t.Fatalf("pruned %d; want 2", n)
	}
	if _, err := st.GetByEmail("resent@example.org"); err != nil {
		t.Fatalf("recent pending pruned: %v", err)
	}
	// An old pending account with a live (re-sent) token survives.
	old := mustCreate(t, st, "old@example.org", "Old")
	clk.Advance(49 * time.Hour)
	st.IssueToken(old.ID, PurposeActivate, 48*time.Hour, "")
	if n, _ := st.PruneStalePending(48 * time.Hour); n != 1 { // only "resent" goes now
		t.Fatalf("second prune = %d; want 1", n)
	}
	if _, err := st.GetByEmail("old@example.org"); err != nil {
		t.Fatal("pending user with a live token was pruned")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/users && go test -run Token ./...`
Expected: FAIL (`undefined: IssueToken`).

- [ ] **Step 3: Implement `onetime.go`**

```go
package users

import (
	"database/sql"
	"errors"
	"time"
)

// Purpose is what a one-time link may be used for.
type Purpose string

const (
	PurposeActivate    Purpose = "activate"
	PurposeReset       Purpose = "reset"
	PurposeEmailChange Purpose = "email_change"
)

// IssueToken creates a one-time token and invalidates the user's earlier
// unused tokens for the same purpose, so only the newest link works.
// newEmail is stored for PurposeEmailChange and ignored when empty.
func (s *Store) IssueToken(userID int64, p Purpose, ttl time.Duration, newEmail string) (string, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tokens SET used_at = ? WHERE user_id = ? AND purpose = ? AND used_at IS NULL`,
		unix(now), userID, string(p)); err != nil {
		return "", err
	}
	var ne any
	if newEmail != "" {
		ne = newEmail
	}
	if _, err := tx.Exec(`INSERT INTO tokens (token_hash, user_id, purpose, new_email, expires_at) VALUES (?, ?, ?, ?, ?)`,
		hash, userID, string(p), ne, unix(now.Add(ttl))); err != nil {
		return "", err
	}
	return raw, tx.Commit()
}

// ConsumeToken validates and burns a token. A purpose mismatch returns
// ErrTokenInvalid without burning it. Expired tokens return ErrTokenExpired.
func (s *Store) ConsumeToken(raw string, p Purpose) (userID int64, newEmail string, err error) {
	hash := HashToken(raw)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()
	var purpose string
	var expires int64
	var ne sql.NullString
	var used sql.NullInt64
	err = tx.QueryRow(`SELECT user_id, purpose, new_email, expires_at, used_at FROM tokens WHERE token_hash = ?`, hash).
		Scan(&userID, &purpose, &ne, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrTokenInvalid
	}
	if err != nil {
		return 0, "", err
	}
	if purpose != string(p) || used.Valid {
		return 0, "", ErrTokenInvalid
	}
	now := unix(s.now())
	if now >= expires {
		return 0, "", ErrTokenExpired
	}
	if _, err := tx.Exec(`UPDATE tokens SET used_at = ? WHERE token_hash = ?`, now, hash); err != nil {
		return 0, "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return userID, ne.String, nil
}

// InvalidateTokens burns all of a user's unused tokens for purpose p.
func (s *Store) InvalidateTokens(userID int64, p Purpose) error {
	_, err := s.db.Exec(`UPDATE tokens SET used_at = ? WHERE user_id = ? AND purpose = ? AND used_at IS NULL`,
		unix(s.now()), userID, string(p))
	return err
}

// PruneTokens deletes tokens that expired more than keep ago.
func (s *Store) PruneTokens(keep time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE expires_at < ?`, unix(s.now())-int64(keep/time.Second))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd internal/users && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/users/onetime.go internal/users/onetime_test.go
git commit -m "feat(users): add single-use activation, reset and email-change tokens"
```

---

### Task 7: Audit log and mail log

**Files:**
- Create: `internal/users/audit.go`, `internal/users/mail.go`
- Test: `internal/users/audit_test.go`, `internal/users/mail_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/users/audit_test.go`:
```go
package users

import "testing"

func TestAuditWriteAndRead(t *testing.T) {
	st, clk := newTestStore(t)
	admin := mustCreate(t, st, "a@example.org", "Adm")
	u := mustCreate(t, st, "u@example.org", "Usr")
	aid, uid := admin.ID, u.ID
	if err := st.Audit(&aid, "user.disable", &uid, map[string]string{"reason": "spam"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(nil, "user.register", &uid, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := st.AuditFor(u.ID, 50)
	if err != nil || len(entries) != 2 {
		t.Fatalf("AuditFor = %d, %v", len(entries), err)
	}
	var disable *AuditEntry
	for i := range entries {
		if entries[i].Action == "user.disable" {
			disable = &entries[i]
		}
	}
	if disable == nil || disable.ActorUserID == nil || *disable.ActorUserID != aid ||
		disable.Detail["reason"] != "spam" || !disable.At.Equal(clk.Now()) {
		t.Fatalf("disable entry = %+v", disable)
	}
	// Entries survive the target's deletion.
	st.Delete(u.ID)
	if entries, _ := st.AuditFor(uid, 50); len(entries) != 2 {
		t.Fatalf("audit rows lost on delete: %d", len(entries))
	}
}
```

`internal/users/mail_test.go`:
```go
package users

import (
	"errors"
	"testing"
	"time"
)

func TestMailEventsSummaryFollowsNewest(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "m@example.org", "Mai")
	uid := u.ID
	id, err := st.LogMail(&uid, "m@example.org", "activate", "<msg-1@brevo>")
	if err != nil {
		t.Fatal(err)
	}
	t0 := clk.Now()
	if _, found, err := st.RecordMailEvent("<msg-1@brevo>", "delivered", t0.Add(time.Minute), ""); err != nil || !found {
		t.Fatalf("delivered: %v %v", found, err)
	}
	gotUID, found, err := st.RecordMailEvent("<msg-1@brevo>", "hard_bounce", t0.Add(3*time.Minute), "mailbox full")
	if err != nil || !found || gotUID == nil || *gotUID != uid {
		t.Fatalf("hard_bounce: %v %v %v", gotUID, found, err)
	}
	// Older event arriving late does not overwrite the summary.
	st.RecordMailEvent("<msg-1@brevo>", "opened", t0.Add(2*time.Minute), "")
	// Duplicate is ignored.
	st.RecordMailEvent("<msg-1@brevo>", "delivered", t0.Add(time.Minute), "")

	rec, err := st.MailByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.LastEvent != "hard_bounce" || rec.LastReason != "mailbox full" || len(rec.Events) != 3 {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Events[0].Event != "delivered" { // chronological
		t.Fatalf("events not chronological: %+v", rec.Events)
	}
	if _, found, _ := st.RecordMailEvent("<unknown@brevo>", "delivered", t0, ""); found {
		t.Fatal("unknown message id reported as found")
	}
}

func TestMailForUserAndLatest(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "Aaa")
	b := mustCreate(t, st, "b@example.org", "Bbb")
	aid, bid := a.ID, b.ID
	st.LogMail(&aid, "a@example.org", "activate", "<a1>")
	clk.Advance(time.Minute)
	st.LogMail(&aid, "a@example.org", "reset", "<a2>")
	st.LogMail(&bid, "b@example.org", "activate", "")

	list, err := st.MailForUser(a.ID, 10)
	if err != nil || len(list) != 2 || list[0].Purpose != "reset" {
		t.Fatalf("MailForUser = %+v, %v", list, err)
	}
	latest, err := st.LatestMailByUser()
	if err != nil || latest[a.ID].Purpose != "reset" || latest[b.ID].Purpose != "activate" {
		t.Fatalf("LatestMailByUser = %+v, %v", latest, err)
	}
}

func TestPruneMail(t *testing.T) {
	st, clk := newTestStore(t)
	st.LogMail(nil, "x@example.org", "activate", "<old>")
	clk.Advance(91 * 24 * time.Hour)
	st.LogMail(nil, "y@example.org", "activate", "<new>")
	n, err := st.PruneMail(90 * 24 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("PruneMail = %d, %v", n, err)
	}
}

func TestDeleteHashesMailAddresses(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "gone@example.org", "Gone")
	uid := u.ID
	mailID, err := st.LogMail(&uid, "gone@example.org", "activate", "<m1@x>")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetByID(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user still present: %v", err)
	}
	rec, err := st.MailByID(mailID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UserID != nil || rec.ToEmail != HashedEmail("gone@example.org") {
		t.Fatalf("mail record after delete: %+v", rec)
	}
	if err := st.Delete(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/users && go test -run 'Audit|Mail' ./...`
Expected: FAIL (`undefined: Audit`, `LogMail`).

- [ ] **Step 3: Implement `audit.go`**

```go
package users

import (
	"database/sql"
	"encoding/json"
	"time"
)

// AuditEntry is one recorded action. Rows outlive the users they mention.
type AuditEntry struct {
	ID           int64
	At           time.Time
	ActorUserID  *int64 // nil = system or API key
	Action       string
	TargetUserID *int64
	Detail       map[string]string
}

// Audit records an action. detail may be nil.
func (s *Store) Audit(actor *int64, action string, target *int64, detail map[string]string) error {
	if detail == nil {
		detail = map[string]string{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO audit_log (at, actor_user_id, action, target_user_id, detail) VALUES (?, ?, ?, ?, ?)`,
		unix(s.now()), nullInt(actor), action, nullInt(target), string(b))
	return err
}

// AuditFor returns entries where userID is the target or the actor, newest first.
func (s *Store) AuditFor(userID int64, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, at, actor_user_id, action, target_user_id, detail FROM audit_log
		WHERE target_user_id = ? OR actor_user_id = ? ORDER BY at DESC, id DESC LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var actor, target sql.NullInt64
		var detail string
		if err := rows.Scan(&e.ID, &at, &actor, &e.Action, &target, &detail); err != nil {
			return nil, err
		}
		e.At = fromUnix(at)
		if actor.Valid {
			v := actor.Int64
			e.ActorUserID = &v
		}
		if target.Valid {
			v := target.Int64
			e.TargetUserID = &v
		}
		e.Detail = map[string]string{}
		_ = json.Unmarshal([]byte(detail), &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Implement `mail.go`**

```go
package users

import (
	"database/sql"
	"errors"
	"time"
)

// MailEvent is one provider delivery event.
type MailEvent struct {
	Event  string
	At     time.Time
	Reason string
}

// MailRecord is one sent mail with its delivery summary and history.
type MailRecord struct {
	ID                int64
	UserID            *int64
	ToEmail           string
	Purpose           string
	ProviderMessageID string
	SentAt            time.Time
	LastEvent         string
	LastEventAt       time.Time
	LastReason        string
	Events            []MailEvent // chronological; filled by MailByID / MailForUser
}

const mailCols = `id, user_id, to_email, purpose, provider_message_id, sent_at, last_event, last_event_at, last_reason`

func scanMail(row rowScanner) (*MailRecord, error) {
	var m MailRecord
	var uid sql.NullInt64
	var msgID sql.NullString
	var sent, lastAt int64
	if err := row.Scan(&m.ID, &uid, &m.ToEmail, &m.Purpose, &msgID, &sent, &m.LastEvent, &lastAt, &m.LastReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if uid.Valid {
		v := uid.Int64
		m.UserID = &v
	}
	m.ProviderMessageID = msgID.String
	m.SentAt, m.LastEventAt = fromUnix(sent), fromUnix(lastAt)
	return &m, nil
}

// LogMail records a sent mail. messageID may be empty if the provider gave none.
func (s *Store) LogMail(userID *int64, to, purpose, messageID string) (int64, error) {
	now := unix(s.now())
	var mid any
	if messageID != "" {
		mid = messageID
	}
	res, err := s.db.Exec(`INSERT INTO mail_log (user_id, to_email, purpose, provider_message_id, sent_at, last_event, last_event_at)
		VALUES (?, ?, ?, ?, ?, 'sent', ?)`, nullInt(userID), to, purpose, mid, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecordMailEvent appends a provider event to the mail with that provider
// message id. Duplicates (same event and time) are ignored; the summary
// columns follow the newest event. found is false for unknown ids.
func (s *Store) RecordMailEvent(messageID, event string, at time.Time, reason string) (userID *int64, found bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id, lastAt int64
	var uid sql.NullInt64
	err = tx.QueryRow(`SELECT id, user_id, last_event_at FROM mail_log WHERE provider_message_id = ?`, messageID).
		Scan(&id, &uid, &lastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO mail_events (mail_id, event, at, reason) VALUES (?, ?, ?, ?)`,
		id, event, unix(at), reason); err != nil {
		return nil, false, err
	}
	if unix(at) >= lastAt {
		if _, err := tx.Exec(`UPDATE mail_log SET last_event = ?, last_event_at = ?, last_reason = ? WHERE id = ?`,
			event, unix(at), reason, id); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if uid.Valid {
		v := uid.Int64
		userID = &v
	}
	return userID, true, nil
}

func (s *Store) eventsFor(mailID int64) ([]MailEvent, error) {
	rows, err := s.db.Query(`SELECT event, at, reason FROM mail_events WHERE mail_id = ? ORDER BY at, rowid`, mailID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailEvent
	for rows.Next() {
		var e MailEvent
		var at int64
		if err := rows.Scan(&e.Event, &at, &e.Reason); err != nil {
			return nil, err
		}
		e.At = fromUnix(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MailByID returns one record with its events.
func (s *Store) MailByID(id int64) (*MailRecord, error) {
	m, err := scanMail(s.db.QueryRow(`SELECT `+mailCols+` FROM mail_log WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if m.Events, err = s.eventsFor(m.ID); err != nil {
		return nil, err
	}
	return m, nil
}

// MailForUser returns a user's mails, newest first, each with its events.
func (s *Store) MailForUser(userID int64, limit int) ([]MailRecord, error) {
	rows, err := s.db.Query(`SELECT `+mailCols+` FROM mail_log WHERE user_id = ? ORDER BY sent_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	var out []MailRecord
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Events, err = s.eventsFor(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LatestMailByUser maps user id → that user's most recent mail (no events).
func (s *Store) LatestMailByUser() (map[int64]MailRecord, error) {
	rows, err := s.db.Query(`SELECT ` + mailCols + ` FROM mail_log m WHERE user_id IS NOT NULL
		AND id = (SELECT MAX(id) FROM mail_log WHERE user_id = m.user_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]MailRecord{}
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			return nil, err
		}
		out[*m.UserID] = *m
	}
	return out, rows.Err()
}

// PruneMail deletes mail records (and their events) older than maxAge.
func (s *Store) PruneMail(maxAge time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM mail_log WHERE sent_at < ?`, unix(s.now())-int64(maxAge/time.Second))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

Note on `MailForUser`: rows are closed before the event queries run, because the store
uses a single connection (`SetMaxOpenConns(1)`). An open cursor would deadlock the
nested query.

- [ ] **Step 5: Run the whole module's tests**

Run: `cd internal/users && go vet ./... && go test -race ./...`
Expected: PASS, no vet findings.

- [ ] **Step 6: Commit**

```bash
git add internal/users
git commit -m "feat(users): add the audit log and the mail delivery log"
```

---

### Task 8: `internal/mailer` interface and `Fake`

**Files:**
- Create: `internal/mailer/go.mod`, `internal/mailer/mailer.go`, `internal/mailer/fake.go`
- Test: `internal/mailer/fake_test.go`

- [ ] **Step 1: Create the module**

`internal/mailer/go.mod`:
```
module github.com/meshcore-analyzer/mailer

go 1.22
```

- [ ] **Step 2: Write the failing test**

```go
package mailer

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRecordsAndFails(t *testing.T) {
	f := &Fake{}
	id, err := f.Send(context.Background(), Message{To: "a@example.org", Subject: "Hi", Text: "link"})
	if err != nil || id == "" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	m, gotID, ok := f.Last()
	if !ok || m.To != "a@example.org" || gotID != id {
		t.Fatalf("Last = %+v %q %v", m, gotID, ok)
	}
	f.SetEvents(id, []Event{{MessageID: id, Event: EventDelivered}})
	evs, _ := f.Events(context.Background(), id)
	if len(evs) != 1 || evs[0].Event != EventDelivered {
		t.Fatalf("Events = %+v", evs)
	}
	boom := errors.New("down")
	f.SetSendErr(boom)
	if _, err := f.Send(context.Background(), Message{To: "b@example.org"}); !errors.Is(err, boom) {
		t.Fatalf("Send with error = %v", err)
	}
	if len(f.Sent()) != 1 {
		t.Fatalf("failed send was recorded: %d", len(f.Sent()))
	}
	if !IsUndeliverable(EventHardBounce) || !IsUndeliverable(EventInvalidEmail) || IsUndeliverable(EventSoftBounce) {
		t.Fatal("IsUndeliverable classification wrong")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd internal/mailer && go test ./...`
Expected: FAIL (`undefined: Fake`).

- [ ] **Step 4: Implement `mailer.go`**

```go
// Package mailer sends CoreScope's account mails and reads back delivery
// events. Brevo is the default provider; Fake is the in-memory test double.
package mailer

import (
	"context"
	"time"
)

// Message is one outgoing mail. HTML and Text carry the same content.
type Message struct {
	To      string
	ToName  string
	Subject string
	HTML    string
	Text    string
	Tag     string // provider tag, e.g. "activate"
}

// Canonical delivery event names stored by CoreScope, independent of provider.
const (
	EventSent         = "sent"
	EventDelivered    = "delivered"
	EventOpened       = "opened"
	EventClicked      = "clicked"
	EventSoftBounce   = "soft_bounce"
	EventHardBounce   = "hard_bounce"
	EventInvalidEmail = "invalid_email"
	EventDeferred     = "deferred"
	EventSpam         = "spam"
	EventBlocked      = "blocked"
	EventError        = "error"
	EventUnsubscribed = "unsubscribed"
)

// Event is one delivery event for a sent message.
type Event struct {
	MessageID string
	Email     string
	Event     string // canonical name
	Reason    string
	At        time.Time
}

// Mailer sends mail and can report delivery events for a message id.
type Mailer interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
	Events(ctx context.Context, messageID string) ([]Event, error)
}

// IsUndeliverable reports events after which an address should be flagged.
func IsUndeliverable(event string) bool {
	return event == EventHardBounce || event == EventInvalidEmail
}
```

- [ ] **Step 5: Implement `fake.go`**

```go
package mailer

import (
	"context"
	"fmt"
	"sync"
)

// Fake records sent messages in memory. Safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	sent    []Message
	ids     []string
	sendErr error
	events  map[string][]Event
}

func (f *Fake) Send(_ context.Context, m Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return "", f.sendErr
	}
	id := fmt.Sprintf("<fake-%d@corescope.test>", len(f.sent)+1)
	f.sent = append(f.sent, m)
	f.ids = append(f.ids, id)
	return id, nil
}

func (f *Fake) Events(_ context.Context, messageID string) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Event(nil), f.events[messageID]...), nil
}

// SetSendErr makes subsequent Sends fail with err (nil restores success).
func (f *Fake) SetSendErr(err error) {
	f.mu.Lock()
	f.sendErr = err
	f.mu.Unlock()
}

// SetEvents sets what Events returns for messageID.
func (f *Fake) SetEvents(messageID string, evs []Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string][]Event{}
	}
	f.events[messageID] = evs
}

// Sent returns a copy of all successfully sent messages.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// Last returns the most recent message and its id.
func (f *Fake) Last() (Message, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return Message{}, "", false
	}
	return f.sent[len(f.sent)-1], f.ids[len(f.ids)-1], true
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `cd internal/mailer && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/mailer
git commit -m "feat(mailer): add the Mailer interface and an in-memory fake"
```

---

### Task 9: Brevo client (send, events API, event normalization)

**Files:**
- Create: `internal/mailer/brevo.go`
- Test: `internal/mailer/brevo_test.go`

Brevo API facts used here (verified 2026-10-06 against developers.brevo.com):
- **Send:** `POST https://api.brevo.com/v3/smtp/email`, header `api-key`.
  - Body: `sender{email,name}`, `to[{email,name}]`, `subject`, `htmlContent`,
    `textContent`, `tags[]`.
  - `201` (or `202` when scheduled) returns `{"messageId":"<…>"}`. Errors are 4xx
    `{"code":"…","message":"…"}`.
- **Events:** `GET https://api.brevo.com/v3/smtp/statistics/events?messageId=…&limit=…&sort=asc`
  returns `{"events":[{"date","email","event","messageId","reason",…}]}`.
- Event names differ between the webhook (`hard_bounce`, `click`, `request`,
  `unique_opened`, `proxy_open` …) and the events API (`hardBounces`, `clicks`,
  `requests`, `loadedByProxy` …). `NormalizeBrevoEvent` maps both to the canonical
  names. Unknown names pass through lowercased.

- [ ] **Step 1: Write the failing tests**

```go
package mailer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrevoSendRequestShape(t *testing.T) {
	var gotPath, gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("api-key")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"messageId":"<201798300811.5787683@relay.domain.com>"}`))
	}))
	defer srv.Close()
	b := NewBrevo("xkeysib-test", "noreply@example.org", "CoreScope")
	b.BaseURL = srv.URL

	id, err := b.Send(context.Background(), Message{To: "a@example.org", ToName: "Alice", Subject: "S",
		HTML: "<p>h</p>", Text: "t", Tag: "activate"})
	if err != nil || id != "<201798300811.5787683@relay.domain.com>" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	if gotPath != "/smtp/email" || gotKey != "xkeysib-test" {
		t.Fatalf("path=%q key=%q", gotPath, gotKey)
	}
	sender := body["sender"].(map[string]any)
	to := body["to"].([]any)[0].(map[string]any)
	if sender["email"] != "noreply@example.org" || sender["name"] != "CoreScope" ||
		to["email"] != "a@example.org" || to["name"] != "Alice" ||
		body["subject"] != "S" || body["htmlContent"] != "<p>h</p>" || body["textContent"] != "t" ||
		body["tags"].([]any)[0] != "activate" {
		t.Fatalf("request body = %+v", body)
	}
}

func TestBrevoSendErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
	}))
	defer srv.Close()
	b := NewBrevo("bad", "noreply@example.org", "")
	b.BaseURL = srv.URL
	_, err := b.Send(context.Background(), Message{To: "a@example.org"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Key not found") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatal("API key leaked into the error text")
	}
}

func TestBrevoEvents(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"events":[
			{"date":"2026-10-06T12:00:05.000+02:00","email":"a@example.org","event":"requests","messageId":"<m1>"},
			{"date":"2026-10-06T12:00:09.000+02:00","email":"a@example.org","event":"hardBounces","messageId":"<m1>","reason":"user unknown"}]}`))
	}))
	defer srv.Close()
	b := NewBrevo("k", "noreply@example.org", "")
	b.BaseURL = srv.URL
	evs, err := b.Events(context.Background(), "<m1>")
	if err != nil || len(evs) != 2 {
		t.Fatalf("Events = %+v, %v", evs, err)
	}
	if !strings.Contains(gotQuery, "messageId=%3Cm1%3E") {
		t.Fatalf("query = %q", gotQuery)
	}
	if evs[0].Event != EventSent || evs[1].Event != EventHardBounce || evs[1].Reason != "user unknown" ||
		evs[1].At.UTC().Hour() != 10 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestNormalizeBrevoEvent(t *testing.T) {
	cases := map[string]string{
		"request": EventSent, "requests": EventSent, "delivered": EventDelivered,
		"opened": EventOpened, "unique_opened": EventOpened, "proxy_open": EventOpened, "loadedByProxy": EventOpened,
		"click": EventClicked, "clicks": EventClicked,
		"soft_bounce": EventSoftBounce, "softBounces": EventSoftBounce, "bounces": EventSoftBounce,
		"hard_bounce": EventHardBounce, "hardBounces": EventHardBounce,
		"invalid_email": EventInvalidEmail, "invalid": EventInvalidEmail,
		"spam": EventSpam, "blocked": EventBlocked, "deferred": EventDeferred, "error": EventError,
		"Something_New": "something_new",
	}
	for in, want := range cases {
		if got := NormalizeBrevoEvent(in); got != want {
			t.Errorf("NormalizeBrevoEvent(%q) = %q; want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/mailer && go test -run Brevo ./...`
Expected: FAIL (`undefined: NewBrevo`).

- [ ] **Step 3: Implement `brevo.go`**

```go
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const brevoBaseURL = "https://api.brevo.com/v3"

// Brevo sends through Brevo's transactional API.
type Brevo struct {
	APIKey    string
	FromEmail string
	FromName  string
	BaseURL   string // overridable for tests
	HTTP      *http.Client
}

func NewBrevo(apiKey, fromEmail, fromName string) *Brevo {
	return &Brevo{APIKey: apiKey, FromEmail: fromEmail, FromName: fromName,
		BaseURL: brevoBaseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type brevoAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoSendRequest struct {
	Sender      brevoAddress   `json:"sender"`
	To          []brevoAddress `json:"to"`
	Subject     string         `json:"subject"`
	HTMLContent string         `json:"htmlContent"`
	TextContent string         `json:"textContent,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
}

type brevoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (b *Brevo) Send(ctx context.Context, m Message) (string, error) {
	req := brevoSendRequest{
		Sender:      brevoAddress{Email: b.FromEmail, Name: b.FromName},
		To:          []brevoAddress{{Email: m.To, Name: m.ToName}},
		Subject:     m.Subject,
		HTMLContent: m.HTML,
		TextContent: m.Text,
	}
	if m.Tag != "" {
		req.Tags = []string{m.Tag}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/smtp/email", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	data, status, err := b.do(httpReq)
	if err != nil {
		return "", fmt.Errorf("brevo: send: %w", err)
	}
	if status != http.StatusCreated && status != http.StatusAccepted {
		return "", brevoErr("send", status, data)
	}
	var out struct {
		MessageID string `json:"messageId"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.MessageID == "" {
		return "", errors.New("brevo: send: response has no messageId")
	}
	return out.MessageID, nil
}

type brevoEventsResponse struct {
	Events []struct {
		Date      string `json:"date"`
		Email     string `json:"email"`
		Event     string `json:"event"`
		MessageID string `json:"messageId"`
		Reason    string `json:"reason"`
	} `json:"events"`
}

func (b *Brevo) Events(ctx context.Context, messageID string) ([]Event, error) {
	q := url.Values{"messageId": {messageID}, "limit": {"100"}, "sort": {"asc"}}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+"/smtp/statistics/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	data, status, err := b.do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("brevo: events: %w", err)
	}
	if status != http.StatusOK {
		return nil, brevoErr("events", status, data)
	}
	var resp brevoEventsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("brevo: events: %w", err)
	}
	out := make([]Event, 0, len(resp.Events))
	for _, e := range resp.Events {
		at, _ := parseBrevoDate(e.Date)
		out = append(out, Event{MessageID: e.MessageID, Email: e.Email, Event: NormalizeBrevoEvent(e.Event), Reason: e.Reason, At: at})
	}
	return out, nil
}

func (b *Brevo) do(r *http.Request) ([]byte, int, error) {
	r.Header.Set("api-key", b.APIKey)
	r.Header.Set("Accept", "application/json")
	resp, err := b.HTTP.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return data, resp.StatusCode, err
}

func brevoErr(op string, status int, body []byte) error {
	var e brevoError
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return fmt.Errorf("brevo: %s: HTTP %d %s: %s", op, status, e.Code, e.Message)
	}
	return fmt.Errorf("brevo: %s: HTTP %d", op, status)
}

func parseBrevoDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("brevo: unrecognized date %q", s)
}

// brevoEventNames maps webhook and events-API spellings to canonical names.
// "bounces" (events API, unspecified kind) is treated as soft so it never
// flags an address on its own.
var brevoEventNames = map[string]string{
	"request": EventSent, "requests": EventSent,
	"delivered": EventDelivered,
	"opened": EventOpened, "unique_opened": EventOpened, "proxy_open": EventOpened,
	"unique_proxy_open": EventOpened, "loadedbyproxy": EventOpened,
	"click": EventClicked, "clicks": EventClicked,
	"soft_bounce": EventSoftBounce, "softbounces": EventSoftBounce, "bounces": EventSoftBounce,
	"hard_bounce": EventHardBounce, "hardbounces": EventHardBounce,
	"invalid_email": EventInvalidEmail, "invalid": EventInvalidEmail,
	"deferred": EventDeferred, "spam": EventSpam, "blocked": EventBlocked,
	"error": EventError, "unsubscribed": EventUnsubscribed,
}

// NormalizeBrevoEvent maps a Brevo event name to CoreScope's canonical name.
func NormalizeBrevoEvent(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if v, ok := brevoEventNames[key]; ok {
		return v
	}
	return key
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd internal/mailer && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mailer/brevo.go internal/mailer/brevo_test.go
git commit -m "feat(mailer): add the Brevo client for sending and reading events"
```

---

### Task 10: Brevo webhook parser

**Files:**
- Create: `internal/mailer/brevo_webhook.go`
- Test: `internal/mailer/brevo_webhook_test.go`

Brevo webhook facts (developers.brevo.com, "Transactional webhooks" and "Secured
webhook calls"):
- The payload carries `event`, `email`, `message-id`, `reason`, `date`, `ts`,
  `ts_event` and `ts_epoch`.
- A webhook can be configured with `"auth": {"type": "bearer", "token": "…"}`, which
  makes Brevo send `Authorization: Bearer …`. A2 checks that header.
- Batched webhooks send a JSON array. Both shapes are accepted.

- [ ] **Step 1: Write the failing tests**

```go
package mailer

import (
	"testing"
	"time"
)

func TestParseBrevoWebhookSingle(t *testing.T) {
	body := []byte(`{"event":"hard_bounce","email":"a@example.org","id":1,"date":"2026-10-06 12:00:00",
		"ts":1791288000,"message-id":"<m1@relay>","ts_event":1791288005,"reason":"user unknown","subject":"x"}`)
	evs, err := ParseBrevoWebhook(body)
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseBrevoWebhook = %+v, %v", evs, err)
	}
	e := evs[0]
	if e.MessageID != "<m1@relay>" || e.Event != EventHardBounce || e.Reason != "user unknown" ||
		!e.At.Equal(time.Unix(1791288005, 0).UTC()) {
		t.Fatalf("event = %+v", e)
	}
}

func TestParseBrevoWebhookBatchAndFallbacks(t *testing.T) {
	body := []byte(`[{"event":"delivered","message-id":"<a>","ts":1791288000},
		{"event":"unique_opened","message-id":"<b>","date":"2026-10-06 12:00:00"},
		{"event":"delivered","email":"no-id@example.org"}]`)
	evs, err := ParseBrevoWebhook(body)
	if err != nil || len(evs) != 2 {
		t.Fatalf("batch = %+v, %v", evs, err)
	}
	if evs[0].At.Unix() != 1791288000 || evs[1].Event != EventOpened || evs[1].At.IsZero() {
		t.Fatalf("batch events = %+v", evs)
	}
}

func TestParseBrevoWebhookRejectsGarbage(t *testing.T) {
	for _, body := range []string{``, `not json`, `{}`, `[]`, `{"event":"delivered"}`} {
		if _, err := ParseBrevoWebhook([]byte(body)); err == nil {
			t.Errorf("ParseBrevoWebhook(%q) accepted", body)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd internal/mailer && go test -run Webhook ./...`
Expected: FAIL (`undefined: ParseBrevoWebhook`).

- [ ] **Step 3: Implement `brevo_webhook.go`**

```go
package mailer

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

type brevoWebhookEvent struct {
	Event     string `json:"event"`
	Email     string `json:"email"`
	MessageID string `json:"message-id"`
	Reason    string `json:"reason"`
	Date      string `json:"date"`
	Ts        int64  `json:"ts"`
	TsEvent   int64  `json:"ts_event"`
}

// ParseBrevoWebhook parses a transactional webhook body: one event object or,
// for batched webhooks, an array. Entries without a message id or event name
// are skipped; an error is returned if nothing usable remains.
func ParseBrevoWebhook(body []byte) ([]Event, error) {
	trimmed := bytes.TrimSpace(body)
	var raw []brevoWebhookEvent
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, err
		}
	} else {
		var one brevoWebhookEvent
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return nil, err
		}
		raw = append(raw, one)
	}
	out := make([]Event, 0, len(raw))
	for _, e := range raw {
		if e.MessageID == "" || e.Event == "" {
			continue
		}
		var at time.Time
		switch {
		case e.TsEvent > 0:
			at = time.Unix(e.TsEvent, 0).UTC()
		case e.Ts > 0:
			at = time.Unix(e.Ts, 0).UTC()
		default:
			at, _ = parseBrevoDate(e.Date)
		}
		if at.IsZero() {
			at = time.Now().UTC()
		}
		out = append(out, Event{MessageID: e.MessageID, Email: e.Email, Event: NormalizeBrevoEvent(e.Event), Reason: e.Reason, At: at})
	}
	if len(out) == 0 {
		return nil, errors.New("brevo webhook: no usable events")
	}
	return out, nil
}
```

- [ ] **Step 4: Run all mailer tests**

Run: `cd internal/mailer && go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mailer/brevo_webhook.go internal/mailer/brevo_webhook_test.go
git commit -m "feat(mailer): parse Brevo transactional webhooks"
```

---

## A1 done when

- `cd internal/users && go test -race ./...` and `cd internal/mailer && go test -race ./...`
  both pass.
- Both `go.mod` files declare `go 1.22`.
- No file under `cmd/` has changed.
