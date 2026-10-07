# Optional User Management, Sub-project D: Proposals and Approved Hashtag Channels, Design Spec

**Status:** written 2026-10-07 while the operator was away. The decisions in the table
below were taken by Claude with its recommended option, as the operator asked; each one
is open for review and can be reversed.
**Builds on:** A (`docs/specs/2026-10-06-user-management-design.md`), B
(`docs/specs/2026-10-06-user-settings-sync-design.md`) and C
(`docs/specs/2026-10-07-admin-dashboard-design.md`). Roadmap #2128, part D.
**Prior art:** #2092 describes a suggestion flow that runs in a downstream fork
(dborup/CoreScope#99). This spec reuses its state machine, name rules and the rule that
configured channels win, and replaces its anonymous suggestions and file queue with
accounts and `users.db`.

## Problem

Hashtag channels are public by construction: the key is `sha256("#name")[:16]`, so anyone
who knows the name can read the channel. An instance decrypts only the hashtag channels
the operator lists in `hashChannels`. Users who want a channel shown (a city, a club) have
to ask the operator out of band, and the operator edits `config.json` and restarts the
ingestor. #2092 asked for a way to suggest a channel in the UI and have an admin approve
it.

## Goals

1. A logged-in user can propose a hashtag channel; an admin approves, rejects, or later
   revokes it.
2. An approved channel is decrypted by the ingestor without a restart and is listed for
   everyone on the Channels page, also before it has traffic.
3. The proposal mechanism is generic enough that a second kind can be added without a new
   table, but this spec ships exactly one kind.
4. Off by default. With the feature off, nothing changes.

## Non-goals

- Private (PSK) channels. Their keys are secrets and stay in the browser (#725).
- Anonymous proposals. Proposals need an account (attribution, per-user limits).
- Moderating message content.
- Any other proposal kind.

## Decisions (taken by Claude, open for review)

| # | Topic | Decision | Alternative not taken |
|---|---|---|---|
| 1 | Who proposes | Logged-in active users only. | Anonymous visitors with a global rate limit (#2092's model). |
| 2 | Generic vs specific | One generic `proposals` table with a `kind` column; one kind, `hashtag_channel`. | A `channel_proposals` table. |
| 3 | Storage | `users.db` (schema v4), written by the server through `internal/users`. | Analyzer DB through a file queue to the ingestor (#2092). |
| 4 | How the ingestor learns approved channels | The ingestor opens `users.db` read-only and re-reads the approved names every 60 seconds. | Server writes a JSON file the ingestor watches. |
| 5 | Feature flag | `userManagement.channelProposals.enabled`, default false, plus limits. | Always on with user management. |
| 6 | Re-proposal | Revoked can be proposed again (back to pending); rejected stays blocked until pruned; pending/approved duplicates are refused. | Free re-proposal. |
| 7 | Retention | Rejected and revoked proposals are deleted after 90 days; approved and pending are kept. | 30 days (#2092). |

## Name rules (one Go function, mirrored in the browser)

`internal/channel.ValidateHashtagName(s string) (string, error)`:
- Trim surrounding whitespace; prefix `#` if missing.
- At most 31 UTF-8 bytes including `#` (firmware `ChannelDetails.name[32]`, one byte for
  the terminating NUL; `src/helpers/ChannelDetails.h:8` in MeshCore).
- At least one character after `#`.
- No control characters, no bidi controls, no line or paragraph separators, no Unicode
  format characters (Cf) except ZWJ (U+200D), so emoji sequences keep working.
- Case is preserved: the key is derived from the exact string.
- `#public` and `Public` are refused (the built-in Public channel).

The browser mirrors the rules for instant feedback; the server is the authority, and the
ingestor re-validates every name it loads.

## Storage (`internal/users`, schema v4)

```
CREATE TABLE proposals (
  id            INTEGER PRIMARY KEY,
  kind          TEXT NOT NULL,              -- 'hashtag_channel'
  subject       TEXT NOT NULL,              -- the validated name, e.g. '#mycity'
  status        TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','revoked')),
  proposer_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  reviewer_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  note          TEXT NOT NULL DEFAULT '',   -- reviewer's reason, shown to the proposer
  created_at    INTEGER NOT NULL,
  decided_at    INTEGER
);
CREATE UNIQUE INDEX proposals_kind_subject ON proposals(kind, subject);
CREATE INDEX proposals_status ON proposals(status);
```

One row per (kind, subject). State changes update the row; history is in the audit log.

State machine (enforced in one transaction per change):

| From | Action | To |
|---|---|---|
| (none) | propose | pending |
| revoked | propose | pending (proposer replaced by the new one) |
| rejected | propose | refused until the row is pruned |
| pending | approve | approved |
| pending | reject | rejected |
| approved | revoke | revoked |
| pending, approved | propose | refused ("already proposed" / "already approved") |

Store methods: `Propose(kind, subject, userID)`, `Decide(id, action, reviewerID, note)`,
`ListProposals(filter)`, `ApprovedSubjects(kind)`, `ProposalsByUser(userID)`,
`PruneProposals(maxAge)`.

## Config

```json
"userManagement": {
  "channelProposals": {
    "enabled": false,
    "maxPending": 100,
    "maxApproved": 128,
    "perUserPerDay": 5
  }
}
```

`maxApproved` bounds the ingestor's key set: every extra key is tried on every GRP_TXT
packet (`decodeGrpTxt` iterates the whole map).

## Server (`cmd/server`, routes only when user management and proposals are on)

- `POST /api/proposals` `{kind, subject}` (withUser): validates the name, applies the
  per-user and global limits, returns the proposal. Errors: 400 invalid name, 409
  duplicate or rejected, 429 limit.
- `GET /api/account/proposals` (withUser): the caller's own proposals and their status.
- `GET /api/admin/proposals?status=&kind=` (withAdmin): the review list with proposer
  and reviewer display names.
- `POST /api/admin/proposals/{id}/{approve|reject|revoke}` `{note}` (withAdmin). Approve
  refuses when `maxApproved` is reached.
- `GET /api/channels` gains `approvedChannels: ["#name", ...]` when the feature is on, so
  the page lists them before they have traffic. Off: the response is unchanged.
- Audit actions: `proposal.create`, `proposal.approve`, `proposal.reject`,
  `proposal.revoke`, with `{kind, subject}` in detail.
- The janitor prunes rejected and revoked proposals older than 90 days.
- All routes in OpenAPI under the `users` tag.

## Ingestor (`cmd/ingestor`)

- Config: parse `userManagement.enabled`, `userManagement.dbPath` and
  `userManagement.channelProposals.enabled` from the shared `config.json`; resolve the
  `users.db` path with the server's rule (`dbPath`, else `users.db` next to the analyzer
  DB).
- When enabled and the file exists: open it with `mode=ro`, and every 60 seconds read
  `ApprovedSubjects('hashtag_channel')` with raw SQL (no `internal/users` import), re-run
  `ValidateHashtagName`, derive keys, and swap a new key map in through an
  `atomic.Pointer[map[string]string]`. Each message decodes against the current snapshot.
- Merge order: approved names are added below built-in, `channel-rainbow.json`,
  `hashChannels` and `channelKeys`. A configured name always wins, so revoking a proposal
  never stops decryption of a configured channel.
- Revoke removes the key from the next snapshot: future packets are no longer decrypted;
  messages already stored stay as they are.
- A missing or unreadable `users.db` logs once and keeps the configured keys.
- Read/write invariant: the ingestor gains a read-only dependency on `users.db`; it never
  writes it. AGENTS.md is updated to say so.

## Frontend

- Channels page, add-channel dialog: a "Propose for everyone" button next to the existing
  hashtag "Monitor" action, shown only to logged-in users when the feature is on. It
  validates the name live and shows the result ("Proposed, an admin will review it").
- Channel list: approved channels show even without traffic, with a small "approved"
  marker.
- Account page: a "My proposals" section with status and the reviewer's note.
- Admin area: a fourth tab "Proposals" (`#/admin?tab=proposals&status=pending`) with
  approve / reject / revoke, an optional note, and a warning on approve that the channel
  becomes readable for everyone on this instance.

## Security and privacy

- Approving makes a channel's messages readable by every visitor. Nothing is
  auto-approved; the approve dialog states this.
- Proposals carry the proposer's account; only admins see who proposed what.
- PSK channels are out of scope; no secret key ever reaches the server (#725).
- Rate limits: per user per day, plus the global pending cap.

## Performance

Proposals live in `users.db` and are small. The ingestor reads at most `maxApproved`
names once a minute and swaps a map; decode cost grows with the number of keys, bounded
by `maxApproved` (default 128).

## Testing

- `internal/channel`: name rules table (31-byte limit with multibyte characters, ZWJ
  emoji accepted, bidi and Cf refused, Public refused, case preserved).
- `internal/users`: migration v3 → v4; every state transition, refused transitions,
  duplicates, prune.
- `cmd/server`: each route (auth, CSRF, limits, 400/409/429), `approvedChannels` on and
  off, audit rows, feature off: routes absent and `/api/channels` unchanged.
- `cmd/ingestor`: loads approved names from a read-only `users.db`, configured names win,
  revoke removes the key from the next snapshot, missing file keeps configured keys,
  concurrent decode while swapping (race detector).
- Frontend unit (vm): propose flow states, admin tab actions, escaping of names.
- Playwright (e2etest build): a user proposes `#e2e-test`, the admin approves it, the
  channel appears in the list; revoke removes it from the approved list.
