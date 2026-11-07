package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	olPK  = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	olPK2 = "1111111111111111111111111111111111111111111111111111111111111111"
)

func olHash(pw string) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

func olTopic(pk string) string { return "meshcore/BRU/" + pk + "/packets" }

// olIndex builds an index without a store: logins for users 1 and 2, user 3
// revoked.
func olIndex() *observerLoginIndex {
	ix := &observerLoginIndex{byUsername: map[string]indexedLogin{}, byUser: map[int64]string{}, owners: map[string]*int64{}, owned: map[int64]int{}, confirmed: map[string]bool{}, shared: map[string]bool{}}
	ix.setLogin(users.MQTTLogin{UserID: 1, Username: "obs_alice0000000", PasswordHash: olHash("pw-alice")})
	ix.setLogin(users.MQTTLogin{UserID: 2, Username: "obs_bob000000000", PasswordHash: olHash("pw-bob")})
	now := time.Now()
	ix.setLogin(users.MQTTLogin{UserID: 3, Username: "obs_carol0000000", PasswordHash: olHash("pw-carol"), RevokedAt: &now})
	return ix
}

func i64(v int64) *int64 { return &v }

type bindRecorder struct {
	calls []string
	owner *int64
	err   error
}

func (b *bindRecorder) fn(pk string, uid int64) (*int64, bool, error) {
	b.calls = append(b.calls, fmt.Sprintf("%s/%d", pk, uid))
	if b.err != nil {
		return nil, false, b.err
	}
	return b.owner, b.owner != nil && *b.owner == uid, nil
}

func noBind(t *testing.T) func(string, int64) (*int64, bool, error) {
	return func(pk string, uid int64) (*int64, bool, error) {
		t.Fatalf("bind called for %s/%d", pk, uid)
		return nil, false, nil
	}
}

// confirmNoop is a store that never confirms: the index keeps the binding
// unconfirmed, as before confirmed_at existed.
func confirmNoop(string, int64) (bool, error) { return false, nil }

func noConfirm(t *testing.T) func(string, int64) (bool, error) {
	return func(pk string, uid int64) (bool, error) {
		t.Fatalf("confirm called for %s/%d", pk, uid)
		return false, nil
	}
}

type confirmRecorder struct {
	calls []string
	ok    bool
	err   error
}

func (c *confirmRecorder) fn(pk string, uid int64) (bool, error) {
	c.calls = append(c.calls, fmt.Sprintf("%s/%d", pk, uid))
	return c.ok, c.err
}

const olShared = "meshcore_shared"

// olSharedIndex is olIndex with olShared as the shared login.
func olSharedIndex() *observerLoginIndex {
	ix := olIndex()
	ix.shared[olShared] = true
	return ix
}

func TestAuthorizeSharedLoginDeniedOnConfirmed(t *testing.T) {
	ix := olSharedIndex()
	ix.setOwner(olPK, i64(1))
	ix.confirm(olPK, 1)
	if res, pk, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookDeny || pk != olPK {
		t.Fatalf("confirmed: res = %s pk = %q", res, pk)
	}
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(strings.ToLower(olPK)), noBind(t), noConfirm(t)); res != hookDeny {
		t.Fatalf("confirmed, lowercase topic: res = %s", res)
	}
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK2), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("unbound: res = %s", res)
	}
	// Any topic the ingestor would attribute to the confirmed pubkey is
	// denied, whatever its segment count.
	for _, topic := range []string{
		"meshcore/BRU/" + olPK,
		"meshcore/BRU/" + olPK + "/",
		"meshcore/BRU/" + olPK + "/neighbors/x",
		"meshcore/BRU/" + olPK + "/neighbors/",
		"meshcore/BRU/" + strings.ToLower(olPK) + "/neighbors/x",
		"meshcore//" + olPK + "/packets",
		"meshcore/client/" + olPK + "/packets",
	} {
		if res, pk, _ := ix.authorize(olShared, "publish", topic, noBind(t), noConfirm(t)); res != hookDeny || pk != olPK {
			t.Fatalf("confirmed %q: res = %s pk = %q", topic, res, pk)
		}
	}
	for _, topic := range []string{"meshcore/#", "meshcore/BRU", "meshcore/BRU/", "other/BRU/" + olPK + "/packets", "meshcore/BRU/" + olPK2 + "/neighbors/x"} {
		if res, _, _ := ix.authorize(olShared, "publish", topic, noBind(t), noConfirm(t)); res != hookIgnore {
			t.Fatalf("malformed or unconfirmed %q: res = %s", topic, res)
		}
	}
	for _, action := range []string{"subscribe", ""} {
		if res, _, _ := ix.authorize(olShared, action, olTopic(olPK), noBind(t), noConfirm(t)); res != hookIgnore {
			t.Fatalf("action %q: res = %s", action, res)
		}
	}
	// A login not in shared stays ignored on a confirmed pubkey.
	if res, _, _ := ix.authorize("other_shared", "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("unlisted login: res = %s", res)
	}
}

func TestAuthorizeSharedLoginIgnoredOnUnconfirmed(t *testing.T) {
	ix := olSharedIndex()
	ix.setOwner(olPK, i64(1))
	ix.setOwner(olPK2, nil)
	for _, pk := range []string{olPK, olPK2} {
		for _, topic := range []string{olTopic(pk), "meshcore/BRU/" + pk, "meshcore/BRU/" + pk + "/neighbors/x", "meshcore/client/" + pk + "/packets"} {
			if res, _, _ := ix.authorize(olShared, "publish", topic, noBind(t), noConfirm(t)); res != hookIgnore {
				t.Fatalf("%q: res = %s", topic, res)
			}
		}
	}
}

func TestAuthorizeOwnerPublishConfirms(t *testing.T) {
	ix := olSharedIndex()
	ix.setOwner(olPK, i64(1))
	c := &confirmRecorder{ok: true}
	if res, _, _ := ix.authorize("obs_alice0000000", "publish", olTopic(olPK), noBind(t), c.fn); res != hookAllow {
		t.Fatalf("owner publish: res = %s", res)
	}
	if len(c.calls) != 1 || c.calls[0] != olPK+"/1" {
		t.Fatalf("confirm calls %v", c.calls)
	}
	// Confirmed in the index: no second store write.
	if res, _, _ := ix.authorize("obs_alice0000000", "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookAllow {
		t.Fatalf("second owner publish: res = %s", res)
	}
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookDeny {
		t.Fatalf("shared after confirm: res = %s", res)
	}
}

func TestAuthorizeOwnerPublishConfirmFalseOrError(t *testing.T) {
	for name, c := range map[string]*confirmRecorder{
		"false": {ok: false},
		"error": {err: errors.New("database is locked")},
	} {
		t.Run(name, func(t *testing.T) {
			ix := olSharedIndex()
			ix.setOwner(olPK, i64(1))
			if res, _, _ := ix.authorize("obs_alice0000000", "publish", olTopic(olPK), noBind(t), c.fn); res != hookAllow {
				t.Fatalf("owner publish: res = %s", res)
			}
			if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookIgnore {
				t.Fatalf("shared: res = %s", res)
			}
			// Still unconfirmed, so the next owner publish tries again.
			ix.authorize("obs_alice0000000", "publish", olTopic(olPK), noBind(t), c.fn)
			if len(c.calls) != 2 {
				t.Fatalf("confirm calls %v", c.calls)
			}
		})
	}
}

func TestAuthorizeNonOwnerNeverConfirms(t *testing.T) {
	ix := olSharedIndex()
	ix.setOwner(olPK, i64(2))
	ix.setOwner(olPK2, nil)
	for _, pk := range []string{olPK, olPK2} {
		if res, _, _ := ix.authorize("obs_alice0000000", "publish", olTopic(pk), noBind(t), noConfirm(t)); res != hookDeny {
			t.Fatalf("%s: res = %s", pk[:8], res)
		}
	}
	ix.setOwner(olPK, i64(3))
	if res, _, _ := ix.authorize("obs_carol0000000", "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookDeny {
		t.Fatalf("revoked owner: res = %s", res)
	}
}

func TestAuthorizeAutoBindIsConfirmed(t *testing.T) {
	ix := olSharedIndex()
	b := &bindRecorder{owner: i64(1)}
	if res, _, created := ix.authorize("obs_alice0000000", "publish", olTopic(olPK), b.fn, noConfirm(t)); res != hookAllow || !created {
		t.Fatalf("auto-bind: res = %s created = %v", res, created)
	}
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookDeny {
		t.Fatalf("shared after auto-bind: res = %s", res)
	}
	// The store gave the key to someone else: nothing is confirmed.
	b2 := &bindRecorder{owner: i64(2)}
	if res, _, _ := ix.authorize("obs_alice0000000", "publish", olTopic(olPK2), b2.fn, noConfirm(t)); res != hookDeny {
		t.Fatalf("lost race: res = %s", res)
	}
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK2), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("shared on other owner's unconfirmed key: res = %s", res)
	}
}

func TestAuthorizeSharedLoginAfterReassign(t *testing.T) {
	ix := olSharedIndex()
	ix.setOwner(olPK, i64(1))
	ix.confirm(olPK, 1)
	ix.setOwner(olPK, i64(1)) // same owner keeps the confirmation
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookDeny {
		t.Fatalf("same-owner reassign: res = %s", res)
	}
	ix.setOwner(olPK, i64(2))
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("reassigned: res = %s", res)
	}
	ix.confirm(olPK, 1) // not the owner any more: no effect
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("confirm by old owner: res = %s", res)
	}
	ix.confirm(olPK, 2)
	ix.unbind(olPK)
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("unbound: res = %s", res)
	}
	ix.setOwner(olPK2, i64(1))
	ix.confirm(olPK2, 1)
	ix.removeUser(1)
	if res, _, _ := ix.authorize(olShared, "publish", olTopic(olPK2), noBind(t), noConfirm(t)); res != hookIgnore {
		t.Fatalf("orphaned by user removal: res = %s", res)
	}
}

func TestGenerateMQTTCredentials(t *testing.T) {
	userRe := regexp.MustCompile(`^obs_[a-z0-9]{12}$`)
	passRe := regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	seenU, seenP := map[string]bool{}, map[string]bool{}
	for i := 0; i < 1000; i++ {
		u, p, err := generateMQTTCredentials()
		if err != nil {
			t.Fatal(err)
		}
		if !userRe.MatchString(u) {
			t.Fatalf("username %q does not match %s", u, userRe)
		}
		if !passRe.MatchString(p) {
			t.Fatalf("password %q does not match %s", p, passRe)
		}
		if seenU[u] || seenP[p] {
			t.Fatalf("duplicate draw at %d: %q / %q", i, u, p)
		}
		seenU[u], seenP[p] = true, true
	}
}

func TestAuthenticate(t *testing.T) {
	ix := olIndex()
	cases := []struct {
		name, user, pw string
		want           hookResult
	}{
		{"right password", "obs_alice0000000", "pw-alice", hookAllow},
		{"wrong password", "obs_alice0000000", "pw-bob", hookDeny},
		{"empty password", "obs_alice0000000", "", hookDeny},
		{"unknown username", "shared-login", "pw-alice", hookIgnore},
		{"revoked", "obs_carol0000000", "pw-carol", hookDeny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ix.authenticate(c.user, c.pw); got != c.want {
				t.Fatalf("authenticate(%q) = %q, want %q", c.user, got, c.want)
			}
		})
	}
}

func TestParsePublishTopic(t *testing.T) {
	lower := strings.ToLower(olPK)
	cases := []struct {
		topic string
		want  string
		ok    bool
	}{
		{"meshcore/BRU/" + olPK + "/packets", olPK, true},
		{"meshcore/BRU/" + olPK + "/status", olPK, true},
		{"meshcore/BRU/" + lower + "/packets", olPK, true},
		{"meshcore/BRU/" + olPK, "", false},
		{"meshcore/BRU/" + olPK + "/packets/extra", "", false},
		{"meshcore/BRU/" + olPK + "/", "", false},
		{"meshcore//" + olPK + "/packets", "", false},
		{"other/BRU/" + olPK + "/packets", "", false},
		{"MESHCORE/BRU/" + olPK + "/packets", "", false},
		{"meshcore/client/" + olPK + "/packets", "", false},
		{"meshcore/BRU/" + olPK[:63] + "G/packets", "", false},
		{"meshcore/BRU/" + olPK[:63] + "/packets", "", false},
		{"meshcore/BRU/" + olPK + "0/packets", "", false},
		{"meshcore/BRU/ " + olPK[:63] + "/packets", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := parsePublishTopic(c.topic)
		if ok != c.ok || got != c.want {
			t.Errorf("parsePublishTopic(%q) = %q, %v; want %q, %v", c.topic, got, ok, c.want, c.ok)
		}
	}
}

func TestAuthorize_BindTable(t *testing.T) {
	t.Run("owner is this user allow", func(t *testing.T) {
		ix := olIndex()
		ix.setOwner(olPK, i64(1))
		res, pk, created := ix.authorizePublish("obs_alice0000000", olTopic(olPK), noBind(t), confirmNoop)
		if res != hookAllow || pk != olPK || created {
			t.Fatalf("got %q %q %v", res, pk, created)
		}
	})
	t.Run("owner is other user deny", func(t *testing.T) {
		ix := olIndex()
		ix.setOwner(olPK, i64(2))
		res, pk, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK), noBind(t), confirmNoop)
		if res != hookDeny || pk != olPK {
			t.Fatalf("got %q %q", res, pk)
		}
	})
	t.Run("orphaned deny", func(t *testing.T) {
		ix := olIndex()
		ix.setOwner(olPK, nil)
		res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK), noBind(t), confirmNoop)
		if res != hookDeny {
			t.Fatalf("got %q", res)
		}
	})
	t.Run("absent binds to this user allow", func(t *testing.T) {
		ix := olIndex()
		b := &bindRecorder{owner: i64(1)}
		res, pk, created := ix.authorizePublish("obs_alice0000000", olTopic(olPK), b.fn, confirmNoop)
		if res != hookAllow || pk != olPK || !created {
			t.Fatalf("got %q %q %v", res, pk, created)
		}
		if len(b.calls) != 1 || b.calls[0] != olPK+"/1" {
			t.Fatalf("bind calls %v", b.calls)
		}
		// Cached: the second publish does not bind again.
		if res, _, created := ix.authorizePublish("obs_alice0000000", olTopic(olPK), noBind(t), confirmNoop); res != hookAllow || created {
			t.Fatalf("second publish %q %v", res, created)
		}
	})
	t.Run("absent but store says other owner deny", func(t *testing.T) {
		ix := olIndex()
		b := &bindRecorder{owner: i64(2)}
		res, _, created := ix.authorizePublish("obs_alice0000000", olTopic(olPK), b.fn, confirmNoop)
		if res != hookDeny || created {
			t.Fatalf("got %q %v", res, created)
		}
		if res, _, _ := ix.authorizePublish("obs_bob000000000", olTopic(olPK), noBind(t), confirmNoop); res != hookAllow {
			t.Fatalf("returned owner not cached: bob got %q", res)
		}
	})
	t.Run("absent but store says orphaned deny", func(t *testing.T) {
		ix := olIndex()
		b := &bindRecorder{owner: nil}
		if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK), b.fn, confirmNoop); res != hookDeny {
			t.Fatalf("got %q", res)
		}
		if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK), noBind(t), confirmNoop); res != hookDeny {
			t.Fatalf("orphan not cached: %q", res)
		}
	})
	t.Run("unknown username ignore", func(t *testing.T) {
		ix := olIndex()
		res, _, _ := ix.authorizePublish("shared-login", olTopic(olPK), noBind(t), confirmNoop)
		if res != hookIgnore {
			t.Fatalf("got %q", res)
		}
		if res, _, _ := ix.authorize("shared-login", "subscribe", olTopic(olPK), noBind(t), confirmNoop); res != hookIgnore {
			t.Fatalf("subscribe by unknown got %q", res)
		}
	})
	t.Run("revoked deny", func(t *testing.T) {
		ix := olIndex()
		ix.setOwner(olPK, i64(3))
		if res, _, _ := ix.authorizePublish("obs_carol0000000", olTopic(olPK), noBind(t), confirmNoop); res != hookDeny {
			t.Fatalf("got %q", res)
		}
	})
	t.Run("subscribe deny", func(t *testing.T) {
		ix := olIndex()
		ix.setOwner(olPK, i64(1))
		if res, _, _ := ix.authorize("obs_alice0000000", "subscribe", olTopic(olPK), noBind(t), confirmNoop); res != hookDeny {
			t.Fatalf("got %q", res)
		}
		if res, _, _ := ix.authorize("obs_alice0000000", "publish", olTopic(olPK), noBind(t), confirmNoop); res != hookAllow {
			t.Fatalf("publish via authorize got %q", res)
		}
	})
}

func TestAuthorize_LowercaseTopicMatchesBinding(t *testing.T) {
	ix := olIndex()
	ix.setOwner(olPK, i64(1))
	res, pk, _ := ix.authorizePublish("obs_alice0000000", olTopic(strings.ToLower(olPK)), noBind(t), confirmNoop)
	if res != hookAllow || pk != olPK {
		t.Fatalf("lowercase topic: got %q %q", res, pk)
	}
	ix.setOwner(olPK2, i64(2))
	if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(strings.ToLower(olPK2)), noBind(t), confirmNoop); res != hookDeny {
		t.Fatalf("lowercase topic of other user's key: got %q", res)
	}
}

func TestAuthorize_MalformedTopicNeverBinds(t *testing.T) {
	ix := olIndex()
	b := &bindRecorder{owner: i64(1)}
	for _, topic := range []string{
		"meshcore/BRU/" + olPK,
		"meshcore/BRU/" + olPK + "/packets/x",
		"meshcore/client/" + olPK + "/packets",
		"meshcore/BRU/nothex/packets",
		"meshcore/#",
		"other/BRU/" + olPK + "/packets",
	} {
		if res, _, _ := ix.authorizePublish("obs_alice0000000", topic, b.fn, confirmNoop); res != hookDeny {
			t.Errorf("%q: got %q, want deny", topic, res)
		}
	}
	if len(b.calls) != 0 {
		t.Fatalf("bind called %d times: %v", len(b.calls), b.calls)
	}
}

func TestAuthorize_BindErrorDenies(t *testing.T) {
	ix := olIndex()
	b := &bindRecorder{err: errors.New("database is locked")}
	if res, _, created := ix.authorizePublish("obs_alice0000000", olTopic(olPK), b.fn, confirmNoop); res != hookDeny || created {
		t.Fatalf("got %q %v", res, created)
	}
	ix.mu.RLock()
	_, cached := ix.owners[olPK]
	ix.mu.RUnlock()
	if cached {
		t.Fatal("owner cached after bind error")
	}
	b2 := &bindRecorder{owner: i64(1)}
	if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK), b2.fn, confirmNoop); res != hookAllow || len(b2.calls) != 1 {
		t.Fatalf("retry after error: %q, calls %v", res, b2.calls)
	}
}

func TestObserverLoginIndexMutations(t *testing.T) {
	ix := olIndex()
	// Reissue: the new username works, the old one is unknown.
	ix.setLogin(users.MQTTLogin{UserID: 1, Username: "obs_alicenew0000", PasswordHash: olHash("pw-new")})
	if got := ix.authenticate("obs_alice0000000", "pw-alice"); got != hookIgnore {
		t.Fatalf("old username after reissue: %q", got)
	}
	if got := ix.authenticate("obs_alicenew0000", "pw-new"); got != hookAllow {
		t.Fatalf("new username: %q", got)
	}
	// Reissue clears a revocation.
	ix.setLogin(users.MQTTLogin{UserID: 3, Username: "obs_carol0000000", PasswordHash: olHash("pw-carol")})
	if got := ix.authenticate("obs_carol0000000", "pw-carol"); got != hookAllow {
		t.Fatalf("reissued carol: %q", got)
	}
	ix.revoke(2)
	if got := ix.authenticate("obs_bob000000000", "pw-bob"); got != hookDeny {
		t.Fatalf("revoked bob: %q", got)
	}
	ix.revoke(99) // no login: no effect, no panic
}

func TestNewObserverLoginIndexLoads(t *testing.T) {
	st, err := users.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var ids []int64
	for _, e := range []string{"a@example.org", "b@example.org"} {
		u, err := st.CreatePending(e, e, "$argon2id$placeholder")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	for i, id := range ids {
		if err := st.CreateMQTTLogin(users.MQTTLogin{UserID: id, Username: fmt.Sprintf("obs_load%08d", i), PasswordHash: olHash("pw"), PasswordEnc: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RevokeMQTTLogin(ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BindObserver(olPK, ids[0], "auto"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BindObserver(olPK2, ids[1], "auto"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ids[1]); err != nil {
		t.Fatal(err)
	}

	ix, err := newObserverLoginIndex(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.authenticate("obs_load00000000", "pw"); got != hookAllow {
		t.Fatalf("loaded login: %q", got)
	}
	if res, _, _ := ix.authorizePublish("obs_load00000000", olTopic(olPK), noBind(t), confirmNoop); res != hookAllow {
		t.Fatalf("loaded binding: %q", res)
	}
	// The second user's deletion cascaded its login and orphaned its key.
	if got := ix.authenticate("obs_load00000001", "pw"); got != hookIgnore {
		t.Fatalf("deleted user's login: %q", got)
	}
	if res, _, _ := ix.authorizePublish("obs_load00000000", olTopic(olPK2), noBind(t), confirmNoop); res != hookDeny {
		t.Fatalf("orphaned binding: %q", res)
	}
}

func TestNewObserverLoginIndexLoadsConfirmed(t *testing.T) {
	st, err := users.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var ids []int64
	for _, e := range []string{"a@example.org", "b@example.org"} {
		u, err := st.CreatePending(e, e, "$argon2id$placeholder")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	pk3 := strings.Repeat("3", 64)
	if _, _, err := st.BindObserver(olPK, ids[0], "auto"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BindObserver(olPK2, ids[0], "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BindObserver(pk3, ids[1], "auto"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ids[1]); err != nil { // pk3: orphan, confirmed_at kept in the store
		t.Fatal(err)
	}
	ix, err := newObserverLoginIndex(st, map[string]bool{olShared: true})
	if err != nil {
		t.Fatal(err)
	}
	for pk, want := range map[string]hookResult{olPK: hookDeny, olPK2: hookIgnore, pk3: hookIgnore} {
		if res, _, _ := ix.authorize(olShared, "publish", olTopic(pk), noBind(t), noConfirm(t)); res != want {
			t.Errorf("%s: res = %s, want %s", pk[:8], res, want)
		}
	}
}

// olBenchIndex: 500 logins, 2000 observers bound four per login.
func olBenchIndex() (*observerLoginIndex, []string, []string) {
	ix := &observerLoginIndex{byUsername: map[string]indexedLogin{}, byUser: map[int64]string{}, owners: map[string]*int64{}, owned: map[int64]int{}, confirmed: map[string]bool{}, shared: map[string]bool{}}
	names := make([]string, 500)
	for i := range names {
		names[i] = fmt.Sprintf("obs_bench%07d", i)
		ix.setLogin(users.MQTTLogin{UserID: int64(i + 1), Username: names[i], PasswordHash: olHash("pw")})
	}
	topics := make([]string, 2000)
	for i := range topics {
		pk := fmt.Sprintf("%064X", i)
		ix.setOwner(pk, i64(int64(i%500+1)))
		ix.confirm(pk, int64(i%500+1))
		topics[i] = olTopic(pk)
	}
	return ix, names, topics
}

func BenchmarkAuthorizeBound(b *testing.B) {
	ix, names, topics := olBenchIndex()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := i % 2000
		if res, _, _ := ix.authorizePublish(names[j%500], topics[j], nil, nil); res != hookAllow {
			b.Fatalf("got %q", res)
		}
	}
}

func TestAuthorizeFastEnough(t *testing.T) {
	ix, names, topics := olBenchIndex()
	const n = 10000
	start := time.Now()
	for i := 0; i < n; i++ {
		j := i % 2000
		if res, _, _ := ix.authorizePublish(names[j%500], topics[j], nil, nil); res != hookAllow {
			t.Fatalf("got %q", res)
		}
	}
	if per := time.Since(start) / n; per >= 50*time.Microsecond {
		t.Fatalf("authorizePublish %v/op, want < 50µs", per)
	}
}

func olKey(i int) string { return fmt.Sprintf("%064X", 0xC0DE0000+i) }

func TestAuthorize_AutoBindCap(t *testing.T) {
	ix := olIndex()
	b := &bindRecorder{owner: i64(1)}
	for i := 0; i < maxAutoBindsPerUser; i++ {
		if res, _, created := ix.authorizePublish("obs_alice0000000", olTopic(olKey(i)), b.fn, confirmNoop); res != hookAllow || !created {
			t.Fatalf("bind %d: %q %v", i, res, created)
		}
	}
	if len(b.calls) != maxAutoBindsPerUser {
		t.Fatalf("bind calls %d, want %d", len(b.calls), maxAutoBindsPerUser)
	}
	b.calls = nil
	if res, pk, _ := ix.authorizePublish("obs_alice0000000", olTopic(olKey(maxAutoBindsPerUser)), b.fn, confirmNoop); res != hookDeny || pk != olKey(maxAutoBindsPerUser) {
		t.Fatalf("bind past cap: %q %q", res, pk)
	}
	if len(b.calls) != 0 {
		t.Fatalf("bind called past cap: %v", b.calls)
	}
	if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olKey(0)), noBind(t), confirmNoop); res != hookAllow {
		t.Fatalf("owned key at cap: %q", res)
	}
	// Another user is not affected by alice's count.
	b2 := &bindRecorder{owner: i64(2)}
	if res, _, _ := ix.authorizePublish("obs_bob000000000", olTopic(olKey(maxAutoBindsPerUser)), b2.fn, confirmNoop); res != hookAllow {
		t.Fatalf("bob: %q", res)
	}
}

func TestSetOwnerBypassesCap(t *testing.T) {
	ix := olIndex()
	for i := 0; i < maxAutoBindsPerUser; i++ {
		ix.setOwner(olKey(i), i64(1))
	}
	// Admin assignment past the cap is accepted and counted.
	ix.setOwner(olPK, i64(1))
	if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK), noBind(t), confirmNoop); res != hookAllow {
		t.Fatalf("admin-assigned key past cap: %q", res)
	}
	if got := ix.ownedCount(1); got != maxAutoBindsPerUser+1 {
		t.Fatalf("count %d, want %d", got, maxAutoBindsPerUser+1)
	}
	// Reassign two keys to bob and orphan one: alice drops to cap-2.
	ix.setOwner(olPK, i64(2))
	ix.setOwner(olKey(0), i64(2))
	ix.setOwner(olKey(1), nil)
	if got := ix.ownedCount(1); got != maxAutoBindsPerUser-2 {
		t.Fatalf("alice count %d, want %d", got, maxAutoBindsPerUser-2)
	}
	if got := ix.ownedCount(2); got != 2 {
		t.Fatalf("bob count %d, want 2", got)
	}
	// Re-setting the same owner does not double count.
	ix.setOwner(olPK, i64(2))
	if got := ix.ownedCount(2); got != 2 {
		t.Fatalf("bob count after repeat %d, want 2", got)
	}
	// Below the cap again: alice may auto-bind.
	b := &bindRecorder{owner: i64(1)}
	if res, _, _ := ix.authorizePublish("obs_alice0000000", olTopic(olPK2), b.fn, confirmNoop); res != hookAllow || len(b.calls) != 1 {
		t.Fatalf("auto-bind below cap: %q calls %v", res, b.calls)
	}
}

func TestOwnersSnapshotOmitsOrphans(t *testing.T) {
	uid := int64(7)
	ix := &observerLoginIndex{owners: map[string]*int64{"AA": &uid, "BB": nil}}
	got := ix.ownersSnapshot()
	if len(got) != 1 || got["AA"] != 7 {
		t.Fatalf("snapshot = %v", got)
	}
	got["AA"] = 9
	if *ix.owners["AA"] != 7 {
		t.Fatal("snapshot aliases the index")
	}
}
