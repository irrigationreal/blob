# Static sites

The Blob serves static sites via a generated Caddy container. Three paths get you there:

```sh
# 1. Auto-detect: no blob.yaml in CWD + index.html + no Dockerfile → static site.
cd ~/my-static-thing && blob deploy

# 2. Explicit: --static flag forces form: static, root: . even with a blob.yaml present.
cd ~/my-static-thing && blob deploy --static .

# 3. Manifest: write a blob.yaml.
cat > blob.yaml <<EOF
name: my-static-thing
static: .          # shorthand for `form: static, root: .`
EOF
blob deploy
```

All three end at the same place: the source tarball uploads, blobd's `prepareStaticBuild` synthesizes a Dockerfile + Caddyfile, the regular build path produces an image, Nomad schedules it, Traefik routes `<name>.<base-domain>` to it.

## Auto-detect rule

A `blob deploy` invoked with NO `blob.yaml` AND NO manifest-set form will infer `form: static, root: .` when:

- `index.html` exists in the source root, AND
- `Dockerfile` does NOT exist in the source root.

Caddy then serves the source tree as static files (compression on, `try_files {path} {path}/ {path}.html` for extensionless URLs).

If a Dockerfile is present the auto-detect is skipped — a Dockerfile is a strong signal the operator wants their own container, not a static-site wrapping. Pass `--static` to override.

## SPA mode

For React/Vite/Next-export style apps where unmatched paths should fall through to `/index.html`:

```yaml
name: my-spa
static: dist           # serve the build output
spa: true              # try_files {path} /index.html
```

`build:` is also honored — runs in the source dir before Caddy builds the image, so you can ship a Vite/Astro/Hugo built tree with one manifest:

```yaml
name: my-spa
static: dist
build: pnpm install && pnpm run build
spa: true
```

## What the runtime is

`internal/server/static.go:prepareStaticBuild` generates two files inside the source tarball:

- `Caddyfile.blob` — `:8080`, root `/srv`, `file_server`, `encode zstd gzip`, with the `try_files` block tuned for SPA-vs-extensionless behavior.
- `Dockerfile.blob-static` — `FROM caddy:2-alpine`, copies `<root>/` into `/srv`, copies the Caddyfile, exposes 8080, runs `caddy run`.

Caddy was picked over nginx because it handles the `try_files {path}/ {path}.html` extensionless-URL case in one line, supports zstd alongside gzip without a module rebuild, and renews internal certs automatically (irrelevant for the platform path, but useful if operators expose Caddy directly later).

## Excludes

Source uploads honor `.blobignore` at the source root (one entry per line, dirname/basename match). Use this to skip dev artifacts that would otherwise blow past Cloudflare's 100 MB upload limit:

```
# .blobignore
node_modules
dist-old
.cache
big-data-set
```

Plus the always-on hardcoded excludes: `.git`, `node_modules`, `.next`, `dist`, `build`, `.venv`, `__pycache__`, `.DS_Store`, `target`, `.terraform`. Override with `static: dist` if your build output IS in `dist` — the manifest's `root` field is matched first, before the tarball's exclude pass.

## Verified live

```sh
$ cd ~/code/vesper-riff && blob deploy --static .
auto-detected static site (index.html present, no Dockerfile)
packaging /Users/pp/code/vesper-riff...
  registry-login   233ms  ok 
  build            874ms  ok 
  push             800ms  ok 
  schedule       12038ms  ok 
  ready             18ms  ok 
https://vesper-riff.irrigate.cc

$ curl -sS -o /tmp/r.html -w "%{http_code}\n" https://vesper-riff.irrigate.cc/
200

$ head -5 /tmp/r.html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Glass Corridor</title>
```

vesper-riff has no blob.yaml and no Dockerfile — `blob deploy --static .` (and `blob deploy` with no flag) both work.

## Out of scope (as of v0.18)

- Hugo/Jekyll/Astro/Next.js SSG build-step pipelines beyond what `build:` already does manually
- Asset bundling, image optimization, sourcemap stripping
- Per-path edge caching beyond what Caddy provides
- Preview branches scoped to a static site (`blob preview create` works on any form, but doesn't run a fresh `build:` against PR-branch content yet)
