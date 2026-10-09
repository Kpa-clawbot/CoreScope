package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mattn/go-sqlite3"
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

// snapshotSource creates an offline recovery copy without modifying the input.
// The backup API preserves hidden rowids used by observer links and mail event
// ordering. VACUUM INTO may renumber them and is not a safe migration snapshot.
func snapshotSource(ctx context.Context, source, destination string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceDB, err := sql.Open(importSQLiteDriver, sqliteURL(source, "ro"))
	if err != nil {
		return err
	}
	defer sourceDB.Close()
	sourceDB.SetMaxOpenConns(1)
	if _, err := sourceDB.ExecContext(ctx, `PRAGMA trusted_schema=OFF`); err != nil {
		return err
	}
	var schemaVersion int
	if err := sourceDB.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return fmt.Errorf("read SQLite source: %w", err)
	}
	before, err := sourceFingerprintContext(ctx, source)
	if err != nil {
		return err
	}
	reserved, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create new recovery snapshot: %w", err)
	}
	if err := reserved.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	defer func() {
		if err != nil {
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				_ = os.Remove(destination + suffix)
			}
		}
	}()
	destinationDB, err := sql.Open(importSQLiteDriver, sqliteURL(destination, "rw"))
	if err != nil {
		return err
	}
	defer destinationDB.Close()
	destinationDB.SetMaxOpenConns(1)
	src, err := sourceDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := destinationDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer dst.Close()
	err = src.Raw(func(sourceDriver any) error {
		return dst.Raw(func(destinationDriver any) (err error) {
			backup, err := destinationDriver.(*sqlite3.SQLiteConn).Backup("main", sourceDriver.(*sqlite3.SQLiteConn), "main")
			if err != nil {
				return err
			}
			defer func() {
				if finishErr := backup.Finish(); err == nil {
					err = finishErr
				}
			}()
			remaining, progressAt := -1, time.Now()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				done, err := backup.Step(256)
				if err != nil {
					return err
				}
				if done {
					return nil
				}
				if current := backup.Remaining(); current != remaining {
					remaining, progressAt = current, time.Now()
					continue
				}
				if time.Since(progressAt) > 10*time.Second {
					return errors.New("SQLite snapshot made no progress; stop all instance writers before retrying")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	})
	if err != nil {
		return fmt.Errorf("copy SQLite recovery snapshot: %w", err)
	}
	var check string
	if err := dst.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return errors.New("SQLite recovery snapshot failed integrity verification")
	}
	after, err := sourceFingerprintContext(ctx, source)
	if err != nil {
		return err
	}
	if after != before {
		return errors.New("SQLite source changed during snapshot; stop all instance writers before retrying")
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if err := destinationDB.Close(); err != nil {
		return err
	}
	return nil
}

func sqliteURL(path, mode string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	if runtime.GOOS == "windows" {
		abs = "/" + abs
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{"mode": {mode}, "_busy_timeout": {"5000"}}
	u.RawQuery = q.Encode()
	return u.String()
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
