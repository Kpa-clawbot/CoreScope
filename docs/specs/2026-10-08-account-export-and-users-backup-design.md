# Optional User Management: Account Data Export and users.db Backup, Design Spec

**Status:** approved in conversation 2026-10-08.
**Builds on:** A to E of optional user management (`docs/specs/2026-10-06-user-management-design.md`
and the later parts). Roadmap #2128. Small follow-up, no new schema version.

## Problem

1. An instance with accounts holds personal data (address, name, settings, audit and mail
   history). A user has no way to get a copy of it. On an EU-hosted instance the user has
   a right to that copy (GDPR art. 15 and 20). Self-delete already exists
   (`DELETE /api/account`).
2. `users.db` is the only file the server writes. Nothing backs it up: the existing
   `GET /api/backup` snapshots the analyzer database only. Losing `users.db` loses every
   account, setting, proposal and watch.

## Goals

1. A logged-in user downloads all data the instance holds about them as one JSON file.
2. The server keeps rotating local snapshots of `users.db`, and an admin can download a
   consistent snapshot to keep off the server.
3. With user management off, nothing changes.

## Non-goals

- Importing an export into another instance.
- Backing up the analyzer database (exists: `GET /api/backup`).
- Off-site upload (S3, rsync). The admin download and the documented restore cover it.

## 1. Data export

- `GET /api/account/export` (withUser; GET, so no CSRF token is needed and a plain link
  works). Response `application/json`, `Content-Disposition: attachment;
  filename="corescope-account-<YYYY-MM-DD>.json"`, `Cache-Control: no-store`.
- Body: one typed Go struct (no `map[string]interface{}`):
  - `exportedAt`, `instance` (the configured `publicBaseUrl`), `formatVersion: 1`
  - `profile`: id, email, displayName, role, status, createdAt, lastLoginAt,
    emailBouncing
  - `sessions`: per session createdAt, lastSeenAt and the stored user agent if any;
    no token or token hash
  - `settings`: the synced settings document as stored (B), revision and updatedAt, or
    null
  - `proposals`: the user's own proposals (kind, subject, status, note, createdAt,
    decidedAt)
  - `notifications`: prefs (enabled, events) and watches (pubkey, createdAt); no
    unsubscribe token
  - `audit`: every audit row where the user is actor or target (time, action, detail);
    another account in a row appears as its id only, never its address or name
  - `mail`: every mail log row for the user (purpose, sentAt, final status and events)
- Excluded on purpose: password hash, session and activation/reset token hashes, the
  unsubscribe token. They are credentials, not data about the person, and an export file
  is easily mailed around.
- Audit: `user.export` (actor and target the user). The export reflects the state before
  that row is written.
- No limits on row counts: per-user data is small (audit and mail are pruned by the
  janitor).
- Frontend: a "Download my data" button on the account page (a plain link to the route),
  with one line explaining what it contains.

## 2. users.db backup

### Scheduled snapshots

- Config:

  ```json
  "userManagement": {
    "backup": { "enabled": true, "dir": "", "keep": 7 }
  }
  ```

  Absent block: enabled with the defaults (user management on implies a backup).
  `enabled: false` turns it off. `dir` empty means `backups/` next to `users.db`.
  `keep` at or below 0 falls back to 7.
- The server takes a snapshot with `VACUUM INTO` through `internal/users`
  (`Store.Snapshot(path)`), the same technique as `GET /api/backup`. The server's write
  path stays `internal/users` only (AGENTS.md, read/write separation).
- Schedule: once at startup when the newest snapshot is older than 24 hours (or none
  exists), then every 24 hours from the janitor loop. File name
  `users-<YYYYMMDD-HHMMSS>.db`, written to a temporary name and renamed when complete, mode
  0600, directory created with 0700.
- Rotation: after a successful snapshot, delete the oldest `users-*.db` files in that
  directory beyond `keep`. Files not matching that pattern are never touched.
- A failed snapshot logs `[users] backup failed: <err>` and leaves existing snapshots in
  place; the next run tries again.
- Log on success: `[users] backup written: <absolute path> (<n> bytes, kept <k>)`.

### Admin download

- `GET /api/admin/users-backup` (withAdmin): takes a fresh snapshot into a temp
  directory and streams it as `application/octet-stream`,
  `filename="corescope-users-<YYYYMMDD-HHMMSS>.db"`, `Cache-Control: no-store`; the temp
  file is removed afterwards.
- Audit: `user.backup` (actor the admin).
- The file contains password hashes and addresses, so the route is admin-only and the
  docs say to store the download encrypted.

### Restore (documented, not automated)

Stop the server, copy the snapshot over `users.db` (remove `users.db-wal` and
`users.db-shm`), start the server. The schema version check refuses a snapshot from a
newer binary.

## Security and privacy

- The export is for the logged-in user only; there is no admin "export any user" route.
- Snapshot files and the download contain credentials (hashes); 0600 files, admin-only
  download, audit rows for both actions.

## Performance

`users.db` is small (kilobytes to a few megabytes); `VACUUM INTO` takes milliseconds and
runs once a day plus on admin request. The export is a handful of per-user queries. No
analyzer hot path is touched.

## Testing

- `internal/users`: `Snapshot` produces a valid database with the same rows; refuses an
  existing target path.
- `cmd/server` export: every section present with the right rows; credentials absent
  (assert no hash or token value appears anywhere in the body); other accounts appear as
  id only; 401 without session; audit row; feature off: route absent.
- Scheduled backup: snapshot written at startup when none exists, skipped when a fresh
  one exists, rotation keeps `keep` newest and ignores foreign files, failure keeps old
  files, file mode 0600 (skipped on Windows), disabled by config.
- Admin download: 403 for a user, 401 without session, body is a valid SQLite file,
  temp file removed, audit row.
- Frontend unit (vm): the button renders for a logged-in user and links to the route.
- Playwright (e2etest build): the account page shows the button; fetching the export as
  the logged-in user returns JSON with the user's address.
