# PostgreSQL

CoreScope's measurement database (packets, nodes, observers, and the rest) can live in
SQLite or in PostgreSQL. SQLite is the default and needs no changes. PostgreSQL is an
explicit choice, and nothing migrates automatically. Both backends use the same binaries;
configuration picks one.

This guide covers choosing, setting up, moving to and from, backing up and troubleshooting
a PostgreSQL install. For SQLite maintenance (WAL, auto-vacuum) see the
[Database guide](user-guide/database.md).

- [When to choose PostgreSQL](#when-to-choose-postgresql)
- [Requirements](#requirements)
- [How it works](#how-it-works)
- [Set up with Docker Compose](#set-up-with-docker-compose)
- [Set up without Compose](#set-up-without-compose) (includes the Docker image and Kubernetes)
- [Moving an existing SQLite install](#moving-an-existing-sqlite-install)
- [Switching back to SQLite](#switching-back-to-sqlite)
- [Backups and restore](#backups-and-restore)
- [What is SQLite-only](#what-is-sqlite-only)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)

## When to choose PostgreSQL

Stay on SQLite unless you have a reason. It is one file, needs no extra service, and is the
most exercised path.

Consider PostgreSQL when:

- The install is large or keeps data for a long time, and SQLite's single writer plus its
  maintenance passes (retention prunes, `ANALYZE`, `VACUUM`) stall ingest.
- You already run PostgreSQL and its backup tooling and would rather manage one kind of
  database.

There is no general claim that PostgreSQL is faster. In one operator soak test of an earlier
implementation, retention pruning ran about 2.5x faster on PostgreSQL; choose by workload and
hosting, not by that number.

The result of every API call is the same on both backends. A few SQLite-specific features stay
on SQLite (see [What is SQLite-only](#what-is-sqlite-only)). The optional user accounts live in
a SQLite file, `users.db`, unless you move them into PostgreSQL too (see
[User accounts in PostgreSQL](#user-accounts-in-postgresql)).

## Requirements

- PostgreSQL 16 or newer (tested on 17). The ingestor refuses an older server.
- `psql` 15 or newer on the machine that runs `scripts/postgres-roles.sh` (the script reads
  passwords with `\getenv`). The `postgres:17-alpine` image used by the Compose override
  already has it.
- For moving data: `corescope-migrate`. The Docker image ships it at `/app/corescope-migrate`; otherwise build it from `cmd/migrate`.

## How it works

CoreScope runs with two PostgreSQL roles, and the split is enforced, not advisory.

| Role | Used by | Needs |
|------|---------|-------|
| Writer (default `corescope_writer`) | Ingestor | Owns the database and schema. It creates and migrates the schema (versioned migrations recorded in `schema_version`, applied under a PostgreSQL advisory lock). |
| Reader (default `corescope_reader`) | Server | `USAGE` on the schema and `SELECT` on its tables, plus membership in the group role below. Nothing else. |
| `corescope_control_client` (fixed name, `NOLOGIN` group role) | Server, through the reader | `SELECT` and `INSERT` on the geo-prune request and multibyte-snapshot tables and `SELECT` on the prune results, all in the `corescope_control` schema. |

The ingestor is the only process that writes measurement data, as it is with SQLite. It keeps
one writer connection on PostgreSQL too, because the server follows new rows by "id greater
than the last one seen" and ids must therefore commit in increasing order.

The server and the ingestor share no files on PostgreSQL. What used to travel through files
goes through the database instead:

- geo-prune requests and results, and the multibyte-capability snapshot, through the
  `corescope_control` schema (the server inserts a request; the ingestor performs the deletion
  and stores the result),
- the ingestor's statistics (perf page, `/api/healthz` ingest liveness, `/api/mqtt/status`)
  through the `ingestor_stats` table.

The server writes only those control rows, through a separate pool of at most two connections
(plus the account store's own connection, when accounts are in PostgreSQL; see
[User accounts in PostgreSQL](#user-accounts-in-postgresql)).
Its main pool stays forced read-only. The control pool is opened only when the server's role is a
member of `corescope_control_client` and that group holds exactly the control grants above; the
ingestor re-applies those grants at every start. If not, the server still starts, logs one
warning, answers the geo-prune endpoints with 503 and does not publish the multibyte snapshot
(see [Troubleshooting](#troubleshooting)). Measurement writes are still refused at startup.

The server is read-only:

- It sets `default_transaction_read_only=on` on every connection.
- At startup it checks the privileges of its role and **refuses to start** if that role holds
  `INSERT`, `UPDATE`, `DELETE` or `TRUNCATE` on any table in its schema. Ownership, superuser
  and privileges inherited through role membership all count. It applies the same rule to the
  `corescope_control` schema: any write power there other than through
  `corescope_control_client` also refuses startup. The error prints the `GRANT`
  statements for a suitable read-only role.
- It also refuses to start when the schema is missing or older than it expects. Start the
  ingestor first.

`scripts/postgres-roles.sh` creates both roles and the database. Run it rather than writing
the grants by hand.

## Set up with Docker Compose

`docker-compose.postgres.yml` is an override for `docker-compose.yml`. It adds:

- a `postgres` service (`postgres:17-alpine`, data in the named volume `postgres-data`),
- a one-shot `postgres-init` service that runs `scripts/postgres-roles.sh` on every `up`
  (it is idempotent, so each start re-applies the roles' passwords and grants),
- `DB_DRIVER=postgres`, `DISABLE_INGESTOR=true` and only the reader DSN (`DB_DSN_SERVER`) on
  `prod`, which now runs Caddy, Mosquitto and the server,
- an `ingestor` service: the same image running `/app/corescope-ingestor` alone, with the writer
  DSN. It runs as a non-root uid (`INGESTOR_UID`, default 1000) with the share group added
  (gid `CORESCOPE_SHARE_GID`, default 61001), read-only, all capabilities dropped. It joins
  `prod`'s network namespace, so `mqtt://localhost:1883` in `config.json` still reaches the
  broker, and it mounts the same data directory.

The two containers are the security boundary for the writer credential: it is never in `prod`'s
environment or files. Because `ingestor` shares `prod`'s network namespace, restarting or
recreating `prod` alone leaves the ingestor attached to a dead namespace. Use
`docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d`, which restarts
both, not `docker restart` on `prod`.

1. Add three secrets to `.env`. Generate each one, for example with `openssl rand -hex 24`;
   the DSNs carry them unencoded, so keep them to URL-safe characters.

   ```
   POSTGRES_PASSWORD=<postgres-superuser-password>
   CORESCOPE_WRITER_PASSWORD=<writer-password>
   CORESCOPE_READER_PASSWORD=<reader-password>
   ```

   `POSTGRES_PASSWORD` is the `postgres` superuser, used only by `postgres-init`. Optional
   keys: `CORESCOPE_DB` (default `corescope`), `CORESCOPE_WRITER` (`corescope_writer`),
   `CORESCOPE_READER` (`corescope_reader`), `SERVER_RUN_AS_UID` (1001), `INGESTOR_UID` and
   `INGESTOR_GID` (1000), `CORESCOPE_SHARE_GID` (61001). The two uids must differ from each
   other and from 0.

2. Start the stack:

   ```bash
   docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d
   ```

   The ingestor creates the schema on its first start. Until it exists, the server refuses to
   start (see [Troubleshooting](#troubleshooting)).

3. Do **not** set `db.driver` or `db.dsn` in `config.json`. On PostgreSQL the database is chosen
   from the container's environment only (`DB_DRIVER`, `DB_DSN*`); a `config.json` that selects
   PostgreSQL, or contains a `dsn`, is refused at start.

`.env` lives beside the compose file and holds the writer password. Keep it out of git and make
it readable only by the account that runs Compose (`chmod 600 .env`). Do not put it, or any file
holding a DSN, in the data directory mounted at `/app/data`.

PostgreSQL is not published to the host; it is reachable only on the Compose network. To use
`psql` or `pg_dump` from the host, add this to the `postgres` service:

```yaml
    ports: ["127.0.0.1:5432:5432"]
```

Keep the `/app/data` mount. See [In the Docker image](#in-the-docker-image) for what the
container does with it on PostgreSQL (`/app/data/shared`, the separate server user). The server
and the ingestor no longer exchange files: prune requests, the multibyte snapshot and the
ingestor's statistics go through the database. What still needs the directory is `users.db` (and
its backups) when user accounts are enabled and kept in SQLite, plus the data directory's
`config.json`. With accounts in PostgreSQL the server writes nothing to disk.

If you already have SQLite data, copy it before the first start; see
[Moving an existing SQLite install](#moving-an-existing-sqlite-install).

## Set up without Compose

1. Provision the roles and database as a PostgreSQL superuser:

   ```bash
   export PG_ADMIN_URL='postgres://postgres@db.example.org:5432/postgres?sslmode=require'
   export PGPASSWORD='<superuser-password>'
   export CORESCOPE_WRITER_PASSWORD='<writer-password>'
   export CORESCOPE_READER_PASSWORD='<reader-password>'
   sh scripts/postgres-roles.sh
   ```

   | Variable | Default | |
   |----------|---------|---|
   | `PG_ADMIN_URL` | — | Superuser connection, URL or conninfo. Required. |
   | `CORESCOPE_DB` | `corescope` | Database name |
   | `CORESCOPE_WRITER` | `corescope_writer` | Writer role |
   | `CORESCOPE_WRITER_PASSWORD` | — | Required |
   | `CORESCOPE_READER` | `corescope_reader` | Reader role |
   | `CORESCOPE_READER_PASSWORD` | — | Required |
   | `CORESCOPE_ACCOUNTS` | `corescope_accounts` | Accounts role name (only with the next variable) |
   | `CORESCOPE_ACCOUNTS_PASSWORD` | unset | Optional. When set, also creates the accounts role and schema `corescope_users`; see [User accounts in PostgreSQL](#user-accounts-in-postgresql). Unset: neither is created. |

   Names must match `^[a-z_][a-z0-9_]{0,62}$`, must not start with `pg_`, and must not be an
   existing superuser. The script is idempotent and never drops anything: a second run resets
   both passwords and re-applies the grants, revoking any write privilege the reader has since
   been given. Passwords never appear on a command line or in the output, but a server with
   `log_statement=ddl` or `all` logs them as it would for any `ALTER ROLE`.

   The script also creates the group role `corescope_control_client` (fixed name, `NOLOGIN`)
   and grants it to the reader as the reader's only membership. The ingestor grants the group its
   privileges on the control tables at every start; the script grants the same when the tables
   already exist, so either may run first. That is how an **existing install gets the control
   role after an upgrade**: re-run the script with the same environment as at setup (it is safe to
   repeat), then restart the ingestor. Until then geo-prune and the multibyte snapshot are off
   (see [Troubleshooting](#troubleshooting)). The group role's name is fixed and cluster-wide, so
   the script refuses it as a writer or reader name.

   **One CoreScope install per database.** The `corescope_control` schema and `ingestor_stats`
   belong to the database, so everything connected to one database shares them: do not point two
   separate CoreScope installs at the same database (give each its own database, and its own
   writer and reader roles).

   It prints the two connection URLs without passwords:

   ```
   CORESCOPE_WRITER_URL=postgres://corescope_writer@db.example.org:5432/corescope
   CORESCOPE_READER_URL=postgres://corescope_reader@db.example.org:5432/corescope
   ```

   Host and port are the ones the admin connection reached. Replace them if the application
   connects through another address, and add `sslmode` yourself.

   The script refuses a `PG_ADMIN_URL` that contains a password: supply it with `PGPASSWORD` (as
   above) or `~/.pgpass`, so it never appears in `ps` or shell history. It also refuses to adopt
   a database or role that already exists but that it did not create, unless you set
   `CORESCOPE_ADOPT=1`; set that only when you mean to hand an existing database to CoreScope.

2. Point CoreScope at the database. The ingestor gets the **writer** DSN, the server the
   **reader** DSN, so the two processes need different values.

   | Setting | Meaning |
   |---------|---------|
   | `db.driver` in `config.json` | `postgres` (aliases `postgresql`, `pg`; `sqlite`, `sqlite3` or empty is SQLite) |
   | `db.dsn` in `config.json` | `postgres://user:pass@host:5432/dbname?sslmode=require` |
   | `DB_DRIVER` | Same as `db.driver` |
   | `DB_DSN` | Same as `db.dsn` |

   Precedence follows each binary's `DB_PATH` rule. In the ingestor the environment overrides
   `config.json`. In the server `config.json` wins and the environment only fills empty keys.
   Because each process needs its own DSN, the simplest setup is the environment, set per
   process from a file only that process's user can read (for example a systemd
   `EnvironmentFile=`):

   ```
   # /etc/corescope/ingestor.env   (mode 600, owned by the ingestor's user)
   DB_DRIVER=postgres
   DB_DSN=postgres://corescope_writer:<writer-password>@db.example.org:5432/corescope?sslmode=require

   # /etc/corescope/server.env     (mode 600, owned by the server's user)
   DB_DRIVER=postgres
   DB_DSN=postgres://corescope_reader:<reader-password>@db.example.org:5432/corescope?sslmode=require
   ```

   A password in a DSN that the binaries read from the config or the environment is acceptable
   (it is not on any command line), but keep those files out of git, out of world-readable
   locations and out of the data directory, and above all keep the writer's DSN away from the
   server's user.

   `db.dsn` must be a `postgres://` URL or a `keyword=value` string. If you use a shared
   `config.json` for both processes, do not put a DSN in it; use the environment.

3. Start the ingestor first, then the server.

   Percent-encode special characters in a password inside a URL (for example `@` as `%40`).

   | Optional setting | Default | Meaning |
   |------------------|---------|---------|
   | `db.statementTimeoutSec` | `120` | PostgreSQL only: the server's sessions get this `statement_timeout`, in seconds. `0` disables it; a negative value is a startup error. It is set in `config.json` (it is not a secret, so it may live there even where a `dsn` may not). |

### In the Docker image

On PostgreSQL, run the ingestor in its own container, as `docker-compose.postgres.yml` does:
the writer credential then never reaches the container that runs Caddy and the server.

| Container | Settings |
|-----------|----------|
| Ingestor (writer) | `DB_DRIVER=postgres`, `DB_DSN` (the writer DSN); run `/app/corescope-ingestor` directly |
| Server (reader) | `DB_DRIVER=postgres`, `DISABLE_INGESTOR=true`, `DB_DSN_SERVER` (or `DB_DSN`, the reader DSN) |

With `DISABLE_INGESTOR=true` the container has no ingestor, and refuses to start if
`DB_DSN_INGESTOR` is set. `DB_DSN`, `DB_DSN_INGESTOR`, `DB_DSN_SERVER` and `DB_PATH` must not
contain a double quote, a backslash or a newline; use a `postgres://` URL and percent-encode the
password. Keep DSNs out of `config.json`, version control and shell history; put them in `.env`
or a secret store.

**Single container (not recommended).** Running both services in one container still works: set
`DB_DSN_INGESTOR` (writer) and `DB_DSN_SERVER` (reader), each defaulting to `DB_DSN`, and the
entrypoint gives each process its own as `DB_DSN`. The container logs a warning, and it is not
the credential boundary: both DSNs are in the container, and Caddy runs as root.

Whichever layout, the entrypoint arranges the following on PostgreSQL:

- Postgres mode is decided from the container's environment only (`DB_DRIVER`, `DB_DSN*`). A
  `config.json` that selects PostgreSQL while the environment does not is refused, as is a
  `DB_DRIVER=sqlite` combined with a DSN.
- The server runs as its own account, `corescope-server`, with the uid in `SERVER_RUN_AS_UID`
  (default `1001`; set it in the container's environment, not in `.env`). It must not be 0 (or
  the ingestor's uid in the single-container layout). Its environment is an allow-list
  (`docker/server-exec.sh`), so `PG*`, `POSTGRES_*` and other secrets do not reach it. A new
  environment variable the server must read has to be added to that list.
- What the server writes to disk lives in **`/app/data/shared`**: `users.db` (with its
  `-wal`/`-shm` and `backups/`), only when accounts are enabled and not in PostgreSQL; the
  geo-prune and multibyte exchanges go through the database. The directory is group `corescope-share` (gid `CORESCOPE_SHARE_GID`, default 61001,
  above the range of ordinary users) and sticky. The ingestor's `DB_PATH` points into it (on
  PostgreSQL `DB_PATH` only locates local files such as `users.db`, not the database). A
  `users.db` left in `/app/data` by an earlier SQLite run is moved there once, with its backups.
- If you set `userManagement.dbPath`, it must be under `/app/data/shared`; the entrypoint warns
  otherwise, because the server can write only there. (Unused when accounts are in PostgreSQL.)
- `DB_DSN_ACCOUNTS` (the accounts store, see
  [User accounts in PostgreSQL](#user-accounts-in-postgresql)) is passed to the server only.
  The ingestor never receives it.
- `config.json` must not contain a `dsn`: the container refuses to start. It is made group
  `corescope-share`, mode 0660, so the geo-filter editor can save into it and the ingestor can
  read it; `/app/data` itself is not opened up. It must be readable by the server user.
- `/app/data/.env` must **not** be readable by the server user (`chmod 600`); the container
  refuses to start otherwise.
- The empty-database guard (below) still checks your original SQLite file, through
  `CORESCOPE_SQLITE_GUARD_PATH` (`/app/data/meshcore.db` in the override; the entrypoint sets it
  from your `DB_PATH` in the single-container layout).
- Caddy's admin API is off in the image's Caddyfiles. A custom Caddyfile without a global
  `admin off` block gets a root-only admin socket and a warning at start; add
  `{ admin off }` at its top.

SQLite mode is unchanged: one account for both services and the database where it always was.

### Kubernetes and high availability

PostgreSQL is what lets CoreScope's read side scale out: any number of servers can read the same
database. The write side cannot. Plan the cluster around that asymmetry.

| Component | Replicas | Why |
|---|---|---|
| Ingestor | **exactly 1** | The only writer. Servers follow new rows by "id greater than the last one seen", which is complete only when ids commit in order — one writer, one connection. |
| Server | 2 or more | Read-only on measurement data (its only writes are control requests for the ingestor, see [How it works](#how-it-works)) and independent: each loads its own in-memory store from PostgreSQL and polls for new rows. No sticky sessions are needed; each pushes its own live feed over WebSocket. Verified with five replicas. |
| PostgreSQL | your HA setup | CoreScope needs one stable read-write endpoint (see below). |

> [!WARNING]
> **Never run more than one ingestor against the same database or broker.** Two ingestors
> insert the same packets twice over, commit ids out of order (the servers' live feed then
> silently skips rows), and both connect to MQTT with the same client id, so the broker
> disconnects each one whenever the other connects — they flap and ingest stalls. In practice
> this happens in three ways:
>
> 1. **The ingestor is a container in the server's Deployment** (the single-pod layout of a
>    small install). Scaling that Deployment to N starts N ingestors. Split them first.
> 2. **The ingestor's Deployment uses `RollingUpdate`.** A rollout starts the new pod before
>    stopping the old one. Use `strategy: Recreate`.
> 3. **Migrating from a combined pod to the split layout** while the old pods are still running:
>    the new ingestor and the old pods' ingestors overlap. Scale the old Deployment to 0 (or
>    remove its ingestor container) before starting the new ingestor Deployment.
>
> Never put a HorizontalPodAutoscaler on the ingestor.

**What "HA" means for each piece.** Server replicas make the UI and API survive pod and node
failures. The ingestor is recovered rather than replicated: if it
dies, Kubernetes restarts it. Its MQTT session is a clean session, so packets published while it
is down are not delivered to it later — a restart costs the seconds of traffic it was down for.
That is the trade-off for a single writer; there is no leader election to fail over to a hot
standby.

**No shared data volume.** On PostgreSQL the servers and the ingestor exchange everything through
the database (geo-prune requests and results and the multibyte snapshot in the
`corescope_control` schema, the ingestor's statistics in `ingestor_stats`). No `ReadWriteMany`
volume is needed, servers can be spread across nodes, and the pods need no volume at all. The one
exception is **user management with `users.db`**: by default the accounts are a SQLite file on
disk, written by the server. In that case mount a volume for the server and run a **single
server replica**; several replicas must not share that file. To run several replicas with
accounts, keep them in PostgreSQL instead (`userManagement.dsn` / `DB_DSN_ACCOUNTS`, see
[User accounts in PostgreSQL](#user-accounts-in-postgresql)): the pods then need no volume and
any number of replicas works.

**Control role.** The reader must belong to `corescope_control_client`, which
`scripts/postgres-roles.sh` creates. A cluster set up before this existed needs one re-run of the
script (see [Set up without Compose](#set-up-without-compose)); otherwise geo-prune answers 503.

**PostgreSQL itself.** Point both roles at the **primary** through a stable name — for example
the read-write service of a PostgreSQL operator (CloudNativePG's `<cluster>-rw`). Don't point
servers at a read replica: replication lag makes the live feed late and a lagging replica can be
behind the ingestor's own writes. Connections are ordinary pooled connections, so after a
failover new connections reach the new primary through the same name.

**Network.** Keep PostgreSQL on a `ClusterIP` service only, with a `NetworkPolicy` that admits just
the CoreScope pods (ingestor, servers) and the one-shot copy Job. On clusters whose policy
controller admits a new pod's IP asynchronously (k3s/kube-router), a pod's first connection can be
refused for a second or two; the binaries retry the first connect for up to 30 seconds.

**Roles and secrets.** Run `scripts/postgres-roles.sh` inside the PostgreSQL pod over its local
socket (`kubectl exec … sh -s < scripts/postgres-roles.sh`), with the passwords in that pod's
environment from a Secret, so they never appear on a command line. Keep the writer DSN in a Secret
key mounted **only** into the ingestor and the reader DSN only into the servers. With accounts in
PostgreSQL, also set `CORESCOPE_ACCOUNTS_PASSWORD` for that run and keep the accounts DSN in its
own Secret key (`accounts-dsn` below), mounted **only** into the servers: never into the ingestor.

**Moving data.** Run `corescope-migrate copy` as a one-shot Job (image `/app/corescope-migrate`),
with the ingestor scaled to 0 first, `PGPASSWORD` from the Secret and a password-less URL. If the
SQLite file was last written by a CoreScope version older than this one, start the new image on
SQLite once first so its migrations finish — `copy` refuses an unmigrated source and says so.

#### Reference manifests

A minimal layout (namespace, image, node and storage names are placeholders; add your own
resources, probes and Pod Security settings — the image runs as non-root uid 1000).

```yaml
# Secret "corescope-postgres": keys writer-dsn and reader-dsn, created out of band, e.g.
#   writer-dsn: postgres://corescope_writer:<pw>@postgres:5432/corescope?sslmode=disable
#   reader-dsn: postgres://corescope_reader:<pw>@postgres:5432/corescope?sslmode=disable
# With user accounts in PostgreSQL, a third key (never mounted into the ingestor):
#   accounts-dsn: postgres://corescope_accounts:<pw>@postgres:5432/corescope?sslmode=disable
apiVersion: apps/v1
kind: Deployment
metadata: {name: corescope-ingestor}
spec:
  replicas: 1                      # never more than one
  strategy: {type: Recreate}       # never two at once, even during a rollout
  selector: {matchLabels: {app: corescope-ingestor}}
  template:
    metadata: {labels: {app: corescope-ingestor}}
    spec:
      securityContext: {runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000}
      containers:
        - name: ingestor
          image: <registry>/corescope:<version>
          command: ["/app/corescope-ingestor", "-config", "/app/config/config.json"]
          workingDir: /app
          env:
            - {name: DB_DRIVER, value: postgres}
            - name: DB_DSN
              valueFrom: {secretKeyRef: {name: corescope-postgres, key: writer-dsn}}
          volumeMounts:
            - {name: config, mountPath: /app/config, readOnly: true}
      volumes:
        - {name: config, secret: {secretName: corescope-config}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: corescope}
spec:
  replicas: 3
  strategy: {type: RollingUpdate, rollingUpdate: {maxUnavailable: 1, maxSurge: 1}}
  selector: {matchLabels: {app: corescope}}
  template:
    metadata: {labels: {app: corescope}}
    spec:
      securityContext: {runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000}
      containers:
        - name: server
          image: <registry>/corescope:<version>
          # No volume: on PostgreSQL nothing is shared with the ingestor through files.
          # With user management on and the accounts in users.db (SQLite), also mount a
          # volume for it (below) and keep replicas: 1. With the accounts in PostgreSQL
          # (DB_DSN_ACCOUNTS below), no volume and any replica count.
          command: ["/app/corescope-server", "-config-dir", "/app/config",
                    "-public", "/app/public", "-port", "3000"]
          workingDir: /app
          env:
            - {name: DB_DRIVER, value: postgres}
            - name: DB_DSN
              valueFrom: {secretKeyRef: {name: corescope-postgres, key: reader-dsn}}
            # Only with user accounts in PostgreSQL (userManagement.enabled in config.json):
            # - name: DB_DSN_ACCOUNTS
            #   valueFrom: {secretKeyRef: {name: corescope-postgres, key: accounts-dsn}}
          ports: [{name: http, containerPort: 3000}]
          readinessProbe: {httpGet: {path: /api/healthz, port: http}}
          volumeMounts:
            - {name: config, mountPath: /app/config, readOnly: true}
            # Only with user accounts in users.db (SQLite, single replica):
            # - {name: data, mountPath: /app/data}
      volumes:
        - {name: config, secret: {secretName: corescope-config}}
        # Only with user accounts in users.db (SQLite, single replica):
        # - {name: data, persistentVolumeClaim: {claimName: corescope-data}}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: corescope}
spec:
  minAvailable: 1                  # servers only; a PDB cannot keep a single ingestor up
  selector: {matchLabels: {app: corescope}}
---
apiVersion: v1
kind: Service                      # selects the servers only
metadata: {name: corescope}
spec:
  selector: {app: corescope}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: postgres-from-corescope}
spec:
  podSelector: {matchLabels: {app: postgres}}
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector: {matchLabels: {app: corescope}}
        - podSelector: {matchLabels: {app: corescope-ingestor}}
        - podSelector: {matchLabels: {app: corescope-migrate}}   # the one-shot copy Job
      ports: [{protocol: TCP, port: 5432}]
```

Labels matter: the Service and the server Deployment select `app: corescope`, so the ingestor
must carry a different label or the Service would route HTTP traffic to it.

## Moving an existing SQLite install

`corescope-migrate copy` streams every row of a SQLite database into an empty PostgreSQL
database and verifies the result. It never deletes or overwrites anything.

1. **Provision PostgreSQL** with the roles script (or the Compose override's `postgres` and
   `postgres-init` services started on their own).

2. **Stop every writer.** The copy reads one snapshot; rows written after it are not copied.
   The tool prints a warning about this. Stop the ingestor, and the server too for a clean
   cut-over. With the Compose override that is `docker compose -f docker-compose.yml -f
   docker-compose.postgres.yml stop ingestor prod` (leave `postgres` running). On a first move
   they have not been started yet; bring up only the database with `up -d postgres
   postgres-init`.

3. **Copy**, connecting as the writer role:

   ```bash
   export PGPASSWORD='<writer-password>'   # or use ~/.pgpass
   corescope-migrate copy \
     -from sqlite:/app/data/meshcore.db \
     -to 'postgres://corescope_writer@db.example.org:5432/corescope?sslmode=require'
   ```

   **In the Docker image**, `corescope-migrate` is at `/app/corescope-migrate`. Run it in a
   one-off container from the same image, with the data directory mounted and the Compose
   network, with the writers stopped and the `postgres` and `postgres-init` services up:

   ```bash
   export PGPASSWORD='<writer-password>'     # the value of CORESCOPE_WRITER_PASSWORD
   docker compose -f docker-compose.yml -f docker-compose.postgres.yml \
     run --rm --no-deps -e PGPASSWORD --entrypoint /app/corescope-migrate prod \
     copy -from sqlite:/app/data/meshcore.db \
          -to 'postgres://corescope_writer@postgres:5432/corescope?sslmode=disable'
   ```

   (`-e PGPASSWORD` with no value passes the variable from your shell, so the password is not on
   the command line. Use the database name and role from your `.env` if you changed them. Run
   `verify` the same way.) Then start the stack with `up -d`.

   Do not put the password in the URL: it would show up in `ps` and shell history. Credentials
   come from the standard PostgreSQL environment (`PGPASSWORD`, ...) or `~/.pgpass`. Passwords
   are redacted in the tool's output. Flags:

   | Flag | Default | Meaning |
   |------|---------|---------|
   | `-from` | — | Source: `sqlite:<path>` or a `postgres://` / `postgresql://` URL. Required. |
   | `-to` | — | Target, same forms. Required. |
   | `-batch` | `5000` | Rows per `COPY` call (PostgreSQL target) or per transaction (SQLite target) |
   | `-allow-pending-migrations` | off | Copy a SQLite source whose async migrations are unfinished (see below) |

   What it does:

   - The source must be fully migrated. A SQLite source must pass the schema-ready check and
     every row in `_async_migrations` must be `done`. If not, start the ingestor once on the
     source and let its migrations finish.
   - The target must be empty. The schema is created if absent. A non-empty target is refused.
   - Into PostgreSQL the copy is one transaction, verified before `COMMIT`. A failure leaves
     the target empty; rerun it.
   - Primary keys are preserved and sequences are set afterwards.
   - The `observation_hops` tables (the SQLite-only hop index) are skipped.
   - Text in a SQLite `INTEGER` column that cannot be converted fails the copy and names the
     table, primary key and column.

   Measured throughput was about 36.7k rows/s into PostgreSQL, roughly 9 minutes per 20
   million observations.

   **`-allow-pending-migrations` (read this before using it).** Use it only when the source cannot finish its async
   migrations by running the ingestor, for example a file prepared with the legacy
   `corescope-migrate -db` command, which never creates `_async_migrations`. The data is copied
   as the source has it, with a warning. PostgreSQL will **not** run those backfills, because
   its schema records them as done, and unfinished `_async_migrations` rows are not copied. For
   a normal install, do not use it: start the ingestor on the source instead. Using it can leave
   you with a PostgreSQL database that is missing the results of migrations the source never
   finished, and CoreScope will not repair that on its own.

4. **Verify**:

   ```bash
   corescope-migrate verify \
     -a sqlite:/app/data/meshcore.db \
     -b 'postgres://corescope_writer@db.example.org:5432/corescope?sslmode=require'
   ```

   It compares per-table row counts and an ordered content digest, prints the first differing
   primary key, and exits 1 on a mismatch. (`copy` already verifies before it commits; this is
   for a second look, or after a later change.)

5. **Switch the configuration** to PostgreSQL as in [Set up with Docker Compose](#set-up-with-docker-compose)
   or [Set up without Compose](#set-up-without-compose).

6. **Start the ingestor, then the server.** With the Compose override, `up -d` starts both.

Keep the old `meshcore.db` file until you are satisfied. The ingestor has a guard for the case
where you forget the copy: on PostgreSQL it refuses to start on an **empty** PostgreSQL database
while the SQLite file at `dbPath` still holds transmissions:

```
refusing to start on an empty PostgreSQL database while <path> still holds N transmissions:
copy it first with "corescope-migrate copy -from sqlite:<path> -to <postgres url>", or move
<path> aside to start with an empty database
```

There is no configuration switch. Either copy the data, or move the SQLite file aside, which
is the deliberate way to start with an empty PostgreSQL database.

The legacy form `corescope-migrate -db <sqlite path>` is unchanged: it brings a SQLite file to
the current schema and is used by CI.

## Switching back to SQLite

Copy PostgreSQL into a new SQLite file the same way, then set the driver back.

1. Stop the ingestor (and the server).
2. Copy to a path that does not exist yet:

   ```bash
   corescope-migrate copy \
     -from 'postgres://corescope_writer@db.example.org:5432/corescope?sslmode=require' \
     -to sqlite:/app/data/meshcore.db
   ```

   Set `PGPASSWORD` (or use `~/.pgpass`) first; keep the password out of the URL.

   The tool builds `<path>.partial` and renames it after verification (about 61k rows/s
   measured). It refuses a target file that already exists, and on a failure it prints the exact
   `rm` command to clear the partial file. Move any old `meshcore.db` (and its `-wal` and `-shm`)
   aside first.

   In the Docker image, run it the same way as in
   [Moving an existing SQLite install](#moving-an-existing-sqlite-install) (`run --rm --no-deps
   --entrypoint /app/corescope-migrate prod copy -from ... -to sqlite:/app/data/meshcore.db`)
   while the PostgreSQL service is up and `ingestor` and `prod` are stopped.

3. Remove `db.driver`, `db.dsn`, `DB_DRIVER`, `DB_DSN`, `DB_DSN_INGESTOR` and `DB_DSN_SERVER`
   (or set the driver to `sqlite`), and drop `docker-compose.postgres.yml` from the `-f` list.
4. Start the ingestor, then the server.

## User accounts in PostgreSQL

The optional [user accounts](user-guide/accounts.md) (`userManagement.enabled`) have two
stores. Choose one:

| | `users.db` (default) | PostgreSQL schema `corescope_users` |
|---|---|---|
| Where | A SQLite file next to the analyzer database, or at `userManagement.dbPath` | Schema `corescope_users` in the CoreScope database |
| Selected by | Nothing (the default) | `userManagement.dsn`, or `DB_DSN_ACCOUNTS` when `dsn` is empty |
| Disk | A volume the server writes to | None |
| Server replicas | One | Any number |
| Backup | Scheduled snapshots and `/api/admin/users-backup` | `pg_dump -n corescope_users` (the snapshots are off, the endpoint answers 501) |

With no accounts DSN set, nothing here applies and `users.db` works as before. The accounts
store also works when the measurement database is SQLite; the rest of this section assumes
PostgreSQL for both.

### Create the role

Set `CORESCOPE_ACCOUNTS_PASSWORD` when you run `scripts/postgres-roles.sh` (a second run on an
existing install is safe):

```bash
export PG_ADMIN_URL='postgres://postgres@db.example.org:5432/postgres?sslmode=require'
export PGPASSWORD='<superuser-password>'
export CORESCOPE_WRITER_PASSWORD='<writer-password>'
export CORESCOPE_READER_PASSWORD='<reader-password>'
export CORESCOPE_ACCOUNTS_PASSWORD='<accounts-password>'
sh scripts/postgres-roles.sh
```

Besides the writer and reader URLs it prints a third, without the password:

```
CORESCOPE_ACCOUNTS_URL=postgres://corescope_accounts@db.example.org:5432/corescope
```

The script creates the role `corescope_accounts` (override the name with `CORESCOPE_ACCOUNTS`)
and makes it the owner of schema `corescope_users`. The role may connect to the database and
has nothing in schema `public` or `corescope_control`, no role memberships, no `TEMPORARY`, at
most 32 connections and the same statement and idle-transaction timeouts as the reader. The
store creates and migrates its own tables in that schema.

It also creates the group role `corescope_users_reader` (fixed name, `NOLOGIN`) and grants it to
the writer. That is how the ingestor reads two tables, the approved channel proposals and the
linked companions (see [What changes with several replicas](#what-changes-with-several-replicas)).
The script does this on every run, with or without accounts.

### Give the server the DSN

The accounts DSN goes to the **servers only**, never to the ingestor. Set it in the server's
environment, next to its reader DSN:

```
# /etc/corescope/server.env
DB_DRIVER=postgres
DB_DSN=postgres://corescope_reader:<reader-password>@db.example.org:5432/corescope?sslmode=require
DB_DSN_ACCOUNTS=postgres://corescope_accounts:<accounts-password>@db.example.org:5432/corescope?sslmode=require
```

or as `userManagement.dsn` in `config.json`. `config.json` wins; the environment variable is used
only when `dsn` is empty. Because the config file is typically shared with the ingestor and holds
no passwords, prefer the environment. The DSN is never logged. `userManagement.dbPath` is
unused once a DSN is set.

With Compose, set `CORESCOPE_ACCOUNTS_PASSWORD` in `.env` as well. `postgres-init` then sets up the
role and schema and `prod` gets `DB_DSN_ACCOUNTS`; the ingestor does not. Without it `prod` gets
an empty value, which the entrypoint drops. In the single-container layout and in Kubernetes
(see [Kubernetes and high availability](#kubernetes-and-high-availability)) pass the variable to
the server container only. The Docker image's server user gets it through its environment
allow-list. The Docker image refuses `DB_DSN_ACCOUNTS` in a container that also runs the ingestor; outside Docker, do not put a password-bearing `userManagement.dsn` in a `config.json` the ingestor also reads, and use the `DB_DSN_ACCOUNTS` environment variable for the server only.

You still need `userManagement.enabled` and the rest of the block (`publicBaseUrl`, mail
settings) in `config.json`, as in the [accounts guide](user-guide/accounts.md).

### The startup safety check

The server's rule is that it never writes measurement data. The accounts role can write (it owns
its schema), so the server proves at startup that it can write nothing else. It refuses to start
when the accounts role:

- is the same role as the server's measurement reader, or
- could write the measurement schema or the `corescope_control` schema as itself or through any
  role it is a member of: superuser or `CREATEROLE`, owner of or `CREATE` on either schema,
  `INSERT`, `UPDATE`, `DELETE` or `TRUNCATE` on a table (or `INSERT`/`UPDATE` on a column), or
  `UPDATE`/`USAGE` on a sequence.

The role from `scripts/postgres-roles.sh` passes. The refusal messages are listed under
[Troubleshooting](#accounts). The check runs on the measurement connection and reads the
catalog only.

### Move an existing `users.db`

Skip this for a new install. Stop every CoreScope server first: the copy reads one snapshot and
accounts changed afterwards are not copied. `copy-users` creates the schema's tables if needed
and refuses a target that already has rows. Connect as the accounts role; keep the password out
of the URL:

```bash
export PGPASSWORD='<accounts-password>'     # or use ~/.pgpass
corescope-migrate copy-users \
  -from sqlite:/app/data/shared/users.db \
  -to 'postgres://corescope_accounts@db.example.org:5432/corescope?sslmode=require'
```

`-batch` (default 5000) sets the rows per `COPY` call. The copy is verified before it finishes.
To check the two stores again later, or compare a backup:

```bash
corescope-migrate verify-users \
  -a sqlite:/app/data/shared/users.db \
  -b 'postgres://corescope_accounts@db.example.org:5432/corescope?sslmode=require'
```

`-a` and `-b` each take a `sqlite:<users.db>` path or a `postgres://` URL. In the Docker image
run the tool as for [`copy`](#moving-an-existing-sqlite-install)
(`run --rm --no-deps -e PGPASSWORD --entrypoint /app/corescope-migrate prod copy-users ...`).
Then set the accounts DSN, start the servers, and keep the old `users.db` until you are sure.

### What changes with several replicas

With the accounts in PostgreSQL you can run any number of servers:

- The cleanup job (janitor) and the notification check run on one replica at a time, under a
  PostgreSQL advisory lock. A mail is claimed in the database before it is sent, so an overlap
  does not send it twice.
- Login and other rate limits are counted in the database, so all replicas share one limit. They
  use fixed windows, so up to twice the limit can pass around a window boundary. If the database
  cannot be reached, each replica limits on its own in memory.
- Approved channels and observer logins are reloaded from the database every 30 seconds, so a
  change made through one replica reaches the others within that time.
- The ingestor reads `corescope_users.proposals` and `corescope_users.companion_links` over its
  own database connection, in a read-only transaction, through the group role
  `corescope_users_reader`. It does not need `dbPath` or a copy of `users.db`. On each read
  (approved channels once a minute, linked companions every 60 s plus misses) it checks whether
  schema `corescope_users` exists. If it does, it reads that schema. If it does not, it reads
  `users.db` read-only at `userManagement.dbPath` (or next to `dbPath`), as on SQLite, so
  Postgres measurement data with the accounts in `users.db` still gets channel proposals and
  `clientRxCoverage.requireLinkedCompanion`. The schema exists once you opt in: running
  `scripts/postgres-roles.sh` with `CORESCOPE_ACCOUNTS_PASSWORD` creates it, as does the server's
  first start with an accounts DSN. Between the script and that first server start the schema
  exists but has no tables: the ingestor then sees no approved channels beyond the configured
  ones, and does not filter linked companions (it warns once). Start the server soon after
  running the script, or leave `CORESCOPE_ACCOUNTS_PASSWORD` unset until you move the accounts.
- The scheduled users backup is off and `GET /api/admin/users-backup` answers 501. "Download my
  data" still works.

### Back up the accounts

`pg_dump` of the measurement tables does not contain the accounts. Dump the schema, as the
accounts role or a superuser:

```bash
pg_dump -Fc -n corescope_users \
  -h db.example.org -U corescope_accounts -d corescope \
  -f corescope-users-$(date -u +%Y%m%dT%H%M%SZ).dump
```

The dump holds every password hash and mail address: protect it like the database. To restore,
stop the servers, restore into an empty database or schema with `pg_restore`, and start them.
The schema version check refuses a dump from a newer CoreScope.

### Roll back to `users.db`

There is no copy from PostgreSQL back to SQLite for accounts: `corescope-migrate copy-users`
only reads a `users.db`. Rolling back therefore returns to the `users.db` you left behind, and
accounts created or changed in PostgreSQL since the move are not in it. Keep that file (and stop
the servers before you copy, so it is a clean snapshot) until you are sure.

1. Stop every server.
2. Remove `userManagement.dsn` and `DB_DSN_ACCOUNTS`. In the Docker image with Compose, unset
   `CORESCOPE_ACCOUNTS_PASSWORD`.
3. Put the `users.db` back where the server looks for it (`userManagement.dbPath`, or next to the
   analyzer database; `/app/data/shared/users.db` in the Docker image), with a volume, and start
   a single server.

Drop the schema (`DROP SCHEMA corescope_users CASCADE`, as a superuser) and the accounts role only
when you no longer need them.

## Backups and restore

`GET /api/backup` is a SQLite feature. On PostgreSQL it returns HTTP 501:

```json
{"error":"backups of a PostgreSQL database are taken with pg_dump; see docs/postgresql.md"}
```

### Back up

Use `pg_dump` in custom format, as the writer role or a superuser. Supply the password through
`~/.pgpass` or an exported `PGPASSWORD`, not on the command line:

```bash
pg_dump -Fc \
  -h db.example.org -U corescope_writer -d corescope \
  -f corescope-$(date -u +%Y%m%dT%H%M%SZ).dump
```

With the Compose override, run it inside the container, or publish the port as described
[above](#set-up-with-docker-compose):

```bash
docker compose -f docker-compose.yml -f docker-compose.postgres.yml exec -T postgres \
  pg_dump -Fc -U postgres -d corescope > corescope.dump
```

Keep copies off the host, and test restores. The dump holds everything in the measurement
database, so protect it like the database itself.

A portable alternative is a SQLite snapshot made with `corescope-migrate copy` from PostgreSQL
to a new SQLite file (see [Switching back to SQLite](#switching-back-to-sqlite)).

### Restore

1. Stop the ingestor and the server.
2. Restore into an **empty** database:

   ```bash
   createdb -h db.example.org -U postgres -O corescope_writer corescope
   pg_restore -h db.example.org -U postgres -d corescope --no-owner \
     --role=corescope_writer corescope.dump
   ```

   This assumes the old `corescope` database was dropped or renamed first. Use a superuser as
   shown, or the writer role.

3. Run the roles script again, so the reader gets `SELECT` on the restored tables and the
   default privileges that cover future ones:

   ```bash
   sh scripts/postgres-roles.sh
   ```

   Use the same environment (names and passwords) as at setup. The script refuses an existing
   database that the writer does not own, and an existing role it did not set up, unless
   `CORESCOPE_ADOPT=1` is set. A database created with `-O corescope_writer` as above passes.

4. Start the ingestor, then the server. The ingestor applies any newer migrations.

### Other files to back up

- `config.json`, `theme.json`, deployment settings and secrets, as in
  [Backup & Restore](deployment.md#backup--restore).
- The user accounts, if enabled. By default they are in `users.db`, a SQLite file, which is
  backed up as before, through `/api/admin/users-backup` or its scheduled snapshots; see the
  [account backup guide](user-guide/accounts.md#backups). A `pg_dump` of the measurement tables
  does not contain it. In the Docker image on PostgreSQL it is `/app/data/shared/users.db`, with
  snapshots in `/app/data/shared/backups/`. With the accounts in PostgreSQL they are the schema
  `corescope_users`: dump it with `pg_dump -n corescope_users` (see
  [Back up the accounts](#back-up-the-accounts)).
- `config.json` is rewritten by geo-filter saves. A save is normally atomic. When the config
  directory is not writable (the Docker image on PostgreSQL), the server first copies the
  current file to `config.json.bak` in the shared directory (`/app/data/shared`) and then
  rewrites `config.json` in place, which is not atomic and is logged as such. After a failed
  save, restore from that `.bak`.

## What is SQLite-only

These do not apply on PostgreSQL:

| Feature | On PostgreSQL |
|---------|---------------|
| `retention.hopIndexDays` (the hop index) | Ignored. The ingestor logs one warning and treats it as 0. The node reach panel uses its path scan. |
| `db.vacuumOnStartup`, `db.incrementalVacuumPages`, `db.analysisLimit` | Ignored |
| WAL checkpoints, incremental vacuum, planner-statistics refresh | Do not run. The ingestor logs once that maintenance is handled by PostgreSQL autovacuum. |
| `GET /api/backup` | 501, use `pg_dump` |
| `GET /api/perf/sqlite` | 404 `{"error":"not available: the database is PostgreSQL"}` |
| `users.db` | Used unless accounts are moved to PostgreSQL (`userManagement.dsn`); see [User accounts in PostgreSQL](#user-accounts-in-postgresql) |

Retention settings (`retention.*Days`) work the same on both backends. They run in the ingestor.

## Monitoring

The **Perf** page shows "Database (PostgreSQL)" instead of "Database (SQLite)", hides the WAL
and freelist rows, and labels row counts as estimates.

`GET /api/perf` carries the same data. Its `sqlite` block (the name is kept for compatibility)
has `"engine": "sqlite"` or `"engine": "postgres"`. On PostgreSQL:

- `dbSizeMB` is `pg_database_size`,
- `rows` are planner estimates (`pg_stat_user_tables.n_live_tup`), not exact counts,
- `walSizeMB`, `freelistMB` and `walPages` are 0.

The result is cached for 30 seconds.

Table bloat, autovacuum, replication and connection limits are PostgreSQL's to report; monitor
them with the tools you already use for PostgreSQL.

`corescope-decrypt -db` also accepts a `postgres://` URL, opened read-only.

## Troubleshooting

Startup refusals are deliberate. Messages below are the text CoreScope prints; passwords are
never included.

### Server

**`postgres role "<role>" can write to tables in schema "<schema>" (...); the server must connect as a read-only role.`**
The server's DSN uses a role with write access (the writer, a superuser, an owner, or a role
that inherits write grants). Use the reader role in the server's DSN. The message includes the
`CREATE ROLE` and `GRANT` statements for a read-only role; `scripts/postgres-roles.sh` does the
same. If you gave the reader a write privilege later, re-run the script, which revokes it.

**`database schema is at version N, need M: start the ingestor first so it can create or upgrade the schema`**
The schema is missing or older than this server. Start the ingestor (it creates and migrates
the schema), then the server.

**`default_transaction_read_only is "off" after connecting, want "on"; remove any override of it from db.dsn and from the role or database settings`**
A setting overrides the read-only default. Remove any `default_transaction_read_only` override
from `db.dsn` and from the role and database settings (`ALTER ROLE ... SET`, `ALTER DATABASE ... SET`).

**`postgres role "<role>" has no current schema; set search_path in db.dsn to the CoreScope schema`**
The role's `search_path` resolves to no existing schema. Set `search_path` in the DSN, or use
the default `public`.

**`[db] WARNING: geo-prune and multibyte capability publishing are disabled: <reason>`**, and the geo-prune endpoints answer 503 (`geo-prune is unavailable: the server's Postgres role is not a member of corescope_control_client with the control grants; see the server log at startup`)
The server started without its control pool. The reason names the cause: the role
`corescope_control_client` does not exist, the server's role does not inherit it, or the group
does not hold exactly the control grants. Re-run `scripts/postgres-roles.sh` (it creates the
group role and grants it to the reader), then start the ingestor, which repairs the grants. The
ingestor logs `role corescope_control_client is missing or not set up` when it sees the group
missing. The rest of the server, and ingest, are unaffected.

**`postgres role "<role>" could gain write access to schema "<schema>": ...` naming `<role> has INSERT on corescope_control.<table>`, `PUBLIC has CREATE on schema corescope_control`, `role <role> owns corescope_control.<table>` and similar**
Something other than `corescope_control_client` holds write power in the control schema: a
direct grant to the reader, a grant to `PUBLIC`, a reachable role that owns a control table or
the schema, or `CREATE` on the schema. The server refuses to start. Revoke it, or re-run
`scripts/postgres-roles.sh`, which revokes everything on that schema from `PUBLIC` and the reader
and re-grants only the group's privileges.

**Queries fail with `canceling statement due to statement timeout`**
On PostgreSQL the server's sessions have a `statement_timeout` of 120 seconds by default. Raise
`db.statementTimeoutSec` in `config.json` for a slow query on a very large database, or set it to
`0` to disable the limit. Prefer fixing the cause (missing `ANALYZE`, an undersized server) over
disabling it.

**`db.dsn: unparseable postgres URL`**, **`db.dsn: malformed keyword=value connection string (expected keyword=value)`**, **`db.dsn: unterminated quoted value in connection string`**
The DSN is not valid. Use a `postgres://user:pass@host:5432/db?sslmode=...` URL and
percent-encode special characters in the password.

### Accounts

These apply to accounts in PostgreSQL (`userManagement.dsn` / `DB_DSN_ACCOUNTS`). The server
refuses to start; nothing else is changed.

**`userManagement.dsn connects as "<role>", the measurement reader role: give the account store its own role (one that owns schema corescope_users and can write nothing else), so the server's measurement connection stays read-only`**
The accounts DSN uses the same role as `db.dsn`. Create the accounts role with
`scripts/postgres-roles.sh` and `CORESCOPE_ACCOUNTS_PASSWORD`, and use it in the accounts DSN.

**`userManagement.dsn connects as postgres role "<role>", which could write outside the account store: <list>. The accounts role may own schema corescope_users and nothing else: revoke these (and any membership granting them), so the server cannot write measurement data (schema "<schema>") or the control tables (schema "corescope_control")`**
The list names each way the role could write the measurement or control schema, for example
`role <role> has SUPERUSER`, `role <role> owns schema <schema>`, `can write <schema>.<table>
(via role <other>)` or `can advance or set sequence ...`. Revoke the privilege or the role
membership that grants it. Re-running `scripts/postgres-roles.sh` resets the role it created.
Do not use a superuser or the writer role as the accounts role.

**`userManagement.dsn: unparseable Postgres DSN`**, **`userManagement.dsn: no user to connect as`**
The DSN is not valid, or names no user (and `PGUSER` is unset). Use a `postgres://user:pass@host/db`
URL and percent-encode special characters in the password. The message never quotes the DSN.

**`accounts role check: ...`**
The check could not read the catalog on the measurement connection; the text after the colon names
the step and the database error.

**The ingestor does not see new approved channels or linked companions.**
It reads `corescope_users` through the group role `corescope_users_reader`, which the writer must
belong to; re-run `scripts/postgres-roles.sh`. Right after the script (with an accounts password)
and before the server's first start with the accounts DSN, the schema exists without tables, so
nothing is read until the server has created them. Without the schema the ingestor reads
`users.db` instead. The ingestor's log is the place to look.

**`GET /api/admin/users-backup` answers 501.**
Expected with the accounts in PostgreSQL. Use `pg_dump -n corescope_users`.

### Ingestor

**`refusing to start on an empty PostgreSQL database while <path> still holds N transmissions: ...`**
See [Moving an existing SQLite install](#moving-an-existing-sqlite-install): copy the SQLite
file first, or move it aside to start empty.

**`refusing to start on an empty PostgreSQL database: cannot tell whether <path> still holds data (...); move <path> aside to start with an empty database`**
The guard could not read the SQLite file at `dbPath`. Fix the cause shown in the parentheses
(permissions, a corrupt file), or move the file aside.

**`opening postgres: db.dsn is not a valid postgres:// URL`**
Fix the DSN as above.

**`pgschema: PostgreSQL N is not supported; CoreScope needs PostgreSQL 16 or newer`**
Upgrade the PostgreSQL server.

**`pgschema: database schema is at version N but this binary only knows up to M; a newer CoreScope has upgraded it, run that version`**
A newer CoreScope migrated this database. Run that version or newer; migrations are
forward-only.

**`applying postgres schema: ...` or permission denied when creating tables**
The ingestor's role cannot create tables. It must own the database and schema; use the writer
role from `scripts/postgres-roles.sh`.

### Configuration

**`db.driver "<value>" is not recognised; use "sqlite" (or sqlite3) or "postgres" (or postgresql, pg)`**
Fix the driver name.

**`db.driver=postgres requires db.dsn (or DB_DSN)`**
Set a DSN. In the Docker image, set `DB_DSN` (the ingestor container) and `DB_DSN_SERVER` (the
server container); in the single-container layout, `DB_DSN_INGESTOR` and `DB_DSN_SERVER`.

**`[entrypoint] DB_DSN / DB_DSN_INGESTOR / DB_DSN_SERVER must not contain a double quote, backslash or newline`**
Use a `postgres://` URL and percent-encode the password.

**Docker image refuses to start on PostgreSQL**
The entrypoint logs `[entrypoint] postgres: ...` and exits. Typical causes and fixes:
`config.json sets db.dsn` (remove it; pass the DSNs in the containers' environment);
`config.json selects PostgreSQL (db.driver), but the environment does not` (set `DB_DRIVER=postgres`
and the DSN on the container, and remove `db.driver` from `config.json`);
`DISABLE_INGESTOR=true but DB_DSN_INGESTOR is set` (the writer DSN must not be given to the
server container); `DB_DRIVER=... but a DB_DSN* is set` (unset one of them); `/app/data/.env is readable by corescope-server` (`chmod 600` it);
`config.json is not readable by corescope-server` (make it readable by others or by group
`corescope-share`); `SERVER_RUN_AS_UID` is not a number, is 0, equals the ingestor's uid, or
its uid or gid belongs to another account or group (choose another value);
`corescope-server cannot reach /app/data/shared` (`/app/data` must be searchable by others);
`/app/config.json must be a regular file in /app/data` (not a symlink or a file elsewhere).
`WARNING: userManagement.dbPath ... is outside /app/data/shared` is not fatal: move `users.db`
into the shared directory and point `dbPath` at it (or move the accounts to PostgreSQL, which
needs no file).

**A geo-filter save is logged as non-atomic**
Expected when `config.json`'s directory is not writable by the server (the Docker image on
PostgreSQL): it backed the file up to `config.json.bak` in `/app/data/shared` and rewrote it in
place. If the save failed midway, copy the `.bak` back.

**The ingestor container is up but sees no MQTT broker, or stopped working after `prod` restarted (Compose).**
The ingestor shares `prod`'s network namespace. Run `docker compose -f docker-compose.yml -f
docker-compose.postgres.yml up -d` so both are recreated together.

**The server connects with the wrong DSN.**
Remove `db.driver` and `db.dsn` from `config.json`; the server prefers `config.json` over the
environment, and on PostgreSQL in the image `config.json` may not select the database.

**The container warns "the ingestor runs in this container, so its writer DSN is in this container too."**
You are on the single-container layout. Use the Compose override, or run the ingestor as its own
container and set `DISABLE_INGESTOR=true` on the server's.

**The container warns that `/etc/caddy/Caddyfile` does not disable Caddy's admin API.**
Add a global block `{ admin off }` at the top of your custom Caddyfile.

### `corescope-migrate`

**`target is not empty (table <name> has rows); refusing to overwrite. ...`**
`copy` only writes to an empty target. Use a new PostgreSQL database, or a SQLite path that does
not exist yet. Nothing is dropped for you.

**`source <db> ... ; start the ingestor once on the source so its migrations finish, then rerun, or pass -allow-pending-migrations to copy it as it is`**
The SQLite source has unfinished async migrations or no `_async_migrations` table. Start the
ingestor on it and let the migrations finish. Use `-allow-pending-migrations` only as described
[above](#moving-an-existing-sqlite-install). A PostgreSQL source is always strict.

**`target SQLite file <path> already exists; refusing to overwrite. ...`**, **`<path>-wal exists (left over from an earlier database at this path); ...`**
Give a path that does not exist yet, or remove the leftover file.

**A failed copy into SQLite leaves `<path>.partial`.**
The tool prints the `rm` command. Run it and rerun the copy. A failed copy into PostgreSQL leaves
the target empty.

### `scripts/postgres-roles.sh`

**`PG_ADMIN_URL must not contain a password (it would be visible in ps); set PGPASSWORD or use ~/.pgpass`**
Remove the password from the URL and set `PGPASSWORD` or use `~/.pgpass`.

**`role <name> exists but was not set up by this script as the CoreScope writer|reader; set CORESCOPE_ADOPT=1 to take it over (its password is reset)`**, **`database <db> exists and is owned by <owner>, not <writer>; set CORESCOPE_ADOPT=1 to take it over`**
The script marks the roles it sets up and will not touch another application's role or database.
If the existing role or database really is meant for CoreScope, rerun with `CORESCOPE_ADOPT=1`
(the role's password is reset). It never touches a superuser, even then.

**`PG_ADMIN_URL must connect as a superuser (connected as <role>)`**, **`role <name> is a superuser; choose another name for CoreScope`**
Connect as a superuser, and pick role names that are not existing superusers; the script will not
demote an account.

**`... must match ^[a-z_][a-z0-9_]{0,62}$ and not start with pg_`**
Choose a valid role or database name.

**`psql not found`**, or an error near `\getenv`
Install `psql` 15 or newer.
