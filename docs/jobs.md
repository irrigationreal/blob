# Jobs (one-shot + cron)

`blob jobs` (v0.23) is the platform's primitive for running batch work — short-lived containers that exit after their command finishes. Two flavors:

- **One-shot** (`blob jobs run`) — fires once, blocks until the alloc terminates, returns logs.
- **Periodic** (`blob jobs schedule`) — fires on a cron expression, each fire creates a child Nomad alloc whose logs are addressable by fire index.

Both flavors can attach to a parent app with `--app <name>` (or as a positional). When set, the job inherits the parent's resolved services env — the same `MONGODB_URL`, `CLICKHOUSE_URL`, `POSTGRES_URL`, `STORAGE_*`, etc. that the long-running web-service / daemon sees. This is the whole point of jobs as a first-class form: they should run against the same bound services as the rest of the app.

## One-shot

```sh
blob jobs run my-app \
  --image alpine \
  --cpu 200 --memory 256 \
  -- /bin/sh -c 'echo hello from $(hostname); curl -s $POSTGRES_URL/health'
```

The CLI splits on the first `--` sentinel: everything before is flags, everything after is the container command + args (entrypoint override). The platform waits for the alloc to terminate (default 120s timeout, override with `--timeout 600`) and prints status + exit code. Fetch logs with the printed `blob jobs logs <id>` command.

If `--name` is not provided, an `run-<unix>` name is generated. The Nomad job id is always `blob-job-<name>`.

## Periodic

```sh
blob jobs schedule nightly-backup my-app \
  --cron '0 3 * * *' \
  --image registry.irrigate.cc/my-backup:latest \
  -- /backup.sh --bucket s3://my-app-backups
```

Cron expressions are five fields, UTC. The Nomad `periodic { prohibit_overlap = true }` stanza is set, so a fire that's still running when the next fire is due will skip rather than stack.

`blob jobs logs <id> --fire N` retrieves the Nth fire's logs (1-indexed; `0` or omitted = most recent fire).

```sh
blob jobs logs blob-job-nightly-backup           # most recent fire
blob jobs logs blob-job-nightly-backup --fire 3  # third fire
```

## Inherited env

Bind a job to a parent app and the job's container env is seeded with the parent's resolved services. Example: a web app deployed as

```yaml
name: my-app
form: web-service
services:
  - my-mongo
```

— the web service receives `MONGODB_URL`, `MONGO_URL`, `MONGODB_HOST`, `MONGODB_PORT`, `MONGODB_USER`, `MONGODB_PASSWORD`, `MONGODB_DATABASE`. A `blob jobs run my-app …` against the same parent receives those same vars. Per-job literal env via `--env KEY=VAL` overlays anything inherited.

The Services list is persisted in the app's jobMeta on every deploy. Re-deploy any app that pre-dates v0.23 once before binding jobs to it; otherwise the job won't see the parent's services env (it'll just get an empty literal env + your overlays).

## Lifecycle

| State        | Meaning                                                    |
|--------------|------------------------------------------------------------|
| `pending`    | scheduled, alloc not yet placed                            |
| `running`    | one or more allocs running                                 |
| `dead`       | one-shot terminated (check `exit_code`)                    |
| `stopped`    | the parent periodic was paused with `nomad job stop`       |

`blob jobs cancel <id>` runs `nomad job stop -purge`, drops the rendered job file, and removes the meta.

## Docker volumes

Jobs run on the same Nomad cluster as web-services. They can reach managed-service ports (postgres 15432, mongodb 15700, etc.) via the same UFW rules — no extra config needed.

If a job needs persistent state, mount a Nomad host volume by setting `--volume /host/path:/container/path` (not yet wired in v0.23 — declare it in the manifest's `volumes:` and re-deploy as `form: job` instead, which goes through the regular deploy code path).

## Concurrency

`prohibit_overlap = true` on periodic jobs. One-shot jobs default to `count = 1` (no parallelism); for fan-out, run multiple `blob jobs run` calls with different `--name`s.

## Verified live (v0.23 ship)

```sh
$ blob jobs run blob-mongo-demo --image alpine -- /bin/sh -c 'echo MONGO_URL=$MONGO_URL | head -c 80'
running job (image=alpine, app=blob-mongo-demo)...
  id:        blob-job-run-1777863690
  status:    dead
  exit:      0

$ blob jobs logs blob-job-run-1777863690
--- stdout (fire=0) ---
MONGO_URL=mongodb://blob:d7d3a332ab38916dd5adf92154c65471d35d@65.21.9.22:15700/d
```

The bound app's resolved `MONGO_URL` reached the alpine container's env unchanged.
