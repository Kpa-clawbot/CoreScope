# CoreScope QA artifacts

Project-specific assets for the [`qa-suite`](https://github.com/Kpa-clawbot/ai-sdlc/tree/master/skills/qa-suite) skill.

## Layout

```
qa/
├── README.md                  ← this file
├── plans/
│   └── <release>.md           ← per-release test plans (one file per RC)
└── scripts/
    ├── api-contract-diff.sh   ← CoreScope-tuned API contract diff
    ├── blacklist-test.sh      ← hide/retain/recover deployment check
    └── test-blacklist-sql.sh  ← disposable native PostgreSQL security gate
```

## How to run

```
qa staging              # use the latest plans/v*-rc.md against staging
qa pr 806               # use plans/pr-806.md if it exists, else latest plans/v*-rc.md
qa v3.6.0-rc            # use plans/v3.6.0-rc.md
```

The parent agent loads the qa-suite skill, which reads:
1. The plan file from `qa/plans/`
2. Bundled scripts from `qa/scripts/`
3. The reusable engine + qa-engineer persona from the skill itself

## Adding a new plan

For each release candidate, copy the latest `plans/v*-rc.md` to `plans/<new-tag>.md` and update:
- The commit-range header (`vN.M..master`)
- Any new sections for new features in the release
- The "Test data" section if new fixture types are needed
- The GO criteria (which sections are blockers)

## Adding a new script

Custom scripts go in `qa/scripts/` with `mode=auto: <script-name>` referenced from the plan. The qa-engineer subagent runs them with two args: `BASELINE_URL TARGET_URL`.

Authoring rules from the qa-suite skill:
- 4-way error classification: `curl-failed` / `parse-empty` / `shape-diff` / field-missing
- Distinguish HTTP errors from jq parse failures
- Don't silence stderr — script bugs must surface
- Exit code = number of failures

## PostgreSQL blacklist retention probe

The active blacklist check reads PostgreSQL using a restricted telemetry reader.
Set `CORESCOPE_READER_DATABASE_URL` privately on the QA runner, or configure native
libpq variables/service files (`PGDATABASE`, `PGUSER`, `PGPASSFILE`, `PGSERVICE`,
and connection/TLS settings). A URL belongs in `CORESCOPE_READER_DATABASE_URL`;
`PGDATABASE` itself does not expand URLs. Do not put credentials in command-line
arguments. `TARGET_DB_PATH` is obsolete and rejected.

With no explicit local connection, the script probes the target container and
then the target host over its existing SSH transport. Configure a private reader
connection there. PostgreSQL 18 `psql` is required; URL parsing also needs Python
3 (`CORESCOPE_QA_PYTHON` can select the executable). Native libpq settings do not
need Python on the target. The shipped container has `psql`; use its private
libpq settings or run the URL-based reader probe locally/on a Python-equipped host.

Every probe checks actual read-only grants and the completed telemetry readiness
marker, then binds the pubkey with psql's native extended-query protocol. SQL and
hex-encoded values travel on stdin. Connection credentials remain in the private
environment. Failures report SQLSTATE classes without printing connection strings.

The local security gate contacts no HTTP endpoint or SSH host:

```sh
# Export CORESCOPE_TEST_POSTGRES_URL privately for a disposable PostgreSQL 18 admin.
bash qa/scripts/test-blacklist-sql.sh
```

The gate requires Python 3, `psql`, Go and a C compiler. It creates temporary
schemas and separate owner/reader LOGIN roles, bootstraps with the shipping
migration tool, and verifies both legitimate and hostile pubkeys. It also prepares
and imports a disposable copy of the committed historical SQLite fixture before
comparing native PostgreSQL counts. SQLite is used only by these explicit offline
tools. Missing database/client prerequisites fail the gate; no SQL checks skip.
`CORESCOPE_QA_MIGRATOR` and `CORESCOPE_QA_PREPARER` may select previously built
matching helper binaries. Temporary objects and private files are removed on exit.
