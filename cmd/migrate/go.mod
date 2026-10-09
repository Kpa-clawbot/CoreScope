module github.com/corescope/migrate

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.7.6
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/meshcore-analyzer/dbconfig v0.0.0
	github.com/meshcore-analyzer/dbschema v0.0.0
	github.com/meshcore-analyzer/pgutil v0.0.0
	github.com/meshcore-analyzer/sqliteutil v0.0.0
	github.com/meshcore-analyzer/users v0.0.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/sqlite v1.34.5 // indirect
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/meshcore-analyzer/packetpath v0.0.0
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/sys v0.32.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

replace github.com/meshcore-analyzer/dbschema => ../../internal/dbschema

replace github.com/meshcore-analyzer/users => ../../internal/users

replace github.com/meshcore-analyzer/pgutil => ../../internal/pgutil

replace github.com/meshcore-analyzer/packetpath => ../../internal/packetpath

replace github.com/meshcore-analyzer/dbconfig => ../../internal/dbconfig

replace github.com/meshcore-analyzer/sqliteutil => ../../internal/sqliteutil
