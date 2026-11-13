module github.com/corescope/ingestor

go 1.26.0

require (
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/meshcore-analyzer/geofilter v0.0.0
	github.com/meshcore-analyzer/sigvalidate v0.0.0
)

replace github.com/meshcore-analyzer/geofilter => ../../internal/geofilter

replace github.com/meshcore-analyzer/sigvalidate => ../../internal/sigvalidate

require github.com/meshcore-analyzer/packetpath v0.0.0

replace github.com/meshcore-analyzer/packetpath => ../../internal/packetpath

require github.com/meshcore-analyzer/dbconfig v0.0.0

replace github.com/meshcore-analyzer/dbconfig => ../../internal/dbconfig

require github.com/meshcore-analyzer/perfio v0.0.0

replace github.com/meshcore-analyzer/perfio => ../../internal/perfio

require github.com/meshcore-analyzer/dbschema v0.0.0

replace github.com/meshcore-analyzer/dbschema => ../../internal/dbschema

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.11.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

require github.com/meshcore-analyzer/prunequeue v0.0.0

replace github.com/meshcore-analyzer/prunequeue => ../../internal/prunequeue

require (
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/meshcore-analyzer/mbcapqueue v0.0.0
)

replace github.com/meshcore-analyzer/mbcapqueue => ../../internal/mbcapqueue

require (
	github.com/meshcore-analyzer/channel v0.0.0
	github.com/meshcore-analyzer/dbx v0.0.0
)

replace github.com/meshcore-analyzer/channel => ../../internal/channel

replace github.com/meshcore-analyzer/dbx => ../../internal/dbx
