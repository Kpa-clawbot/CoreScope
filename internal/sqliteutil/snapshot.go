// Package sqliteutil provides native snapshots without a writable source handle.
package sqliteutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
	"net/url"
	"os"
	"strings"
	"time"
)

// Snapshot copies one consistent SQLite image, including committed WAL pages,
// hidden rowids and AUTOINCREMENT sequences, to a new private file. It never
// writes the source. The caller may impose a shorter deadline; ten minutes is
// the upper bound. Failure removes only the new destination and its sidecars.
// Offline importers must separately enforce their source-immutability policy.
func Snapshot(ctx context.Context, source, destination string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if source == "" || destination == "" || strings.HasPrefix(source, "file:") || strings.Contains(source, "://") || strings.HasPrefix(destination, "file:") || strings.Contains(destination, "://") {
		return errors.New("SQLite snapshot requires filesystem paths")
	}
	sourceURI, err := dbconfig.SQLiteURI(source, url.Values{"mode": {"ro"}, "_query_only": {"on"}, "_busy_timeout": {"100"}})
	if err != nil {
		return err
	}
	destinationURI, err := dbconfig.SQLiteURI(destination, url.Values{"mode": {"rw"}, "_busy_timeout": {"100"}})
	if err != nil {
		return err
	}
	sourceDB, err := sql.Open("sqlite3", sourceURI)
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
	destinationDB, err := sql.Open("sqlite3", destinationURI)
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
					return errors.New("SQLite snapshot made no progress")
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
	if err := dst.Close(); err != nil {
		return err
	}
	if err := destinationDB.Close(); err != nil {
		return err
	}
	return nil
}
