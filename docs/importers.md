# Importers

Translate third-party app definitions into a `blob.yaml` (and, where useful, a `Dockerfile`). Goal: get an existing project deployable on The Blob with one command.

```
blob import <kind> <path> [--out PATH] [--yes]
blob from-nextjs <dir> [--yes]
blob from-netlify <dir-or-netlify.toml> [--yes]
blob deploy --from <kind> <path>
```

Available kinds: `compose`, `procfile`, `fly`, `nextjs`, `netlify`, `render`, `vercel`, `nix`, `helm`, `kubernetes`.

Every importer prints the generated YAML and a list of warnings about anything it could not translate. Existing files are not overwritten unless `--yes` is passed.

## Helm (`import helm`)

Accepts a chart directory or packaged `chart.tgz`. The importer runs `helm template blob-import <path>` locally, then translates the rendered Kubernetes YAML.

| Kubernetes object | blob.yaml |
|---|---|
| `Deployment` / `StatefulSet` with matching `Service` | `form: web-service`, `port:` from Service targetPort/containerPort |
| `Deployment` / `StatefulSet` without a service | `form: daemon` unless a container port implies web-service |
| `DaemonSet` | daemon or web-service, with warning that per-node placement is not preserved |
| `Service` | associated with matching workload by selector |
| `Ingress` host rules | `domain:` / `domains:` for the matched service |
| `Job` | `form: job` |
| `CronJob` | `form: cronjob`, `schedule:` |
| literal container `env` | `env:` map |
| CPU/memory requests or limits | `cpu:` / `memory:` |
| PVC volume mounts | `volumes:` |
| extra containers | `sidecars:` |

Dropped with warnings: ConfigMaps, Secrets, RBAC, service accounts, HPAs, PDBs, NetworkPolicies, probes, init containers, pod scheduling fields, security context, image pull secrets, non-PVC volumes, service types, Ingress TLS secrets, and path routing. Recreate those as Blob secrets, managed services, deploy plugins, autoscaling, or edge/app config.

## Kubernetes manifests (`import kubernetes`)

Accepts a single `.yaml` / `.yml` file or a directory. Directories are read recursively and all YAML documents are translated with the same Kubernetes object mapper used by the Helm importer.

```sh
blob import kubernetes ./k8s
blob deploy --from kubernetes ./k8s
```

Supported objects and warnings match the Helm table above. Use this path when a project already has rendered manifests or Kustomize output and there is no Helm chart to render first.

## Render (`import render`)

Accepts either a `render.yaml`/`render.yml` file or a directory containing one.

| render.yaml | blob.yaml |
|---|---|
| `services[].type: web` | `form: web-service` |
| `runtime: static` or `staticPublishPath` | `form: static`, `root:` |
| `type: worker` / `pserv` | `form: daemon` |
| `type: cron` + `schedule` | `form: cronjob`, `schedule:` |
| `envVars[].key/value` | `env:` map |
| `disk.name/mountPath` | `volumes:` |
| `numInstances` | `replicas:` |

Web services default to Render's conventional `PORT=10000` unless an env var sets `PORT`. Node, Python, and Go services without an existing Dockerfile get a generated Dockerfile using the service's `buildCommand` and `startCommand`. Docker services keep the existing Dockerfile path assumption; if Render uses a custom `dockerfilePath` or `dockerContext`, the importer warns so you can move files or adjust paths.

Dropped with warnings: Render databases, env var groups, generated/secret env vars, regions, plans, health check paths, headers/routes, and stateful services. Recreate those as Blob managed services, secrets, or edge/app config.

## Vercel (`import vercel`)

Accepts either a `vercel.json` file or a directory containing one. Static projects become `form: static`.

| vercel.json | blob.yaml |
|---|---|
| `name` | manifest `name` |
| `outputDirectory` | `root:` |
| `buildCommand` | `build:` |
| `installCommand` + package.json | inferred `build:` when `buildCommand` is absent |
| `env` / `build.env` | `env:` map |

If the project is Next.js and has `next.config.{js,mjs,ts}`, the importer delegates to the Next.js importer so `output: 'standalone'` and `output: 'export'` keep the same behavior as `blob from-nextjs`.

Dropped with warnings: routes, rewrites, redirects, headers, functions, and crons. Recreate cron jobs with `blob jobs schedule`; move serverless functions into `form: function` or a web-service.

## Nix flakes (`import nix`)

Accepts either a `flake.nix` file or a directory containing one. The importer writes a `blob.yaml`, a Dockerfile, and a `.dockerignore`.

The generated Dockerfile uses `nixos/nix`, runs `nix build`, exposes `PORT=8080`, and starts the first executable under `result/bin`. This works for flakes with a default package that builds a web binary. If the flake only exposes `apps`, `devShells`, or `nixosConfigurations`, the importer warns and you should edit the Dockerfile or command before deploying.

## Next.js (`from-nextjs`)

Detection: `package.json` has `"next"` in `dependencies` AND a `next.config.{ts,js,mjs}` exists. Picks the package manager from the lockfile (`pnpm-lock.yaml` → pnpm, `yarn.lock` → yarn, `bun.lockb` / `bun.lock` → bun, otherwise npm).

### `output:` handling

The importer reads `next.config` and aligns it with one of two supported deploy paths:

| existing `output:` | action | result |
|---|---|---|
| `'standalone'` | none | web-service, 3-stage Docker build runs `node server.js` |
| `'export'` | none | static-site (`form: static`, `root: out`, build runs `next build`) - Caddy serves the prerendered HTML |
| any other value | rewrite to `'standalone'` | web-service, modified config emitted to ExtraFiles |
| not set | inject `output: 'standalone',` | web-service, modified config emitted |

Injection target is the first `{` after a known config-object marker, in priority order:

1. `NextConfig =` (typed TS: `const cfg: NextConfig = {`)
2. `NextConfig=` (no spaces)
3. `export default {` (bare default export)
4. `module.exports =` / `module.exports={` (CommonJS)

If none of those match the importer falls back to a regex that catches the bare `const|let|var <ident> = {` shape - common in plain `.mjs` configs that JSDoc the type instead of using a TS annotation. If even the fallback fails, the importer leaves the file alone and surfaces a warning.

### Dockerfile

Three stages: `deps` (install), `builder` (`next build` with `NEXT_TELEMETRY_DISABLED=1`), `runner` (copies `.next/standalone` + `.next/static` + `public`, runs as non-root uid 1001, listens on `:3000`).

Install commands deliberately do **not** use `--frozen-lockfile` / `npm ci`. An importer parachuting into someone else's project should not fail because the lockfile drifted; if you want the strict guarantee, edit the Dockerfile after import.

### `basePath`

If `next.config` has `basePath: '/some/path'` it's preserved as-is - Traefik routes the entire host to the app, so the basePath only affects the app's internal routes. The importer prints an informational warning.

## Netlify (`from-netlify`)

Accepts either `<dir>` or `<dir>/netlify.toml`. If you pass a directory we look for `netlify.toml` inside.

### Field mapping

| netlify.toml | blob.yaml |
|---|---|
| `[build] command` | `build:` |
| `[build] publish` | `root:` (defaults to `.` if absent) |
| `[build.environment]` | `env:` map |
| (always) | `form: static` |

App name is the parent directory's basename, sanitized (lowercase, hyphens). For relative paths (`.`), this resolves to the absolute path's basename.

### What's dropped (warned, not silent)

- `[[redirects]]` - Caddy/Traefik handle redirects differently. Translate critical ones to a Caddyfile or app-level routes.
- `[[headers]]` - set custom headers via your origin app or a Traefik middleware.
- `[functions]` and `[[edge_functions]]` - Netlify functions are not auto-translated yet; move them into `form: function` handlers.
- `[[plugins]]` - replicate their effects in the build command if you need them.

## Compose (`import compose`)

Translates `docker-compose.yaml` → blob.yaml. One service-with-ports → single-component manifest. Multiple service-with-ports → multi-component App.

Stateful images (`postgres`, `mysql`, `mariadb`, `mongo`, `redis`, `valkey`, `rabbitmq`, `nats`, `kafka`, `clickhouse`, `elasticsearch`, `opensearch`, `minio`) are flagged as warnings with a hint to use the matching `blob <kind> create` instead and bind via `services:`.

Not translated (warned): `[[networks]]`, `configs`, `secrets`, `[deploy].replicas`, `[[healthcheck]]`. Bind mounts (`/host:/container`) are dropped with a warning - they aren't portable across nodes; use `volumes:` for Docker named volumes instead.

## Procfile (`import procfile`)

Each `<process>: <command>` line becomes a component:

| process name | form | port |
|---|---|---|
| `web` | web-service | 8080 |
| `release` | job | (manual `blob exec` after deploy - Heroku auto-runs these, blob does not) |
| any other | daemon | n/a |

Commands with shell metacharacters (`|`, `&`, `;`, `<`, `>`, `$`, backtick) are wrapped in `sh -c`. Otherwise they're split on whitespace. Requires a Dockerfile in the project - blob does not run buildpacks.

## Fly (`import fly`)

Maps `fly.toml` → blob.yaml.

| fly.toml | blob.yaml |
|---|---|
| `app` | manifest `name` |
| `[build] image` | `image:` |
| `[env]` | `env:` |
| `[http_service] internal_port` (or first `[[services]]`) | `port:` |
| `[[mounts]] source/destination` | `volumes:` |
| `[processes]` (multiple) | multi-component App |

Dropped with warnings: `primary_region` (blob is single-region), `[deploy] release_command` (blob doesn't auto-run release tasks - use `blob exec` after deploy), `[[checks]]` (blob has its own per-form healthchecks), `[[statics]]`, `[[vm]]` (set `cpu`/`memory` in blob.yaml directly).
