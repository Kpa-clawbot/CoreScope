package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"strings"
	"sync"

	"github.com/meshcore-analyzer/users"
)

// hookResult is the answer to a broker auth or authz hook. hookIgnore tells
// the broker to fall through to its own users (shared login, bridges).
type hookResult string

const (
	hookAllow  hookResult = "allow"
	hookDeny   hookResult = "deny"
	hookIgnore hookResult = "ignore"
)

const (
	mqttUsernamePrefix = "obs_"
	mqttUsernameChars  = "abcdefghijklmnopqrstuvwxyz0123456789"
	mqttPasswordChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// maxAutoBindsPerUser caps the pubkeys a user can claim by publishing;
	// admin assignment is not capped.
	maxAutoBindsPerUser = 20
)

// randomString draws n characters from alphabet with crypto/rand, rejecting
// bytes above the largest multiple of len(alphabet) so every character is
// equally likely.
func randomString(alphabet string, n int) (string, error) {
	limit := 256 - 256%len(alphabet)
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < n {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

// generateMQTTCredentials returns a fresh username (obs_ + 12 [a-z0-9]) and
// password (32 [A-Za-z0-9]).
func generateMQTTCredentials() (username, password string, err error) {
	suffix, err := randomString(mqttUsernameChars, 12)
	if err != nil {
		return "", "", err
	}
	password, err = randomString(mqttPasswordChars, 32)
	if err != nil {
		return "", "", err
	}
	return mqttUsernamePrefix + suffix, password, nil
}

type indexedLogin struct {
	userID  int64
	hash    [32]byte
	revoked bool
	// disabled follows the account status; it is separate from revoked so
	// enabling an account does not undo a revocation.
	disabled bool
}

func (l indexedLogin) denied() bool { return l.revoked || l.disabled }

// observerLoginIndex answers the broker hooks from memory. owners holds every
// bound pubkey; a nil owner means the binding is orphaned. owned counts the
// pubkeys each user owns in owners. confirmed holds the owned pubkeys whose
// owner's login has published under them; shared is the broker's own shared
// login names, denied on a confirmed pubkey. shared is fixed after load.
type observerLoginIndex struct {
	mu         sync.RWMutex
	byUsername map[string]indexedLogin
	byUser     map[int64]string
	owners     map[string]*int64
	owned      map[int64]int
	confirmed  map[string]bool
	shared     map[string]bool
}

// newObserverLoginIndex loads every login and binding from st once. An
// orphaned binding is never confirmed: nobody owns it.
func newObserverLoginIndex(st *users.Store, shared map[string]bool) (*observerLoginIndex, error) {
	logins, err := st.AllMQTTLogins()
	if err != nil {
		return nil, err
	}
	bindings, err := st.ObserverBindings()
	if err != nil {
		return nil, err
	}
	ix := &observerLoginIndex{
		byUsername: make(map[string]indexedLogin, len(logins)),
		byUser:     make(map[int64]string, len(logins)),
		owners:     make(map[string]*int64, len(bindings)),
		owned:      make(map[int64]int, len(logins)),
		confirmed:  make(map[string]bool, len(bindings)),
		shared:     shared,
	}
	ids := make([]int64, 0, len(logins))
	for _, l := range logins {
		ix.setLogin(l)
		ids = append(ids, l.UserID)
	}
	accounts, err := st.UsersByID(ids)
	if err != nil {
		return nil, err
	}
	for id, u := range accounts {
		if u.Status == users.StatusDisabled {
			ix.setDisabled(id, true)
		}
	}
	for _, b := range bindings {
		ix.setOwner(b.Pubkey, b.UserID)
		if b.UserID != nil && b.ConfirmedAt != nil {
			ix.confirmed[b.Pubkey] = true
		}
	}
	return ix, nil
}

// setLogin adds or replaces the user's login; a reissued username drops the
// old one.
func (ix *observerLoginIndex) setLogin(l users.MQTTLogin) {
	var h [32]byte
	hex.Decode(h[:], []byte(l.PasswordHash))
	ix.mu.Lock()
	defer ix.mu.Unlock()
	disabled := false
	if old, ok := ix.byUser[l.UserID]; ok {
		disabled = ix.byUsername[old].disabled
		delete(ix.byUsername, old)
	}
	ix.byUser[l.UserID] = l.Username
	ix.byUsername[l.Username] = indexedLogin{userID: l.UserID, hash: h, revoked: l.RevokedAt != nil, disabled: disabled}
}

// setDisabled follows the account status of userID's login.
func (ix *observerLoginIndex) setDisabled(userID int64, disabled bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	name, ok := ix.byUser[userID]
	if !ok {
		return
	}
	l := ix.byUsername[name]
	l.disabled = disabled
	ix.byUsername[name] = l
}

// removeUser follows an account deletion: the login goes, and every pubkey
// the user owned stays in owners as an orphan so nobody can auto-claim it.
func (ix *observerLoginIndex) removeUser(userID int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if name, ok := ix.byUser[userID]; ok {
		delete(ix.byUsername, name)
		delete(ix.byUser, userID)
	}
	for pk, o := range ix.owners {
		if o != nil && *o == userID {
			ix.setOwnerLocked(pk, nil)
		}
	}
}

func (ix *observerLoginIndex) revoke(userID int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	name, ok := ix.byUser[userID]
	if !ok {
		return
	}
	l := ix.byUsername[name]
	l.revoked = true
	ix.byUsername[name] = l
}

// setOwner records pk's owner; nil marks it orphaned.
func (ix *observerLoginIndex) setOwner(pk string, uid *int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.setOwnerLocked(pk, uid)
}

// setOwnerLocked moves pk to uid and keeps owned and confirmed in step: the
// confirmation survives only when the owner stays the same. Caller holds mu.
func (ix *observerLoginIndex) setOwnerLocked(pk string, uid *int64) {
	old, ok := ix.owners[pk]
	if ok && old != nil {
		if ix.owned[*old]--; ix.owned[*old] <= 0 {
			delete(ix.owned, *old)
		}
	}
	if old == nil || uid == nil || *old != *uid {
		delete(ix.confirmed, pk)
	}
	ix.owners[pk] = uid
	if uid != nil {
		ix.owned[*uid]++
	}
}

// confirm marks pk confirmed when uid still owns it.
func (ix *observerLoginIndex) confirm(pk string, uid int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if o := ix.owners[pk]; o != nil && *o == uid {
		ix.confirmed[pk] = true
	}
}

func (ix *observerLoginIndex) ownedCount(uid int64) int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.owned[uid]
}

// userIDFor returns the owner of username's login.
func (ix *observerLoginIndex) userIDFor(username string) (int64, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	l, ok := ix.byUsername[username]
	return l.userID, ok
}

func (ix *observerLoginIndex) authenticate(username, password string) hookResult {
	ix.mu.RLock()
	l, ok := ix.byUsername[username]
	ix.mu.RUnlock()
	if !ok {
		return hookIgnore
	}
	if l.denied() {
		return hookDeny
	}
	h := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(h[:], l.hash[:]) != 1 {
		return hookDeny
	}
	return hookAllow
}

// parsePublishTopic accepts meshcore/<iata>/<pubkey>/<type> (exactly four
// non-empty segments, iata not "client") and returns the pubkey uppercased.
func parsePublishTopic(topic string) (pubkey string, ok bool) {
	seg := strings.Split(topic, "/")
	if len(seg) != 4 || seg[0] != "meshcore" || seg[1] == "" || seg[1] == "client" || seg[3] == "" {
		return "", false
	}
	if len(seg[2]) != 64 {
		return "", false
	}
	pk, err := users.NormalizeObserverKey(seg[2])
	if err != nil {
		return "", false
	}
	return pk, true
}

// authorize answers an authz hook for any action: unknown usernames are
// ignored, anything but publish is denied for managed logins and ignored for
// shared logins.
func (ix *observerLoginIndex) authorize(username, action, topic string, bind func(pk string, uid int64) (*int64, bool, error), confirm func(pk string, uid int64) (bool, error)) (hookResult, string, bool) {
	if action != "publish" {
		ix.mu.RLock()
		_, known := ix.byUsername[username]
		ix.mu.RUnlock()
		if !known {
			return hookIgnore, "", false
		}
		return hookDeny, "", false
	}
	return ix.authorizePublish(username, topic, bind, confirm)
}

// authorizeShared denies a shared login on any meshcore/<x>/<pubkey>... topic
// whose pubkey is confirmed, whatever the segment count, because the ingestor
// attributes such a topic to that pubkey. Everything else is ignored, so the
// broker decides as before.
func (ix *observerLoginIndex) authorizeShared(username, topic string) (hookResult, string, bool) {
	seg := strings.SplitN(topic, "/", 4)
	if len(seg) < 3 || seg[0] != "meshcore" {
		return hookIgnore, "", false
	}
	pk, err := users.NormalizeObserverKey(seg[2])
	if err != nil {
		return hookIgnore, "", false
	}
	ix.mu.RLock()
	confirmed := ix.confirmed[pk]
	ix.mu.RUnlock()
	if !confirmed {
		return hookIgnore, "", false
	}
	log.Printf("[observer-logins] publish denied: user=%s observer=%s: confirmed owner", username, pk[:8])
	return hookDeny, pk, false
}

// authorizePublish applies the binding table. bind is the store's
// first-claim-wins insert; it runs outside the lock and is called only for a
// well-formed topic whose pubkey the index does not know. confirm is the
// store's one-time confirmed_at write; it runs outside the lock and is called
// only for an allowed owner publish on an unconfirmed pubkey. A failed
// confirm still allows the publish.
func (ix *observerLoginIndex) authorizePublish(username, topic string, bind func(pk string, uid int64) (*int64, bool, error), confirm func(pk string, uid int64) (bool, error)) (res hookResult, boundPK string, created bool) {
	if ix.shared[username] {
		return ix.authorizeShared(username, topic)
	}
	ix.mu.RLock()
	l, ok := ix.byUsername[username]
	ix.mu.RUnlock()
	if !ok {
		return hookIgnore, "", false
	}
	if l.denied() {
		return hookDeny, "", false
	}
	pk, ok := parsePublishTopic(topic)
	if !ok {
		return hookDeny, "", false
	}
	ix.mu.RLock()
	owner, bound := ix.owners[pk]
	atCap := ix.owned[l.userID] >= maxAutoBindsPerUser
	ix.mu.RUnlock()
	if !bound {
		if atCap {
			log.Printf("[observer-logins] publish denied: user=%s observer=%s: auto-bind cap reached", username, pk[:8])
			return hookDeny, pk, false
		}
		var err error
		owner, created, err = bind(pk, l.userID)
		if err != nil {
			log.Printf("[observer-logins] bind failed: user=%s observer=%s: %v", username, pk[:8], err)
			return hookDeny, pk, false
		}
		ix.mu.Lock()
		if _, raced := ix.owners[pk]; !raced {
			ix.setOwnerLocked(pk, owner)
		}
		// BindObserver confirms the row it creates.
		if o := ix.owners[pk]; created && o != nil && *o == l.userID {
			ix.confirmed[pk] = true
		}
		ix.mu.Unlock()
	}
	if owner == nil || *owner != l.userID {
		log.Printf("[observer-logins] publish denied: user=%s observer=%s", username, pk[:8])
		return hookDeny, pk, false
	}
	ix.mu.RLock()
	confirmed := ix.confirmed[pk]
	ix.mu.RUnlock()
	if !confirmed {
		ok, err := confirm(pk, l.userID)
		if err != nil {
			log.Printf("[observer-logins] confirm failed: user=%s observer=%s: %v", username, pk[:8], err)
		} else if ok {
			ix.confirm(pk, l.userID)
		}
	}
	return hookAllow, pk, created
}

// ownerOf returns pk's current owner, nil when unbound or orphaned.
func (ix *observerLoginIndex) ownerOf(pk string) *int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.owners[pk]
}

// unbind forgets pk so the next publisher can claim it; setOwnerLocked drops
// its confirmation.
func (ix *observerLoginIndex) unbind(pk string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, ok := ix.owners[pk]; ok {
		ix.setOwnerLocked(pk, nil)
		delete(ix.owners, pk)
	}
}

// ownersSnapshot copies the owner of every bound pubkey (uppercase key);
// orphaned bindings are left out.
func (ix *observerLoginIndex) ownersSnapshot() map[string]int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make(map[string]int64, len(ix.owners))
	for pk, uid := range ix.owners {
		if uid != nil {
			out[pk] = *uid
		}
	}
	return out
}
