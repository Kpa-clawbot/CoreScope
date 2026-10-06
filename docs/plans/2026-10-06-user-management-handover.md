# User Management — Handover (2026-10-06, end of work-PC session)

Branch `feat/user-management` on the fork `efiten/CoreScope`, based on `upstream/master` @ `43634034`.
Spec: `docs/specs/2026-10-06-user-management-design.md`. Plans: `docs/plans/2026-10-06-user-management-a*.md`.

## State

**A1 is done.** `internal/users` and `internal/mailer` are built, reviewed and tested.

- `internal/users`:
  - Commits: one per plan task 1–7.
  - Review fix commit `a0c3bcf4`:
    - invisible characters written as escapes
    - mail addresses hashed on prune/delete, per row
    - argon2 parameters validated (no panic on bad hashes)
    - token burn guarded with `used_at IS NULL`
    - limits clamped
    - `?` and `#` rejected in `dbPath`
- `internal/mailer`:
  - Commits: tasks 8–10.
  - Hardening commit `b866bfc1`:
    - the HTTP client never follows redirects, so the `api-key` header can't leak to another host
    - empty messages are rejected
    - nil-client fallback
    - consistent date fallback
    - `Fake.SetEvents` stores a copy
- `cd internal/<module> && go vet ./... && go test -count=1 ./...` passes for both.
- **`-race` was not run.** The work PC has no gcc/cgo. Run `go test -race ./...` in both modules on the home PC; CI runs it as well once A3 Task 7 lands.

`cmd/server` and `public/` are untouched, so with the feature off nothing in the product has changed yet.

## Deviations from the plan (already applied)

- **Invisible characters.** The A1 plan file contained literal invisible characters
  (ZWJ, RLO, ZWSP, LS/PS) instead of escape sequences, which is a Trojan-Source risk.
  The plan and the code both use `\uXXXX` escapes now.
- **Extra hardening.** The fixes listed under State go beyond the plan text. They came
  out of the reviews. Some function bodies differ from the plan snippets
  (`Delete`, `PruneStalePending`, `VerifyPassword`, `ConsumeToken`, `LookupSession`,
  `Brevo.Send`, `Brevo.do`). The code is authoritative.
- **A2 plan, Task 7 (webhook) adjusted.** An authenticated but unusable payload now
  answers **200** and logs. It used to answer 400, and Brevo would keep retrying. The
  test expectation for `"garbage"` changed to 200.

## Next: A2 on the home PC

1. `git fetch origin && git switch feat/user-management && git pull`
2. Optional: `git fetch upstream` and rebase on `upstream/master` if it has moved.
3. Run `go test -race ./...` in `internal/users` and `internal/mailer`.
4. Execute `docs/plans/2026-10-06-user-management-a2-server.md`. Note the plan's snippets
   predate the A1 hardening; the A1 public API is unchanged.
5. Then A3, then test on the fork (staging with a real Brevo key, per A3 "done when").
6. Only after fork testing: open **one upstream issue** listing every issue this resolves
   or affects, then the PR. Candidates: #2092, #1835, #895, #1765/#1767, #106, #381,
   #665, #2100, #502/#288, #1508, #331, #819, #775/#663/#664/#666/#730. #725 is a
   constraint: channel keys stay out of the server.

## Notes

- No upstream actions from the work PC (gh account `efiten-opteco` is active there). The
  fork was pushed as `efiten` with `gh auth switch`.
- Commits use author `efiten <erwin.fiten@gmail.com>`.
