package dbx

import "testing"

func TestQuoteIdent(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:    `"plain"`,
		``:         `""`,
		`a"b`:      `"a""b"`,
		`"`:        `""""`,
		`x"; DROP`: `"x""; DROP"`,
	} {
		if got := QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestIsPostgresURL(t *testing.T) {
	for in, want := range map[string]bool{
		"postgres://u@h/db":     true,
		"postgresql://u@h/db":   true,
		"host=h dbname=d":       false,
		"/var/lib/corescope.db": false,
		"":                      false,
		" postgres://u@h/db":    false,
		"sqlite:postgres://x":   false,
	} {
		if got := IsPostgresURL(in); got != want {
			t.Errorf("IsPostgresURL(%q) = %v, want %v", in, got, want)
		}
	}
}
