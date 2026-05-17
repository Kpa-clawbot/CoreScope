package main

import "testing"

func TestSQLPlaceholders(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, ""}, {0, ""}, {1, "?"}, {2, "?,?"}, {3, "?,?,?"}, {5, "?,?,?,?,?"},
	}
	for _, c := range cases {
		if got := sqlPlaceholders(c.n); got != c.want {
			t.Errorf("sqlPlaceholders(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
