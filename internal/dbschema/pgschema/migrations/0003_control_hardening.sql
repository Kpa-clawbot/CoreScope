-- Version 3: the control tables (0002) no longer trust the server for what it
-- need not choose.
--
-- created_at comes from the database clock: the DEFAULT is the queues' pgNow
-- (internal/prunequeue, internal/mbcapqueue) to the character, so rows written
-- before and after this migration keep sorting in time order as text.
-- pgschema.EnsureControlGrants gives the control role column-level
-- INSERT (id, payload) only, so the server cannot set created_at at all.
--
-- The payload caps shrink to what the queues ever send: 1 MiB for a prune
-- request or result, 4 MiB for a multibyte-capability snapshot. 0002's 16 MiB
-- CHECKs stay (they are implied by these). The new constraints are added
-- NOT VALID and then validated: the existing rows are checked (a request
-- table holds a handful of small rows) and never rewritten; one larger than
-- its cap fails the migration, which rolls back and leaves version 2.
--
-- The control schema is shared by every CoreScope schema in the database (see
-- 0002), so this file may run again on tables it already changed: SET DEFAULT
-- is idempotent, each constraint is added only when missing, and VALIDATE of
-- a validated constraint does nothing.
--
-- Never edit this file once shipped: add a new NNNN_name.sql instead.

ALTER TABLE corescope_control.prune_requests
	ALTER COLUMN created_at SET DEFAULT to_char((clock_timestamp()) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"');
ALTER TABLE corescope_control.prune_results
	ALTER COLUMN created_at SET DEFAULT to_char((clock_timestamp()) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"');
ALTER TABLE corescope_control.mbcap_snapshots
	ALTER COLUMN created_at SET DEFAULT to_char((clock_timestamp()) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"');

DO $$
DECLARE
  c record;
BEGIN
  FOR c IN
    SELECT * FROM (VALUES ('prune_requests', 'prune_requests_payload_max', 1048576),
                          ('prune_results', 'prune_results_payload_max', 1048576),
                          ('mbcap_snapshots', 'mbcap_snapshots_payload_max', 4194304)) AS v(tbl, con, max)
  LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = format('corescope_control.%I', c.tbl)::regclass AND conname = c.con) THEN
      EXECUTE format('ALTER TABLE corescope_control.%I ADD CONSTRAINT %I CHECK (octet_length(payload) <= %s) NOT VALID',
                     c.tbl, c.con, c.max);
    END IF;
    EXECUTE format('ALTER TABLE corescope_control.%I VALIDATE CONSTRAINT %I', c.tbl, c.con);
  END LOOP;
END $$;
