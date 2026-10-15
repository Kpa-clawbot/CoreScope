package main

import (
	"strings"
	"sync"
)

// internMax bounds the intern table. Observer IDs, names, IATA codes and
// directions number in the hundreds on a busy instance, so the cap is only a
// guard against an unexpected flood of distinct values.
const internMax = 1 << 16

var (
	internMu    sync.RWMutex
	internTable = make(map[string]string)
)

// internStr returns the canonical copy of s, so a store of hundreds of
// thousands of observations keeps one copy of each string that repeats
// across them (observer IDs, names, IATA codes, directions) rather than the
// one each row scan allocates.
//
// The table holds its copies for the life of the process: they are few, and
// a strongly held copy is what makes the sharing survive garbage collections
// (unique.Make holds its copies weakly, so a value interned again after a GC
// came back as a new copy). Past internMax entries a string is returned as
// it came: nothing is shared, nothing breaks. A hit takes only the read lock
// and does not allocate.
func internStr(s string) string {
	if s == "" {
		return ""
	}
	internMu.RLock()
	c, ok := internTable[s]
	internMu.RUnlock()
	if ok {
		return c
	}
	internMu.Lock()
	defer internMu.Unlock()
	if c, ok := internTable[s]; ok {
		return c
	}
	if len(internTable) >= internMax {
		return s
	}
	c = strings.Clone(s) // do not pin a larger buffer s may be a slice of
	internTable[c] = c
	return c
}
