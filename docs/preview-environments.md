# Preview environments

Ephemeral per-branch deploys of an existing app, reachable at `<app>-<branch>.<base-domain>`.

```sh
blob preview create <app> --branch <name>
blob preview list <app>
blob preview destroy <app> <branch>
```

## Lifecycle

A preview is a lightweight wrapper over the existing deploy/destroy paths. There is no separate scheduler, builder, or registry — every step reuses the parent app's machinery.

### `blob preview create my-app --branch test1`

1. Server reads the parent app's last-uploaded source from `/srv/blob/sources/my-app/` (the same tarball `blob deploy` uploads). If the parent has never been deployed, the call fails with `no uploaded source for parent app …`.
2. Server reads the parent's `blob.yaml` from inside that source dir and copies the relevant fields (`form`, `port`, `env`, `services`, `command`, `cpu`, `memory`, `replicas`, `root`, `build`) into a fresh `DeployRequest`. For multi-component App manifests, only the first component is deployed as a preview (limitation, see below).
3. Synthesizes job name `my-app-test1` and domain `my-app-test1.<base-domain>`.
4. Calls `deployFromSource` — same path `blob deploy` uses. Build, push, schedule, ready.
5. Writes a sentinel file `/srv/blob/previews/my-app/test1.json` recording app, branch, job id, domain, URL, created-at.

### `blob preview list my-app`

Reads `/srv/blob/previews/my-app/*.json` and prints one row per preview.

### `blob preview destroy my-app test1`

1. Calls the standard `destroyApp(ctx, "my-app-test1")` — Nomad job stop, sources purge, meta purge.
2. Removes the sentinel file. Idempotent: missing-job is not an error.

## State

| path | what it is |
|---|---|
| `/srv/blob/sources/my-app/` | the parent's source tarball — shared between parent and previews, never duplicated |
| `/srv/blob/previews/my-app/test1.json` | preview sentinel (mode 0600) |
| `/srv/blob/jobs/my-app-test1.nomad` | the synthetic Nomad job HCL — created by `deployFromSource`, removed by `destroyApp` |
| `/srv/blob/jobs/my-app-test1.meta.json` | the synthetic job's meta — same lifecycle |

Nothing about the preview deploy is special at the Nomad layer; it's just another job.

## Hostname

`<app>-<branch>.<base-domain>`. The wildcard cert covering `*.<base-domain>` (provisioned by Traefik's ACME flow on first parent deploy) covers preview hosts automatically — no extra DNS config needed as long as the wildcard A record points at the platform host.

`<branch>` must match `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$` — same shape as the existing app-name regex with a 30-char ceiling so combined `<app>-<branch>` stays under DNS label limits.

## Verification (v0.12 ship)

```
$ blob preview create peepal --branch test1
ready in 14.8s
  url:    https://peepal-test1.irrigate.cc
  job:    peepal-test1
  domain: peepal-test1.irrigate.cc

$ curl -sS https://peepal-test1.irrigate.cc/ | head -1
<!DOCTYPE html><html lang="en"><head>...<title>PeePal — Community Fund</title>...

$ blob preview list peepal
BRANCH                 JOB          DOMAIN                           CREATED
test1                  peepal-test1 peepal-test1.irrigate.cc         2026-05-03T23:44:18Z

$ blob preview destroy peepal test1
destroyed preview peepal/test1

$ ssh platform@65.21.9.22 'sudo nomad job status | grep peepal'
peepal                         service         50        running  …
   ↑ only the parent remains; peepal-test1 is gone

$ curl -sS -o /dev/null -w "%{http_code}\n" https://peepal-test1.irrigate.cc/
502    ← Traefik can't route, no backend exists
```

## Limitations

- **Multi-component apps deploy only the first component as a preview.** The whole-app shape (one Nomad job per component, same source tarball) is shipped for `blob deploy --app` but not yet for previews. Workaround: split your App into discrete apps if you need full multi-component preview coverage. Lifting this is a follow-up.
- **No webhook auto-create.** Spec marked the `POST /v1/webhooks/preview` HMAC receiver as stretch; deferred. CI integration today: `blob preview create $APP --branch $CI_BRANCH` from your pipeline on PR open, `blob preview destroy $APP $CI_BRANCH` on close.
- **No automatic stale-cleanup.** Old preview sentinels persist until you `blob preview destroy` them. If you want a TTL, add a cron that scans `/srv/blob/previews/` and deletes by `created_at`.
- **Parent must have been deployed at least once.** The preview reads from the parent's source dir; it doesn't accept an upload from the CLI directly. To preview a brand-new app, do one `blob deploy` of the parent first.
- **Env/secrets are inherited verbatim from the parent.** No per-branch overrides yet. If you need a different `DATABASE_URL` per preview, manage it via `blob secrets set --env preview-test1 …` and add a `--env` flag to preview create as a follow-up.
