package dbx

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"syscall"
	"time"
)

const (
	pingBackoffStart = 250 * time.Millisecond
	pingBackoffMax   = 2 * time.Second
)

// IsTransientConnError reports whether err is a network-level failure that a
// new pod commonly sees right after start (NetworkPolicy not yet admitting its
// IP, a service name not resolvable yet) and that is worth retrying. Auth
// failures, a missing database and TLS errors are not transient.
func IsTransientConnError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTemporary || dns.IsNotFound || dns.IsTimeout
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// PingWithRetry pings db, retrying transient connection errors with backoff
// (250 ms doubling, capped at 2 s) until maxWait elapses or ctx is done. It
// returns the last error; a non-transient error is returned at once. It logs
// nothing.
func PingWithRetry(ctx context.Context, db *sql.DB, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	delay := pingBackoffStart
	for {
		err := db.PingContext(ctx)
		if err == nil || !IsTransientConnError(err) {
			return err
		}
		if ctx.Err() != nil || time.Now().Add(delay).After(deadline) {
			return err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
		if delay *= 2; delay > pingBackoffMax {
			delay = pingBackoffMax
		}
	}
}
