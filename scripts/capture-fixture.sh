#!/bin/bash
# Capture a fixture DB from staging for E2E tests
# Usage: ./scripts/capture-fixture.sh [source_url]
#
# Downloads nodes, observers, and recent packets from the staging API
# and creates a SQLite database suitable for E2E testing.

set -e

SOURCE_URL="${1:-https://analyzer.00id.net}"
DB_PATH="test-fixtures/e2e-fixture.db"

echo "Capturing fixture from $SOURCE_URL..."

mkdir -p test-fixtures
rm -f "$DB_PATH"

# Create schema
sqlite3 "$DB_PATH" <<'SQL'
CREATE TABLE nodes (
    public_key TEXT PRIMARY KEY,
    name TEXT,
    role TEXT,
    lat REAL,
    lon REAL,
    last_seen TEXT,
    first_seen TEXT,
    advert_count INTEGER DEFAULT 0,
    battery_mv INTEGER,
    temperature_c REAL
);

CREATE TABLE observers (
    id TEXT PRIMARY KEY,
    name TEXT,
    iata TEXT,
    last_seen TEXT,
    first_seen TEXT,
    packet_count INTEGER DEFAULT 0,
    model TEXT,
    firmware TEXT,
    client_version TEXT,
    radio TEXT,
    battery_mv INTEGER,
    uptime_secs INTEGER,
    noise_floor REAL
);

CREATE TABLE transmissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    raw_hex TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    first_seen TEXT NOT NULL,
    route_type INTEGER,
    payload_type INTEGER,
    payload_version INTEGER,
    decoded_json TEXT,
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
    observer_idx INTEGER,
    direction TEXT,
    snr REAL,
    rssi REAL,
    score INTEGER,
    path_json TEXT,
    timestamp INTEGER NOT NULL
);
SQL

# Fetch nodes
echo "Fetching nodes..."
curl -sf "$SOURCE_URL/api/nodes?limit=200" | python3 -c "
import json, sys, sqlite3
data = json.load(sys.stdin)
nodes = data.get('nodes', data) if isinstance(data, dict) else data
db = sqlite3.connect('$DB_PATH')
for n in nodes[:200]:
    db.execute('INSERT OR IGNORE INTO nodes VALUES (?,?,?,?,?,?,?,?,?,?)',
        (n.get('public_key',''), n.get('name',''), n.get('role',''),
         n.get('lat'), n.get('lon'), n.get('last_seen',''), n.get('first_seen',''),
         n.get('advert_count',0), n.get('battery_mv'), n.get('temperature_c')))
db.commit()
print(f'  Inserted {min(len(nodes), 200)} nodes')
db.close()
"

# Fetch observers
echo "Fetching observers..."
curl -sf "$SOURCE_URL/api/observers" | python3 -c "
import json, sys, sqlite3
data = json.load(sys.stdin)
observers = data.get('observers', data) if isinstance(data, dict) else data
db = sqlite3.connect('$DB_PATH')
for o in observers:
    db.execute('INSERT OR IGNORE INTO observers VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)',
        (o.get('id',''), o.get('name',''), o.get('iata',''),
         o.get('last_seen',''), o.get('first_seen',''),
         o.get('packet_count',0), o.get('model',''), o.get('firmware',''),
         o.get('client_version',''), o.get('radio',''),
         o.get('battery_mv'), o.get('uptime_secs'), o.get('noise_floor')))
db.commit()
print(f'  Inserted {len(observers)} observers')
db.close()
"

# Fetch recent packets
echo "Fetching recent packets..."
curl -sf "$SOURCE_URL/api/packets?limit=500" | python3 -c "
import json, sys, sqlite3
data = json.load(sys.stdin)
packets = data.get('packets', data) if isinstance(data, dict) else data
db = sqlite3.connect('$DB_PATH')
for p in packets:
    try:
        cur = db.execute('INSERT OR IGNORE INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?,?,?,?,?,?,?)',
            (p.get('raw_hex',''), p.get('hash',''), p.get('first_seen',''),
             p.get('route_type'), p.get('payload_type'), p.get('payload_version'),
             p.get('decoded_json')))
        tid = cur.lastrowid
        if tid and p.get('observer_id'):
            db.execute('INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp) VALUES (?,?,?,?,?,?,?,?)',
                (tid, p.get('observer_id'), p.get('direction'),
                 p.get('snr'), p.get('rssi'), None,
                 p.get('path_json'),
                 int(p.get('timestamp','0')) if p.get('timestamp','').isdigit() else 0))
    except Exception as e:
        pass  # Skip duplicates
db.commit()
print(f'  Inserted {len(packets)} transmissions')
db.close()
"

SIZE=$(du -h "$DB_PATH" | cut -f1)
echo "✅ Fixture DB created: $DB_PATH ($SIZE)"
echo "   Nodes: $(sqlite3 "$DB_PATH" 'SELECT COUNT(*) FROM nodes')"
echo "   Observers: $(sqlite3 "$DB_PATH" 'SELECT COUNT(*) FROM observers')"
echo "   Transmissions: $(sqlite3 "$DB_PATH" 'SELECT COUNT(*) FROM transmissions')"
echo "   Observations: $(sqlite3 "$DB_PATH" 'SELECT COUNT(*) FROM observations')"
