# User Management A2 — Server Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `internal/users` and `internal/mailer` into `cmd/server`:
- config and startup
- the auth, account, admin and webhook endpoints
- `requireAdmin`, which accepts the API key **or** an admin session
- the client-config flag and OpenAPI entries

All of it is covered by Go HTTP tests.

**Architecture:**
- `Server` gains `auth *authService`. It is nil unless `userManagement.enabled`, and
  every new route is registered only when it is non-nil.
- Handlers that need a person use `withUser` / `withAdmin`, which check the session
  plus CSRF.
- Unauthenticated state-changing endpoints use `requireOrigin`.
- The existing seven `requireAPIKey` call sites switch to `requireAdmin`. With
  `auth == nil` that behaves exactly like today.

**Tech Stack:** Go 1.22, gorilla/mux, the A1 modules.

**Prerequisite:** A1 complete. Spec: `docs/specs/2026-10-06-user-management-design.md`.
Ground rules: `docs/plans/2026-10-06-user-management-a.md`.

## File map (all under `cmd/server/` unless noted)

| File | Responsibility |
|---|---|
| `go.mod` | `require` + `replace` for `users`, `mailer` |
| `../../Dockerfile` | `COPY internal/users/`, `COPY internal/mailer/` in the server builder |
| `config.go` | `UserManagement` field on `Config` |
| `user_mgmt_config.go` | Config types, `UserManagementEnabled`, `resolveUserManagement` → `userMgmtSettings` |
| `auth_service.go` | `authService`, `initUserManagement`, `closeUserManagement`, janitor, helpers |
| `auth_ratelimit.go` | Token-bucket `rateLimiter`, `authService.allow` |
| `auth_session.go` | Cookie, `currentUser`, origin/CSRF checks, `withUser`, `withAdmin`, `requireOrigin` |
| `auth_types.go` | Request/response structs, `decodeJSON`, JSON mappers |
| `auth_mail.go` | Mail composition, `sendMail`, `ingestMailEvents` |
| `auth_handlers.go` | register, activate, login, logout, me, forgot, reset |
| `account_handlers.go` | profile, password, email change, sessions, self-delete |
| `admin_users_handlers.go` | admin list/detail/disable/enable/delete/role/resend/activate/mail refresh |
| `mail_webhook.go` | Brevo webhook handler |
| `auth_routes.go` | `registerAuthRoutes`, `e2eRoutes` hook var |
| `routes.go` | `auth` field, `requireAdmin`, call sites, `registerAuthRoutes` call, client config |
| `types.go` | `ClientConfigResponse.UserManagement` |
| `main.go` | `initUserManagement` / `closeUserManagement` |
| `openapi.go` | `Session` flag, `CookieAuth` scheme, route entries |
| `*_test.go` | `auth_fixture_test.go`, `user_mgmt_config_test.go`, `auth_flow_test.go`, `account_test.go`, `admin_users_test.go`, `mail_webhook_test.go`, `require_admin_test.go`, `user_mgmt_off_test.go`, additions to `readonly_invariant_test.go` |

Shared conventions for every handler:
- **Errors:** `writeError(w, code, msg)` produces `{"error": msg}`.
- **Success:** `writeJSON(w, v)` (200), or `okResponse` for actions without a body.
- **Request bodies:** `decodeJSON` (16 KiB cap, unknown fields rejected).
- **Validation:** failures return 400 with the `users.ValidationError` message.
- **Server log:** never log tokens, passwords, hashes, cookies or email addresses;
  log the user id `#N` instead.

---

### Task 1: Wire the modules into `cmd/server` and Docker

**Files:**
- Modify: `cmd/server/go.mod`, `Dockerfile`

- [ ] **Step 1: Add the requires and replaces**

Append to `cmd/server/go.mod`:
```
require github.com/meshcore-analyzer/users v0.0.0

replace github.com/meshcore-analyzer/users => ../../internal/users

require github.com/meshcore-analyzer/mailer v0.0.0

replace github.com/meshcore-analyzer/mailer => ../../internal/mailer
```

- [ ] **Step 2: Add the Docker COPY lines**

In `Dockerfile`, in the `# Build server` section (`WORKDIR /build/server`), after
`COPY internal/lora/ ../../internal/lora/`, add:
```
COPY internal/users/ ../../internal/users/
COPY internal/mailer/ ../../internal/mailer/
```

- [ ] **Step 3: Verify**

Run:
```bash
cd cmd/server && go build ./... && cd ../.. && bash scripts/check-dockerfile-internal-pkgs.sh
```
Expected: the build succeeds and the check script reports no missing COPY lines. Do **not**
run `go mod tidy` yet: nothing imports the modules until Task 2, so tidy would drop the
requires. Task 2 Step 5 runs it.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile cmd/server/go.mod cmd/server/go.sum
git commit -m "build(server): wire internal/users and internal/mailer into the server"
```

---

### Task 2: Config types and `resolveUserManagement`

**Files:**
- Modify: `cmd/server/config.go` (the `Config` struct, next to `ClientRfSamples`)
- Create: `cmd/server/user_mgmt_config.go`
- Test: `cmd/server/user_mgmt_config_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validUM() *UserManagementConfig {
	return &UserManagementConfig{
		Enabled:       true,
		AdminEmails:   []string{" Boss@Example.org "},
		PublicBaseURL: "https://scope.example.org/",
		Mail:          UserMailConfig{BrevoAPIKey: "xkeysib-abc", FromEmail: "noreply@example.org"},
	}
}

func noEnv(string) string { return "" }

func TestResolveUserManagementDefaults(t *testing.T) {
	set, err := resolveUserManagement(validUM(), filepath.Join("data", "meshcore.db"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.dbPath != filepath.Join("data", "users.db") {
		t.Errorf("dbPath = %q", set.dbPath)
	}
	if !set.adminEmails["boss@example.org"] {
		t.Errorf("adminEmails not normalized: %v", set.adminEmails)
	}
	if set.baseURL.String() != "https://scope.example.org" || !set.secureCookie {
		t.Errorf("baseURL = %q secure=%v", set.baseURL, set.secureCookie)
	}
	if set.sessionTTL != 30*24*time.Hour || set.provider != "brevo" || set.fromName != "CoreScope" {
		t.Errorf("defaults: ttl=%v provider=%q fromName=%q", set.sessionTTL, set.provider, set.fromName)
	}
}

func TestResolveUserManagementEnvWins(t *testing.T) {
	env := map[string]string{"CORESCOPE_BREVO_API_KEY": "from-env", "CORESCOPE_BREVO_WEBHOOK_SECRET": "env-secret-0123456789"}
	set, err := resolveUserManagement(validUM(), "meshcore.db", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if set.brevoAPIKey != "from-env" || set.webhookSecret != "env-secret-0123456789" {
		t.Fatalf("env not applied: %q %q", set.brevoAPIKey, set.webhookSecret)
	}
}

func TestResolveUserManagementErrors(t *testing.T) {
	cases := map[string]func(u *UserManagementConfig){
		"publicBaseUrl":   func(u *UserManagementConfig) { u.PublicBaseURL = "scope.example.org" },
		"fromEmail":       func(u *UserManagementConfig) { u.Mail.FromEmail = "" },
		"Brevo API key":   func(u *UserManagementConfig) { u.Mail.BrevoAPIKey = "" },
		"adminEmails":     func(u *UserManagementConfig) { u.AdminEmails = []string{"not-an-address"} },
		"not supported":   func(u *UserManagementConfig) { u.Mail.Provider = "smtp" },
		"e2etest builds":  func(u *UserManagementConfig) { u.Mail.Provider = "fake" },
		"webhookSecret":   func(u *UserManagementConfig) { u.Mail.WebhookSecret = "short" },
	}
	for want, mutate := range cases {
		u := validUM()
		mutate(u)
		_, err := resolveUserManagement(u, "meshcore.db", noEnv)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestUserManagementEnabled(t *testing.T) {
	var nilCfg *Config
	if nilCfg.UserManagementEnabled() || (&Config{}).UserManagementEnabled() ||
		(&Config{UserManagement: &UserManagementConfig{}}).UserManagementEnabled() {
		t.Fatal("off states reported as enabled")
	}
	if !(&Config{UserManagement: &UserManagementConfig{Enabled: true}}).UserManagementEnabled() {
		t.Fatal("enabled not reported")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/server && go test -run 'ResolveUserManagement|UserManagementEnabled' .`
Expected: FAIL (`undefined: UserManagementConfig`).

- [ ] **Step 3: Add the `Config` field**

In `cmd/server/config.go`, inside `type Config struct`, directly after the
`ClientRfSamples` field, add:
```go
	// UserManagement gates optional accounts
	// (docs/specs/2026-10-06-user-management-design.md). Absent/nil ⇒ off;
	// see UserManagementEnabled.
	UserManagement *UserManagementConfig `json:"userManagement,omitempty"`
```

- [ ] **Step 4: Implement `user_mgmt_config.go`**

```go
package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

// UserManagementConfig is the "userManagement" block of config.json.
type UserManagementConfig struct {
	Enabled        bool           `json:"enabled"`
	DBPath         string         `json:"dbPath,omitempty"`
	AdminEmails    []string       `json:"adminEmails,omitempty"`
	PublicBaseURL  string         `json:"publicBaseUrl,omitempty"`
	SessionDays    int            `json:"sessionDays,omitempty"`
	TrustedProxies []string       `json:"trustedProxies,omitempty"`
	Mail           UserMailConfig `json:"mail"`
}

// UserMailConfig is userManagement.mail.
type UserMailConfig struct {
	Provider      string `json:"provider,omitempty"`
	BrevoAPIKey   string `json:"brevoApiKey,omitempty"`
	FromEmail     string `json:"fromEmail,omitempty"`
	FromName      string `json:"fromName,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

// UserManagementEnabled reports whether optional accounts are on. Nil config
// or absent section ⇒ off (the default).
func (c *Config) UserManagementEnabled() bool {
	return c != nil && c.UserManagement != nil && c.UserManagement.Enabled
}

// userMgmtSettings is the validated, resolved form the auth service runs on.
type userMgmtSettings struct {
	dbPath         string
	adminEmails    map[string]bool
	baseURL        *url.URL // no trailing slash, no query or fragment
	secureCookie   bool
	sessionTTL     time.Duration
	trustedProxies []*net.IPNet
	provider       string // "brevo" or (e2etest builds only) "fake"
	brevoAPIKey    string
	fromEmail      string
	fromName       string
	webhookSecret  string
}

const defaultSessionDays = 30

// fakeMailerAllowed is flipped only by the e2etest build (auth_e2e.go).
var fakeMailerAllowed bool

// resolveUserManagement validates the block and fills defaults. It refuses
// configurations where nobody could activate an account, so the server
// fails at startup instead of running half-working.
func resolveUserManagement(u *UserManagementConfig, measurementDBPath string, getenv func(string) string) (*userMgmtSettings, error) {
	set := &userMgmtSettings{adminEmails: map[string]bool{}}

	set.dbPath = strings.TrimSpace(u.DBPath)
	if set.dbPath == "" {
		set.dbPath = filepath.Join(filepath.Dir(measurementDBPath), "users.db")
	}
	for _, raw := range u.AdminEmails {
		e, err := users.NormalizeEmail(raw)
		if err != nil {
			return nil, fmt.Errorf("userManagement.adminEmails: %q is not a valid address", raw)
		}
		set.adminEmails[e] = true
	}

	base, err := url.Parse(strings.TrimSpace(u.PublicBaseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("userManagement.publicBaseUrl must be an absolute http(s) URL, e.g. https://corescope.example.org")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawQuery, base.Fragment = "", ""
	set.baseURL = base
	set.secureCookie = base.Scheme == "https"

	days := u.SessionDays
	if days <= 0 {
		days = defaultSessionDays
	}
	if days > 365 {
		days = 365
	}
	set.sessionTTL = time.Duration(days) * 24 * time.Hour
	set.trustedProxies = parseCIDRList(u.TrustedProxies, "userManagement.trustedProxies")

	set.provider = strings.ToLower(strings.TrimSpace(u.Mail.Provider))
	if set.provider == "" {
		set.provider = "brevo"
	}
	set.brevoAPIKey = envOrValue(getenv, "CORESCOPE_BREVO_API_KEY", u.Mail.BrevoAPIKey)
	set.webhookSecret = envOrValue(getenv, "CORESCOPE_BREVO_WEBHOOK_SECRET", u.Mail.WebhookSecret)
	from, err := users.NormalizeEmail(u.Mail.FromEmail)
	if err != nil {
		return nil, errors.New("userManagement.mail.fromEmail must be a valid address")
	}
	set.fromEmail = from
	set.fromName = strings.TrimSpace(u.Mail.FromName)
	if set.fromName == "" {
		set.fromName = "CoreScope"
	}

	switch set.provider {
	case "brevo":
		if set.brevoAPIKey == "" {
			return nil, errors.New("userManagement.mail: a Brevo API key is required (mail.brevoApiKey or CORESCOPE_BREVO_API_KEY)")
		}
	case "fake":
		if !fakeMailerAllowed {
			return nil, errors.New(`userManagement.mail.provider "fake" is only available in e2etest builds`)
		}
	default:
		return nil, fmt.Errorf("userManagement.mail.provider %q is not supported (use \"brevo\")", u.Mail.Provider)
	}
	if set.webhookSecret != "" && len(set.webhookSecret) < 16 {
		return nil, errors.New("userManagement.mail.webhookSecret must be at least 16 characters")
	}
	return set, nil
}

func envOrValue(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return strings.TrimSpace(fallback)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd cmd/server && go mod tidy && go test -run 'ResolveUserManagement|UserManagementEnabled' .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): add the optional userManagement config block"
```

---

### Task 3: `authService`, rate limiter, sessions and middleware

**Files:**
- Create: `cmd/server/auth_service.go`, `cmd/server/auth_ratelimit.go`,
  `cmd/server/auth_session.go`, `cmd/server/auth_types.go`, `cmd/server/auth_mail.go`,
  `cmd/server/auth_routes.go`
- Modify: `cmd/server/routes.go` (the `Server` struct gets `auth`; `RegisterRoutes`
  calls `registerAuthRoutes`)
- Test: `cmd/server/auth_ratelimit_test.go`, `cmd/server/auth_fixture_test.go`

- [ ] **Step 1: Write the failing rate-limiter test**

`cmd/server/auth_ratelimit_test.go`:
```go
package main

import (
	"testing"
	"time"
)

func TestRateLimiterBurstAndRefill(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.take("k"); !ok {
			t.Fatalf("take %d refused within burst", i)
		}
	}
	ok, wait := l.take("k")
	if ok || wait <= 0 || wait > 20*time.Second {
		t.Fatalf("4th take = %v, wait %v", ok, wait)
	}
	if ok, _ := l.take("other"); !ok {
		t.Fatal("keys are not independent")
	}
	now = now.Add(21 * time.Second) // one token per 20s
	if ok, _ := l.take("k"); !ok {
		t.Fatal("no refill after 21s")
	}
	now = now.Add(time.Hour)
	l.gc()
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("gc kept %d full buckets", n)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd cmd/server && go test -run RateLimiter .`
Expected: FAIL (`undefined: newRateLimiter`).

- [ ] **Step 3: Implement `auth_ratelimit.go`**

```go
package main

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter is a keyed token bucket: n requests per period, refilled
// continuously. In-memory, so limits reset on restart. That is acceptable
// for brute-force damping, not a quota system.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*rlBucket
	now     func() time.Time
}

type rlBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(n int, per time.Duration) *rateLimiter {
	return &rateLimiter{rate: float64(n) / per.Seconds(), burst: float64(n),
		buckets: map[string]*rlBucket{}, now: time.Now}
}

// take consumes one token for key. When none is left it reports how long
// until the next one.
func (l *rateLimiter) take(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &rlBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// gc drops buckets that have refilled completely (they carry no state).
func (l *rateLimiter) gc() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// allow applies l to the client IP (only when IPs can be told apart; same
// rule as the /ws limiter, see ws_limits.go) and to each extra key. It
// writes 429 + Retry-After and returns false when any bucket is empty.
func (a *authService) allow(w http.ResponseWriter, r *http.Request, l *rateLimiter, extraKeys ...string) bool {
	keys := extraKeys
	if ip, distinct := a.ipr.clientIP(r); distinct && ip != nil {
		keys = append([]string{"ip:" + ip.String()}, extraKeys...)
	} else {
		a.warnIndistinct.Do(func() {
			log.Printf("[users] rate limits by IP are off: requests arrive from a proxy and userManagement.trustedProxies is empty; per-address limits still apply")
		})
	}
	for _, k := range keys {
		if ok, wait := l.take(k); !ok {
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Implement `auth_service.go`**

```go
package main

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// authService is the optional user-management layer. Server.auth is nil
// unless userManagement.enabled, and nothing in this file runs then.
type authService struct {
	st   *users.Store
	mail mailer.Mailer
	set  *userMgmtSettings
	ipr  *wsLimiter // only its clientIP rule is used

	login  *rateLimiter
	signup *rateLimiter // register, forgot, resend
	hook   *rateLimiter

	warnIndistinct sync.Once
	stop           chan struct{}
	stopOnce       sync.Once
}

func newAuthService(set *userMgmtSettings, st *users.Store, m mailer.Mailer) *authService {
	return &authService{
		st: st, mail: m, set: set,
		ipr:    &wsLimiter{trustedProxies: set.trustedProxies},
		login:  newRateLimiter(10, 15*time.Minute),
		signup: newRateLimiter(5, time.Hour),
		hook:   newRateLimiter(600, time.Minute),
		stop:   make(chan struct{}),
	}
}

// initUserManagement builds s.auth when the feature is on. Call it after
// NewServer and before RegisterRoutes. measurementDBPath is passed to
// users.Open as a forbidden path, so users.db can never be the analyzer DB.
func (s *Server) initUserManagement(measurementDBPath string) error {
	if !s.cfg.UserManagementEnabled() {
		return nil
	}
	set, err := resolveUserManagement(s.cfg.UserManagement, measurementDBPath, os.Getenv)
	if err != nil {
		return err
	}
	st, err := users.Open(set.dbPath, measurementDBPath)
	if err != nil {
		return err
	}
	var m mailer.Mailer
	if set.provider == "fake" {
		m = &mailer.Fake{}
	} else {
		m = mailer.NewBrevo(set.brevoAPIKey, set.fromEmail, set.fromName)
	}
	s.auth = newAuthService(set, st, m)
	s.auth.logStartup()
	go s.auth.janitor(time.Hour)
	return nil
}

// closeUserManagement stops the janitor and closes users.db. Safe when off.
func (s *Server) closeUserManagement() {
	if s.auth == nil {
		return
	}
	s.auth.stopOnce.Do(func() {
		close(s.auth.stop)
		if err := s.auth.st.Close(); err != nil {
			log.Printf("[users] close: %v", err)
		}
	})
}

func (a *authService) logStartup() {
	log.Printf("[users] user management enabled: db=%s, %d config admin(s), webhook=%v",
		a.set.dbPath, len(a.set.adminEmails), a.set.webhookSecret != "")
	admins, err := a.st.List(users.ListFilter{Role: users.RoleAdmin})
	if err != nil {
		log.Printf("[users] list admins: %v", err)
		return
	}
	for _, u := range admins {
		if !a.isConfigAdmin(u.Email) {
			log.Printf("[users] note: admin #%d is not in adminEmails (promoted in the UI, or removed from config)", u.ID)
		}
	}
}

func (a *authService) janitor(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		a.prune()
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
	}
}

func (a *authService) prune() {
	if _, err := a.st.PruneStalePending(48 * time.Hour); err != nil {
		log.Printf("[users] prune pending: %v", err)
	}
	if _, err := a.st.PruneExpiredSessions(); err != nil {
		log.Printf("[users] prune sessions: %v", err)
	}
	if _, err := a.st.PruneTokens(7 * 24 * time.Hour); err != nil {
		log.Printf("[users] prune tokens: %v", err)
	}
	if _, err := a.st.PruneMail(90 * 24 * time.Hour); err != nil {
		log.Printf("[users] prune mail log: %v", err)
	}
	a.login.gc()
	a.signup.gc()
	a.hook.gc()
}

func (a *authService) isConfigAdmin(email string) bool { return a.set.adminEmails[email] }

// roleFor is the role an address gets on activation.
func (a *authService) roleFor(email string) users.Role {
	if a.isConfigAdmin(email) {
		return users.RoleAdmin
	}
	return users.RoleUser
}

func (a *authService) audit(actor *int64, action string, target *int64, detail map[string]string) {
	if err := a.st.Audit(actor, action, target, detail); err != nil {
		log.Printf("[users] audit %s: %v", action, err)
	}
}

func idPtr(id int64) *int64 { return &id }
```

- [ ] **Step 5: Implement `auth_session.go`**

```go
package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	sessionCookieName = "cs_session"
	csrfHeader        = "X-CS-CSRF"
)

func (a *authService) setSessionCookie(w http.ResponseWriter, raw string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: raw, Path: "/",
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: a.set.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}

func (a *authService) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.set.secureCookie, SameSite: http.SameSiteLaxMode})
}

// currentUser returns the active user behind the session cookie, or nils.
// A session last seen more than a day ago is extended (sliding expiry)
// and its cookie re-issued.
func (a *authService) currentUser(w http.ResponseWriter, r *http.Request) (*users.User, *users.Session) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	sess, err := a.st.LookupSession(c.Value)
	if err != nil {
		return nil, nil
	}
	u, err := a.st.GetByID(sess.UserID)
	if err != nil || u.Status != users.StatusActive {
		return nil, nil
	}
	if time.Since(sess.LastSeenAt) > 24*time.Hour {
		if err := a.st.ExtendSession(sess.ID, a.set.sessionTTL); err == nil {
			a.setSessionCookie(w, c.Value, time.Now().Add(a.set.sessionTTL))
		}
	}
	return u, sess
}

// originOK requires the request's Origin (or, if absent, Referer) to be the
// configured publicBaseUrl's origin.
func (a *authService) originOK(r *http.Request) bool {
	want := a.set.baseURL.Scheme + "://" + a.set.baseURL.Host
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, want)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && strings.EqualFold(u.Scheme+"://"+u.Host, want)
	}
	return false
}

func (a *authService) csrfOK(r *http.Request, sess *users.Session) bool {
	return a.originOK(r) && constantTimeEqual(r.Header.Get(csrfHeader), sess.CSRFToken)
}

func isSafeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

type authedHandler func(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session)

// withUser requires a logged-in active user; state-changing methods must
// also pass the origin + CSRF-token check.
func (s *Server) withUser(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, sess := s.auth.currentUser(w, r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if !isSafeMethod(r.Method) && !s.auth.csrfOK(r, sess) {
			writeError(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r, u, sess)
	}
}

// withAdmin is withUser plus role admin.
func (s *Server) withAdmin(h authedHandler) http.HandlerFunc {
	return s.withUser(func(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
		if u.Role != users.RoleAdmin {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		h(w, r, u, sess)
	})
}

// requireOrigin guards unauthenticated state-changing endpoints (login CSRF).
func (s *Server) requireOrigin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.originOK(r) {
			writeError(w, http.StatusForbidden, "request origin not allowed")
			return
		}
		h(w, r)
	}
}
```

- [ ] **Step 6: Implement `auth_types.go`**

```go
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/users"
)

type okResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type meResponse struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Role        users.Role `json:"role"`
	CSRFToken   string     `json:"csrfToken"`
}

type registerRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type emailRequest struct {
	Email string `json:"email"`
}

type tokenRequest struct {
	Token string `json:"token"`
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type profileRequest struct {
	DisplayName string `json:"displayName"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type emailChangeRequest struct {
	NewEmail        string `json:"newEmail"`
	CurrentPassword string `json:"currentPassword"`
}

type passwordConfirmRequest struct {
	CurrentPassword string `json:"currentPassword"`
}

type roleRequest struct {
	Role users.Role `json:"role"`
}

type sessionJSON struct {
	ID         int64  `json:"id"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
	UserAgent  string `json:"userAgent"`
	Current    bool   `json:"current"`
}

type mailEventJSON struct {
	Event  string `json:"event"`
	At     string `json:"at"`
	Reason string `json:"reason,omitempty"`
}

type mailJSON struct {
	ID          int64           `json:"id"`
	Purpose     string          `json:"purpose"`
	To          string          `json:"to"`
	SentAt      string          `json:"sentAt"`
	LastEvent   string          `json:"lastEvent"`
	LastEventAt string          `json:"lastEventAt"`
	LastReason  string          `json:"lastReason,omitempty"`
	Events      []mailEventJSON `json:"events,omitempty"`
}

type adminUserJSON struct {
	ID                int64        `json:"id"`
	Email             string       `json:"email"`
	DisplayName       string       `json:"displayName"`
	Role              users.Role   `json:"role"`
	Status            users.Status `json:"status"`
	CreatedAt         string       `json:"createdAt"`
	ActivatedAt       *string      `json:"activatedAt"`
	ActivatedManually bool         `json:"activatedManually"`
	ActivatedBy       *int64       `json:"activatedBy"`
	LastLoginAt       *string      `json:"lastLoginAt"`
	EmailBouncing     bool         `json:"emailBouncing"`
	ConfigAdmin       bool         `json:"configAdmin"`
	LastMail          *mailJSON    `json:"lastMail"`
}

type auditJSON struct {
	ID           int64             `json:"id"`
	At           string            `json:"at"`
	ActorUserID  *int64            `json:"actorUserId"`
	Action       string            `json:"action"`
	TargetUserID *int64            `json:"targetUserId"`
	Detail       map[string]string `json:"detail"`
}

type adminUserDetailJSON struct {
	User     adminUserJSON `json:"user"`
	Sessions []sessionJSON `json:"sessions"`
	Mail     []mailJSON    `json:"mail"`
	Audit    []auditJSON   `json:"audit"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

func meFrom(u *users.User, sess *users.Session) meResponse {
	return meResponse{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, CSRFToken: sess.CSRFToken}
}

func sessionToJSON(s users.Session, currentID int64) sessionJSON {
	return sessionJSON{ID: s.ID, CreatedAt: rfc3339(s.CreatedAt), LastSeenAt: rfc3339(s.LastSeenAt),
		ExpiresAt: rfc3339(s.ExpiresAt), UserAgent: s.UserAgent, Current: s.ID == currentID}
}

func mailToJSON(m users.MailRecord) mailJSON {
	out := mailJSON{ID: m.ID, Purpose: m.Purpose, To: m.ToEmail, SentAt: rfc3339(m.SentAt),
		LastEvent: m.LastEvent, LastEventAt: rfc3339(m.LastEventAt), LastReason: m.LastReason}
	for _, e := range m.Events {
		out.Events = append(out.Events, mailEventJSON{Event: e.Event, At: rfc3339(e.At), Reason: e.Reason})
	}
	return out
}

func (a *authService) adminRow(u users.User, last map[int64]users.MailRecord) adminUserJSON {
	row := adminUserJSON{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, Status: u.Status,
		CreatedAt: rfc3339(u.CreatedAt), ActivatedAt: rfc3339Ptr(u.ActivatedAt), ActivatedManually: u.ActivatedBy != nil,
		ActivatedBy: u.ActivatedBy, LastLoginAt: rfc3339Ptr(u.LastLoginAt), EmailBouncing: u.EmailBouncing,
		ConfigAdmin: a.isConfigAdmin(u.Email)}
	if m, ok := last[u.ID]; ok {
		mj := mailToJSON(m)
		row.LastMail = &mj
	}
	return row
}

// decodeJSON reads a small JSON body into dst, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// writeValidation writes 400 for a users.ValidationError and reports
// whether it did.
func writeValidation(w http.ResponseWriter, err error) bool {
	var ve *users.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Msg)
		return true
	}
	return false
}

func writeTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, users.ErrTokenExpired):
		writeError(w, http.StatusGone, "this link has expired")
	case errors.Is(err, users.ErrTokenInvalid):
		writeError(w, http.StatusGone, "this link is invalid or was already used")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
```

- [ ] **Step 7: Implement `auth_mail.go`**

```go
package main

import (
	"context"
	"html"
	"log"
	"net/url"
	"strings"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

type mailContent struct {
	subject     string
	greeting    string
	paragraphs  []string
	actionLabel string
	actionURL   string
}

// link builds {publicBaseUrl}/#/account/{page}?token=… (never from the
// request Host, see the spec's Configuration section).
func (a *authService) link(page, token string) string {
	return a.set.baseURL.String() + "/#/account/" + page + "?token=" + url.QueryEscape(token)
}

func (a *authService) render(to, toName, tag string, c mailContent) mailer.Message {
	footer := "You received this because this address was used on " + a.set.baseURL.Host +
		". If that was not you, you can ignore this mail."
	var text, h strings.Builder
	text.WriteString(c.greeting + "\n\n")
	h.WriteString("<p>" + html.EscapeString(c.greeting) + "</p>")
	for _, p := range c.paragraphs {
		text.WriteString(p + "\n\n")
		h.WriteString("<p>" + html.EscapeString(p) + "</p>")
	}
	if c.actionURL != "" {
		text.WriteString(c.actionLabel + ":\n" + c.actionURL + "\n\n")
		h.WriteString(`<p><a href="` + html.EscapeString(c.actionURL) + `">` + html.EscapeString(c.actionLabel) + `</a></p>`)
	}
	text.WriteString(footer + "\n")
	h.WriteString(`<p style="color:#666;font-size:12px">` + html.EscapeString(footer) + `</p>`)
	return mailer.Message{To: to, ToName: toName, Subject: "[" + a.set.fromName + "] " + c.subject,
		HTML: h.String(), Text: text.String(), Tag: tag}
}

func (a *authService) activationMail(u *users.User, token string) mailer.Message {
	return a.render(u.Email, u.DisplayName, "activate", mailContent{
		subject: "Activate your account", greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Confirm your address to activate your account. The link works once and expires in 48 hours."},
		actionLabel: "Activate my account", actionURL: a.link("activate", token)})
}

func (a *authService) resetMail(u *users.User, token string) mailer.Message {
	return a.render(u.Email, u.DisplayName, "reset", mailContent{
		subject: "Reset your password", greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Someone asked to reset the password of your account. The link works once and expires in 1 hour. Using it logs out all your devices."},
		actionLabel: "Choose a new password", actionURL: a.link("reset", token)})
}

func (a *authService) registerNoticeMail(u *users.User) mailer.Message {
	return a.render(u.Email, u.DisplayName, "register-notice", mailContent{
		subject: "Registration attempt with your address", greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{"Someone tried to register a new account with your address, but you already have one.",
			"If it was you, log in, or use \"forgot password\" if you lost it."},
		actionLabel: "Log in", actionURL: a.set.baseURL.String() + "/#/account/login"})
}

func (a *authService) emailChangeConfirmMail(u *users.User, newEmail, token string) mailer.Message {
	return a.render(newEmail, u.DisplayName, "email-change", mailContent{
		subject: "Confirm your new address", greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Confirm that this address should replace the one on your account. The link expires in 24 hours."},
		actionLabel: "Confirm new address", actionURL: a.link("confirm-email", token)})
}

func (a *authService) emailChangeNoticeMail(u *users.User, newEmail string) mailer.Message {
	return a.render(u.Email, u.DisplayName, "email-change-notice", mailContent{
		subject: "Address change requested", greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{"A change of your account address to " + newEmail + " was requested. It takes effect only when confirmed from the new address.",
			"If this was not you, change your password now."}})
}

// sendMail sends msg and records it in the mail log. The error is logged
// (without tokens) and returned so the caller can answer 503.
func (a *authService) sendMail(ctx context.Context, u *users.User, purpose string, msg mailer.Message) error {
	id, err := a.mail.Send(ctx, msg)
	if err != nil {
		log.Printf("[users] mail %s for user #%d failed: %v", purpose, u.ID, err)
		return err
	}
	if _, err := a.st.LogMail(idPtr(u.ID), msg.To, purpose, id); err != nil {
		log.Printf("[users] mail log for user #%d: %v", u.ID, err)
	}
	return nil
}

// ingestMailEvents records provider events and flags undeliverable addresses.
// Shared by the webhook and the admin "refresh" pull.
func (a *authService) ingestMailEvents(evs []mailer.Event) {
	for _, ev := range evs {
		uid, found, err := a.st.RecordMailEvent(ev.MessageID, ev.Event, ev.At, ev.Reason)
		if err != nil {
			log.Printf("[users] record mail event: %v", err)
			continue
		}
		if found && uid != nil && mailer.IsUndeliverable(ev.Event) {
			if err := a.st.SetEmailBouncing(*uid, true); err != nil {
				log.Printf("[users] flag bouncing for user #%d: %v", *uid, err)
			}
		}
	}
}
```

- [ ] **Step 8: Implement `auth_routes.go` (handlers are added in Tasks 4–7)**

```go
package main

import "github.com/gorilla/mux"

// e2eRoutes is set only by the e2etest build (auth_e2e.go).
var e2eRoutes func(s *Server, r *mux.Router)

// registerAuthRoutes adds every user-management route. Called by
// RegisterRoutes only when s.auth != nil, so with the feature off these
// paths are plain 404s.
func (s *Server) registerAuthRoutes(r *mux.Router) {
	// Tasks 4–7 add routes here.
	if e2eRoutes != nil {
		e2eRoutes(s, r)
	}
}
```

- [ ] **Step 9: Hook into `Server` and `RegisterRoutes`**

In `cmd/server/routes.go`, add to `type Server struct` (after `knownChannels`):
```go
	// Optional user management (docs/specs/2026-10-06-user-management-design.md).
	// Nil unless userManagement.enabled; see initUserManagement.
	auth *authService
```
In `RegisterRoutes`, directly after `r.Use(cdnDetectionMiddleware)`, add:
```go
	// Optional user management: routes exist only when the feature is on.
	if s.auth != nil {
		s.registerAuthRoutes(r)
	}
```

- [ ] **Step 10: Write the shared test fixture**

`cmd/server/auth_fixture_test.go`:
```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

const (
	testBase   = "https://scope.example.org"
	testAPIKey = "test-secret-key-strong-enough"
	testHook   = "webhook-secret-0123456789"
)

type authFixture struct {
	srv    *Server
	router *mux.Router
	fake   *mailer.Fake
	st     *users.Store
}

// client is a browser: its session cookie and CSRF token.
type client struct {
	cookie *http.Cookie
	csrf   string
	me     meResponse
}

func newTestAuthService(t *testing.T, adminEmails ...string) (*authService, *mailer.Fake) {
	t.Helper()
	set := &userMgmtSettings{
		dbPath: filepath.Join(t.TempDir(), "users.db"), adminEmails: map[string]bool{},
		sessionTTL: 30 * 24 * time.Hour, provider: "fake",
		fromEmail: "noreply@example.org", fromName: "CoreScope", webhookSecret: testHook,
	}
	set.baseURL, _ = url.Parse(testBase)
	set.secureCookie = true
	for _, e := range adminEmails {
		set.adminEmails[e] = true
	}
	st, err := users.Open(set.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &mailer.Fake{}
	return newAuthService(set, st, fake), fake
}

// newAuthFixture builds a Server with auth on and only the auth routes.
func newAuthFixture(t *testing.T, adminEmails ...string) *authFixture {
	t.Helper()
	a, fake := newTestAuthService(t, adminEmails...)
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	return &authFixture{srv: srv, router: r, fake: fake, st: a.st}
}

type reqMod func(*http.Request)

func as(c *client) reqMod {
	return func(r *http.Request) {
		if c.cookie != nil {
			r.AddCookie(c.cookie)
		}
		if c.csrf != "" {
			r.Header.Set(csrfHeader, c.csrf)
		}
	}
}

func header(k, v string) reqMod { return func(r *http.Request) { r.Header.Set(k, v) } }
func fromIP(ip string) reqMod  { return func(r *http.Request) { r.RemoteAddr = ip + ":5555" } }

func (f *authFixture) do(method, path string, body any, mods ...reqMod) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "203.0.113.10:5555"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !isSafeMethod(method) {
		req.Header.Set("Origin", testBase)
	}
	for _, m := range mods {
		m(req)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T from %q: %v", v, w.Body.String(), err)
	}
	return v
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, code, w.Body.String())
	}
}

var tokenRE = regexp.MustCompile(`token=([A-Za-z0-9_%\-]+)`)

// lastToken extracts the token from the newest fake mail's link.
func (f *authFixture) lastToken(t *testing.T) string {
	t.Helper()
	m, _, ok := f.fake.Last()
	if !ok {
		t.Fatal("no mail was sent")
	}
	sm := tokenRE.FindStringSubmatch(m.Text)
	if sm == nil {
		t.Fatalf("no token in mail text: %q", m.Text)
	}
	tok, _ := url.QueryUnescape(sm[1])
	return tok
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", sessionCookieName)
	return nil
}

// registerAndActivate runs the link flow and returns the logged-in client.
func (f *authFixture) registerAndActivate(t *testing.T, email, name, password string) *client {
	t.Helper()
	w := f.do("POST", "/api/auth/register", registerRequest{Email: email, DisplayName: name, Password: password})
	expectStatus(t, w, 200)
	w = f.do("POST", "/api/auth/activate", tokenRequest{Token: f.lastToken(t)})
	expectStatus(t, w, 200)
	me := decode[meResponse](t, w)
	return &client{cookie: sessionFrom(t, w), csrf: me.CSRFToken, me: me}
}

func (f *authFixture) login(t *testing.T, email, password string) *client {
	t.Helper()
	w := f.do("POST", "/api/auth/login", loginRequest{Email: email, Password: password})
	expectStatus(t, w, 200)
	me := decode[meResponse](t, w)
	return &client{cookie: sessionFrom(t, w), csrf: me.CSRFToken, me: me}
}
```

- [ ] **Step 11: Build and run the tests**

Run: `cd cmd/server && go vet . && go test -run 'RateLimiter|ResolveUserManagement' .`
Expected: PASS. The fixture compiles: it uses only types and functions from this task and
reaches routes over HTTP. Those routes 404 until Task 4.

- [ ] **Step 12: Commit**

```bash
git add cmd/server
git commit -m "feat(server): add the auth service, rate limiter, session middleware and mail templates"
```

---

### Task 4: Register, activate, login, logout, me, forgot, reset

**Files:**
- Create: `cmd/server/auth_handlers.go`
- Modify: `cmd/server/auth_routes.go`
- Test: `cmd/server/auth_flow_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

const pw = "correct horse battery"

func TestAuthRegisterActivateLoginLogout(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("POST", "/api/auth/register", registerRequest{Email: "Alice@Example.org", DisplayName: "Alice", Password: pw})
	expectStatus(t, w, 200)
	if m, _, _ := f.fake.Last(); m.To != "alice@example.org" || !strings.Contains(m.Subject, "Activate") {
		t.Fatalf("activation mail = %+v", m)
	}
	// Pending accounts cannot log in.
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "alice@example.org", Password: pw}), 401)

	w = f.do("POST", "/api/auth/activate", tokenRequest{Token: f.lastToken(t)})
	expectStatus(t, w, 200)
	me := decode[meResponse](t, w)
	ck := sessionFrom(t, w)
	if me.Role != users.RoleUser || me.CSRFToken == "" || !ck.HttpOnly || !ck.Secure {
		t.Fatalf("activate: me=%+v cookie=%+v", me, ck)
	}
	c := &client{cookie: ck, csrf: me.CSRFToken}
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 200)
	expectStatus(t, f.do("POST", "/api/auth/logout", nil, as(c)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 401)
	f.login(t, "alice@example.org", pw)
}

func TestAuthRegisterIsEnumerationSafe(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	first := f.do("POST", "/api/auth/register", registerRequest{Email: "carol@example.org", DisplayName: "Carol", Password: pw})
	again := f.do("POST", "/api/auth/register", registerRequest{Email: "bob@example.org", DisplayName: "Bobby", Password: pw})
	if first.Code != again.Code || first.Body.String() != again.Body.String() {
		t.Fatalf("responses differ: %d %q vs %d %q", first.Code, first.Body, again.Code, again.Body)
	}
	if m, _, _ := f.fake.Last(); m.To != "bob@example.org" || !strings.Contains(m.Subject, "Registration attempt") {
		t.Fatalf("existing-account notice = %+v", m)
	}
	// Re-registering a still-pending address re-sends the activation link.
	f.do("POST", "/api/auth/register", registerRequest{Email: "carol@example.org", DisplayName: "Carol", Password: pw})
	if m, _, _ := f.fake.Last(); m.To != "carol@example.org" || !strings.Contains(m.Subject, "Activate") {
		t.Fatalf("pending re-register mail = %+v", m)
	}
}

func TestAuthValidationErrors(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("POST", "/api/auth/register", registerRequest{Email: "x@example.org", DisplayName: "X", Password: pw})
	expectStatus(t, w, 400)
	w = f.do("POST", "/api/auth/register", registerRequest{Email: "x@example.org", DisplayName: "Xx", Password: "short"})
	expectStatus(t, w, 400)
	w = f.do("POST", "/api/auth/register", map[string]string{"email": "x@example.org", "bogus": "1"})
	expectStatus(t, w, 400)
}

func TestAuthActivateAdminFromConfig(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	c := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	if c.me.Role != users.RoleAdmin {
		t.Fatalf("config admin activated as %q", c.me.Role)
	}
	// Activation links are single-use.
	expectStatus(t, f.do("POST", "/api/auth/activate", tokenRequest{Token: "bogus"}), 410)
}

func TestAuthLoginGenericFailure(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	unknown := f.do("POST", "/api/auth/login", loginRequest{Email: "nobody@example.org", Password: pw})
	wrong := f.do("POST", "/api/auth/login", loginRequest{Email: "dave@example.org", Password: "wrong password!"})
	if unknown.Code != 401 || wrong.Code != 401 || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown=%d %q wrong=%d %q", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
	}
}

func TestAuthConfigAdminRoleRestoredOnLogin(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	c := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	f.st.SetRole(c.me.ID, users.RoleUser) // e.g. demoted by direct DB edit
	if again := f.login(t, "boss@example.org", pw); again.me.Role != users.RoleAdmin {
		t.Fatalf("config admin logged in as %q", again.me.Role)
	}
}

func TestAuthForgotAndReset(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "erin@example.org", "Erin", pw)
	sent := len(f.fake.Sent())
	expectStatus(t, f.do("POST", "/api/auth/forgot", emailRequest{Email: "nobody@example.org"}), 200)
	if len(f.fake.Sent()) != sent {
		t.Fatal("forgot for an unknown address sent mail")
	}
	expectStatus(t, f.do("POST", "/api/auth/forgot", emailRequest{Email: "erin@example.org"}), 200)
	tok := f.lastToken(t)
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: tok, Password: "a brand new secret"}), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 401) // all sessions ended
	f.login(t, "erin@example.org", "a brand new secret")
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: tok, Password: "another new secret"}), 410)
}

func TestAuthLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	for i := 0; i < 10; i++ {
		expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: "nope nope nope"}), 401)
	}
	w := f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: "nope nope nope"})
	expectStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Per-address bucket: another IP is still limited for the same address.
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: "nope"}, fromIP("198.51.100.7")), 429)
}

func TestAuthOriginRequiredForLogin(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: pw}, header("Origin", "https://evil.example"))
	expectStatus(t, w, 403)
}

func TestAuthMailFailureRollsBackRegistration(t *testing.T) {
	f := newAuthFixture(t)
	f.fake.SetSendErr(errors.New("brevo down"))
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "fay@example.org", DisplayName: "Fay", Password: pw}), 503)
	if _, err := f.st.GetByEmail("fay@example.org"); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("user kept after failed mail: %v", err)
	}
	f.fake.SetSendErr(nil)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "fay@example.org", DisplayName: "Fay", Password: pw}), 200)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/server && go test -run TestAuth .`
Expected: FAIL (routes return 404: they aren't registered yet).

- [ ] **Step 3: Implement `auth_handlers.go`**

```go
package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	msgCheckMail  = "If the address can receive mail, a message is on its way. Check your inbox."
	msgBadLogin   = "incorrect email or password"
	msgMailFailed = "mail could not be sent, try again later"
)

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, err := users.NormalizeEmail(req.Email)
	if writeValidation(w, err) {
		return
	}
	name, err := users.ValidateDisplayName(req.DisplayName)
	if writeValidation(w, err) {
		return
	}
	if writeValidation(w, users.ValidatePassword(req.Password)) {
		return
	}
	if !a.allow(w, r, a.signup, "email:"+email) {
		return
	}
	// Hash before the lookup so new and existing addresses cost the same.
	hash, err := users.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing, err := a.st.GetByEmail(email); err == nil {
		// Identical response either way (no account enumeration).
		if existing.Status == users.StatusPending {
			if tok, err := a.st.IssueToken(existing.ID, users.PurposeActivate, 48*time.Hour, ""); err == nil {
				_ = a.sendMail(r.Context(), existing, "activate", a.activationMail(existing, tok))
			}
		} else {
			_ = a.sendMail(r.Context(), existing, "register-notice", a.registerNoticeMail(existing))
		}
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	u, err := a.st.CreatePending(email, name, hash)
	if errors.Is(err, users.ErrEmailTaken) { // lost a race with a concurrent register
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	tok, err := a.st.IssueToken(u.ID, users.PurposeActivate, 48*time.Hour, "")
	if err == nil {
		err = a.sendMail(r.Context(), u, "activate", a.activationMail(u, tok))
	}
	if err != nil {
		if derr := a.st.Delete(u.ID); derr != nil {
			log.Printf("[users] rollback of user #%d failed: %v", u.ID, derr)
		}
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	a.audit(nil, "user.register", idPtr(u.ID), nil)
	writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	uid, _, err := a.st.ConsumeToken(req.Token, users.PurposeActivate)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	u, err := a.st.GetByID(uid)
	if err != nil || u.Status != users.StatusPending {
		writeError(w, http.StatusGone, "this account is already activated, log in instead")
		return
	}
	if err := a.st.Activate(u.ID, a.roleFor(u.Email), nil); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(nil, "user.activate", idPtr(u.ID), nil)
	if u, err = a.st.GetByID(u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.startSession(w, r, u)
}

// startSession creates a session, sets the cookie and answers with /me.
func (a *authService) startSession(w http.ResponseWriter, r *http.Request, u *users.User) {
	raw, sess, err := a.st.CreateSession(u.ID, a.set.sessionTTL, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = a.st.TouchLogin(u.ID)
	a.setSessionCookie(w, raw, sess.ExpiresAt)
	writeJSON(w, meFrom(u, sess))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, emailErr := users.NormalizeEmail(req.Email)
	key := "email:" + email
	if emailErr != nil {
		key = "email:invalid"
	}
	if !a.allow(w, r, a.login, key) {
		return
	}
	var u *users.User
	if emailErr == nil {
		u, _ = a.st.GetByEmail(email)
	}
	if u == nil {
		users.BurnPasswordCheck(req.Password)
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		return
	}
	ok, err := users.VerifyPassword(u.PasswordHash, req.Password)
	if err != nil || !ok || u.Status != users.StatusActive {
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		return
	}
	// Config wins: an address in adminEmails is always admin.
	if a.isConfigAdmin(u.Email) && u.Role != users.RoleAdmin {
		if err := a.st.SetRole(u.ID, users.RoleAdmin); err == nil {
			u.Role = users.RoleAdmin
			a.audit(nil, "user.role.config", idPtr(u.ID), map[string]string{"role": "admin"})
		}
	}
	a.startSession(w, r, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		_ = s.auth.st.DeleteSessionByToken(c.Value)
	}
	s.auth.clearSessionCookie(w)
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, sess := s.auth.currentUser(w, r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, meFrom(u, sess))
}

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req emailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, err := users.NormalizeEmail(req.Email)
	if err != nil {
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	if !a.allow(w, r, a.signup, "email:"+email) {
		return
	}
	u, err := a.st.GetByEmail(email)
	if err != nil || u.Status != users.StatusActive {
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	tok, err := a.st.IssueToken(u.ID, users.PurposeReset, time.Hour, "")
	if err == nil {
		err = a.sendMail(r.Context(), u, "reset", a.resetMail(u, tok))
	}
	if err != nil {
		_ = a.st.InvalidateTokens(u.ID, users.PurposeReset)
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req resetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if writeValidation(w, users.ValidatePassword(req.Password)) {
		return
	}
	uid, _, err := a.st.ConsumeToken(req.Token, users.PurposeReset)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	hash, err := users.HashPassword(req.Password)
	if err == nil {
		err = a.st.SetPassword(uid, hash)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = a.st.DeleteUserSessions(uid, 0)
	a.audit(idPtr(uid), "user.password.reset", idPtr(uid), nil)
	writeJSON(w, okResponse{OK: true, Message: "Password changed. Log in with your new password."})
}
```

- [ ] **Step 4: Register the routes**

In `auth_routes.go`, replace the comment `// Tasks 4–7 add routes here.` with:
```go
	r.HandleFunc("/api/auth/register", s.requireOrigin(s.handleRegister)).Methods("POST")
	r.HandleFunc("/api/auth/activate", s.requireOrigin(s.handleActivate)).Methods("POST")
	r.HandleFunc("/api/auth/login", s.requireOrigin(s.handleLogin)).Methods("POST")
	r.HandleFunc("/api/auth/logout", s.requireOrigin(s.handleLogout)).Methods("POST")
	r.HandleFunc("/api/auth/me", s.handleMe).Methods("GET")
	r.HandleFunc("/api/auth/forgot", s.requireOrigin(s.handleForgot)).Methods("POST")
	r.HandleFunc("/api/auth/reset", s.requireOrigin(s.handleReset)).Methods("POST")
	// Tasks 5–7 add routes here.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd cmd/server && go test -run TestAuth .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): add registration, activation, login and password reset"
```

---

### Task 5: Account endpoints

**Files:**
- Create: `cmd/server/account_handlers.go`
- Modify: `cmd/server/auth_routes.go`
- Test: `cmd/server/account_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestAccountProfileAndCSRF(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "gil@example.org", "Gil", pw)
	noCSRF := &client{cookie: c.cookie}
	expectStatus(t, f.do("PATCH", "/api/account", profileRequest{DisplayName: "Gilbert"}, as(noCSRF)), 403)
	expectStatus(t, f.do("PATCH", "/api/account", profileRequest{DisplayName: "Gilbert"}, as(c), header("Origin", "https://evil.example")), 403)
	w := f.do("PATCH", "/api/account", profileRequest{DisplayName: "Gilbert"}, as(c))
	expectStatus(t, w, 200)
	if decode[meResponse](t, w).DisplayName != "Gilbert" {
		t.Fatal("display name not changed")
	}
	expectStatus(t, f.do("PATCH", "/api/account", profileRequest{DisplayName: "G"}, as(c)), 400)
}

func TestAccountPasswordChangeKeepsCurrentSession(t *testing.T) {
	f := newAuthFixture(t)
	a := f.registerAndActivate(t, "hal@example.org", "Hal", pw)
	b := f.login(t, "hal@example.org", pw)
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: "wrong one!!", NewPassword: "new secret pass"}, as(a)), 403)
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: pw, NewPassword: "new secret pass"}, as(a)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(a)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(b)), 401)
}

func TestAccountEmailChange(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "ivy@example.org", "Ivy", pw)
	expectStatus(t, f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "ivy.new@example.org", CurrentPassword: pw}, as(c)), 200)
	sent := f.fake.Sent()
	confirm, notice := sent[len(sent)-2], sent[len(sent)-1]
	if confirm.To != "ivy.new@example.org" || notice.To != "ivy@example.org" || !strings.Contains(notice.Text, "ivy.new@example.org") {
		t.Fatalf("confirm=%+v notice=%+v", confirm, notice)
	}
	tok := tokenRE.FindStringSubmatch(confirm.Text)[1]
	expectStatus(t, f.do("POST", "/api/account/confirm-email", tokenRequest{Token: tok}), 200)
	f.login(t, "ivy.new@example.org", pw)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "ivy@example.org", Password: pw}), 401)
}

func TestAccountSessionsListAndRevoke(t *testing.T) {
	f := newAuthFixture(t)
	a := f.registerAndActivate(t, "jo@example.org", "Jo", pw)
	b := f.login(t, "jo@example.org", pw)
	w := f.do("GET", "/api/account/sessions", nil, as(a))
	expectStatus(t, w, 200)
	list := decode[[]sessionJSON](t, w)
	if len(list) != 2 {
		t.Fatalf("sessions = %+v", list)
	}
	var other int64
	for _, s := range list {
		if !s.Current {
			other = s.ID
		}
	}
	expectStatus(t, f.do("DELETE", fmt.Sprintf("/api/account/sessions/%d", other), nil, as(a)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(b)), 401)
	expectStatus(t, f.do("DELETE", "/api/account/sessions/999999", nil, as(a)), 404)
}

func TestAccountDelete(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	expectStatus(t, f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: pw}, as(boss)), 409) // last admin
	u := f.registerAndActivate(t, "kim@example.org", "Kim", pw)
	expectStatus(t, f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: "wrong one!!"}, as(u)), 403)
	w := f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: pw}, as(u))
	expectStatus(t, w, 200)
	if ck := w.Result().Cookies(); len(ck) == 0 || ck[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", ck)
	}
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "kim@example.org", Password: pw}), 401)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/server && go test -run TestAccount .`
Expected: FAIL (404s).

- [ ] **Step 3: Implement `account_handlers.go`**

```go
package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

// checkCurrentPassword writes 403 and returns false on a wrong password.
func checkCurrentPassword(w http.ResponseWriter, u *users.User, password string) bool {
	if ok, err := users.VerifyPassword(u.PasswordHash, password); err != nil || !ok {
		writeError(w, http.StatusForbidden, "current password is incorrect")
		return false
	}
	return true
}

func (s *Server) handleAccountPatch(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	var req profileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := users.ValidateDisplayName(req.DisplayName)
	if writeValidation(w, err) {
		return
	}
	if err := s.auth.st.SetDisplayName(u.ID, name); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	u.DisplayName = name
	writeJSON(w, meFrom(u, sess))
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	var req passwordChangeRequest
	if !decodeJSON(w, r, &req) || !checkCurrentPassword(w, u, req.CurrentPassword) {
		return
	}
	if writeValidation(w, users.ValidatePassword(req.NewPassword)) {
		return
	}
	hash, err := users.HashPassword(req.NewPassword)
	if err == nil {
		err = s.auth.st.SetPassword(u.ID, hash)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = s.auth.st.DeleteUserSessions(u.ID, sess.ID)
	s.auth.audit(idPtr(u.ID), "user.password.change", idPtr(u.ID), nil)
	writeJSON(w, okResponse{OK: true, Message: "Password changed. Your other devices were logged out."})
}

func (s *Server) handleAccountEmail(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	var req emailChangeRequest
	if !decodeJSON(w, r, &req) || !checkCurrentPassword(w, u, req.CurrentPassword) {
		return
	}
	newEmail, err := users.NormalizeEmail(req.NewEmail)
	if writeValidation(w, err) {
		return
	}
	if newEmail == u.Email {
		writeError(w, http.StatusBadRequest, "that is already your address")
		return
	}
	done := okResponse{OK: true, Message: "Check the new address for a confirmation link."}
	if _, err := a.st.GetByEmail(newEmail); err == nil {
		writeJSON(w, done) // taken: same answer, nothing sent (no enumeration)
		return
	}
	tok, err := a.st.IssueToken(u.ID, users.PurposeEmailChange, 24*time.Hour, newEmail)
	if err == nil {
		err = a.sendMail(r.Context(), u, "email-change", a.emailChangeConfirmMail(u, newEmail, tok))
	}
	if err != nil {
		_ = a.st.InvalidateTokens(u.ID, users.PurposeEmailChange)
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	_ = a.sendMail(r.Context(), u, "email-change-notice", a.emailChangeNoticeMail(u, newEmail))
	a.audit(idPtr(u.ID), "user.email.change.requested", idPtr(u.ID), nil)
	writeJSON(w, done)
}

// handleConfirmEmail needs no session: the link may be opened on any device.
func (s *Server) handleConfirmEmail(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	uid, newEmail, err := a.st.ConsumeToken(req.Token, users.PurposeEmailChange)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	if err := a.st.SetEmail(uid, newEmail); err != nil {
		if errors.Is(err, users.ErrEmailTaken) {
			writeError(w, http.StatusConflict, "that address is already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(uid), "user.email.change", idPtr(uid), nil)
	writeJSON(w, okResponse{OK: true, Message: "Your address was changed."})
}

func (s *Server) handleAccountSessions(w http.ResponseWriter, _ *http.Request, u *users.User, sess *users.Session) {
	list, err := s.auth.st.ListSessions(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]sessionJSON, 0, len(list))
	for _, x := range list {
		out = append(out, sessionToJSON(x, sess.ID))
	}
	writeJSON(w, out)
}

func (s *Server) handleAccountSessionDelete(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if err := s.auth.st.DeleteSession(u.ID, id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if id == sess.ID {
		s.auth.clearSessionCookie(w)
	}
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	var req passwordConfirmRequest
	if !decodeJSON(w, r, &req) || !checkCurrentPassword(w, u, req.CurrentPassword) {
		return
	}
	if u.Role == users.RoleAdmin {
		if n, err := a.st.CountActiveAdmins(); err != nil || n <= 1 {
			writeError(w, http.StatusConflict, "you are the last admin; promote someone else first")
			return
		}
	}
	if err := a.st.Delete(u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(u.ID), "user.delete.self", idPtr(u.ID), nil)
	a.clearSessionCookie(w)
	writeJSON(w, okResponse{OK: true, Message: "Your account was deleted."})
}
```

- [ ] **Step 4: Register the routes**

In `auth_routes.go`, replace `// Tasks 5–7 add routes here.` with:
```go
	r.HandleFunc("/api/account", s.withUser(s.handleAccountPatch)).Methods("PATCH")
	r.HandleFunc("/api/account", s.withUser(s.handleAccountDelete)).Methods("DELETE")
	r.HandleFunc("/api/account/password", s.withUser(s.handleAccountPassword)).Methods("POST")
	r.HandleFunc("/api/account/email", s.withUser(s.handleAccountEmail)).Methods("POST")
	r.HandleFunc("/api/account/confirm-email", s.requireOrigin(s.handleConfirmEmail)).Methods("POST")
	r.HandleFunc("/api/account/sessions", s.withUser(s.handleAccountSessions)).Methods("GET")
	r.HandleFunc("/api/account/sessions/{id}", s.withUser(s.handleAccountSessionDelete)).Methods("DELETE")
	// Tasks 6–7 add routes here.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd cmd/server && go test -run 'TestAccount|TestAuth' .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): add account endpoints for profile, password, address, sessions and deletion"
```

---

### Task 6: Admin user management

**Files:**
- Create: `cmd/server/admin_users_handlers.go`
- Modify: `cmd/server/auth_routes.go`
- Test: `cmd/server/admin_users_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"fmt"
	"testing"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// adminFixture: config admin "boss" plus regular user "uma".
func adminFixture(t *testing.T) (*authFixture, *client, *client) {
	f := newAuthFixture(t, "boss@example.org")
	return f, f.registerAndActivate(t, "boss@example.org", "Boss", pw), f.registerAndActivate(t, "uma@example.org", "Uma", pw)
}

func userPath(id int64, suffix string) string { return fmt.Sprintf("/api/admin/users/%d%s", id, suffix) }

func TestAdminUsersRequiresAdmin(t *testing.T) {
	f, _, uma := adminFixture(t)
	expectStatus(t, f.do("GET", "/api/admin/users", nil), 401)
	expectStatus(t, f.do("GET", "/api/admin/users", nil, as(uma)), 403)
}

func TestAdminListAndDetail(t *testing.T) {
	f, boss, uma := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "pending@example.org", DisplayName: "Pen", Password: pw})
	w := f.do("GET", "/api/admin/users?status=pending", nil, as(boss))
	expectStatus(t, w, 200)
	rows := decode[[]adminUserJSON](t, w)
	if len(rows) != 1 || rows[0].Email != "pending@example.org" || rows[0].LastMail == nil || rows[0].LastMail.Purpose != "activate" {
		t.Fatalf("pending rows = %+v", rows)
	}
	expectStatus(t, f.do("GET", "/api/admin/users?status=bogus", nil, as(boss)), 400)
	w = f.do("GET", userPath(uma.me.ID, ""), nil, as(boss))
	expectStatus(t, w, 200)
	d := decode[adminUserDetailJSON](t, w)
	if d.User.Email != "uma@example.org" || len(d.Sessions) != 1 || len(d.Mail) != 1 || len(d.Audit) == 0 {
		t.Fatalf("detail = %+v", d)
	}
	all := decode[[]adminUserJSON](t, f.do("GET", "/api/admin/users", nil, as(boss)))
	for _, r := range all {
		if r.Email == "boss@example.org" && !r.ConfigAdmin {
			t.Fatal("config admin not marked")
		}
	}
}

func TestAdminDisableEnable(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/disable"), nil, as(boss)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(uma)), 401)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "uma@example.org", Password: pw}), 401)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/disable"), nil, as(boss)), 409)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/enable"), nil, as(boss)), 200)
	f.login(t, "uma@example.org", pw)
}

func TestAdminGuards(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("POST", userPath(boss.me.ID, "/disable"), nil, as(boss)), 409) // self
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/role"), roleRequest{Role: users.RoleAdmin}, as(boss)), 200)
	umaAdmin := f.login(t, "uma@example.org", pw)
	expectStatus(t, f.do("POST", userPath(boss.me.ID, "/disable"), nil, as(umaAdmin)), 409)                          // config admin
	expectStatus(t, f.do("DELETE", userPath(boss.me.ID, ""), nil, as(umaAdmin)), 409)                                // config admin
	expectStatus(t, f.do("POST", userPath(boss.me.ID, "/role"), roleRequest{Role: users.RoleUser}, as(umaAdmin)), 409) // config admin
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/role"), roleRequest{Role: "root"}, as(boss)), 400)
	// uma may demote herself: boss remains.
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/role"), roleRequest{Role: users.RoleUser}, as(umaAdmin)), 200)
}

func TestAdminLastAdminCannotDemoteSelf(t *testing.T) {
	f := newAuthFixture(t)
	solo := f.registerAndActivate(t, "solo@example.org", "Solo", pw)
	f.st.SetRole(solo.me.ID, users.RoleAdmin) // UI-promoted, not a config admin
	expectStatus(t, f.do("POST", userPath(solo.me.ID, "/role"), roleRequest{Role: users.RoleUser}, as(solo)), 409)
}

func TestAdminManualActivate(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "late@example.org", DisplayName: "Late", Password: pw})
	link := f.lastToken(t)
	late, _ := f.st.GetByEmail("late@example.org")
	expectStatus(t, f.do("POST", userPath(late.ID, "/activate"), nil, as(boss)), 200)
	got, _ := f.st.GetByID(late.ID)
	if got.Status != users.StatusActive || got.ActivatedBy == nil || *got.ActivatedBy != boss.me.ID {
		t.Fatalf("after manual activate: %+v", got)
	}
	expectStatus(t, f.do("POST", "/api/auth/activate", tokenRequest{Token: link}), 410)
	expectStatus(t, f.do("POST", userPath(late.ID, "/activate"), nil, as(boss)), 409)
	entries, _ := f.st.AuditFor(late.ID, 10)
	found := false
	for _, e := range entries {
		found = found || e.Action == "user.activate.manual"
	}
	if !found {
		t.Fatal("no user.activate.manual audit row")
	}
	f.login(t, "late@example.org", pw)
}

func TestAdminResendActivation(t *testing.T) {
	f, boss, uma := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "re@example.org", DisplayName: "Re", Password: pw})
	old := f.lastToken(t)
	re, _ := f.st.GetByEmail("re@example.org")
	expectStatus(t, f.do("POST", userPath(re.ID, "/resend-activation"), nil, as(boss)), 200)
	fresh := f.lastToken(t)
	if fresh == old {
		t.Fatal("no new link sent")
	}
	expectStatus(t, f.do("POST", "/api/auth/activate", tokenRequest{Token: old}), 410)
	expectStatus(t, f.do("POST", "/api/auth/activate", tokenRequest{Token: fresh}), 200)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/resend-activation"), nil, as(boss)), 409)
}

func TestAdminDelete(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("DELETE", userPath(uma.me.ID, ""), nil, as(boss)), 200)
	expectStatus(t, f.do("GET", userPath(uma.me.ID, ""), nil, as(boss)), 404)
}

func TestAdminMailRefresh(t *testing.T) {
	f, boss, uma := adminFixture(t)
	mails, _ := f.st.MailForUser(uma.me.ID, 10)
	m := mails[0]
	f.fake.SetEvents(m.ProviderMessageID, []mailer.Event{
		{MessageID: m.ProviderMessageID, Event: mailer.EventHardBounce, Reason: "user unknown", At: m.SentAt.Add(60e9)},
	})
	w := f.do("POST", userPath(uma.me.ID, fmt.Sprintf("/mail/%d/refresh", m.ID)), nil, as(boss))
	expectStatus(t, w, 200)
	if got := decode[mailJSON](t, w); got.LastEvent != mailer.EventHardBounce || got.LastReason != "user unknown" {
		t.Fatalf("refreshed = %+v", got)
	}
	if u, _ := f.st.GetByID(uma.me.ID); !u.EmailBouncing {
		t.Fatal("hard bounce did not flag the address")
	}
	expectStatus(t, f.do("POST", userPath(boss.me.ID, fmt.Sprintf("/mail/%d/refresh", m.ID)), nil, as(boss)), 404) // wrong owner
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/server && go test -run TestAdmin .`
Expected: FAIL (404s).

- [ ] **Step 3: Implement `admin_users_handlers.go`**

```go
package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

// adminTarget loads {id} from the path; writes 400/404 and returns nil on failure.
func (s *Server) adminTarget(w http.ResponseWriter, r *http.Request) *users.User {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return nil
	}
	u, err := s.auth.st.GetByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return nil
	}
	return u
}

// guardRemoval blocks disabling/deleting/demoting yourself (except a
// non-last-admin self-demotion, allowed via allowSelf), config admins, and
// the last active admin. Writes 409 and returns false when blocked.
func (s *Server) guardRemoval(w http.ResponseWriter, actor, target *users.User, allowSelf bool) bool {
	if target.ID == actor.ID && !allowSelf {
		writeError(w, http.StatusConflict, "use My account to change your own account")
		return false
	}
	if s.auth.isConfigAdmin(target.Email) {
		writeError(w, http.StatusConflict, "this admin is listed in adminEmails; remove them from the config first")
		return false
	}
	if target.Role == users.RoleAdmin && target.Status == users.StatusActive {
		if n, err := s.auth.st.CountActiveAdmins(); err != nil || n <= 1 {
			writeError(w, http.StatusConflict, "this is the last admin; promote someone else first")
			return false
		}
	}
	return true
}

func (s *Server) writeAdminRow(w http.ResponseWriter, id int64) {
	u, err := s.auth.st.GetByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	last, _ := s.auth.st.LatestMailByUser()
	writeJSON(w, s.auth.adminRow(*u, last))
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	q := r.URL.Query()
	f := users.ListFilter{Status: users.Status(q.Get("status")), Role: users.Role(q.Get("role")), Query: q.Get("q")}
	switch f.Status {
	case "", users.StatusPending, users.StatusActive, users.StatusDisabled:
	default:
		writeError(w, http.StatusBadRequest, "invalid status filter")
		return
	}
	if f.Role != "" && !f.Role.Valid() {
		writeError(w, http.StatusBadRequest, "invalid role filter")
		return
	}
	list, err := s.auth.st.List(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	last, _ := s.auth.st.LatestMailByUser()
	out := make([]adminUserJSON, 0, len(list))
	for _, u := range list {
		out = append(out, s.auth.adminRow(u, last))
	}
	writeJSON(w, out)
}

func (s *Server) handleAdminUserDetail(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	u := s.adminTarget(w, r)
	if u == nil {
		return
	}
	a := s.auth
	last, _ := a.st.LatestMailByUser()
	d := adminUserDetailJSON{User: a.adminRow(*u, last), Sessions: []sessionJSON{}, Mail: []mailJSON{}, Audit: []auditJSON{}}
	if list, err := a.st.ListSessions(u.ID); err == nil {
		for _, x := range list {
			d.Sessions = append(d.Sessions, sessionToJSON(x, 0))
		}
	}
	if mails, err := a.st.MailForUser(u.ID, 50); err == nil {
		for _, m := range mails {
			d.Mail = append(d.Mail, mailToJSON(m))
		}
	}
	if entries, err := a.st.AuditFor(u.ID, 100); err == nil {
		for _, e := range entries {
			d.Audit = append(d.Audit, auditJSON{ID: e.ID, At: rfc3339(e.At), ActorUserID: e.ActorUserID,
				Action: e.Action, TargetUserID: e.TargetUserID, Detail: e.Detail})
		}
	}
	writeJSON(w, d)
}

func (s *Server) handleAdminDisable(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil || !s.guardRemoval(w, actor, t, false) {
		return
	}
	if t.Status == users.StatusDisabled {
		writeError(w, http.StatusConflict, "already disabled")
		return
	}
	if err := s.auth.st.SetStatus(t.ID, users.StatusDisabled); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = s.auth.st.DeleteUserSessions(t.ID, 0)
	s.auth.audit(idPtr(actor.ID), "user.disable", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminEnable(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if t.Status != users.StatusDisabled {
		writeError(w, http.StatusConflict, "only disabled accounts can be enabled")
		return
	}
	if err := s.auth.st.SetStatus(t.ID, users.StatusActive); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auth.audit(idPtr(actor.ID), "user.enable", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil || !s.guardRemoval(w, actor, t, false) {
		return
	}
	if err := s.auth.st.Delete(t.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auth.audit(idPtr(actor.ID), "user.delete", idPtr(t.ID), nil)
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleAdminRole(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	var req roleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	if req.Role == t.Role {
		s.writeAdminRow(w, t.ID)
		return
	}
	if req.Role == users.RoleUser && !s.guardRemoval(w, actor, t, true) {
		return
	}
	if err := s.auth.st.SetRole(t.ID, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auth.audit(idPtr(actor.ID), "user.role", idPtr(t.ID), map[string]string{"role": string(req.Role)})
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminResendActivation(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if t.Status != users.StatusPending {
		writeError(w, http.StatusConflict, "only pending accounts need activation")
		return
	}
	tok, err := a.st.IssueToken(t.ID, users.PurposeActivate, 48*time.Hour, "")
	if err == nil {
		err = a.sendMail(r.Context(), t, "activate", a.activationMail(t, tok))
	}
	if err != nil {
		_ = a.st.InvalidateTokens(t.ID, users.PurposeActivate)
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	a.audit(idPtr(actor.ID), "user.activation.resend", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

// handleAdminActivate activates a pending user without the mail link, for
// when mail keeps failing. The address stays unverified; activated_by and
// the audit row keep that visible.
func (s *Server) handleAdminActivate(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if t.Status != users.StatusPending {
		writeError(w, http.StatusConflict, "only pending accounts can be activated")
		return
	}
	_ = a.st.InvalidateTokens(t.ID, users.PurposeActivate)
	if err := a.st.Activate(t.ID, a.roleFor(t.Email), idPtr(actor.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(actor.ID), "user.activate.manual", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminMailRefresh(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	a := s.auth
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	mailID, err := strconv.ParseInt(mux.Vars(r)["mailId"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid mail id")
		return
	}
	rec, err := a.st.MailByID(mailID)
	if err != nil || rec.UserID == nil || *rec.UserID != t.ID {
		writeError(w, http.StatusNotFound, "mail not found")
		return
	}
	if rec.ProviderMessageID == "" {
		writeError(w, http.StatusConflict, "this mail has no provider message id")
		return
	}
	evs, err := a.mail.Events(r.Context(), rec.ProviderMessageID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mail provider: "+err.Error())
		return
	}
	a.ingestMailEvents(evs)
	if rec, err = a.st.MailByID(mailID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, mailToJSON(*rec))
}
```

- [ ] **Step 4: Register the routes**

In `auth_routes.go`, replace `// Tasks 6–7 add routes here.` with:
```go
	r.HandleFunc("/api/admin/users", s.withAdmin(s.handleAdminUsers)).Methods("GET")
	r.HandleFunc("/api/admin/users/{id}", s.withAdmin(s.handleAdminUserDetail)).Methods("GET")
	r.HandleFunc("/api/admin/users/{id}", s.withAdmin(s.handleAdminDelete)).Methods("DELETE")
	r.HandleFunc("/api/admin/users/{id}/disable", s.withAdmin(s.handleAdminDisable)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/enable", s.withAdmin(s.handleAdminEnable)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/role", s.withAdmin(s.handleAdminRole)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/resend-activation", s.withAdmin(s.handleAdminResendActivation)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/activate", s.withAdmin(s.handleAdminActivate)).Methods("POST")
	r.HandleFunc("/api/admin/users/{id}/mail/{mailId}/refresh", s.withAdmin(s.handleAdminMailRefresh)).Methods("POST")
	// Task 7 adds the webhook route here.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd cmd/server && go test -run 'TestAdmin|TestAccount|TestAuth' .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): add admin user management, manual activation and mail status refresh"
```

---

### Task 7: Brevo webhook

**Files:**
- Create: `cmd/server/mail_webhook.go`
- Modify: `cmd/server/auth_routes.go`
- Test: `cmd/server/mail_webhook_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestBrevoWebhookAuthAndIngest(t *testing.T) {
	f, _, uma := adminFixture(t)
	mails, _ := f.st.MailForUser(uma.me.ID, 10)
	m := mails[0]
	body := fmt.Sprintf(`{"event":"hard_bounce","email":"uma@example.org","message-id":%q,"ts_event":%d,"reason":"mailbox unavailable"}`,
		m.ProviderMessageID, m.SentAt.Unix()+30)
	post := func(auth, b string) int {
		req := httptest.NewRequest("POST", "/api/mail/brevo/webhook", bytes.NewBufferString(b))
		req.RemoteAddr = "203.0.113.50:443"
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w.Code
	}
	if c := post("", body); c != 401 {
		t.Fatalf("no auth = %d", c)
	}
	if c := post("Bearer wrong-secret-xxxxxxxx", body); c != 401 {
		t.Fatalf("wrong auth = %d", c)
	}
	if c := post("Bearer "+testHook, "garbage"); c != 200 { // authenticated junk: 200 so Brevo stops retrying
		t.Fatalf("garbage = %d", c)
	}
	if c := post("Bearer "+testHook, body); c != 200 {
		t.Fatalf("valid = %d", c)
	}
	rec, _ := f.st.MailByID(m.ID)
	if rec.LastEvent != "hard_bounce" || rec.LastReason != "mailbox unavailable" {
		t.Fatalf("mail record = %+v", rec)
	}
	if u, _ := f.st.GetByID(uma.me.ID); !u.EmailBouncing {
		t.Fatal("bounce flag not set")
	}
	unknown := `{"event":"delivered","message-id":"<nope@x>","ts_event":1}`
	if c := post("Bearer "+testHook, unknown); c != 200 {
		t.Fatalf("unknown id = %d (must be 200 so Brevo stops retrying)", c)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd cmd/server && go test -run BrevoWebhook .`
Expected: FAIL (404).

- [ ] **Step 3: Implement `mail_webhook.go`**

```go
package main

import (
	"io"
	"log"
	"net/http"

	"github.com/meshcore-analyzer/mailer"
)

// handleBrevoWebhook ingests Brevo transactional events. Brevo is configured
// with auth {"type":"bearer","token":<webhookSecret>}, so it sends
// "Authorization: Bearer <secret>". Unknown message ids get 200 so Brevo
// does not retry forever.
func (s *Server) handleBrevoWebhook(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	if !a.allow(w, r, a.hook) {
		return
	}
	if !constantTimeEqual(r.Header.Get("Authorization"), "Bearer "+a.set.webhookSecret) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "body too large")
		return
	}
	evs, err := mailer.ParseBrevoWebhook(body)
	if err != nil {
		// Authenticated but unusable (e.g. an event type without message-id):
		// answer 200 so Brevo does not retry forever; log for the operator.
		log.Printf("[users] brevo webhook: ignored payload: %v", err)
		writeJSON(w, okResponse{OK: true})
		return
	}
	a.ingestMailEvents(evs)
	writeJSON(w, okResponse{OK: true})
}

```

- [ ] **Step 4: Register the route**

In `auth_routes.go`, replace `// Task 7 adds the webhook route here.` with:
```go
	// The webhook exists only when a secret is configured.
	if s.auth.set.webhookSecret != "" {
		r.HandleFunc("/api/mail/brevo/webhook", s.handleBrevoWebhook).Methods("POST")
	}
```

- [ ] **Step 5: Run the tests**

Run: `cd cmd/server && go test -run 'BrevoWebhook|TestAdmin' .`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): ingest Brevo delivery events through a bearer-authenticated webhook"
```

---

### Task 8: `requireAdmin` on the existing operator endpoints

**Files:**
- Modify: `cmd/server/routes.go` (the definition next to `requireAPIKey`, plus 7 call sites)
- Test: `cmd/server/require_admin_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAdminKeyOrAdminSession(t *testing.T) {
	f, boss, uma := adminFixture(t)
	p := "/api/test/admin-only"
	f.router.Handle(p, f.srv.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, okResponse{OK: true})
	}))).Methods("GET", "POST")

	expectStatus(t, f.do("GET", p, nil), 401) // no credentials: the API-key gate answers
	expectStatus(t, f.do("GET", p, nil, header("X-API-Key", testAPIKey)), 200)
	expectStatus(t, f.do("GET", p, nil, as(uma)), 403)                           // user session
	expectStatus(t, f.do("GET", p, nil, as(boss)), 200)                          // admin session, safe method
	expectStatus(t, f.do("POST", p, nil, as(&client{cookie: boss.cookie})), 403) // no CSRF token
	expectStatus(t, f.do("POST", p, nil, as(boss)), 200)
	// A request carrying X-API-Key is judged on the key alone.
	expectStatus(t, f.do("GET", p, nil, as(boss), header("X-API-Key", "wrong-key-wrong-key")), 401)
}

func TestPerfResetAcceptsAdminSession(t *testing.T) {
	srv, router := setupTestServerWithAPIKey(t, testAPIKey)
	f := newAuthFixture(t, "boss@example.org")
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	srv.auth = f.srv.auth // requireAdmin reads s.auth per request
	req := httptest.NewRequest("POST", "/api/perf/reset", nil)
	req.Header.Set("Origin", testBase)
	req.AddCookie(boss.cookie)
	req.Header.Set(csrfHeader, boss.csrf)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	expectStatus(t, w, http.StatusOK)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/server && go test -run 'RequireAdmin|PerfResetAcceptsAdmin' .`
Expected: FAIL (`undefined: requireAdmin`).

- [ ] **Step 3: Implement `requireAdmin`**

In `cmd/server/routes.go`, directly after the closing brace of `requireAPIKey`, add:
```go
// requireAdmin gates operator endpoints. It accepts a strong X-API-Key
// (exactly requireAPIKey's rules) or, when user management is on, the
// session of an admin; a cookie-authenticated unsafe method must also pass
// the CSRF check. A request that sends X-API-Key is judged on the key alone.
// With user management off this is requireAPIKey.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	keyGate := s.requireAPIKey(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil && r.Header.Get("X-API-Key") == "" {
			if u, sess := s.auth.currentUser(w, r); u != nil {
				if u.Role != users.RoleAdmin {
					writeError(w, http.StatusForbidden, "admin role required")
					return
				}
				if !isSafeMethod(r.Method) && !s.auth.csrfOK(r, sess) {
					writeError(w, http.StatusForbidden, "CSRF check failed")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
		}
		keyGate.ServeHTTP(w, r)
	})
}
```
Add `"github.com/meshcore-analyzer/users"` to `routes.go`'s imports.

- [ ] **Step 4: Switch the call sites**

Run: `cd cmd/server && sed -i 's/s\.requireAPIKey(http\.HandlerFunc(/s.requireAdmin(http.HandlerFunc(/g' routes.go`
Then: `grep -n 'requireAPIKey(\|requireAdmin(' routes.go`
Expected: 7 `requireAdmin(http.HandlerFunc(` call sites (geo-filter PUT, perf/reset,
prune-geo-filter, prune-geo-filter/status, debug/affinity, dropped-packets, backup), plus
the two definitions. `requireAPIKey(next)` remains, used inside `requireAdmin`.

- [ ] **Step 5: Run the new tests and the existing API-key tests**

Run: `cd cmd/server && go test -run 'RequireAdmin|PerfResetAcceptsAdmin|APIKey|ApiKey|WriteEndpoints' .`
Expected: PASS. The existing `TestWriteEndpointsRequireAPIKey` and
`apikey_security_test.go` pass unchanged, because `auth == nil` there.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): accept an admin session on API-key endpoints"
```

---

### Task 9: Client config flag, startup wiring, "off is unchanged", invariant

**Files:**
- Modify: `cmd/server/types.go`, `cmd/server/routes.go` (`handleConfigClient`), `cmd/server/main.go`,
  `cmd/server/readonly_invariant_test.go`
- Test: `cmd/server/user_mgmt_off_test.go`

- [ ] **Step 1: Write the failing tests**

`cmd/server/user_mgmt_off_test.go`:
```go
package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserManagementOffIsUnchanged(t *testing.T) {
	srv, router := setupTestServer(t)
	if srv.auth != nil {
		t.Fatal("auth built without config")
	}
	for _, p := range []string{"/api/auth/me", "/api/admin/users", "/api/account/sessions"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Errorf("%s = %d with the feature off; want 404", p, w.Code)
		}
	}
	base := httptest.NewRecorder()
	router.ServeHTTP(base, httptest.NewRequest("GET", "/api/config/client", nil))
	if strings.Contains(base.Body.String(), "userManagement") {
		t.Fatal("client config mentions userManagement while off")
	}
	// An explicit {"enabled": false} block is byte-identical to no block.
	srv.cfg.UserManagement = &UserManagementConfig{Enabled: false}
	if err := srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db")); err != nil || srv.auth != nil {
		t.Fatalf("initUserManagement off: auth=%v err=%v", srv.auth, err)
	}
	off := httptest.NewRecorder()
	router.ServeHTTP(off, httptest.NewRequest("GET", "/api/config/client", nil))
	if off.Body.String() != base.Body.String() {
		t.Fatal("client config differs between absent and disabled block")
	}
}

func TestClientConfigAdvertisesUserManagement(t *testing.T) {
	srv, router := setupTestServer(t)
	a, _ := newTestAuthService(t)
	srv.auth = a
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
	if !strings.Contains(w.Body.String(), `"userManagement":{"enabled":true}`) {
		t.Fatalf("client config = %s", w.Body.String())
	}
}

func TestInitUserManagementRefusesMeasurementDB(t *testing.T) {
	dir := t.TempDir()
	measurement := filepath.Join(dir, "meshcore.db")
	os.WriteFile(measurement, nil, 0o644)
	srv := &Server{cfg: &Config{UserManagement: &UserManagementConfig{
		Enabled: true, DBPath: measurement, PublicBaseURL: testBase,
		Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"},
	}}}
	err := srv.initUserManagement(measurement)
	if err == nil || !strings.Contains(err.Error(), "measurement database") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitUserManagementCreatesUsersDB(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{cfg: &Config{UserManagement: &UserManagementConfig{
		Enabled: true, PublicBaseURL: testBase,
		Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"},
	}}}
	if err := srv.initUserManagement(filepath.Join(dir, "meshcore.db")); err != nil {
		t.Fatal(err)
	}
	defer srv.closeUserManagement()
	if _, err := os.Stat(filepath.Join(dir, "users.db")); err != nil {
		t.Fatalf("users.db not created next to the measurement DB: %v", err)
	}
}
```
Append to `cmd/server/readonly_invariant_test.go`:
```go
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
```
(`os`, `regexp` and `strings` are already imported by that file. Check, and add any that
are missing.)

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/server && go test -run 'UserManagementOff|ClientConfigAdvertises|InitUserManagement|UsersOpenIsTheOnly' .`
Expected: `TestClientConfigAdvertisesUserManagement` FAILS (no field yet). The others may
already pass; that is fine.

- [ ] **Step 3: Add the client-config field**

In `cmd/server/types.go`, in `type ClientConfigResponse struct`, after `PathTrust`:
```go
	// Present only when user management is on, so the payload is unchanged
	// for every instance that leaves it off.
	UserManagement *ClientUserManagement `json:"userManagement,omitempty"`
```
and after the `CustomizerClientConfig` type:
```go
// ClientUserManagement tells the frontend that accounts exist.
type ClientUserManagement struct {
	Enabled bool `json:"enabled"`
}
```
In `routes.go` `handleConfigClient`, inside the `ClientConfigResponse{…}` literal after
`PathTrust:           &pathTrust,`, add:
```go
		UserManagement:      s.clientUserManagement(),
```
and add this method next to `handleConfigClient`:
```go
func (s *Server) clientUserManagement() *ClientUserManagement {
	if s.auth == nil {
		return nil
	}
	return &ClientUserManagement{Enabled: true}
}
```

- [ ] **Step 4: Wire startup and shutdown in `main.go`**

After `srv.store = store` (just before `router := mux.NewRouter()`), add:
```go
	// Optional user management (off by default). Fails startup on a bad
	// config rather than running with registration that cannot work.
	if err := srv.initUserManagement(resolvedDB); err != nil {
		log.Fatalf("[users] %v", err)
	}
```
In the shutdown goroutine, after `hub.Close()`, add:
```go
		// 3b. Close users.db (user management, opt-in; no-op when off).
		srv.closeUserManagement()
```

- [ ] **Step 5: Run the whole server suite**

Run: `cd cmd/server && go vet . && go test -race ./...`
Expected: PASS, including `TestServerSourceHasNoCachedRWCalls` and
`TestUsersOpenIsTheOnlyServerWritePath`.

- [ ] **Step 6: Commit**

```bash
git add cmd/server
git commit -m "feat(server): advertise user management to the client and wire startup and shutdown"
```

---

### Task 10: OpenAPI entries

**Files:**
- Modify: `cmd/server/openapi.go`

- [ ] **Step 1: Run the completeness gate to see it fail**

Run: `cd cmd/server && go test -run OpenAPI .`
Expected: FAIL. `openapi_completeness_test.go` lists the new `/api/auth/*`,
`/api/account*`, `/api/admin/users*` and `/api/mail/brevo/webhook` routes as missing.

- [ ] **Step 2: Add `Session` to `routeMeta` and the security mapping**

In `type routeMeta struct`, after `Auth`:
```go
	// Session marks routes that accept the user-management session cookie
	// (optional feature). With Auth too, either credential works.
	Session bool `json:"session,omitempty"`
```
Replace the `if meta.Auth { … }` block in `buildOpenAPISpec` with:
```go
			var security []map[string]interface{}
			if meta.Auth {
				security = append(security, map[string]interface{}{"ApiKeyAuth": []string{}})
			}
			if meta.Session {
				security = append(security, map[string]interface{}{"CookieAuth": []string{}})
			}
			if len(security) > 0 {
				op["security"] = security
			}
```
In `components.securitySchemes`, next to `ApiKeyAuth`, add:
```go
				"CookieAuth": map[string]interface{}{
					"type":        "apiKey",
					"in":          "cookie",
					"name":        "cs_session",
					"description": "User-management session (only when userManagement.enabled). Unsafe methods also need the X-CS-CSRF header from GET /api/auth/me.",
				},
```

- [ ] **Step 3: Mark the existing operator routes and add the new ones**

On **every** existing `routeDescriptions` entry that has `Auth: true`, add
`Session: true`. Those are exactly the `requireAdmin` routes. Find them with
`grep -n 'Auth: true' openapi.go`.

In `routeDescriptions()`, add a block:
```go
		// User management (optional; routes exist only when userManagement.enabled)
		"POST /api/auth/register":    {Summary: "Register an account", Description: "Creates a pending account and mails an activation link. The response is identical whether or not the address is already registered.", Tag: "users"},
		"POST /api/auth/activate":    {Summary: "Activate an account", Description: "Consumes the mailed activation token, activates the account and starts a session.", Tag: "users"},
		"POST /api/auth/login":       {Summary: "Log in", Description: "Email + password. Sets the cs_session cookie. Rate-limited per IP and per address.", Tag: "users"},
		"POST /api/auth/logout":      {Summary: "Log out", Tag: "users"},
		"GET /api/auth/me":           {Summary: "Current user", Description: "Returns the logged-in user and the CSRF token, or 401.", Tag: "users", Session: true},
		"POST /api/auth/forgot":      {Summary: "Request a password reset", Tag: "users"},
		"POST /api/auth/reset":       {Summary: "Reset the password", Description: "Consumes the mailed reset token and ends all sessions of the user.", Tag: "users"},
		"PATCH /api/account":         {Summary: "Update profile", Tag: "users", Session: true},
		"DELETE /api/account":        {Summary: "Delete own account", Tag: "users", Session: true},
		"POST /api/account/password": {Summary: "Change password", Tag: "users", Session: true},
		"POST /api/account/email":    {Summary: "Request an address change", Tag: "users", Session: true},
		"POST /api/account/confirm-email":   {Summary: "Confirm an address change", Tag: "users"},
		"GET /api/account/sessions":         {Summary: "List own sessions", Tag: "users", Session: true},
		"DELETE /api/account/sessions/{id}": {Summary: "Revoke one own session", Tag: "users", Session: true},
		"GET /api/admin/users":              {Summary: "List users (admin)", Tag: "users", Session: true, QueryParams: []paramMeta{{Name: "status", Description: "pending | active | disabled", Type: "string"}, {Name: "role", Description: "user | admin", Type: "string"}, {Name: "q", Description: "Substring of email or display name", Type: "string"}}},
		"GET /api/admin/users/{id}":         {Summary: "User detail with sessions, mail log and audit (admin)", Tag: "users", Session: true},
		"DELETE /api/admin/users/{id}":      {Summary: "Delete a user (admin)", Tag: "users", Session: true},
		"POST /api/admin/users/{id}/disable":           {Summary: "Disable a user (admin)", Tag: "users", Session: true},
		"POST /api/admin/users/{id}/enable":            {Summary: "Enable a user (admin)", Tag: "users", Session: true},
		"POST /api/admin/users/{id}/role":              {Summary: "Change a user's role (admin)", Tag: "users", Session: true},
		"POST /api/admin/users/{id}/resend-activation": {Summary: "Resend the activation mail (admin)", Tag: "users", Session: true},
		"POST /api/admin/users/{id}/activate":          {Summary: "Activate a pending user manually (admin)", Description: "For when mail keeps failing. The address stays unverified; recorded as activatedBy + audit row.", Tag: "users", Session: true},
		"POST /api/admin/users/{id}/mail/{mailId}/refresh": {Summary: "Pull delivery events for one mail from the provider (admin)", Tag: "users", Session: true},
		"POST /api/mail/brevo/webhook": {Summary: "Brevo delivery-event webhook", Description: "Authenticated with Authorization: Bearer <userManagement.mail.webhookSecret>. Registered only when the secret is set.", Tag: "users"},
```
If `tagDescriptions` exists, add `"users": "Optional user management (accounts, sessions, admin)."`.

- [ ] **Step 4: Run the gate and the suite**

Run: `cd cmd/server && gofmt -l . ; go test -run 'OpenAPI|Openapi' . && go test ./...`
Expected: `gofmt -l` prints nothing (run `gofmt -w openapi.go` if it does), and all tests
pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/openapi.go
git commit -m "docs(openapi): document the user-management routes and the session scheme"
```

---

## A2 done when

- `cd cmd/server && go vet ./... && go test -race ./...` passes.
- `bash scripts/check-dockerfile-internal-pkgs.sh` passes.
- With no `userManagement` block, `git diff upstream/master -- cmd/server` shows only
  additive code: no changed behavior in existing handlers except
  `requireAPIKey` → `requireAdmin` at the 7 call sites.
