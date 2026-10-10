package main

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
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

func TestBrevoWebhookAbsentWithoutSecret(t *testing.T) {
	f, _, _ := adminFixture(t)
	f.srv.auth.set.webhookSecret = ""
	r := newAuthFixtureRouterOnly(f)
	req := httptest.NewRequest("POST", "/api/mail/brevo/webhook", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("no secret configured = %d, want 404", w.Code)
	}
}

func newAuthFixtureRouterOnly(f *authFixture) *mux.Router {
	r := mux.NewRouter()
	f.srv.registerAuthRoutes(r)
	return r
}

func TestPostalWebhookAuthAndIngest(t *testing.T) {
	f, _, uma := adminFixture(t)
	f.srv.auth.set.provider = "postal"
	router := newAuthFixtureRouterOnly(f)
	uid := uma.me.ID
	mailID, err := f.st.LogMail(&uid, "uma@example.org", "activate", "4242")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := f.st.MailByID(mailID)
	body := fmt.Sprintf(`{"event":"MessageDeliveryFailed","timestamp":%d.5,"uuid":"u1","payload":{
		"message":{"id":4242,"to":"uma@example.org"},"status":"HardFail",
		"details":"Permanent SMTP delivery error when sending to mx.example.org","output":"550 5.1.1 mailbox unavailable",
		"timestamp":%d.0}}`, rec.SentAt.Unix()+35, rec.SentAt.Unix()+30)
	post := func(user, pass, b string) int {
		req := httptest.NewRequest("POST", "/api/mail/postal/webhook", bytes.NewBufferString(b))
		req.RemoteAddr = "203.0.113.50:443"
		if pass != "" {
			req.SetBasicAuth(user, pass)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if c := post("", "", body); c != 401 {
		t.Fatalf("no auth = %d", c)
	}
	if c := post("postal", "wrong-secret-xxxxxxxx", body); c != 401 {
		t.Fatalf("wrong auth = %d", c)
	}
	if c := post("postal", testHook, `{"event":"DomainDNSError","payload":{}}`); c != 200 { // 200 so Postal stops retrying
		t.Fatalf("server event = %d", c)
	}
	if c := post("postal", testHook, body); c != 200 {
		t.Fatalf("valid = %d", c)
	}
	rec, _ = f.st.MailByID(mailID)
	if rec.LastEvent != "hard_bounce" || rec.LastReason != "Permanent SMTP delivery error when sending to mx.example.org: 550 5.1.1 mailbox unavailable" {
		t.Fatalf("mail record = %+v", rec)
	}
	if u, _ := f.st.GetByID(uid); !u.EmailBouncing {
		t.Fatal("bounce flag not set")
	}
	unknown := `{"event":"MessageSent","payload":{"message":{"id":999999}}}`
	if c := post("postal", testHook, unknown); c != 200 {
		t.Fatalf("unknown id = %d (must be 200 so Postal stops retrying)", c)
	}
}

// Postal also hard-fails for reasons on its own side. Those must not flag the
// address: email_bouncing only clears when the user changes address, so the
// account would stop getting notifications for good.
func TestPostalWebhookSenderSideFailureDoesNotFlag(t *testing.T) {
	f, _, uma := adminFixture(t)
	f.srv.auth.set.provider = "postal"
	router := newAuthFixtureRouterOnly(f)
	uid := uma.me.ID
	for i, details := range []string{
		"Message is likely spam. Threshold is 5.0 and the message scored 7.2.",
		"Maximum number of delivery attempts (18) has been reached. Added uma@example.org to suppression list because delivery has failed 18 times.",
	} {
		providerID := fmt.Sprint(5000 + i)
		mailID, err := f.st.LogMail(&uid, "uma@example.org", "notify", providerID)
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := f.st.MailByID(mailID)
		body := fmt.Sprintf(`{"event":"MessageDeliveryFailed","payload":{"message":{"id":%s},"status":"HardFail",
			"details":%q,"output":"","timestamp":%d}}`, providerID, details, rec.SentAt.Unix()+30)
		req := httptest.NewRequest("POST", "/api/mail/postal/webhook", bytes.NewBufferString(body))
		req.RemoteAddr = "203.0.113.50:443"
		req.SetBasicAuth("postal", testHook)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%q: status %d", details, w.Code)
		}
		if rec, _ = f.st.MailByID(mailID); rec.LastEvent != "error" || rec.LastReason != details {
			t.Fatalf("%q: mail record = %+v", details, rec)
		}
	}
	if u, _ := f.st.GetByID(uid); u.EmailBouncing {
		t.Fatal("a failure on Postal's side flagged the address as bouncing")
	}
}

func TestWebhookRouteFollowsProvider(t *testing.T) {
	f, _, _ := adminFixture(t)
	code := func(r *mux.Router, path string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewBufferString("{}")))
		return w.Code
	}
	f.srv.auth.set.provider = "postal"
	r := newAuthFixtureRouterOnly(f)
	if c := code(r, "/api/mail/brevo/webhook"); c != 404 {
		t.Fatalf("brevo route with the postal provider = %d, want 404", c)
	}
	if c := code(r, "/api/mail/postal/webhook"); c != 401 {
		t.Fatalf("postal route = %d, want 401", c)
	}
	f.srv.auth.set.provider = "brevo"
	r = newAuthFixtureRouterOnly(f)
	if c := code(r, "/api/mail/postal/webhook"); c != 404 {
		t.Fatalf("postal route with the brevo provider = %d, want 404", c)
	}
}
