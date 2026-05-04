# Managed services

The Blob provisions stateful services as Nomad jobs with persistent volumes and injects credentials into apps that bind them.

v0.5 ships **managed Postgres** (with backup/restore) and **managed Valkey**. ScyllaDB and NATS are planned and follow the same pattern.

## Postgres

### Create

```sh
blob postgres create my-pg
```

Optional flags:

- `--version 16` — image tag suffix (default `16`). Any tag that exists for `postgres:<v>-alpine` works.
- `--database mydb` — initial database name (default: same as `--name` with `-` → `_`).

What happens on the host:

1. blobd allocates an unused port from `15432-15531`.
2. Generates a random 36-char password.
3. Saves metadata in `/srv/blob/postgres/<name>.json` (mode 0600).
4. Renders a Nomad service job at `/srv/blob/jobs/pg-<name>.nomad` and runs it.
5. Creates a Docker named volume `blob-pg-<name>` mounted at `/var/lib/postgresql/data`.
6. Waits for `nomad job status` to report running.

### Bind apps to it

Add `services:` to the app's `blob.yaml`:

```yaml
name: guestbook
form: web-service
port: 8080
services:
  - my-pg.payments      # recommended: <instance>.<project> binding (v0.6+)
```

Two binding shapes:

| Syntax              | What you get                                                         | Use when                                |
|---                  |---                                                                   |---                                      |
| `my-pg.payments`    | A scoped role + database for this project (see "Per-project users") | Default. One Postgres instance shared across many apps |
| `my-pg`             | The instance superuser + the instance-named database                | Single-tenant; legacy. Still works.     |

For both shapes, blobd injects:

- `DATABASE_URL` (full DSN)
- `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`

Multiple bindings: each instance also gets a prefixed set: `PRIMARY_URL`, `PRIMARY_HOST`, …, `ANALYTICS_URL`, … so an app can read+write to several Postgres instances simultaneously.

### Use it

```sh
blob postgres list                  # show name, version, status, host:port, masked URL
blob postgres url my-pg             # full DSN with password — pipe into psql or your app config
blob postgres connect my-pg         # if psql is in your PATH, opens a session against it
```

From outside the cluster (the static port is open at the platform's public IP):

```sh
psql "$(blob postgres url my-pg)"
docker run --rm postgres:16-alpine psql "$(blob postgres url my-pg)"
```

### Backups

```sh
blob postgres backup my-pg          # snapshot now
blob postgres backups my-pg         # list snapshots
blob postgres restore my-pg latest  # restore newest (or pass an explicit filename / path)
blob postgres restore my-pg latest --force  # required if the database currently has data
```

How it works:

- **Backup**: runs `pg_dump --clean --if-exists --create -d <db>` inside the running Postgres container, gzips the output, writes to `/srv/blob/backups/postgres/<name>/<UTC-ISO>.sql.gz` (mode 0700).
- **Restore**: pipes a gzipped dump back into `psql -d postgres -v ON_ERROR_STOP=1`. The dump's `--create --clean --if-exists` directives drop and recreate the database, so the round-trip is exact for one database (table data + schema + sequences).
- **Force**: `restore` refuses to run if the target database is non-empty, unless `--force` is set.

Verified end-to-end as part of v0.5 ship: insert a sentinel row → backup → delete the row → restore latest → row reappears with the same `id`, `label`, and timestamp.

#### What backups don't include yet

- **Project-owned databases** (`my-pg.payments` etc.) are NOT in the per-instance backup. Backups currently only cover the instance's superuser-owned database. A future release will iterate all project databases on the instance and back each separately.

### Off-host backup shipping (v0.7)

Postgres backups can be shipped to any S3-compatible object store (AWS S3, Cloudflare R2, Backblaze B2, MinIO, Wasabi). Configure once; the in-process scheduler then dumps + uploads on a cron and applies retention.

```sh
# Configure the destination (server-side; secret key stored at /srv/blob/postgres/<name>/backup-config.json mode 0600)
blob postgres backup-config set my-pg \
  --s3-endpoint http://65.21.9.22:30149 \
  --s3-region us-east-1 \
  --s3-bucket blob-backups \
  --s3-prefix my-pg/ \
  --s3-access-key-id <KEY> \
  --s3-secret-access-key <SECRET> \
  --s3-use-path-style \
  --schedule "0 3 * * *" \
  --retention-daily 7 --retention-weekly 4 --retention-monthly 6 \
  --enable

blob postgres backup-config get my-pg     # secret key shown as ***
blob postgres backup-config test my-pg    # HEAD bucket round-trip
blob postgres backup-config clear my-pg

# `blob postgres backup` now ships in addition to writing locally; a `.sha256` sidecar is uploaded alongside each `.sql.gz`.
blob postgres backup my-pg
blob postgres backups my-pg     # unified view: LOCAL/REMOTE columns + sha256

# Restore from the remote without first downloading
blob postgres restore my-pg latest --from s3 --force
blob postgres restore my-pg s3://blob-backups/my-pg/2026-05-03T20-08-46Z.sql.gz --force
```

#### Schedule and retention

- Schedule is a 5-field cron expression in UTC, evaluated by an in-process loop in `blobd`. Examples: `0 3 * * *` (daily 03:00 UTC), `*/5 * * * *` (every 5 min for testing).
- Retention is the **union** of three buckets:
  - `daily=N` keeps the newest backup per UTC date for the most recent N dates
  - `weekly=N` keeps the newest backup per ISO year-week for the most recent N weeks
  - `monthly=N` keeps the newest backup per year-month for the most recent N months
- A backup that is the newest in any bucket is kept. Anything outside all buckets is deleted from BOTH local and remote on the next cycle. Filenames not matching the `YYYY-MM-DDTHH-MM-SSZ.sql.gz` template are always kept (defensive).

#### Behind a reverse proxy (Cloudflare, etc.)

If the S3-compatible endpoint is fronted by Cloudflare or another proxy that normalizes headers, AWS SigV4 will fail with `SignatureDoesNotMatch` for `PutObject` (the Go SDK signs `Accept-Encoding`/`Content-Type` etc., which proxies often rewrite). Point the endpoint at the origin host directly (e.g. via the Nomad-allocated port for a self-hosted MinIO, a private VPC endpoint for AWS, or a "DNS only" subdomain) and the issue disappears.

#### Recursive dogfood (v0.7 ship verification)

The platform backs itself up to itself: a MinIO instance is deployed as a Blob app at `~/code/blob-dogfood/blob-minio/`, and the demo Postgres ships its dumps to `s3://blob-backups/demo/` on that same MinIO. End-to-end:

1. Plant a sentinel row, force a backup → file lands locally AND in MinIO with matching sha256.
2. Drop the table, delete all local backup files, `restore --from s3` → sentinel row reappears.
3. With `*/2 * * * *` schedule and `daily=3`, six backups accumulate over 12 minutes → only three survive in BOTH local and MinIO after the next prune cycle.

### Per-project users (v0.6)

A *project* is a `(role, database, password, statement_timeout)` tuple living on a shared Postgres instance. Apps from unrelated `blob.yaml` files can each bind to their own project on the same instance and cannot see each other's data.

```sh
blob postgres project list <instance>
blob postgres project create <instance> <project> [--timeout 30s]
blob postgres project url <instance> <project>
blob postgres project timeout <instance> <project> <duration>
blob postgres project destroy <instance> <project>
```

What `create` runs (as the instance's `blob` superuser, inside the running Postgres container):

```sql
CREATE ROLE <project> LOGIN PASSWORD '<random>';
CREATE DATABASE <project> OWNER <project>;
REVOKE ALL ON DATABASE <project> FROM PUBLIC;
ALTER ROLE <project> SET statement_timeout = '<ms>';
```

`destroy` is the inverse: `REASSIGN OWNED + DROP OWNED + DROP DATABASE + DROP ROLE`.

Project name rules: `[a-z][a-z0-9_]{0,30}[a-z0-9]`. Lowercase, digits, underscores; must start with a letter. These are SQL identifiers, so we keep them strict.

#### Statement timeout

Default `30s`. Configurable per project:

```sh
blob postgres project timeout demo app-a 2s     # tight bound during chaos drills
blob postgres project timeout demo app-a 5m     # heavy analytics that need it
```

The CLI accepts any Go `time.Duration` string (`2s`, `500ms`, `5m`, `1h`).

`ALTER ROLE ... SET statement_timeout` only affects new sessions, so v0.6 also runs `pg_terminate_backend(...)` against the role's existing connections after each timeout change. Apps using `pg.Pool` and similar reconnect automatically and pick up the new value within a few seconds.

#### Worked example: two apps on one instance

```yaml
# repo A: ~/code/iso-app-a/blob.yaml
name: iso-app-a
form: web-service
port: 8080
services: [demo.app_a]
```

```yaml
# repo B: ~/code/iso-app-b/blob.yaml
name: iso-app-b
form: web-service
port: 8080
services: [demo.app_b]
```

```sh
blob postgres project create demo app_a
blob postgres project create demo app_b
cd ~/code/iso-app-a && blob deploy
cd ~/code/iso-app-b && blob deploy
```

Each app sees:
- `DATABASE_URL` pointing at its own role + database
- `current_user` = the project name
- `current_database()` = the project name
- Cross-project SELECT (`select * from app_b.secrets` from inside iso-app-a) returns `relation "app_b.secrets" does not exist`
- Direct CONNECT to the other database returns `FATAL: permission denied for database "app_b"`

#### What per-project users do NOT do (yet)

- **Schema-level sharing**: there's no way to deliberately share a table between two projects. If two apps need to read the same data, they need either (a) one project they both bind to, or (b) the legacy `services: [<instance>]` binding for one of them with a manual `GRANT`.
- **Backup/restore is per-instance, not per-project**. See backup section above.
- **Quota / disk caps per project**. Postgres has `ALTER USER ... CONNECTION LIMIT` and `ALTER DATABASE ... CONNECTION LIMIT` we can wire later; for now all projects share the instance's resources.

#### Verified live as part of v0.6 ship

Two real apps deployed at <https://iso-app-a.irrigate.cc/secret> and <https://iso-app-b.irrigate.cc/secret>. Each returns its own secret + the cross-project read attempt's error string:

```
GET https://iso-app-a.irrigate.cc/secret
{
  "app": "app_a",
  "pg_user": "app_a",
  "pg_database": "app_a",
  "own_secret": "app_a-secret-yadbpk",
  "cross_project_read": "relation \"app_b.secrets\" does not exist"
}

GET https://iso-app-b.irrigate.cc/secret
{
  "app": "app_b",
  "pg_user": "app_b",
  "pg_database": "app_b",
  "own_secret": "app_b-secret-xgirsa",
  "cross_project_read": "relation \"app_a.secrets\" does not exist"
}
```

Statement timeout round-trip:

```
$ blob postgres project timeout demo app_a 2s
set demo.app_a statement_timeout = 2s
$ time curl -s https://iso-app-a.irrigate.cc/slow
{"ok":false,"error":"canceling statement due to statement timeout"}
real    0m2.23s
$ blob postgres project timeout demo app_a 60s
$ time curl -s https://iso-app-a.irrigate.cc/slow
{"ok":true,"result":{"pg_sleep":"","msg":"finished after sleep"}}
real    0m5.23s
```

### Resource sizing

Default: 500 MHz CPU, 512 MiB RAM. Tune `--cpu` and `--memory` on `create`.

### Destroy

```sh
blob postgres destroy my-pg
```

Stops the Nomad job, removes the meta file, leaves the Docker volume in place.
Reclaim disk with `docker volume rm blob-pg-my-pg`.

---

## Valkey

Valkey is a Redis-compatible in-memory store. AUTH is required (random password generated per instance) and AOF persistence is enabled by default so writes survive restarts.

### Create

```sh
blob valkey create my-cache
```

Optional flags:

- `--version 8` — image tag suffix for `valkey/valkey:<v>-alpine` (default `8`).

### Bind apps to it

```yaml
name: redis-counter
form: web-service
port: 8080
services:
  - my-cache
```

Injected env: `REDIS_URL`, `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`. ioredis, node-redis, redis-rb, redis-py, etc. all read `REDIS_URL` natively.

Multi-binding gets a prefixed set: `MY_CACHE_URL`, `MY_CACHE_HOST`, ….

### Use it

```sh
blob valkey list
blob valkey url my-cache       # redis://:<pw>@<host>:<port>/0
blob valkey destroy my-cache
```

### Resource sizing

Default: 200 MHz CPU, 256 MiB RAM. Bump for write-heavy workloads or large keyspaces. `--maxmemory-policy` and similar Valkey flags will be configurable in a future release; for now edit `/srv/blob/jobs/valkey-<name>.nomad` and re-run.

### Destroy

```sh
blob valkey destroy my-cache
```

Same semantics as Postgres: meta and Nomad job removed, Docker volume `blob-valkey-<name>` preserved.

---

## End-to-end examples shipping with v0.5

### Postgres-backed CRUD app

`/tmp/blob-guestbook/server.js` (Node + `pg`, ~50 lines, real `INSERT`/`SELECT`):

```yaml
# blob.yaml
name: guestbook
form: web-service
port: 8080
services: [my-pg]
```

Live at <https://guestbook.irrigate.cc/>.

### Valkey-backed counter

`~/code/blob-dogfood/redis-counter/server.js` (Node + `express` + `ioredis`):

```yaml
# blob.yaml
name: redis-counter
form: web-service
port: 8080
services: [demo-cache]
```

`GET /` returns the number of times `pages` has been hit. Live at <https://redis-counter.irrigate.cc/>.

### Postgres backup/restore round-trip (verified live)

```
==> insert sentinel
INSERT 0 1
==> backup
backed up in 500ms
  path:  /srv/blob/backups/postgres/demo/2026-05-03T19-16-02Z.sql.gz
  size:  986 B
==> delete sentinel; count=0 confirmed
==> restore latest --force
restored in 400ms
==> sentinel re-appears with original id, label, timestamp
 id |               label               |              ts               
----+-----------------------------------+-------------------------------
  1 | sentinel-row-v0.5-roundtrip-take2 | 2026-05-03 19:16:02.220362+00
```


## Observability (v0.8): Loki + Grafana + Promtail

The Blob ships managed log storage (Loki), dashboards (Grafana), and a per-node log shipper (Promtail). All three are full Nomad jobs with persistent volumes, like postgres/valkey.

```sh
# 1. Stand up Loki — single-binary mode, filesystem store at /loki, ~512 MB resident.
blob loki create platform-logs

# 2. Stand up Grafana — auto-provisioned with a Loki datasource pointing at the
#    instance you pass via --loki, plus a default "All Blob apps" dashboard.
blob grafana create platform-graf --loki platform-logs
blob grafana url platform-graf      # prints url + admin password

# 3. Stand up Promtail — system job, one alloc per node. Tails
#    /opt/nomad/data/alloc/*/alloc/logs/*.std{out,err}.[0-9]* and ships to Loki.
blob promtail create platform-shipper --loki platform-logs
```

### How `blob logs` interacts with Loki

`blob logs <app>` works without any of the above (it falls back to `nomad alloc logs --tail`). When a Loki instance is registered AND the operator passes `--since` or `--grep`, the server resolves the app's allocations via Nomad and queries Loki with `{job="nomad-alloc",alloc=~"<id1>|<id2>|..."}`:

```sh
blob logs my-app --since 5m
blob logs my-app --since 1h --grep "ERROR"
blob logs my-app --since 10m --follow      # polls every 2s, dedup'd
```

The response includes `Source: "loki"` or `Source: "nomad"` so callers can tell which path was used.

### Why we don't slurp historical logs on first start

A naive Promtail deployment on a busy node tails every existing alloc log file from byte 0, replaying potentially hundreds of MB through Loki and instantly OOM-killing the ingester. We avoid that with a `prestart` lifecycle task on the Promtail group that walks the alloc dir, computes each file's current end-of-file offset, and writes a `positions.yaml` to `/alloc/data/`. Promtail starts, loads positions, seeks to the tail, and only forwards lines written AFTER the seed ran. Steady-state throughput drops from MB/s on boot to KB/s.

### Loki tuning

Loki defaults assume a multi-GB host with multiple ingesters. Our config (rendered into `/local/loki-config.yaml` via Nomad template) tightens the in-memory profile so the whole stack fits in a 512 MB allocation:

- `chunk_idle_period: 30s`, `chunk_target_size: 524288`, `max_chunk_age: 1h` — flush early, keep buffers small
- `query_scheduler.max_outstanding_requests_per_tenant: 2048`, `frontend.max_outstanding_per_tenant: 2048` — don't 429 the operator's own queries
- `auth_enabled: false` — single-tenant; Loki is bound to the host's private network only
- `replication_factor: 1`, `ring.kvstore.store: inmemory` — no clustering needed for a single platform node

### UFW

The Loki and Grafana ports (13100, 13000 by default) are bound to `0.0.0.0` so containers in the docker bridge network can reach them. Open them in UFW:

```sh
sudo ufw allow 13000:13100/tcp comment "blob-observability"
```

### Recursive dogfood (v0.8 ship verification)

Sentinel `V08-XYZ-1777844181` was hit against `compose-hello.irrigate.cc` from the platform host; 30 s later `blob logs compose-hello --since 5m` returned the matching nginx access-log line with the `Source: loki` field set. `--grep "V08-XYZ"` returned only the matching lines. Loki resident memory: 85 MiB / 512 MiB cap.

## NATS (v0.10): managed messaging

Single-node NATS with JetStream enabled. One instance per cluster is the expected shape.

```sh
blob nats create platform-nats
# Apps bind via:
#   services:
#     - platform-nats
# and receive NATS_URL in their environment.
```

`platform-nats` listens on `nats://<host>:14222` by default (port pool 14222–14322 for additional instances). UFW: open `14222:14322/tcp` from the docker bridge.

JetStream data persists on the Docker named volume `blob-nats-<name>` mounted at `/data` inside the container. `blob destroy` keeps the volume so you can resurrect — same semantics as postgres/valkey.

### Verified pub/sub round-trip

The `~/code/blob-dogfood/blob-nats-demo/` app deploys with `services: [platform-nats]`, receives `NATS_URL`, subscribes to `blob.demo.>`, and publishes a tick every 5s. `curl https://blob-nats-demo.irrigate.cc/` returns `{"received":N,"lastReceived":"tick-<ms>"}` confirming the pub→sub round-trip lands in the same NATS instance.

## Object storage (v0.14): managed S3-compatible

Single-node MinIO with one auto-provisioned bucket per instance. Apps bind via `services: [<name>]` and receive a full S3-style env block.

```sh
blob storage create assets                     # creates instance + bucket named "assets"
blob storage create user-uploads --bucket prod-uploads --version RELEASE.2025-04-08T15-41-24Z
blob storage url assets                        # prints endpoint + bucket + keys + console URL
blob storage list
blob storage destroy assets                    # Docker volume blob-storage-assets preserved
```

Env injected into bound apps (first storage binding wins the canonical slot):

```
S3_ENDPOINT          http://<host>:<api-port>
S3_BUCKET            <bucket>
S3_ACCESS_KEY        blob-<instance>
S3_SECRET_KEY        <random-48-char-hex>
S3_REGION            us-east-1
S3_USE_PATH_STYLE    true

# AWS-SDK-conventional aliases (so default SDK env works without remapping)
AWS_ACCESS_KEY_ID
AWS_SECRET_ACCESS_KEY
AWS_REGION
AWS_ENDPOINT_URL_S3
```

For two storage bindings, the second instance's env is name-prefixed: `<NAME>_ENDPOINT`, `<NAME>_BUCKET`, `<NAME>_ACCESS_KEY`, `<NAME>_SECRET_KEY`.

API port pool 14500–14598 (paired API/UI). Docker bridge needs `ufw allow 14500:14600/tcp` to reach the data plane from app containers.

The bucket is auto-created via `mc mb --ignore-existing` in a one-off `minio/mc:latest` container right after the MinIO instance comes up. If that step fails (e.g. no docker daemon access), the instance is still live; create the bucket manually:

```sh
docker run --rm --network host minio/mc:latest sh -c \
  'mc alias set s http://<host>:<api-port> blob-<instance> <secret-key> && mc mb s/<bucket>'
```

### Verified live (v0.14 ship)

A two-component blob app with one component bound to `services: [assets]` writes a sentinel file via the AWS SDK and reads it back from another route. Round-trip lands in MinIO; the same file is visible via `mc ls s/assets`.

## MySQL (v0.17): managed relational

Single-node MySQL 8 with one auto-provisioned database + app user per instance. Same template pattern as postgres/valkey.

```sh
blob mysql create my-mysql                      # creates instance + database 'my-mysql'
blob mysql create app-mysql --database app
blob mysql url my-mysql                         # prints full mysql:// DSN with password
blob mysql list
blob mysql destroy my-mysql                     # Docker volume blob-mysql-my-mysql preserved
```

Env injected into bound apps (first mysql binding wins the canonical slot):

```
MYSQL_URL         mysql://blob:<pass>@<host>:<port>/<db>
MYSQL_HOST
MYSQL_PORT
MYSQL_USER
MYSQL_PASSWORD
MYSQL_DATABASE
```

Additional bindings get name-prefixed env: `<NAME>_URL`, `<NAME>_HOST`, etc.

Port pool 15300–15399. Persistent `/var/lib/mysql` on Docker named volume `blob-mysql-<name>`. The official `mysql:8` image's `docker-entrypoint` provisions the database + user on first start (empty data dir); subsequent restarts skip provisioning.

UFW: open `15300:15400/tcp` from the docker bridge so app containers can reach the data plane.

## ClickHouse (v0.17): managed OLAP

Single-node clickhouse-server, OLAP mode. Two ports per instance: HTTP REST (8123-shaped) on the host's HTTP-port, native TCP (9000-shaped) on the host's native-port. Default user `blob` with a generated password.

```sh
blob clickhouse create my-ch                    # creates instance + database 'my-ch'
blob clickhouse create events --database events
blob clickhouse url my-ch                       # prints native clickhouse:// DSN
blob clickhouse list
blob clickhouse destroy my-ch
```

Env injected (first clickhouse binding wins canonical slot):

```
CLICKHOUSE_URL          clickhouse://blob:<pass>@<host>:<native>/<db>
CLICKHOUSE_HTTP_URL     http://blob:<pass>@<host>:<http>/
CLICKHOUSE_HOST
CLICKHOUSE_PORT         (native)
CLICKHOUSE_HTTP_PORT
CLICKHOUSE_USER
CLICKHOUSE_PASSWORD
CLICKHOUSE_DATABASE
```

Port pools: HTTP 15500–15599, native 15600–15699. UFW: open `15500:15700/tcp` from the docker bridge.

Memory floor is real: ClickHouse below 1 GiB will OOM under any non-trivial query. Default `memory: 1024` per instance; bump for production-shaped workloads.
