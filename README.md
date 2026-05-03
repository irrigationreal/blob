# The Blob

Self-hosted, agent-native PaaS. Stand in a folder, run `blob deploy`, get a public HTTPS URL.

This monorepo contains The Blob's runtime. Live deployment: <https://blob.irrigate.cc> (control-plane API), apps land at `https://<name>.irrigate.cc`.

## Quick start

```sh
# 1. Install blobctl (macOS / Linux, amd64 or arm64)
curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/install.sh | sh

# 2. Point it at a Blob endpoint and authenticate
blob login --endpoint https://blob.irrigate.cc --token $YOUR_TOKEN

# 3. In any project folder with a Dockerfile or Compose file
blob init                 # writes blob.yaml
blob deploy               # builds, pushes, schedules, returns the URL
```

## What the runtime ships today (v0.2)

| Capability                                              | Status      |
|---|---|
| Single-command deploy from a folder                     | shipped     |
| `web-service`, `daemon`, `job`, `cronjob` workload forms| shipped     |
| Multi-component **App** manifest (apps with web + worker + cron, etc.) | shipped     |
| **Bundle** sidecars (co-scheduled tasks sharing the network namespace) | shipped     |
| Per-component **command override** (one image, many entrypoints)        | shipped     |
| **Secrets**: AES-256-GCM encrypted store, per-environment, env injection| shipped     |
| **Environments** (`prod`, `staging`, `pr-1234`, …)                     | shipped     |
| **Scaling** (`blob scale <app> <n>`)                                   | shipped     |
| Custom **domains** (per-component or per-app)                          | shipped     |
| Auto HTTPS (Traefik + ACME)                                            | shipped     |
| **Doctor** drift / orphan / liveness checks                            | shipped     |
| Phase-timed deploys (registry-login → build → push → schedule → ready) | shipped     |
| Cross-platform release binaries (linux/darwin × amd64/arm64)            | shipped     |
| `/blob` Claude Code skill                                              | shipped     |

## What the spec describes that the runtime does **not** yet implement

The full spec ([`docs/the-blob-spec.md`](docs/the-blob-spec.md)) lists the shape of the v1 platform. The runtime in this repo is the deploy-and-operate core. The pieces below are described in the spec but are **not** yet wired into `blobd`/`blobctl`. Each item is a real gap, not a faked one — they're flagged so users know what to expect.

- Kata microVM isolation, blebs warm pool, hot journal volumes, rewind
- Resource graph + manifest projection-hash drift detection
- Full **observability stack** (Loki/Tempo/Prometheus); workloads can already emit logs through Nomad's allocation log API but there is no integrated metrics/traces backend yet
- **Autoscaling** beyond explicit `blob scale`
- **Backups** + point-in-time recovery for managed services and Volumes
- **Multi-region** active-passive failover
- **Preview environments** auto-created from CI webhooks (the *Environment* primitive is already in `blob.yaml`; the CI webhook glue isn't)
- **Status pages**
- **Cost** rollups
- **Plugin host**
- Importers beyond Compose/Dockerfile (Helm, Fly, Heroku, Render, Vercel/Netlify, Cloudflare Workers, Procfile, Nix flakes)
- Web console
- Managed-service catalog (Postgres via CloudNativePG, Valkey, NATS, ScyllaDB, etc.) — runs on Nomad today but does not have first-class `blob.yaml` `ManagedService` shape yet
- GPU + confidential compute

The roadmap is to build these on top of the existing `blobctl` ↔ `blobd` API contract, not to rewrite. The manifest format already has hooks (`secrets:`, `volumes:`, `sidecars:`, `components:`, `environment:`, `command:`) so adding observability/backups/managed-services later doesn't break manifests written today.

## blob.yaml

`blob.yaml` is the canonical authoring file. Everything is optional except `name`. Defaults are sensible, flags override.

### Single-component (web service)

```yaml
name: hello
form: web-service       # web-service | daemon | job | cronjob
port: 8080
domain: hello.example.com
env:
  LOG_LEVEL: info
secrets:
  - env: API_TOKEN
    name: hello-api-token
volumes:
  - name: data
    path: /var/lib/hello
```

### Single-component (cronjob)

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

Each component becomes its own Nomad job named `<app>-<component>` (e.g. `my-app-web`, `my-app-worker`). All components share the same uploaded source directory and the same built image; `command` lets each one run a different process.

## CLI reference

```
blob init [--name N] [--port P] [--domain D]
blob login --endpoint URL [--token T]
blob deploy [--name N] [--port P] [--domain D] [--image IMG] [--env ENV]
blob list
blob status <app>
blob logs <app> [-n 200]
blob scale <app> <replicas>
blob destroy <app> [--yes]

blob secrets list [--env ENV]
blob secrets set <name> [--env ENV] [--from FILE | --value V]
blob secrets unset <name> [--env ENV]

blob doctor

blob whoami
blob version
```

Environment variables: `BLOB_HOST`, `BLOB_TOKEN`. They override config file values.

## What `blob deploy` actually does

```
project folder
  → tar.gz (excludes .git, node_modules, .next, dist, build, .venv, ...)
  → POST /v1/sources/<app>          (server unpacks to /srv/blob/sources/<app>)
  → POST /v1/deploy                 (single component)
    or POST /v1/deploy-app          (multi-component)
  → for each component:
    docker login registry           (registry-login phase)
    docker build / compose build    (build phase)
    docker push                     (push phase)
    resolve secrets:                (env + secret store lookup)
    render Nomad HCL job, write meta.json, nomad job run  (schedule phase)
    poll /v1/job/<id>/allocations until running           (ready phase)
  → reply with phase timings and the URL(s)
```

Every phase is timed and surfaced to the user. Failures stop the chain and return the failed phase name with the underlying error.

## blobd — running the control plane

`blobd` is a single Go binary. It needs:

- A working Nomad cluster (HTTP API on `127.0.0.1:4646` by default)
- Docker on the same host (used for `docker build`, `docker push`, registry login)
- A container registry the host can push to
- A Traefik instance with the Nomad provider (so the routing tags written into Nomad jobs become live routes)
- Wildcard DNS pointing at the host (`*.<base-domain>`)

```sh
blobd \
  --listen 127.0.0.1:8787 \
  --base-domain irrigate.cc \
  --dc pve \
  --registry registry.irrigate.cc \
  --state /srv/blob \
  --registry-creds /etc/blob/registry-credentials.txt
```

Bearer token is read from `BLOB_TOKEN`. If unset, the API is open (only safe behind a firewall).

A reference systemd unit is in [`scripts/blobd.service`](scripts/blobd.service) and a deploy script in [`scripts/install-blobd.sh`](scripts/install-blobd.sh). The unit binds to `127.0.0.1:8787`; the recommended setup is to expose blobd through Traefik with the same routing tags any other Blob app uses (see [`scripts/blobd-edge.nomad`](scripts/blobd-edge.nomad)).

## Architecture

```
laptop                                    platform host
─────                                     ─────────────
blobctl ──tar.gz──> /v1/sources/<app> ──> /srv/blob/sources/<app>
        ──json───>  /v1/deploy[-app]
                      │
                      ├── resolve secrets (AES-256-GCM at rest in /srv/blob/secrets)
                      ├── docker login <registry>
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

## Repository layout

```
cmd/
  blobctl/          # the CLI
  blobd/            # the control-plane daemon
internal/
  api/              # request/response types, shared between client and server
  client/           # HTTP client for the API
  config/           # blobctl client config
  manifest/         # blob.yaml parser and validator
  secrets/          # at-rest-encrypted secret store
  server/           # blobd: routes, deploy phases, Nomad job rendering, doctor
  tarball/          # deterministic tar.gz packer with sane excludes
skills/
  blob/             # /blob Claude Code skill
docs/
  the-blob-spec.md  # full Business Requirements + Technical Spec
scripts/
  install.sh        # blobctl installer
  install-blobd.sh  # blobd installer (run on the platform host)
  blobd.service     # systemd unit
  blobd-edge.nomad  # Nomad job that exposes blobd through Traefik
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
