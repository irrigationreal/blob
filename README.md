# The Blob

Self-hosted, agent-native infrastructure that makes a fleet of mixed hardware feel like one big resource pool. Stand in a project folder, run `blob deploy`, and get back a public HTTPS URL.

This monorepo contains the v1 implementation:

- [`cmd/blobctl`](cmd/blobctl) — the CLI you run on your laptop
- [`cmd/blobd`](cmd/blobd) — the control-plane HTTP API that runs on the platform host
- [`skills/blob`](skills/blob) — Claude Code skill (`/blob`) that wraps the same flow
- [`internal/manifest`](internal/manifest) — `blob.yaml` parser and validator
- [`docs/`](docs) — full Business Requirements & Spec (`the-blob-spec.md`)

Live deployment: `https://blob.irrigate.cc` (control-plane API), apps land at `https://<name>.irrigate.cc`.

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

That's it. The CLI tarballs the folder, ships it to `blobd`, which builds on the host (so the architecture matches), pushes to the platform registry, schedules a Nomad job, and Traefik picks up the route through service tags. ACME certificates issue on first hit.

## What `blob deploy` actually does

```
project folder
  → tar.gz (excludes .git, node_modules, .next, dist, build, .venv, ...)
  → POST /v1/sources/<app>          (server unpacks to /srv/blob/sources/<app>)
  → POST /v1/deploy                 (server runs the phases below)
  → docker login registry           (registry-login phase)
  → docker build / compose build    (build phase)
  → docker push                     (push phase)
  → render Nomad HCL job, nomad job run  (schedule phase)
  → poll Nomad until status=running (ready phase)
  → reply with phase timings and the URL
```

Every phase is timed and surfaced to the user. Failures stop the chain and return the failed phase name with the underlying error.

## blob.yaml

`blob.yaml` is the canonical authoring file. Everything is optional except `name`. Defaults are sensible, flags override.

```yaml
name: link-checker          # required, lowercase a-z 0-9 -
form: web-service           # web-service | daemon | job | cronjob
domain: links.example.com   # default: <name>.<base-domain>
port: 8080                  # required for web-service unless inferable
image: ""                   # if set, skip build; deploy this image directly
cpu: 500
memory: 512
replicas: 1
env:
  LOG_LEVEL: info
schedule: ""                # for cronjob form, e.g. "0 * * * *"
```

## Workload forms

Every workload boils down to one of:

| Form          | What it is                                  |
|---            |---                                          |
| `web-service` | long-running HTTP server, public or internal |
| `daemon`      | long-running process, no inbound traffic     |
| `job`         | one-shot task                                |
| `cronjob`     | recurring task on a cron expression          |

`function` and `vm` are reserved in `blob.yaml` for future shapes; not implemented in v1's first cut.

## CLI reference

```
blob init [--name N] [--port P] [--domain D]
blob login --endpoint URL [--token T]
blob deploy [--name N] [--port P] [--domain D] [--image IMG]
blob list
blob status <app>
blob logs <app> [-n 200]
blob destroy <app>
blob whoami
blob version
```

Environment variables: `BLOB_HOST`, `BLOB_TOKEN`. They override config file values.

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

A reference systemd unit is in [`scripts/blobd.service`](scripts/blobd.service) and a deploy script in [`scripts/install-blobd.sh`](scripts/install-blobd.sh). The unit binds to `127.0.0.1:8787`; the recommended setup is to expose blobd through Traefik with the same routing tags any other Blob app uses.

## Architecture (v1, real)

```
laptop                                    platform host
─────                                     ─────────────
blobctl ──tar.gz──> /v1/sources/<app> ──> /srv/blob/sources/<app>
        ──json───>  /v1/deploy
                      │
                      ├── docker login <registry>
                      ├── docker build  -t <registry>/<app>:<tag>
                      ├── docker push   <registry>/<app>:<tag>
                      ├── render Nomad HCL → /srv/blob/jobs/<app>.nomad
                      ├── nomad job run
                      └── poll /v1/job/<app>/allocations until running
                                                │
                                                ▼
                                            Traefik (Nomad provider)
                                                │
                                                ▼
                                          https://<app>.<base-domain>
```

## What's in the spec but not in v1's runtime yet

The spec ([`docs/the-blob-spec.md`](docs/the-blob-spec.md)) describes the full v1 platform: Kata microVMs, blebs (warm pool), the resource graph, hot journal volumes, rewind, projection-hash drift detection, observability stack, autoscaling, multi-region failover, preview environments, status pages, plugins, etc.

The runtime currently shipping in this repo wraps a Nomad/Docker/Traefik substrate to nail the deploy ergonomics first. The spec is the contract for the rest of v1 and the codebase is structured (`internal/manifest`, typed API surface, phase-timed deploys, `blobctl` ↔ `blobd` parity) to grow into it without a rewrite.

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
  server/           # blobd implementation: routes, deploy phases, Nomad job rendering
  tarball/          # deterministic tar.gz packer with sane excludes
skills/
  blob/             # /blob Claude Code skill
docs/
  the-blob-spec.md  # full Business Requirements + Technical Spec
scripts/
  install.sh        # blobctl installer
  install-blobd.sh  # blobd installer (run on the platform host)
  blobd.service     # systemd unit
```

## Building from source

```sh
go build ./...
go build -o /usr/local/bin/blob ./cmd/blobctl
go build -o /usr/local/bin/blobd ./cmd/blobd
```

## License

MIT.
