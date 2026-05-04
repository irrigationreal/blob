# Cost rollups and resource accounting

`blob costs` reports the resources Nomad has reserved across the fleet. It is an accounting surface, not a billing system: it shows CPU shares, memory MiB, disk MiB, allocation counts, and app-to-node placement without exposing allocation ids, secrets, DSNs, or environment values.

Snapshots are rebuilt from Nomad nodes plus active allocations and persisted at:

```text
/srv/blob/costs/latest.json
```

The same collection path refreshes `/srv/blob/resource-graph.json`, so `blob costs` and `blob nodes list` agree on reserved / available / total capacity.

## Commands

```sh
blob costs summary
blob costs apps
blob costs nodes
```

Add an optional monthly platform cost to apportion a rough estimate:

```sh
blob costs summary --monthly-usd 120
blob costs apps --monthly-usd 120
blob costs nodes --monthly-usd 120
```

The estimate is intentionally simple. App estimates are split by reserved memory because memory is the scarce resource for this Blob today. Node estimates are split by total node memory. CPU, disk, and allocation counts remain visible so you can make your own judgement when a workload is CPU-heavy or disk-heavy.

## Summary output

```text
generated: 2026-05-04T06:10:00Z
nodes:     1
apps:      42
allocs:    47
cpu:       18450/13550/32000
memory:    22368/1674/24042MiB
disk:      12600/491152/503752MiB
estimate:  $120.00/mo
```

The resource fields are `reserved/available/total`.

## Per-app output

```text
APP                              CPU      MEM      DISK       ALLOC      EST     NODES                        ENV
platform-prom                    500      512      300        1          $2.75   platform
blob-mongo-demo                  200      256      300        1          $1.37   platform                     prod
```

The API response has the same shape under `/v1/costs/apps`. It includes app/job name, environment when known, CPU shares, memory MiB, disk MiB, active allocation count, node names, and optional estimate.

## Per-node output

```text
ID         NAME       ADDR         STATUS   ELIGIBLE   CPU R/A/T        MEM R/A/T             DISK R/A/T             EST     ALLOC
639cb577   platform   65.21.9.22   ready    eligible   18450/13550/32000 22368/1674/24042MiB  12600/491152/503752MiB $120.00 47
```

This is useful when a high-RAM server looks underused but Nomad has most of its memory reserved. Pair it with `blob nodes recommend` before deploying a large workload.

## API

```text
GET /v1/costs
GET /v1/costs/apps
GET /v1/costs/nodes
GET /v1/costs?monthly_usd=120
```

All endpoints are authenticated and read-only. They require the normal read scope for administrative API surfaces.
