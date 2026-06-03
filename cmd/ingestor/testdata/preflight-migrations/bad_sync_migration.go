// Fixture: migration block WITHOUT an async annotation and WITHOUT being
// wrapped in RunAsyncMigration. This file exists ONLY so that
// ~/.openclaw/skills/pr-preflight/scripts/check-async-migrations.sh
// has a known-bad sample to test against (the script is invoked with
// BASE pointing at master and FIXTURE_DIR pointing here).
//
// DO NOT add a PREFLIGHT annotation to this file. DO NOT wrap the
// migration in RunAsyncMigration. The check script's correctness
// depends on this staying BAD.
package fixtures

const _ = `
CREATE INDEX idx_observations_bad_sync_v1 ON observations(observer_idx, timestamp);
`
