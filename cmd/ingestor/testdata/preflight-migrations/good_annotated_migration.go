// Fixture: migration block WITH an async annotation. Companion to
// bad_sync_migration.go. The preflight check script must accept this
// because of the PREFLIGHT line directly above the migration.
package fixtures

// PREFLIGHT: async=true reason="fixture-only — ALTER ADD COLUMN is O(1) in sqlite"
const _ = `
ALTER TABLE observations ADD COLUMN annotated_good_fixture_col INTEGER DEFAULT 0;
`
