package dbx_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx"
)

// scriptDriver fails Open with each queued error in turn, then succeeds.
type scriptDriver struct {
	mu    sync.Mutex
	errs  []error
	opens atomic.Int32
}

func (d *scriptDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.errs) > 0 {
		e := d.errs[0]
		d.errs = d.errs[1:]
		return nil, e
	}
	return fakeConn{}, nil
}

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

var driverSeq atomic.Int32

func scripted(t *testing.T, errs ...error) (*sql.DB, *scriptDriver) {
	t.Helper()
	d := &scriptDriver{errs: errs}
	name := fmt.Sprintf("dbx-retry-%d", driverSeq.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, d
}

func refused() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
}

func TestPingWithRetryRefusedThenOK(t *testing.T) {
	db, d := scripted(t, refused(), refused())
	if err := dbx.PingWithRetry(context.Background(), db, 10*time.Second); err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if n := d.opens.Load(); n != 3 {
		t.Fatalf("opens = %d, want 3", n)
	}
}

func TestPingWithRetryNonTransientReturnsImmediately(t *testing.T) {
	authErr := errors.New(`FATAL: password authentication failed for user "x" (SQLSTATE 28P01)`)
	db, d := scripted(t, authErr)
	start := time.Now()
	err := dbx.PingWithRetry(context.Background(), db, 10*time.Second)
	if !errors.Is(err, authErr) {
		t.Fatalf("want auth error, got %v", err)
	}
	if d.opens.Load() != 1 || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("retried a non-transient error: opens=%d after %v", d.opens.Load(), time.Since(start))
	}
}

func TestPingWithRetryDeadlineRespected(t *testing.T) {
	errs := make([]error, 100)
	for i := range errs {
		errs[i] = refused()
	}
	db, d := scripted(t, errs...)
	start := time.Now()
	err := dbx.PingWithRetry(context.Background(), db, time.Second)
	el := time.Since(start)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("want last refused error, got %v", err)
	}
	if el > 2*time.Second || d.opens.Load() < 2 {
		t.Fatalf("elapsed %v, opens %d", el, d.opens.Load())
	}
}

func TestPingWithRetryContextCancel(t *testing.T) {
	errs := make([]error, 100)
	for i := range errs {
		errs[i] = refused()
	}
	db, _ := scripted(t, errs...)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := dbx.PingWithRetry(ctx, db, time.Minute); err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("ignored ctx: %v", time.Since(start))
	}
}

func TestIsTransientConnError(t *testing.T) {
	yes := []error{
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EHOSTUNREACH,
		&net.DNSError{Err: "no such host", IsNotFound: true},
		&net.DNSError{Err: "temporary", IsTemporary: true},
		&net.OpError{Op: "dial", Err: timeoutErr{}},
		fmt.Errorf("wrapped: %w", syscall.ECONNREFUSED),
	}
	for _, e := range yes {
		if !dbx.IsTransientConnError(e) {
			t.Errorf("%v should be transient", e)
		}
	}
	no := []error{nil, io.EOF, errors.New("tls: bad certificate"), context.DeadlineExceeded, context.Canceled}
	for _, e := range no {
		if dbx.IsTransientConnError(e) {
			t.Errorf("%v should not be transient", e)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestPingWithRetryPostgresLateListener points a real pgx handle at a local
// port that is closed, then starts a TCP proxy to the test database there
// after 1 s: the ping must ride out the refusals and succeed.
func TestPingWithRetryPostgresLateListener(t *testing.T) {
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set")
	}
	target, err := pgHostPort(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Reserve a port, then free it so connects are refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	proxyDSN, err := withHostPort(dsn, addr)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(time.Second)
		pl, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		go func() { <-stop; pl.Close() }()
		for {
			c, err := pl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer u.Close()
				go io.Copy(u, c)
				io.Copy(c, u)
			}()
		}
	}()
	defer func() { close(stop); <-done }()

	db, err := sql.Open(dbx.PostgresDriverName, proxyDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Now()
	if err := dbx.PingWithRetry(context.Background(), db, 15*time.Second); err != nil {
		t.Fatalf("ping after late listen: %v", err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatalf("succeeded before the listener existed? %v", time.Since(start))
	}
}

func pgHostPort(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("bad dsn")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func withHostPort(dsn, hostport string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("bad dsn")
	}
	u.Host = hostport
	return u.String(), nil
}
