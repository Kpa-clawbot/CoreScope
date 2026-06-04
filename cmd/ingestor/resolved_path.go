package main

// Stubs for issue #1547 fix. These return zero/empty values so the
// failing tests compile + run + fail on assertions (not build errors).
// The real implementation lands in commit 2.

// resolvePath resolves hop prefixes to full pubkeys using the supplied
// prefix index. Stub returns a nil-filled slice so callers see all
// hops as unresolved (mirrors today's broken behavior).
func resolvePath(hops []string, idx prefixIndex) []*string {
	if len(hops) == 0 {
		return nil
	}
	return make([]*string, len(hops))
}

// marshalResolvedPath encodes a resolved path as JSON. Stub returns
// empty string so the writer effectively persists NULL.
func marshalResolvedPath(rp []*string) string {
	return ""
}

// RefreshPrefixIndex rebuilds the in-memory prefix index from nodes.
// Stub: no-op so tests compile.
func (s *Store) RefreshPrefixIndex() error {
	return nil
}
