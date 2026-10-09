package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"

	"github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/sqliteutil"
)

const importSQLiteDriver = "corescope_sqlite_import"
const maxImportRowBytes = 64 * 1024 * 1024

func init() {
	// Apply the bound before any SQLite row reaches the Go driver. A check
	// after Scan would already have allocated an arbitrarily large value.
	sql.Register(importSQLiteDriver, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		conn.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, maxImportRowBytes)
		return nil
	}})
}

// snapshotSource adds offline source immutability checks to the shared native
// backup. Snapshot itself is also used by live backups that permit writers.
func snapshotSource(ctx context.Context, source, destination string) error {
	before, err := sourceFingerprintContext(ctx, source)
	if err != nil {
		return err
	}
	if err := sqliteutil.Snapshot(ctx, source, destination); err != nil {
		return err
	}
	after, err := sourceFingerprintContext(ctx, source)
	if err != nil || before != after {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(destination + suffix)
		}
		if err != nil {
			return err
		}
		return errors.New("SQLite source changed during snapshot; stop all instance writers before retrying")
	}
	return nil
}

func sqliteURL(path, mode string) (string, error) {
	return dbconfig.SQLiteURI(path, url.Values{"mode": {mode}, "_busy_timeout": {"5000"}})
}

func openSQLite(path, mode string) (*sql.DB, error) {
	uri, err := sqliteURL(path, mode)
	if err != nil {
		return nil, err
	}
	return sql.Open(importSQLiteDriver, uri)
}

// Hash the main file and committed-write log without loading either into RAM.
// Shared-memory coordination files are not application data.
func sourceFingerprint(path string) ([2][32]byte, error) {
	return sourceFingerprintContext(context.Background(), path)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func sourceFingerprintContext(ctx context.Context, path string) ([2][32]byte, error) {
	var result [2][32]byte
	if err := ctx.Err(); err != nil {
		return result, err
	}
	for i, suffix := range []string{"", "-wal"} {
		f, err := os.Open(path + suffix)
		if i == 1 && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		if i == 1 {
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return result, err
			}
			// Read-only WAL-mode opens can create/remove a zero-byte sidecar.
			// Every nonempty byte, including headers, still affects identity.
			if info.Size() == 0 {
				if err := f.Close(); err != nil {
					return result, err
				}
				continue
			}
		}
		h := sha256.New()
		_, copyErr := io.CopyBuffer(h, contextReader{ctx, f}, make([]byte, 128*1024))
		closeErr := f.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return result, err
		}
		copy(result[i][:], h.Sum(nil))
	}
	return result, nil
}
