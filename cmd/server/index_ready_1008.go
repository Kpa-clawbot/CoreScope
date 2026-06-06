// Issue #1008 stubs: minimal compiling shims so the red commit's tests
// run to completion and fail on ASSERTIONS, not build errors. The green
// commit replaces these with the real atomic-flag implementation.
package main

// SubpathIndexReady reports whether the subpath index (spIndex/spTxIndex)
// has finished building. Stubbed to true in the red commit so the
// "false-after-Load" assertion fires; the green commit wires this to an
// atomic.Bool set by the background goroutine.
func (s *PacketStore) SubpathIndexReady() bool { return true }

// PathHopIndexReady is the equivalent gate for the byPathHop index.
func (s *PacketStore) PathHopIndexReady() bool { return true }
