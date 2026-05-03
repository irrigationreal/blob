# Autoscaling on The Blob

`blob autoscale set <app>` enables per-app horizontal autoscaling. blobd runs an in-process controller that ticks every 30 seconds, queries the first registered Prometheus instance for the configured metric, and adjusts the app's replica count via `nomad job scale`.

## How the math works

```
ratio   = current_metric / target_metric
desired = ceil(current_replicas * ratio)
desired = clamp(desired, min, max)
```

This is the same model Kubernetes HPA uses. If the metric is twice the target you scale to 2× replicas; if it's half you scale to half (rounded up); when on target you don't move.

`scaleApp` is only called when desired ≠ current and the relevant cooldown window has elapsed. Cooldowns are tracked per direction:

- `--cooldown-up` (default 60s) — minimum interval between scale-up actions
- `--cooldown-down` (default 180s) — minimum interval between scale-down actions

Cooldown timestamps live in memory only. A blobd restart resets them.

## Built-in metrics

```
--metric cpu        # percent CPU per replica (0..100), needs cAdvisor
--metric memory     # MiB working-set per replica, needs cAdvisor
--metric http_qps   # requires app to expose blob_http_requests_total{app="<n>"} on /metrics
```

For anything else, pass the full PromQL with `__APP__` as the placeholder for the app name:

```sh
blob autoscale set my-app \
  --metric 'sum(rate(custom_metric_total{app="__APP__"}[1m]))' \
  --target 100 --min 1 --max 8
```

## What blobd does on metric outage

If Prometheus returns an error (network failure, no result, no Prometheus registered at all), the controller logs the failure and **does nothing**. It never scales to zero just because a metric scrape was momentarily unreachable. The same is true for Nomad outages — `currentReplicas` failing leaves the desired count unchanged.

## Cooldown enforcement is verified by tests

`internal/server/autoscaler_test.go` covers:
- `desiredReplicas` math (8 cases — clamping, boundaries, fractional)
- cooldown window enforcement (back-to-back ticks inside cooldown produce no scale call)
- metric-fetch failure → no-op
- `buildAutoscaleQuery` shape for cpu/memory/http_qps + raw PromQL passthrough

## Verified live (v0.11)

```
$ blob autoscale set blob-whoami-import \
    --metric 'scalar(up{job="blobd"})' \
    --target 0.5 --min 1 --max 3 --cooldown-up 10s --cooldown-down 10s

# After 30s tick:
2026/05/03 22:54:08 autoscale[blob-whoami-import]: 2 → 3 (metric=scalar(up{...}) value=1.000 target=0.500)

# Flipped target to 2.0 → ratio 0.5 → scale-down:
$ blob autoscale set blob-whoami-import --target 2.0 ...
# Within ~30s, replicas: 1 (clamped to min)
```

## Caveats

- cAdvisor is not bundled with The Blob. To use `--metric cpu` or `--metric memory` you need to deploy cAdvisor separately (or any cgroup metrics exporter that produces `container_cpu_usage_seconds_total{container_label_com_hashicorp_nomad_job_name="<app>"}`). docs/observability.md covers the host shape.
- The autoscaler operates on Nomad's `Count` field via the `nomad job scale` CLI. It does not touch the manifest on disk — your blob.yaml's `replicas` is the *initial* count, and any subsequent `blob deploy` will reset to that value. Re-apply autoscale after redeploys, or remove the `replicas:` line so the manifest doesn't override.
- One controller per blobd. If you run blobd HA (currently unsupported) you'd need to elect a leader.
