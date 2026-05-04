# Web console

`blobd` serves a small operator console at `/ui`. It is server-rendered HTML with inline CSS; there is no frontend build chain, static asset pipeline, JavaScript bundle, or separate web app to deploy.

```sh
curl -H "Authorization: Bearer $BLOB_TOKEN" https://blob.irrigate.cc/ui/apps
```

The console uses the same bearer-token auth and RBAC scopes as the JSON API. `BLOB_TOKEN` has owner access. Scoped service tokens can open only the pages their grants allow.

## Pages

| Page | Scope | Shows |
|---|---|---|
| `/ui/apps` | `apps:read` | app name, status, form, replica count, URL, image |
| `/ui/nodes` | `admin:read` | node status and reserved / available / total CPU, memory, disk |
| `/ui/costs` | `admin:read` | fleet rollup plus top memory apps |
| `/ui/doctor` | `admin:read` | current doctor findings and remediations |
| `/ui/status-pages` | `admin:read` | published status-page bindings |
| `/ui/audit` | `audit:read` | recent mutating API actions, without request/response bodies |
| `/ui/identity` | `identity:admin` | token metadata and grants, never token secrets |

The UI deliberately does not render Nomad allocation ids, environment variables, secret values, DSNs, or service credentials. For low-level allocation detail, use authenticated CLI/API calls or Nomad on the host.

## Browser use

The current console is for operators and agents that can send an Authorization header. It does not add a cookie login flow. That keeps auth behavior identical to the existing API and avoids creating a second session system.
