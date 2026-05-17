package main

import (
	"bufio"
	"strings"
	"testing"
)

// TestParseProcSelfIO_EmptyDoesNotMarkOK — #1167 must-fix #3: an empty file
// (or one with no recognised keys) MUST result in ok=false. Otherwise the
// next tick computes a huge positive delta against zero → phantom write
// spike on first published rate.
func TestParseProcSelfIO_EmptyDoesNotMarkOK(t *testing.T) {
	var s procIOSnapshot
	parseProcSelfIOInto(bufio.NewScanner(strings.NewReader("")), &s)
	if s.ok {
		t.Errorf("empty input must produce ok=false, got ok=true (phantom-spike risk)")
	}
}

// TestParseProcSelfIO_NoKnownKeysDoesNotMarkOK — same as above, but the file
// has lines with unrecognised keys (a future /proc schema change). MUST NOT
// be treated as a valid sample.
func TestParseProcSelfIO_NoKnownKeysDoesNotMarkOK(t *testing.T) {
	var s procIOSnapshot
	parseProcSelfIOInto(bufio.NewScanner(strings.NewReader("garbage_key: 42\nother: 99\n")), &s)
	if s.ok {
		t.Errorf("input without recognised keys must produce ok=false, got ok=true")
	}
}

// TestParseProcSelfIO_ValidSampleMarksOK — positive companion: a real
// /proc/self/io-shaped input MUST mark ok=true with the parsed counters.
func TestParseProcSelfIO_ValidSampleMarksOK(t *testing.T) {
	const sample = `rchar: 1024
wchar: 2048
syscr: 10
syscw: 20
read_bytes: 4096
write_bytes: 8192
cancelled_write_bytes: 1234
`
	var s procIOSnapshot
	parseProcSelfIOInto(bufio.NewScanner(strings.NewReader(sample)), &s)
	if !s.ok {
		t.Fatalf("valid sample must produce ok=true")
	}
	if s.readBytes != 4096 || s.writeBytes != 8192 || s.cancelledWrite != 1234 {
		t.Errorf("unexpected parsed counters: %+v", s)
	}
}
