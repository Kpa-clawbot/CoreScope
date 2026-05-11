package dbconfig

import "testing"

func TestGetIncrementalVacuumPages_Default(t *testing.T) {
	var c *DBConfig
	if got := c.GetIncrementalVacuumPages(); got != 1024 {
		t.Fatalf("nil DBConfig: got %d, want 1024", got)
	}
	c = &DBConfig{}
	if got := c.GetIncrementalVacuumPages(); got != 1024 {
		t.Fatalf("zero DBConfig: got %d, want 1024", got)
	}
}

func TestGetIncrementalVacuumPages_Configured(t *testing.T) {
	c := &DBConfig{IncrementalVacuumPages: 512}
	if got := c.GetIncrementalVacuumPages(); got != 512 {
		t.Fatalf("got %d, want 512", got)
	}
}
