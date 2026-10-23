package legacy

import (
	"database/sql"
	"fmt"
)

// CheckLegacySource rejects layouts or data the historical normalizer would
// discard. The importer also rejects unknown tables/columns before copying.
// Supported layouts are v3 observations with the known optional columns absent.
func CheckLegacySource(db *sql.DB) error {
	var integrity string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("legacy SQLite integrity check failed")
	}
	for _, table := range []string{"nodes", "observers", "transmissions", "observations"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("unsupported legacy schema: missing %s", table)
		}
	}
	has, err := TableHasColumn(db, "observations", "observer_idx")
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("legacy v2 observer_id layout is unsupported; upgrade an offline copy with the last SQLite release before importing")
	}
	checks := []struct{ name, query string }{
		{"empty transmission hash/timestamp", `SELECT count(*) FROM transmissions WHERE hash IS NULL OR hash='' OR first_seen IS NULL OR first_seen=''`},
		{"case-colliding node public keys", `SELECT count(*) FROM (SELECT lower(public_key) FROM nodes GROUP BY lower(public_key) HAVING count(*)>1)`},
		{"duplicate observation identity", `SELECT count(*) FROM (SELECT transmission_id,observer_idx,coalesce(path_json,'') FROM observations WHERE observer_idx IS NOT NULL GROUP BY transmission_id,observer_idx,coalesce(path_json,'') HAVING count(*)>1)`},
	}
	for _, check := range checks {
		var n int
		if err := db.QueryRow(check.query).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("legacy source contains %s (%d groups/rows); resolve explicitly before import", check.name, n)
		}
	}
	return nil
}
