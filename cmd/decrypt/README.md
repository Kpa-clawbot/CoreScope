# corescope-decrypt

Standalone CLI tool to decrypt and export MeshCore hashtag channel messages from CoreScope storage. SQLite is the default; PostgreSQL is optional. Both are opened read-only.

## Why

MeshCore hashtag channels use symmetric encryption where the key is derived deterministically from the channel name. The CoreScope ingestor stores **all** `GRP_TXT` packets in the database, including those it cannot decrypt at ingest time.

This tool enables:

- **Retroactive decryption** — decrypt historical messages for any channel whose name you learn after the fact
- **Forensics & analysis** — export channel traffic for offline review
- **Bulk export** — dump an entire channel's history as JSON, HTML, or plain text

## Installation

### From Docker image

The binary is included in the CoreScope Docker image at `/app/corescope-decrypt`:

```bash
docker exec corescope-prod /app/corescope-decrypt --channel "#wardriving" --db /app/data/meshcore.db
```

### From GitHub release

Download the binary for your platform from the [Releases](https://github.com/Kpa-clawbot/CoreScope/releases) page:

```bash
# Linux amd64
curl -LO https://github.com/Kpa-clawbot/CoreScope/releases/latest/download/corescope-decrypt-linux-amd64
chmod +x corescope-decrypt-linux-amd64
./corescope-decrypt-linux-amd64 --help
```

### Build from source

```bash
cd cmd/decrypt
go build -ldflags="-s -w" -o corescope-decrypt .   # cgo: the SQLite driver needs it
```

Building SQLite support requires a C compiler and CGO enabled. Use the packaged image or a release binary matching your platform.

## Usage

```
corescope-decrypt --channel NAME [--db PATH] [--format FORMAT] [--output FILE]
```

Run `corescope-decrypt --help` for full flag documentation.

### Storage selection

The tool reads storage fields from `config.json`, then `data/config.json` if the first file is absent. `--config FILE` selects an explicit file. An unreadable or malformed selected config is an error. Relative paths use the working directory.

For an installed instance, `storage-selection.json` is authoritative: stale `--db` paths or bootstrap backend settings do not override a completed backend switch. The tool finds that record using `--state-dir`, `CORESCOPE_STATE_DIR`, or configured `stateDir`; otherwise it uses the legacy SQLite database directory (default `data`). Keep the installation's stable state directory when exporting after a switch. A shared lease blocks a switch until the export exits.

Without a recorded installation, `--db PATH` reads an existing SQLite file; the default is `data/meshcore.db`. It never creates, adopts, or migrates a database. Incomplete conversions, missing files, and incompatible schemas fail. Offline legacy files remain readable when they contain the columns needed for export.

For PostgreSQL, supply a restricted reader URL through `CORESCOPE_READER_DATABASE_URL`, or the generic `CORESCOPE_DATABASE_URL`. `--database-url` is available but environment settings keep credentials out of command history. PostgreSQL role and schema-readiness checks remain enforced; a failed connection never falls back to SQLite.

Before a selection has been recorded, `db.backend`, `CORESCOPE_DB_BACKEND`, or `--backend sqlite|postgres` makes the choice explicit; conflicting config/environment selectors fail. Otherwise unambiguous native settings determine the engine. CLI paths/URLs override their environment/config values; environment values override config. `DB_PATH` is the legacy SQLite path variable. Mixed unrecorded file and URL settings require an explicit backend choice.

### JSON output (default)

Machine-readable, includes all metadata (observers, path hops, raw hex):

```bash
corescope-decrypt --channel "#wardriving" --db meshcore.db
```

```json
[
  {
    "hash": "a1b2c3...",
    "timestamp": "2026-04-12T17:19:09Z",
    "sender": "XMD Tag 1",
    "message": "@[MapperBot] 37.76985, -122.40525 [0.3w]",
    "channel": "#wardriving",
    "raw_hex": "150206...",
    "path": ["A3", "B0"],
    "observers": [
      {"name": "Observer1", "snr": 9.5, "rssi": -56, "timestamp": "2026-04-12T17:19:10Z"}
    ]
  }
]
```

### HTML output

Self-contained interactive viewer — search, sortable columns, expandable detail rows:

```bash
corescope-decrypt --channel "#wardriving" --db meshcore.db --format html --output wardriving.html
open wardriving.html
```

No external dependencies. The JSON data is embedded directly in the HTML file.

### IRC / log output

Plain-text, one line per message — ideal for `grep`, `awk`, and piping:

```bash
corescope-decrypt --channel "#wardriving" --db meshcore.db --format irc
```

```
[2026-04-12 17:19:09] <XMD Tag 1> @[MapperBot] 37.76985, -122.40525 [0.3w]
[2026-04-12 17:20:25] <XMD Tag 1> @[MapperBot] 37.78075, -122.39774 [0.3w]
[2026-04-12 17:25:30] <mk 🤠> @[MapperBot] 35.32444, -120.62077
```

```bash
# Find all messages from a specific sender
corescope-decrypt --channel "#wardriving" --db meshcore.db --format irc | grep "KE6QR"
```

## How channel encryption works

MeshCore hashtag channels derive their encryption key from the channel name:

1. **Key derivation**: `AES-128 key = SHA-256("#channelname")[:16]` (first 16 bytes)
2. **Channel hash**: `SHA-256(key)[0]` — 1-byte identifier in the packet header, used for fast filtering
3. **Encryption**: AES-128-ECB
4. **MAC**: HMAC-SHA256 with a 32-byte secret (key + 16 zero bytes), truncated to 2 bytes
5. **Plaintext format**: `timestamp(4 LE) + flags(1) + "sender: message\0"`

See the firmware source at `firmware/src/helpers/BaseChatMesh.cpp` for the canonical implementation.

## Testing against the fixture DB

```bash
cd cmd/decrypt
go test ./...

# Explicit PostgreSQL matrix: requires a disposable administrator URL.
CORESCOPE_TEST_BACKEND=postgres go test ./...

# Manual test with the real fixture:
go run . --channel "#wardriving" --db ../../test-fixtures/e2e-fixture.db --format irc
```

The shared crypto library also has independent tests:

```bash
cd internal/channel
go test -v ./...
```

## Limitations

- **Hashtag channels only.** Only channels where the key is derived from `SHA-256("#name")` are supported. Custom PSK channels require the raw key (not implemented).
- **No DM decryption.** Direct messages (`TXT_MSG`) use per-peer asymmetric encryption and cannot be decrypted by this tool.
- **Read-only.** The tool opens the database in read-only mode and never modifies it.
- **Timestamps are UTC.** The sender's embedded timestamp is used when available, displayed in UTC.
