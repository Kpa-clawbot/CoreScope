// Fixture: migration block WITHOUT an async annotation and WITHOUT being
// wrapped in the async-migration helper. This file exists ONLY so that
// ~/.openclaw/skills/pr-preflight/scripts/check-async-migrations.sh
// has a known-bad sample to test against (the script is invoked with
// BASE pointing at master and FIXTURE_DIR pointing here).
//
// DO NOT add a PREFLIGHT annotation to this file. DO NOT wrap the
// migration via the async helper. The check script's correctness
// depends on this staying BAD.
//
// IMPORTANT: this file must NOT contain the literal identifier of the
// async-helper function anywhere (comments, strings, identifiers). The
// preflight gate greps a window of lines above the migration for that
// identifier as an "OK" signal, so mentioning it here would cause the
// gate to *pass* this fixture — defeating its purpose. Refer to the
// helper only obliquely as "the async-migration helper" in prose.
package fixtures

const _ = `
CREATE INDEX idx_observations_bad_sync_v1 ON observations(observer_idx, timestamp);
`
