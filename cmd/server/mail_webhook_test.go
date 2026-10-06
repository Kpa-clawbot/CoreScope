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
