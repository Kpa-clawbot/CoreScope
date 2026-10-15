package dbschema

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// BenchmarkObservationInsert measures what the observation_hops triggers add
// to the ingest write path. Each iteration is one observation in its own
// transaction on a file DB opened with WriterDSN (WAL, synchronous=FULL), the
// way the ingestor commits, with a realistic path of 11 hops.
//
//	go test ./internal/dbschema -run xxx -bench ObservationInsert -benchtime 20000x
func BenchmarkObservationInsert(b *testing.B) {
	path := `["0B","1C","D2","25","E4","3D","9B","CF","A1","77","5E"]`
	for _, withIndex := range []bool{false, true} {
		name := "without_index"
		if withIndex {
			name = "with_index"
		}
		b.Run(name, func(b *testing.B) {
			db, err := sql.Open("sqlite3", WriterDSN(filepath.Join(b.TempDir(), "w.db")))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if _, err := db.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY AUTOINCREMENT, path_json TEXT, timestamp INTEGER NOT NULL)`); err != nil {
				b.Fatal(err)
			}
			if withIndex {
				if err := EnsureObservationHopsSchema(db, nil); err != nil {
					b.Fatal(err)
				}
				db.Exec(`UPDATE ` + ObservationHopsCoverageTable + ` SET since = 0`)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx, err := db.Begin()
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.Exec(`INSERT INTO observations (path_json, timestamp) VALUES (?, ?)`, path, 1_800_000_000+i); err != nil {
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
