package users

import (
	"errors"
	"math"
	"strings"
	"testing"
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
