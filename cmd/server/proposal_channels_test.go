package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

func getBody(router *mux.Router, path string) string {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Body.String()
}

func TestChannelsResponseUnchangedWithoutProposals(t *testing.T) {
	srv, router := setupTestServer(t)
	base := getBody(router, "/api/channels")
	if strings.Contains(base, "approvedChannels") {
		t.Fatalf("approvedChannels without user management: %s", base)
	}
	a, _ := newTestAuthService(t) // user management on, proposals off
	srv.auth = a
	if got := getBody(router, "/api/channels"); got != base {
		t.Fatalf("/api/channels changed with proposals off:\n%s\nvs\n%s", got, base)
	}
}

func TestChannelsResponseListsApprovedChannels(t *testing.T) {
	srv, router := setupTestServer(t)
	a, _ := newTestAuthService(t)
	a.set.proposals = defaultProposalSettings()
	srv.auth = a
	var resp ChannelListResponse
	if err := json.Unmarshal([]byte(getBody(router, "/api/channels")), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ApprovedChannels == nil || len(*resp.ApprovedChannels) != 0 {
		t.Fatalf("approvedChannels with none approved = %v; want present and empty", resp.ApprovedChannels)
	}
	u, err := a.st.CreatePending("pat@example.org", "Pat", "x")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.st.Propose(users.KindHashtagChannel, "#mycity", u.ID, users.ProposalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.Decide(p.ID, users.ProposalApprove, u.ID, "", 0); err != nil {
		t.Fatal(err)
	}
	a.refreshApproved()
	resp = ChannelListResponse{}
	if err := json.Unmarshal([]byte(getBody(router, "/api/channels")), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ApprovedChannels == nil || strings.Join(*resp.ApprovedChannels, ",") != "#mycity" {
		t.Fatalf("approvedChannels = %v; want [#mycity]", resp.ApprovedChannels)
	}
}

func TestDecideRefreshesApprovedChannels(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	admin, user := proposalPeople(t, f)
	p := decode[proposalJSON](t, postProposal(t, f, user, "#a"))
	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/proposals/%d/approve", p.ID), decideRequest{}, as(admin)), 200)
	if got := f.srv.auth.approvedChannels(); strings.Join(got, ",") != "#a" {
		t.Fatalf("after approve = %v", got)
	}
	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/proposals/%d/revoke", p.ID), decideRequest{}, as(admin)), 200)
	if got := f.srv.auth.approvedChannels(); got == nil || len(got) != 0 {
		t.Fatalf("after revoke = %#v; want empty", got)
	}
}

// The snapshot is filled at construction and capped at maxApproved, oldest
// approval first: the ingestor applies the same cap and order.
func TestNewAuthServiceLoadsApprovedChannelsCapped(t *testing.T) {
	a, _ := newTestAuthService(t)
	u, err := a.st.CreatePending("pat@example.org", "Pat", "x")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"#a", "#b"} {
		p, err := a.st.Propose(users.KindHashtagChannel, s, u.ID, users.ProposalLimits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.Decide(p.ID, users.ProposalApprove, u.ID, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	set := *a.set
	set.proposals = defaultProposalSettings()
	set.proposals.maxApproved = 1
	b := newAuthService(&set, a.st, &mailer.Fake{})
	if got := b.approvedChannels(); strings.Join(got, ",") != "#a" {
		t.Fatalf("approvedChannels = %v; want [#a]", got)
	}
	off := newAuthService(a.set, a.st, &mailer.Fake{})
	if got := off.approvedChannels(); got == nil || len(got) != 0 {
		t.Fatalf("proposals off = %#v; want empty", got)
	}
}

func TestClientConfigAdvertisesChannelProposals(t *testing.T) {
	srv, router := setupTestServer(t)
	a, _ := newTestAuthService(t)
	a.set.proposals = defaultProposalSettings()
	srv.auth = a
	if body := getBody(router, "/api/config/client"); !strings.Contains(body, `"userManagement":{"enabled":true,"channelProposals":true,"companionLinking":true}`) {
		t.Fatalf("client config = %s", body)
	}
}
