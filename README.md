# The Blob

Self-hosted, agent-native PaaS. Stand in a folder, run `blob deploy`, get a public HTTPS URL.

This monorepo is The Blob's runtime. Live deployment: <https://blob.irrigate.cc> (control-plane API). Deployed apps land at `https://<name>.irrigate.cc`.

## Quick start

```sh
# 1. Install blobctl (macOS / Linux, amd64 or arm64)
curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/install.sh | sh

# 2. Authenticate
blob login --endpoint https://blob.irrigate.cc --token $YOUR_TOKEN

# 3. In any project folder
blob init                 # auto-detects: Dockerfile, Compose, static, package.json build
blob deploy
```

That's it. The CLI tarballs the folder, ships it to `blobd`, which builds on the host (so the architecture matches), pushes to the platform registry, schedules a Nomad job, and Traefik picks up the route. ACME issues the cert automatically on first hit.

## Live dogfooded apps

These are real side projects pulled out of `~/code` and deployed with no edits beyond `blob init`:

- <https://pong.irrigate.cc/> — single-file static
- <https://emoji-paint.irrigate.cc/>
- <https://dither-logo.irrigate.cc/>
- <https://robot-avatar.irrigate.cc/>
- <https://nye-2025.irrigate.cc/>
- <https://magneto.irrigate.cc/> — added v0.15
- <https://black-ship-scroll.irrigate.cc/> — added v0.15 (uses .blobignore to skip 480 MB of media)
- <https://canada-ripple.irrigate.cc/> — added v0.15

Plus an example with a custom domain attached: <https://static.darv.ai/> (same backing app as `pong`).

### Imported via v0.9 importers

The same platform host runs these via `blob import compose|procfile|fly` → `blob deploy` with no manual blob.yaml edits:

- <https://blob-nginx-import.irrigate.cc/> — `nginx:alpine`, imported from a one-service docker-compose.yml
- <https://blob-whoami-import.irrigate.cc/> — `traefik/whoami`, imported from compose with env vars
- <https://blob-httpbin-import.irrigate.cc/> — `kennethreitz/httpbin`, imported from compose
- <https://python-procfile.irrigate.cc/> — `python -m http.server` from a Heroku Procfile + Dockerfile
- <https://blob-fly-import.irrigate.cc/> — `httpd:alpine` from a fly.toml with `[build] image=`

## What runs today

| Capability                                                     | Status   |
|---|---|
| One-command deploy from any folder                             | shipped  |
| **Auto-detect** Dockerfile / Compose / `index.html` / build script | shipped  |
| **Static sites** via `form: static` (Caddy serves a folder)    | shipped  |
| `web-service`, `daemon`, `job`, `cronjob` workload forms       | shipped  |
| **Kata microVM isolation** via `isolation: kata` or `blob deploy --isolation kata` on nodes bootstrapped with `ENABLE_KATA=1` | shipped |
| Multi-component **App** manifest (web + worker + cron)         | shipped  |
| **Bundle** sidecars (co-scheduled tasks sharing the netns)     | shipped  |
| Per-component **command override**                             | shipped  |
| **Secrets**: AES-256-GCM at rest, per-environment, env injection | shipped |
| **Environments** (`prod`, `staging`, `pr-1234`, …)             | shipped  |
| **Managed Postgres** with `services:` env injection (`DATABASE_URL`) | shipped  |
| **Per-project Postgres users** (`services: [<instance>.<project>]`) with isolated role + database + per-project `statement_timeout` | shipped  |
| **Postgres backups** (`blob postgres backup/backups/restore`) | shipped  |
| **Off-host backup shipping** to S3-compatible stores + scheduled cron + retention | shipped  |
| **Observability**: managed Loki + Grafana + Promtail; `blob logs --since/--grep/--follow` queries Loki when registered, falls back to nomad alloc tail | shipped  |
| **Importers**: `blob import compose|procfile|fly|nextjs|netlify` translate third-party manifests to blob.yaml; `blob deploy --from <kind> <path>` does both in one shot | shipped  |
| **Preview environments**: `blob preview create <app> --branch <name>` for ephemeral per-branch deploys at `<app>-<branch>.<base>`; multi-component preview ships in v0.13; GitHub webhook auto-create on PR open/synchronize/close in v0.13 | shipped  |
| **Object storage**: managed S3-compatible (`blob storage create <name>`); `services: [<storage>]` injects S3_ENDPOINT/S3_BUCKET/S3_ACCESS_KEY/S3_SECRET_KEY (+ AWS_* aliases) | shipped  |
| **Managed MySQL** (`blob mysql create`); `services: [<mysql>]` injects MYSQL_URL/MYSQL_HOST/MYSQL_PORT/MYSQL_USER/MYSQL_PASSWORD/MYSQL_DATABASE | shipped  |
| **Managed ClickHouse** (`blob clickhouse create`); `services: [<ch>]` injects CLICKHOUSE_URL (native) + CLICKHOUSE_HTTP_URL + standard ports/creds | shipped  |
| **Messaging**: managed NATS with JetStream (`services: [<nats>]` injects NATS_URL) | shipped  |
| **Tracing**: managed Tempo (OTLP gRPC); blobd auto-exports deploy spans when a Tempo is registered; Grafana provisioned with Tempo datasource | shipped  |
| **Metrics**: managed Prometheus + Nomad service discovery + blobd /metrics; Grafana provisioned with Prometheus datasource | shipped  |
| **Autoscaling**: per-app horizontal autoscaler (cpu/memory/http_qps/raw PromQL) with min/max + cooldowns; `blob autoscale set <app>` | shipped  |
| **Service rollup**: `blob services list` shows postgres/valkey/loki/grafana/promtail/nats/tempo/prometheus in one table | shipped  |
| **Managed Valkey** (Redis-compatible) with `services:` env injection (`REDIS_URL`) | shipped  |
| **Custom domains** with `blob domains attach` (auto-HTTPS)     | shipped  |
| **Multiple hostnames** per app                                 | shipped  |
| **Scaling** (`blob scale`)                                     | shipped  |
| **Restart** (`blob restart`)                                   | shipped  |
| **Releases** (`blob releases`)                                 | shipped  |
| **Exec** into a running allocation (`blob exec`)               | shipped  |
| **Open** in browser (`blob open`)                              | shipped  |
| **Volumes**: per-app Docker named volumes                      | shipped  |
| **Nodes**: list, drain, undrain, generate join script          | shipped  |
| **Resource graph + placement preflight**: persisted Nomad node/allocation capacity, `blob nodes recommend`, and impossible deploy refusal before Nomad scheduling | shipped |
| **Doctor** drift / orphan / liveness checks                    | shipped  |
| **Manifest projection hashes**: deploy records intended job projection and `blob doctor` detects live/on-disk drift | shipped |
| **Bootstrap script** for turning a fresh server into a Blob    | shipped  |
| Phase-timed deploys                                            | shipped  |
| `/blob` Claude Code skill                                      | shipped  |

## What the spec describes that's not yet here

The full v1 spec ([`docs/the-blob-spec.md`](docs/the-blob-spec.md)) is the destination. The runtime ships the deploy core, operability surfaces, and the first managed services. Honest gap list:

- Blebs warm pool, hot journal volumes, rewind
- **Tempo/Prometheus**: shipped in v0.10 — see managed services above
- **Multi-region** active-passive failover
- **Status pages**, cost rollups, plugins, web console, GPU/confidential compute
- Importers beyond compose/procfile/fly/nextjs/netlify: Helm, Render, Vercel, Nix flakes

## Setting up your own Blob

Three short docs:

- **[`docs/host-setup.md`](docs/host-setup.md)** — turn a fresh server into a Blob (one shell script + a systemd unit).
- **[`docs/joining-nodes.md`](docs/joining-nodes.md)** — add another machine to an existing Blob.
- **[`docs/operator.md`](docs/operator.md)** — day-2 ops: backups, drains, upgrades, recovering from a dead node.
- **[`docs/managed-services.md`](docs/managed-services.md)** — managed Postgres: create, bind apps via `services:`, get the DSN, destroy.

## blob.yaml

`blob.yaml` is the canonical authoring file. Everything is optional except `name`. `blob init` auto-detects a sensible starting point.

### Static site

```yaml
name: my-site
form: static
root: .              # or "dist", "build", "out", "_site", "public"
spa: false           # true = SPA fallback to index.html for any unmatched path
not_found: /404.html # optional: serve this for 404s
```

For React/Vite/Next/etc. with a build step:

```yaml
name: my-spa
form: static
build: "pnpm install && pnpm run build"
root: dist
spa: true
```

### Single web service

```yaml
name: hello
form: web-service
port: 8080
domain: hello.example.com
domains:
  - hello-alt.example.com
secrets:
  - env: API_TOKEN
    name: hello-api-token
volumes:
  - name: data
    path: /var/lib/hello
```

### Cronjob

```yaml
name: nightly-backup
form: cronjob
schedule: "0 3 * * *"
```

### Bundle (sidecar)

```yaml
name: bundle
form: web-service
port: 8080
sidecars:
  - name: tunnel
    image: cloudflare/cloudflared:latest
    args: ["tunnel", "run"]
    cpu: 50
    memory: 64
```

### Multi-component App

```yaml
name: my-app
environment: prod
components:
  - name: web
    form: web-service
    port: 8080
    command: ["node", "web.js"]
    secrets:
      - env: DATABASE_URL
        name: my-app-db
  - name: worker
    form: daemon
    command: ["node", "worker.js"]
    secrets:
      - env: DATABASE_URL
        name: my-app-db
  - name: nightly
    form: cronjob
    schedule: "0 3 * * *"
    command: ["node", "nightly.js"]
```

Each component becomes its own Nomad job named `<app>-<component>` (e.g. `my-app-web`, `my-app-worker`).

## CLI reference

```
blob init [--name N] [--port P] [--domain D] [--form F] [--root D]
blob login --endpoint URL [--token T]
blob deploy [--name N] [--port P] [--domain D] [--image IMG] [--env ENV] [--cpu C] [--memory M] [--replicas N]
blob list
blob status <app>
blob logs <app> [-n 200]
blob scale <app> <replicas>
blob restart <app>
blob releases <app>
blob open <app>
blob exec <app> -- <cmd ...>
blob destroy <app> [--yes]

blob domains attach <app> <host> [--mode MODE]

blob secrets list [--env ENV]
blob secrets set <name> [--env ENV] [--from FILE | --value V]
blob secrets unset <name> [--env ENV]

blob postgres list
blob postgres create <name> [--version V] [--database D]
blob postgres url <name>
blob postgres connect <name>
blob postgres backup <name>
blob postgres backups <name>
blob postgres restore <name> [path|latest] [--force]
blob postgres destroy <name> [--yes]

blob postgres project list <instance>
blob postgres project create <instance> <project> [--timeout 30s]
blob postgres project url <instance> <project>
blob postgres project timeout <instance> <project> <duration>
blob postgres project destroy <instance> <project> [--yes]

blob valkey list
blob valkey create <name> [--version V]
blob valkey url <name>
blob valkey destroy <name> [--yes]

blob nodes list
blob nodes drain <id>
blob nodes undrain <id>
blob nodes join          # prints a one-liner shell script for a new server

blob volumes list

blob doctor

blob whoami
blob version
```

Environment variables: `BLOB_HOST`, `BLOB_TOKEN`. They override config file values.

## Architecture

```
laptop                                    platform host(s)
─────                                     ─────────────
blobctl ──tar.gz──> /v1/sources/<app> ──> /srv/blob/sources/<app>
        ──json───>  /v1/deploy[-app]
                      │
                      ├── resolve secrets (AES-256-GCM at rest in /srv/blob/secrets)
                      ├── docker login <registry>
                      ├── if form=static: synthesize Caddyfile + Dockerfile.blob-static
                      ├── docker build  -t <registry>/<app>:<tag>
                      ├── docker push   <registry>/<app>:<tag>
                      ├── render Nomad HCL → /srv/blob/jobs/<id>.nomad
                      │   plus meta.json (form, env, domain, image)
                      ├── nomad job run
                      └── poll /v1/job/<id>/allocations until running
                                                │
                                                ▼
                                      Traefik (Nomad provider)
                                                │
                                                ▼
                                      https://<id>.<base-domain>
```

Multi-node fleets: any number of additional Nomad clients can register with `blob nodes join`. Workloads place across the fleet automatically based on capacity.

## Repository layout

```
cmd/
  blobctl/                 # the CLI
  blobd/                   # the control-plane daemon
internal/
  api/                     # request/response types, shared between client and server
  client/                  # HTTP client
  config/                  # blobctl config
  detect/                  # auto-detect project type for `blob init`
  manifest/                # blob.yaml parser and validator
  secrets/                 # at-rest-encrypted secret store
  server/                  # blobd: routes, deploy phases, Nomad job rendering
                           # nodes.go: nodes/join/volumes/restart/exec/domains
                           # static.go: form=static (Caddy) build path
                           # doctor: drift/orphan checks
  tarball/                 # deterministic tar.gz packer
skills/blob/               # /blob Claude Code skill
docs/
  the-blob-spec.md         # full Business Requirements + Technical Spec
  host-setup.md            # turn a fresh server into a Blob
  joining-nodes.md         # add a node to an existing Blob
  operator.md              # day-2 ops runbook
scripts/
  install.sh               # blobctl installer (curl | sh)
  bootstrap-host.sh        # one-shot: Docker + Nomad + Traefik + registry
  install-blobd.sh         # systemd unit installer
  blobd.service            # systemd unit
  blobd-edge.nomad         # Nomad job that exposes blobd through Traefik
```

## Building from source

```sh
go build ./...
go build -o /usr/local/bin/blob ./cmd/blobctl
go build -o /usr/local/bin/blobd ./cmd/blobd
go test ./...
```

## License

MIT.
