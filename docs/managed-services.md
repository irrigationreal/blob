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
  - my-pg
```

At deploy, blobd injects:

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

- Roles other than the auto-created `blob` user (single-tenant per instance for now).
- Off-host shipping. The backup file lives on the platform host's disk under `/srv/blob/backups/`. v0.6 will add `--to s3://...` and a scheduled cron. Until then: rsync `/srv/blob/backups/postgres/<name>/` to a destination of your choice.

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

