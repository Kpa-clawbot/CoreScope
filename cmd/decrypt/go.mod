module github.com/corescope/decrypt

go 1.25.0

require (
	github.com/meshcore-analyzer/channel v0.0.0
	github.com/meshcore-analyzer/dbschema v0.0.0
	github.com/meshcore-analyzer/pgutil v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/meshcore-analyzer/dbconfig v0.0.0
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

replace github.com/meshcore-analyzer/channel => ../../internal/channel

replace github.com/meshcore-analyzer/dbschema => ../../internal/dbschema

replace github.com/meshcore-analyzer/pgutil => ../../internal/pgutil

replace github.com/meshcore-analyzer/dbconfig => ../../internal/dbconfig
