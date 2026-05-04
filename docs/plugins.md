# Deploy plugins

Blob deploy plugins are per-app shell hooks stored on the control plane. They are intentionally small: a pre-deploy command, a post-deploy command, an enable flag, and a timeout.

```sh
blob plugins set my-app \
  --pre 'test -f blob.yaml' \
  --post 'curl -fsS https://example.com/deploy-hook?app=$BLOB_APP' \
  --timeout 30

blob plugins list
blob plugins show my-app
blob plugins disable my-app
blob plugins enable my-app
blob plugins remove my-app --yes
```

State is stored at `/srv/blob/plugins/<app>.json` mode 0600. Hooks run inside `blobd` on the platform host with `/bin/sh -c`, so treat commands as operator-controlled configuration, not user-submitted input.

## Runtime

The pre hook runs after source builds, image push, secrets resolution, and service binding, but before the Nomad job is submitted. The post hook runs after the job is scheduled and, for long-running forms, after Blob has observed the job as running.

Both hooks receive:

| Variable | Meaning |
|---|---|
| `BLOB_HOOK` | `pre` or `post` |
| `BLOB_APP` | app name |
| `BLOB_ENVIRONMENT` | environment from the deploy request |
| `BLOB_JOB_ID` | rendered Nomad job id |
| `BLOB_IMAGE` | image scheduled for the deploy |
| `BLOB_URL` | public URL for HTTP forms, empty otherwise |

Hook output is captured only on failure and capped at 64 KiB. Hooks default to a 30 second timeout and may be set up to 300 seconds. On timeout Blob kills the hook process group and fails the deploy phase.

## Failure behavior

A failing pre hook stops the deploy before Nomad receives the new job. A failing post hook reports deploy failure after scheduling; the workload may already be running, so fix the hook and redeploy or inspect the app with `blob status <app>`.

Use deploy hooks for small platform-local glue such as notifying another system, asserting required files, refreshing a cache, or kicking a smoke test. Long migrations and recurring work belong in `blob jobs run` or `blob jobs schedule` so they have their own lifecycle and logs.
