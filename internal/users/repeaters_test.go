package users

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func fp(f float64) *float64 { return &f }

func TestValidateRepeaterEdit(t *testing.T) {
	tests := []struct {
		name    string
		in      RepeaterEdit
		wantErr bool
		check   func(t *testing.T, got RepeaterEdit)
	}{
		{"height negative", RepeaterEdit{HeightM: fp(-0.1)}, true, nil},
		{"height zero", RepeaterEdit{HeightM: fp(0)}, false, func(t *testing.T, g RepeaterEdit) {
			if g.HeightM == nil || *g.HeightM != 0 {
				t.Fatalf("height = %v, want pointer to 0", g.HeightM)
			}
		}},
		{"height 300", RepeaterEdit{HeightM: fp(300)}, false, nil},
		{"height 300.1", RepeaterEdit{HeightM: fp(300.1)}, true, nil},
		{"height nil", RepeaterEdit{}, false, func(t *testing.T, g RepeaterEdit) {
			if g.HeightM != nil {
				t.Fatal("nil height became non-nil")
			}
		}},
		{"height NaN", RepeaterEdit{HeightM: fp(math.NaN())}, true, nil},
		{"height Inf", RepeaterEdit{HeightM: fp(math.Inf(1))}, true, nil},
		{"gain -10", RepeaterEdit{GainDBi: -10}, false, nil},
		{"gain -10.1", RepeaterEdit{GainDBi: -10.1}, true, nil},
		{"gain 30", RepeaterEdit{GainDBi: 30}, false, nil},
		{"gain 30.1", RepeaterEdit{GainDBi: 30.1}, true, nil},
		{"gain NaN", RepeaterEdit{GainDBi: math.NaN()}, true, nil},
		{"gain Inf", RepeaterEdit{GainDBi: math.Inf(-1)}, true, nil},
		{"status planned", RepeaterEdit{Status: "planned"}, true, nil},
		{"status padded", RepeaterEdit{Status: " Active "}, true, nil},
		{"status empty", RepeaterEdit{Status: ""}, false, nil},
		{"status ok", RepeaterEdit{Status: RepeaterStatusMaintenance}, false, nil},
		{"power wind", RepeaterEdit{Power: "wind"}, true, nil},
		{"power ok", RepeaterEdit{Power: RepeaterPowerSolar}, false, nil},
		{"notes cleaned", RepeaterEdit{Notes: "  line1\r\nline2\t\x00  "}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "line1\nline2" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes blank-line run", RepeaterEdit{Notes: "a\n\n\n\n\nb\n\nc"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a\n\nb\n\nc" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes CRLF blank-line run", RepeaterEdit{Notes: "a\r\n\r\n\r\nb"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a\n\nb" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes whitespace-only lines count as blank", RepeaterEdit{Notes: "a\n \n\t\n   \nb"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a\n\nb" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes trailing spaces dropped per line", RepeaterEdit{Notes: "a  \n  b"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a\n  b" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes single newline kept", RepeaterEdit{Notes: "a\nb"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a\nb" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes tab", RepeaterEdit{Notes: "a\tb"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a b" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes lone CR", RepeaterEdit{Notes: "a\rb"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "a b" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes bidi", RepeaterEdit{Notes: "x" + string(rune(0x202e)) + "y" + string(rune(0x2067))}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "xy" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes invalid utf8", RepeaterEdit{Notes: "a" + string([]byte{0xff}) + "b"}, false, func(t *testing.T, g RepeaterEdit) {
			if g.Notes != "ab" {
				t.Fatalf("notes = %q", g.Notes)
			}
		}},
		{"notes 500 padded", RepeaterEdit{Notes: "  " + strings.Repeat("é", 500) + "  "}, false, nil},
		{"notes 500", RepeaterEdit{Notes: strings.Repeat("é", 500)}, false, nil},
		{"notes 501", RepeaterEdit{Notes: strings.Repeat("é", 501)}, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateRepeaterEdit(tc.in)
			if tc.wantErr {
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.Msg == "" {
					t.Fatalf("err = %v, want *ValidationError with Msg", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

const rpk = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
const rpk2 = "cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34cd34"

func activeUser(t *testing.T, st *Store, email, name string) *User {
	t.Helper()
	u := mustCreate(t, st, email, name)
	if err := st.Activate(u.ID, RoleUser, nil); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	got, err := st.GetByID(u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	return got
}

func TestRepeaterInfoSaveAndGet(t *testing.T) {
	st, clk := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	if _, err := st.GetRepeaterInfo(rpk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before save = %v, want ErrNotFound", err)
	}
	prev, err := st.SaveRepeaterInfo(a.ID, rpk, RepeaterEdit{HeightM: fp(0), GainDBi: 2.5, Status: RepeaterStatusActive, Power: RepeaterPowerSolar, Notes: "first"})
	if err != nil || prev != nil {
		t.Fatalf("first save prev=%v err=%v", prev, err)
	}
	got, err := st.GetRepeaterInfo(rpk)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeightM == nil || *got.HeightM != 0 || got.GainDBi != 2.5 || got.Notes != "first" ||
		got.UpdatedBy == nil || *got.UpdatedBy != a.ID || got.UpdatedByName != "Alice" || !got.UpdatedAt.Equal(clk.Now()) {
		t.Fatalf("got %+v", got)
	}
	clk.Advance(time.Hour)
	prev, err = st.SaveRepeaterInfo(a.ID, rpk, RepeaterEdit{})
	if err != nil || prev == nil || prev.Notes != "first" || prev.HeightM == nil || prev.Status != RepeaterStatusActive {
		t.Fatalf("second save prev=%+v err=%v", prev, err)
	}
	got, _ = st.GetRepeaterInfo(rpk)
	if got.HeightM != nil || got.Status != "" || got.Power != "" {
		t.Fatalf("after nil save: %+v", got)
	}
}

func TestRepeaterInfoSaveRejectsInvalid(t *testing.T) {
	st, _ := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	_, err := st.SaveRepeaterInfo(a.ID, rpk, RepeaterEdit{GainDBi: 31})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if _, err := st.GetRepeaterInfo(rpk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row stored: %v", err)
	}
	if _, err := st.SaveRepeaterInfo(a.ID, "nope", RepeaterEdit{}); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bad pubkey err = %v", err)
	}
}

func TestRepeaterInfoUpperCasePubkey(t *testing.T) {
	st, _ := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	if _, err := st.SaveRepeaterInfo(a.ID, strings.ToUpper(rpk), RepeaterEdit{Notes: "x"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRepeaterInfo(" " + rpk + " ")
	if err != nil || got.Pubkey != rpk || got.Notes != "x" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestRepeaterInfoEditorDeleted(t *testing.T) {
	st, _ := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	if _, err := st.SaveRepeaterInfo(a.ID, rpk, RepeaterEdit{Notes: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRepeaterInfo(rpk)
	if err != nil || got.UpdatedBy != nil || got.UpdatedByName != "" || got.Notes != "x" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestRepeaterClaimsMany(t *testing.T) {
	st, clk := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	b := activeUser(t, st, "b@example.org", "Bob")
	if added, err := st.ClaimRepeater(a.ID, rpk); err != nil || !added {
		t.Fatalf("A claim added=%v err=%v", added, err)
	}
	clk.Advance(time.Minute)
	if added, err := st.ClaimRepeater(b.ID, strings.ToUpper(rpk)); err != nil || !added {
		t.Fatalf("B claim added=%v err=%v", added, err)
	}
	if added, err := st.ClaimRepeater(a.ID, rpk); err != nil || added {
		t.Fatalf("A re-claim added=%v err=%v", added, err)
	}
	cl, err := st.RepeaterClaims(rpk)
	if err != nil || len(cl) != 2 || cl[0].UserID != a.ID || cl[0].DisplayName != "Alice" || cl[1].UserID != b.ID || cl[1].DisplayName != "Bob" {
		t.Fatalf("claims %+v err %v", cl, err)
	}
	if by, _ := st.RepeaterClaimsByUser(a.ID); len(by) != 1 {
		t.Fatalf("by user = %+v", by)
	}
	if err := st.UnclaimRepeater(a.ID, rpk); err != nil {
		t.Fatal(err)
	}
	cl, _ = st.RepeaterClaims(rpk)
	if len(cl) != 1 || cl[0].UserID != b.ID {
		t.Fatalf("after unclaim %+v", cl)
	}
	if err := st.UnclaimRepeater(a.ID, rpk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unclaim = %v", err)
	}
}

func TestRepeaterClaimsCascadeOnDelete(t *testing.T) {
	st, _ := newTestStore(t)
	b := activeUser(t, st, "b@example.org", "Bob")
	if _, err := st.ClaimRepeater(b.ID, rpk); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if cl, err := st.RepeaterClaims(rpk); err != nil || len(cl) != 0 {
		t.Fatalf("claims %+v err %v", cl, err)
	}
}

func TestCanEditRepeater(t *testing.T) {
	st, _ := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	b := activeUser(t, st, "b@example.org", "Bob")
	c := activeUser(t, st, "c@example.org", "Carol")
	if err := st.SetRole(c.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetByID(c.ID)
	can := func(u *User, want bool, msg string) {
		t.Helper()
		got, err := st.CanEditRepeater(u, rpk)
		if err != nil || got != want {
			t.Fatalf("%s: got %v err %v, want %v", msg, got, err, want)
		}
	}
	can(a, true, "unclaimed A")
	if _, err := st.ClaimRepeater(b.ID, rpk); err != nil {
		t.Fatal(err)
	}
	can(a, false, "claimed by B, A")
	can(b, true, "claimed by B, B")
	can(c, true, "admin")
	if err := st.SetStatus(b.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	can(a, true, "B disabled, A")
	if cl, _ := st.RepeaterClaims(rpk); len(cl) != 0 {
		t.Fatalf("claims visible for disabled: %+v", cl)
	}
	if by, _ := st.RepeaterClaimsByUser(b.ID); len(by) != 1 {
		t.Fatalf("by user B = %+v", by)
	}
	b, _ = st.GetByID(b.ID)
	can(b, false, "disabled B")
}

func TestAllRepeaterClaimsAndList(t *testing.T) {
	st, _ := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "Alice")
	b := activeUser(t, st, "b@example.org", "Bob")
	for _, pk := range []string{rpk, rpk2} {
		for _, u := range []*User{a, b} {
			if _, err := st.ClaimRepeater(u.ID, pk); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.SetStatus(b.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	m, err := st.AllRepeaterClaims()
	if err != nil || len(m) != 2 || len(m[rpk]) != 1 || m[rpk][0].UserID != a.ID || len(m[rpk2]) != 1 {
		t.Fatalf("map %+v err %v", m, err)
	}
	if _, err := st.SaveRepeaterInfo(a.ID, rpk2, RepeaterEdit{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveRepeaterInfo(a.ID, rpk, RepeaterEdit{}); err != nil {
		t.Fatal(err)
	}
	l, err := st.ListRepeaterInfo()
	if err != nil || len(l) != 2 || l[0].Pubkey != rpk || l[1].Pubkey != rpk2 {
		t.Fatalf("list %+v err %v", l, err)
	}
}
