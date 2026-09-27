-- Synthetic Path Inspector topology (#2060). Apply AFTER fixture migration
-- and timestamp freshening, BEFORE starting the read-only server.
-- Three-byte prefixes meet path trust without changing production thresholds.
-- The shared middle prefix offers two distinct GPS routes: Start-North-End
-- and Start-South-End. No packets/observations are added or reordered.
BEGIN;
INSERT INTO nodes (public_key, name, role, lat, lon, first_seen, last_seen, advert_count) VALUES
  ('f206010000000000000000000000000000000000000000000000000000000000', 'Fixture Path Start', 'repeater', 1.00, 1.00, strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day'), strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 hour'), 1),
  ('f206021111111111111111111111111111111111111111111111111111111111', 'Fixture Path North', 'repeater', 1.01, 1.01, strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day'), strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 hour'), 1),
  ('f206022222222222222222222222222222222222222222222222222222222222', 'Fixture Path South', 'repeater', 0.99, 1.01, strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day'), strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 hour'), 1),
  ('f206030000000000000000000000000000000000000000000000000000000000', 'Fixture Path End', 'repeater', 1.00, 1.02, strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day'), strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 hour'), 1)
ON CONFLICT(public_key) DO UPDATE SET name=excluded.name, role=excluded.role,
  lat=excluded.lat, lon=excluded.lon, first_seen=excluded.first_seen,
  last_seen=excluded.last_seen, advert_count=excluded.advert_count;

-- The server loads this persisted graph at startup and refreshes it from the
-- same table. Edge counts affect ranking, not whether candidates exist or
-- whether a prefix is trusted. Saturated counts keep both routes comfortably
-- above the speculative score cutoff without adding synthetic packet rows.
INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES
  ('f206010000000000000000000000000000000000000000000000000000000000', 'f206021111111111111111111111111111111111111111111111111111111111', 100, strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  ('f206010000000000000000000000000000000000000000000000000000000000', 'f206022222222222222222222222222222222222222222222222222222222222', 100, strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  ('f206021111111111111111111111111111111111111111111111111111111111', 'f206030000000000000000000000000000000000000000000000000000000000', 100, strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  ('f206022222222222222222222222222222222222222222222222222222222222', 'f206030000000000000000000000000000000000000000000000000000000000', 100, strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(node_a, node_b) DO UPDATE SET count=excluded.count, last_seen=excluded.last_seen;
COMMIT;
