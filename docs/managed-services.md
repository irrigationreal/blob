# Managed services

The Blob provisions stateful services as Nomad jobs with persistent volumes and injects credentials into apps that bind them.

v0.4 ships **managed Postgres**. Valkey, NATS, ScyllaDB, etc. are planned and follow the same pattern.

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

If the app binds multiple Postgres instances (`services: [primary, analytics]`), the first one wins the `DATABASE_URL`/`PGHOST` slot and each instance also gets a prefixed set: `PRIMARY_URL`, `PRIMARY_HOST`, …, `ANALYTICS_URL`, `ANALYTICS_HOST`, …

### Use it

From the CLI:

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

### Resource sizing

Default: 500 MHz CPU, 512 MiB RAM, no disk cap (Docker volume grows as Postgres grows). Tune `--cpu` and `--memory` on `create`.

For non-trivial workloads, give it more RAM and pin it to a node with SSD-backed storage. v0.4 doesn't yet implement node-class targeting from the postgres CLI, but you can edit `/srv/blob/jobs/pg-<name>.nomad` and `nomad job run` it back manually if needed.

### Backups

v0.4 does **not** implement managed backups. The data is in a Docker volume on the host (`docker volume inspect blob-pg-<name>`). Until automated backups land, do this nightly:

```sh
docker exec $(docker ps --filter "name=pg" -q) pg_dumpall -U blob | gzip > /backups/$(date -u +%F).sql.gz
```

Restore by piping the gzipped dump back into `psql`. The next blobd release (v0.5) will add a `blob postgres backup` and `blob postgres restore` flow that does this automatically and ships the dumps off-host.

### Destroy

```sh
blob postgres destroy my-pg
```

Stops the Nomad job, removes the meta file, leaves the Docker volume in place. To reclaim the disk:

```sh
docker volume rm blob-pg-my-pg
```

This is deliberate: destroy is reversible by `blob postgres create my-pg` again with the same name; the existing volume gets re-attached and your data is intact.

### What it does not do (yet)

- HA / replication: it's a single Postgres pod. For production HA, run CloudNativePG yourself and bind via raw `secrets:` containing the connection string (it's a normal Kubernetes operator; The Blob can run alongside it on the same nodes).
- Backup automation (see above).
- Automatic SSL termination on the connection. The DSN includes `sslmode=disable` because the wire is on a private network port; for production traffic over public internet, put a TLS-terminating sidecar in front or use SSH tunneling.
- Per-app database isolation. Currently every app binding `services: [demo]` shares one user/database/permissions. The next release will add per-binding role + db creation with scoped credentials.

### End-to-end example: a real CRUD app

`/tmp/blob-guestbook/server.js` (a real ~50-line Node + `pg` Express-like server):

```yaml
# blob.yaml
name: guestbook
form: web-service
port: 8080
services:
  - my-pg
```

```sh
blob postgres create my-pg     # ~25 s, end-to-end ready
blob deploy                    # builds Dockerfile, deploys, app reaches DATABASE_URL on boot
curl -X POST -H 'content-type: application/json' \
  -d '{"message":"hello"}' https://guestbook.<base-domain>/api/entries
curl https://guestbook.<base-domain>/api/entries
```

Verified live as part of v0.4 ship at <https://guestbook.irrigate.cc/>.
