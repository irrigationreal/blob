# Observability on The Blob

The Blob ships logs, traces, and metrics as managed services. Every piece is a single Nomad job with a Docker named volume; you stand them up the same way you'd create a Postgres or Valkey instance.

## Architecture

| Pillar | Storage | Shipper | Default port |
| --- | --- | --- | --- |
| Logs | Loki (single-binary, filesystem) | Promtail (system job, one alloc per node) | 13100 |
| Traces | Tempo (single-binary, filesystem) | OTLP gRPC ingest from app code or blobd itself | 13200 (HTTP), 13201 (OTLP) |
| Metrics | Prometheus (single-binary, TSDB) | Pull-based scrape via Nomad SD + static blobd target | 13300 |
| Dashboards | Grafana | provisioned datasources for whichever of Loki/Tempo/Prometheus you pass at create time | 13000 |

All four bind on `0.0.0.0` (no auth) and are reachable from the Docker bridge once the corresponding UFW rule is in place. They are NOT meant to be reachable from the public internet — keep them behind your platform's private network or a VPN.

## Standing it up

```sh
# 1. Loki — backs the log path
blob loki create platform-logs

# 2. Promtail — ships every node's nomad alloc logs to Loki
blob promtail create platform-shipper --loki platform-logs

# 3. Tempo — accepts OTLP gRPC traces
blob tempo create platform-tempo

# 4. Prometheus — scrapes blobd, traefik, and Nomad-discovered services
blob prometheus create platform-prom

# 5. Grafana — provisioned with whichever of the three you pass
blob grafana create platform-graf \
  --loki platform-logs \
  --tempo platform-tempo \
  --prometheus platform-prom
blob grafana url platform-graf  # prints URL + admin password
```

UFW (one-time, on a fresh host):

```sh
sudo ufw allow 13000:13400/tcp comment "blob-observability"
sudo ufw allow 14222:14322/tcp comment "blob-nats"
sudo ufw allow from 172.17.0.0/16 to any port 8787 proto tcp  # blobd /metrics
sudo ufw allow from 172.17.0.0/16 to any port 4646 proto tcp  # Nomad SD
```

## How blobd produces traces

Once at least one Tempo instance is registered, blobd picks up its OTLP endpoint at startup and exports spans for the deploy and deploy-image code paths via `go.opentelemetry.io/otel`. You can also override via the standard `OTEL_EXPORTER_OTLP_ENDPOINT` env var on the blobd systemd unit.

Service name in spans: `blobd`. Top-level span names: `deploy.source`, `deploy.image`. Attributes include `app`, `form`, and (for image deploys) `image`.

Verify with `curl http://<host>:13200/api/search?tags=service.name%3Dblobd`:

```json
{"traces":[{"traceID":"fbdfe4ca13a1edd5...","rootServiceName":"blobd","rootTraceName":"deploy.image"}]}
```

## How Prometheus discovers targets

The provisioned scrape config has three jobs:

- `blobd` — static target `172.17.0.1:8787/metrics`
- `traefik` — static target `172.17.0.1:8082/metrics` (only `up=1` if you've enabled `--metrics.prometheus` on Traefik)
- `nomad-services` — `nomad_sd_configs` against `172.17.0.1:4646`. Every Nomad service registered with `provider = "nomad"` is pulled. If the workload doesn't expose `/metrics` it shows as `up=0` — that's how operators discover unmonitored apps.

Quick check:

```sh
curl http://<host>:13300/api/v1/query?query=up
```

## How Grafana knows about all three

`blob grafana create --loki <name> --tempo <name> --prometheus <name>` captures each instance's URL at create time, writes a Nomad-rendered `provisioning/datasources/blob.yaml` into the alloc's `local/`, and bind-mounts the dir into the container at `/etc/grafana/provisioning`. The default dashboard `Blob apps — logs / traces / metrics` has three panels: Prometheus `up{}` timeseries, Tempo recent-traces table, Loki `{job=~".+"}` logs panel.

## Memory profile (single-host)

Tuned to fit under 1 GB total resident across the four jobs:

- Loki: 512 MiB cap, ~85 MiB resident steady-state — see [docs/managed-services.md#loki-tuning](managed-services.md)
- Tempo: 512 MiB cap, ~120 MiB resident
- Prometheus: 512 MiB cap, ~150 MiB resident at low cardinality
- Grafana: 384 MiB cap, ~80 MiB resident
- Promtail: 128 MiB cap per node

If you start storing high-cardinality metrics (every-request labels) Prometheus will balloon — that's a Prometheus problem, not a blob problem. Use the `relabel_configs` to drop high-cardinality labels before ingest.
