---
name: blob
description: Deploy the current folder to The Blob — a self-hosted Fly.io-style platform. Use this skill when the user says "deploy this", "ship this", "blob this", or asks to put a project online. Wraps `blobctl` (the CLI) and produces a working public HTTPS URL from a Dockerfile or Compose folder.
---

# The Blob — `/blob` skill

The Blob is a self-hosted PaaS. The CLI is `blob` (also installable as `blobctl`). The control plane is `blobd` running on a platform host. Workloads land at `https://<name>.<base-domain>` automatically.

## When to use this skill

- User says: "deploy this", "ship this", "put this online", "blob this", "let's deploy".
- A folder contains a `Dockerfile`, a `compose.yaml` / `docker-compose.yaml`, a `blob.yaml`, or a runnable language project.
- User wants to redeploy, roll back, tail logs, or destroy a Blob app.

If the project is clearly *not* a deployable web service (a static site without server, a library), say so and stop. Don't force a deploy.

## Prerequisites

The user must have:

- `blob` CLI installed (`curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/install.sh | sh`)
- An endpoint configured: `blob login --endpoint https://blob.irrigate.cc --token $TOKEN`
- A `Dockerfile` or Compose file in the project (or an `--image` to deploy directly)

If `blob whoami` fails, walk the user through `blob login` first.

## Deploy flow

1. Make sure you're in the project root: `pwd` should show the folder containing the Dockerfile/compose file.
2. If there's no `blob.yaml`, run `blob init [--name <slug>] [--port <p>] [--domain <d>]`. Pick a slug from the folder name.
3. Run `blob deploy`. The CLI streams phase timings: registry-login, build, push, schedule, ready.
4. On success, print the URL the CLI returned. Do NOT make up URLs — only echo what the CLI gave you.
5. Curl-test the URL (`curl -sSf <url>`) and report the response so the user can see it works.

## Common commands

```sh
blob whoami                 # confirms endpoint + token
blob init --port 8080       # create blob.yaml
blob deploy                 # build, push, schedule, return URL
blob deploy --image registry.irrigate.cc/foo:tag --port 8080
blob list                   # all apps in the fleet
blob status <app>           # one app's allocations and health
blob logs <app> -n 200      # tail recent stdout/stderr
blob destroy <app>          # tear down (requires typing the name to confirm; pass --yes to skip)
```

## blob.yaml authoring

Minimal:

```yaml
name: <lowercase-slug>
form: web-service       # or daemon | job | cronjob
port: 8080              # required for web-service unless inferable from compose
domain: <name>.<base>   # optional; defaults to <name>.<base-domain>
```

`form` rules:

- **web-service** — long-running HTTP server. Needs `port`. Gets a public HTTPS route automatically.
- **daemon** — long-running, no inbound. No port, no domain.
- **job** — one-shot.
- **cronjob** — needs `schedule` (cron expression).

## Failure modes you will hit

- **`registry creds: open ... permission denied`** — the platform host's `/etc/blob/registry-credentials.txt` isn't owned by the blobd user. Tell the user to run `sudo chown platform:platform /etc/blob/registry-credentials.txt` (or whatever user blobd runs as).
- **`could not detect a port`** — the project has neither a Dockerfile EXPOSE nor a Compose `ports:` mapping. Add `port: <n>` to `blob.yaml`.
- **`build` phase fails** — the Dockerfile is broken. `blob` doesn't fix Dockerfiles; show the build error and tell the user.
- **deploy says ready but the URL 404s** — Traefik usually catches up within a few seconds. Wait briefly and curl again. If it stays 404, run `blob status <app>` to confirm the allocation is healthy.

## What this skill must not do

- Do not invent URLs or "estimated" timings; only print what `blob` returned.
- Do not skip `blob.yaml` validation by passing flags to bypass an obvious problem.
- Do not deploy if the user clearly meant something else (e.g., just opening a folder).
- Do not modify the user's Dockerfile or compose file silently to "fix" build errors. Surface them.

## Reference

Spec, architecture, and roadmap: <https://github.com/darvell/blob>
