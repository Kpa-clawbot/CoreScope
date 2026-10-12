package users

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompanionLinkUpsertAndTransfer(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("ab", 32)

	l, prev, err := st.UpsertCompanionLink(a.ID, strings.ToUpper(pk), "  Car\n ")
	if err != nil || prev != 0 {
		t.Fatalf("first link: prev=%d err=%v", prev, err)
	}
	first := clk.Now()
	if l.Pubkey != pk || l.UserID != a.ID || l.Name != "Car" || !l.LinkedAt.Equal(first) {
		t.Fatalf("first link = %+v", l)
	}

	clk.Advance(time.Hour)
	l, prev, err = st.UpsertCompanionLink(a.ID, pk, "Car 2")
	if err != nil || prev != 0 || l.Name != "Car 2" || !l.LinkedAt.Equal(first) {
		t.Fatalf("re-link by owner = %+v, prev=%d, err=%v", l, prev, err)
	}
	if got, _ := st.GetCompanionLink(pk); got == nil || got.Name != "Car 2" || !got.LinkedAt.Equal(first) {
		t.Fatalf("after re-link Get = %+v", got)
	}

	clk.Advance(time.Hour)
	l, prev, err = st.UpsertCompanionLink(b.ID, pk, "Bike")
	if err != nil || prev != a.ID {
		t.Fatalf("transfer: prev=%d (want %d) err=%v", prev, a.ID, err)
	}
	if l.UserID != b.ID || !l.LinkedAt.Equal(clk.Now()) {
		t.Fatalf("transferred link = %+v", l)
	}
	got, err := st.GetCompanionLink(pk)
	if err != nil || got.UserID != b.ID || got.Name != "Bike" {
		t.Fatalf("Get after transfer = %+v, %v", got, err)
	}
	if list, _ := st.ListCompanionLinks(a.ID); len(list) != 0 {
		t.Fatalf("previous owner still lists it: %+v", list)
	}
	if list, _ := st.ListCompanionLinks(b.ID); len(list) != 1 || list[0].Pubkey != pk {
		t.Fatalf("new owner list = %+v", list)
	}
	if _, _, err := st.UpsertCompanionLink(a.ID, "short", "x"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bad pubkey err = %v", err)
	}
}

func TestCompanionLinkListDeleteAndAll(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk1, pk2, pk3 := strings.Repeat("03", 32), strings.Repeat("01", 32), strings.Repeat("02", 32)
	st.UpsertCompanionLink(a.ID, pk1, "one")
	clk.Advance(time.Minute)
	st.UpsertCompanionLink(a.ID, pk2, "two")
	st.UpsertCompanionLink(b.ID, pk3, "three")

	list, err := st.ListCompanionLinks(a.ID)
	if err != nil || len(list) != 2 || list[0].Pubkey != pk2 || list[1].Pubkey != pk1 {
		t.Fatalf("ListCompanionLinks (newest first) = %+v, %v", list, err)
	}
	all, err := st.LinkedPubkeys()
	if err != nil || !reflect.DeepEqual(all, []string{pk2, pk3, pk1}) {
		t.Fatalf("LinkedPubkeys = %v, %v", all, err)
	}

	if err := st.DeleteCompanionLink(b.ID, pk1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting someone else's link = %v", err)
	}
	if err := st.DeleteCompanionLink(a.ID, strings.ToUpper(pk1)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCompanionLink(pk1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v", err)
	}
	if err := st.DeleteCompanionLink(a.ID, pk1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := st.GetCompanionLink("nothex"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("Get bad pubkey err = %v", err)
	}
}

// Deleting a user removes their links, challenges and device tokens.
func TestCompanionDataCascadesOnUserDelete(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pkA, pkB := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	st.UpsertCompanionLink(a.ID, pkA, "")
	st.UpsertCompanionLink(b.ID, pkB, "")
	if _, _, err := st.CreateLinkChallenge(a.ID, pkA); err != nil {
		t.Fatal(err)
	}
	raw, _, err := st.CreateDeviceSession(a.ID, "Phone", []string{"rx"}, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCompanionLink(pkA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("link survived user delete: %v", err)
	}
	if all, _ := st.LinkedPubkeys(); !reflect.DeepEqual(all, []string{pkB}) {
		t.Fatalf("LinkedPubkeys after delete = %v", all)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM link_challenges WHERE user_id = ?`, a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("challenges after user delete = %d, %v", n, err)
	}
	if _, err := st.LookupSession(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device token survived user delete: %v", err)
	}
}
