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

## Amendments from the implementation plan

1. Store signatures carry the limits: `Propose(kind, subject, userID, ProposalLimits)`,
   `Decide(id, action, reviewerID, note, maxApproved)` and `ApprovedSubjects(kind, limit)`.
   The limit checks run inside the state-change transaction.
2. The per-user daily limit counts the proposals a user created or re-opened in the last
   24 hours in `users.db`, so it survives a restart.
3. Name rules: a name of only ZWJ is refused like an empty one; Public is refused in any
   case (`#public`, `#Public`, `public`, `Public`).
4. With `maxApproved` lowered below the number of approved channels, the server and the
   ingestor both use the oldest approvals (by decision time) up to the cap.
5. An ingestor read error never removes keys: only a successful read changes the set.
6. The channel list escapes channel names in its data attributes and CSS selectors,
   since approved names may contain `"`, `<` and `\`.
7. `users.db` path: the server and the ingestor keep their existing `DB_PATH`
   precedence. Both log the resolved (absolute) path at startup (server
   `[users] user management enabled: db=<path>`, ingestor
   `[proposals] reading approved channels from <path>`), and the operator docs advise
   setting `userManagement.dbPath` explicitly. If the two disagree on a non-Docker setup,
   the ingestor reads another `users.db`; the log lines make that visible.
8. A proposal for a name the instance already decrypts from `config.json` is refused
   with 409 "this channel is already decrypted on this instance". The server reads only
   the names: `hashChannels` normalised as the ingestor does (trimmed, `#` prefixed) and
   the names of `channelKeys` (key values are discarded while parsing). Since a proposal
   subject always starts with `#`, only `#`-prefixed `channelKeys` names can match. The
   comparison is case-sensitive, like the ingestor's key map.
9. A non-object `channelKeys` is ignored for this check with the log line
   `[config] channelKeys is not an object; ignoring it for channel-proposal checks`,
   instead of failing config loading.
10. Proposing a revoked name again replaces the proposer; the original proposer stays in
    the audit log and no longer sees the proposal under My proposals.
11. Approved channels without traffic are listed in `/api/channels` and on the Channels
    page regardless of the region filter, so a regional view may show a channel with no
    regional traffic.
12. The server reloads its approved-name snapshot from `users.db` under a mutex after
    every decision (approve, reject and revoke), instead of changing it in place, so two
    concurrent decisions cannot leave it stale. The reject case costs one small read and
    keeps a single code path.
13. The hidden and control character check exists twice by design: in `internal/users`
    (display names) and in `internal/channel` (channel names). They are separate Go
    modules and the ingestor must not import `internal/users`. Tests pin both copies;
    the browser mirror in `public/channel-proposals.js` is inherent to live validation.
    Since amendment 14 the channel copy is stricter than the display-name copy.
14. Name rules, tightened before release: besides control, bidi, separator and Cf
    characters, a channel name may not contain the invisible fillers of Unicode
    `Other_Default_Ignorable_Code_Point` (for example U+3164, U+115F, U+FFA0, U+034F),
    U+2800 BRAILLE PATTERN BLANK, or any space separator (Zs) other than the ASCII space,
    so interior NBSP and U+3000 are refused. ZWJ (U+200D) and variation selectors such
    as U+FE0F stay allowed, so emoji like `#❤️` keep working. The message is the existing
    "the name contains invisible or control characters". Reason: `#mesh` followed by
    U+3164 passed for a configured `#mesh`, and the ingestor drops any stored name the
    current rule refuses, so tightening after release would silently stop decrypting
    already approved channels. The browser mirror lists Go's
    `Other_Default_Ignorable_Code_Point` table explicitly (JS has no property escape for
    it); Go and JS give the same verdict for every code point.
15. ZWNJ and tag characters stay refused (operator decision, 2026-10-07). Both are Cf, so
    names that need the zero-width non-joiner (U+200C, some Persian and Urdu spellings)
    and subdivision flags built from tag characters (England, Scotland, Wales) cannot be
    proposed. The user guide says so. Allowing them later only widens the rule, so no
    approved name is stranded by that change.
