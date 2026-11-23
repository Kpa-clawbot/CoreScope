-- Version 2: the tables the server and the ingestor exchange state through,
-- so a Postgres deployment needs no filesystem shared between the two.
--
-- corescope_control holds the server -> ingestor requests. It is the only
-- place the read-only server role may write: the NOLOGIN group role
-- corescope_control_client (scripts/postgres-roles.sh grants it to the
-- reader) gets USAGE on the schema, SELECT, INSERT on prune_requests and
-- mbcap_snapshots and SELECT on prune_results from pgschema.EnsureControlGrants,
-- which the ingestor runs at every start. Ids are random hex chosen by the
-- process writing the row, so there are no sequences to grant.
--
-- ingestor_stats (ingestor -> server) lives in the main schema like every
-- other measurement table, so the reader gets SELECT on it through the
-- writer's default privileges; SQLite has the same table (cmd/ingestor/db.go).
-- SQLite has no control tables: SQLite deployments keep the file queues.
--
-- The control schema has a fixed name, so it is shared by every CoreScope
-- schema in one database (the tests run many side by side, each in its own
-- schema): the IF NOT EXISTS clauses let a second schema's migration reuse it.
-- A control schema somebody else owns is refused rather than adopted.
--
-- Never edit this file once shipped: add a new NNNN_name.sql instead.

DO $$
DECLARE
  owner text;
BEGIN
  SELECT pg_get_userbyid(nspowner) INTO owner
    FROM pg_namespace
   WHERE nspname = 'corescope_control'
     AND NOT pg_has_role(current_user, nspowner, 'USAGE');
  IF FOUND THEN
    RAISE EXCEPTION 'schema corescope_control exists and is owned by %, not by %', owner, current_user;
  END IF;
END $$;

CREATE SCHEMA IF NOT EXISTS corescope_control;

CREATE TABLE IF NOT EXISTS corescope_control.prune_requests (
	id         TEXT COLLATE "C" PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16,64}$'),
	created_at TEXT COLLATE "C" NOT NULL,
	payload    TEXT COLLATE "C" NOT NULL CHECK (octet_length(payload) <= 16777216)
);

CREATE TABLE IF NOT EXISTS corescope_control.prune_results (
	id         TEXT COLLATE "C" PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16,64}$'),
	created_at TEXT COLLATE "C" NOT NULL,
	payload    TEXT COLLATE "C" NOT NULL CHECK (octet_length(payload) <= 16777216)
);

CREATE TABLE IF NOT EXISTS corescope_control.mbcap_snapshots (
	id         TEXT COLLATE "C" PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16,64}$'),
	created_at TEXT COLLATE "C" NOT NULL,
	payload    TEXT COLLATE "C" NOT NULL CHECK (octet_length(payload) <= 16777216)
);

CREATE TABLE ingestor_stats (
	id         BIGINT PRIMARY KEY CHECK (id = 1),
	updated_at TEXT COLLATE "C" NOT NULL,
	payload    TEXT COLLATE "C" NOT NULL
);
