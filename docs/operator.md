# Day-2 ops

This is the runbook for keeping a Blob healthy. Pair it with `blob doctor`, which runs many of these checks automatically.

## Where state lives

| What                | Where                                            |
|---                  |---                                               |
| Nomad job specs     | `/srv/blob/jobs/<job>.nomad`                     |
| Job metadata        | `/srv/blob/jobs/<job>.meta.json` (includes the last accepted manifest projection hash from v0.26 onward) |
| Resource graph      | `/srv/blob/resource-graph.json`                     |
| Uploaded sources    | `/srv/blob/sources/<app>/`                       |
| Encrypted secrets   | `/srv/blob/secrets/<env>/<name>.enc`             |
| Secret store key    | `/etc/blob/secret-key`                           |
| Registry images     | `/srv/registry/` (under the registry container)  |
| Traefik certs       | `/srv/traefik/acme.json`                         |
| Registry creds      | `/etc/blob/registry-credentials.txt`             |
| API token           | `/etc/blob/env` (BLOB_TOKEN=...)                 |

If you back nothing else up, back up:
- `/etc/blob/` (token, creds, secret key)
- `/srv/blob/` (jobs + sources + secrets)
- `/srv/traefik/acme.json` (certs and ACME account)

A `tar zcf blob-state-$(date -u +%Y%m%d).tar.gz /etc/blob /srv/blob /srv/traefik/acme.json` covers everything but the registry layers, which can be pulled again from upstreams or rebuilt from sources.

## Common ops

### Restart all allocations of an app

```sh
blob restart <app>
```

This issues `nomad job restart` which blue/greens the allocations one at a time.

### Roll back to a previous revision

`blob releases <app>` lists revisions. To roll back to revision N:

```sh
nomad job revert <app> <N>
```

(There is no `blob rollback` yet; this is on the roadmap. Until then `nomad job revert` is the safe path.)

### Drain a node before reboot

```sh
blob nodes drain <id>
# wait until allocations have moved
blob nodes list
sudo reboot
```

After it's back:

```sh
blob nodes undrain <id>
```

### Tail logs across all allocs

```sh
blob logs <app> -n 500
```

Currently returns the most recent N lines from the first running allocation. Streaming logs (`--follow`) is on the roadmap; for now `nomad alloc logs -f <id>` on the host is the workaround.

### Inspect a container

```sh
blob exec <app> -- sh
```

Drops you into a shell inside the running container. (Currently runs the command you give and returns the output; interactive `-it` shells are on the roadmap.)

### Recover a workload after a node death

Nomad reschedules service-type workloads automatically. For workloads that pinned a `volumes:` mount to a specific node, the workload is dead until either the node returns or you redeploy. There's no automatic volume migration in v0.3.

To redeploy from scratch:

```sh
cd <project>
blob deploy
```

The image is rebuilt and pushed; the registry should still have the previous image too, so you can also force a re-schedule:

```sh
nomad job stop -purge <app>
nomad job run /srv/blob/jobs/<app>.nomad
```

### Recovering registry creds

If `/etc/blob/registry-credentials.txt` is lost:

```sh
NEW_USER=blob
NEW_PASS=$(openssl rand -hex 24)
htpasswd -Bbn "$NEW_USER" "$NEW_PASS" | sudo tee /etc/blob/registry.htpasswd >/dev/null
sudo bash -c "cat > /etc/blob/registry-credentials.txt <<EOF
username: $NEW_USER
password: $NEW_PASS
EOF"
sudo chmod 600 /etc/blob/registry-credentials.txt
sudo systemctl restart blobd
```

### Cert renewal is automatic

Traefik renews Let's Encrypt certs automatically before expiry. Nothing to do.

If renewal stalls, the most common cause is rate-limiting from too many failed challenges. Check:

```sh
nomad alloc logs $(nomad job allocs edge-traefik -t '{{(index . 0).ID}}') | grep -i acme
```

### When the API is unreachable

```sh
sudo systemctl status blobd
sudo journalctl -u blobd -n 100 --no-pager
```

If `blobd` itself is healthy but Traefik isn't routing to it: `nomad job status blobd-edge`.

If Nomad itself is unhappy: `nomad agent-info` and `journalctl -u nomad -n 100`.

## Postgres backup shipping (v0.7+)

Each managed Postgres instance can ship dumps to any S3-compatible store on a UTC cron with daily/weekly/monthly retention. State at `/srv/blob/postgres/<instance>/backup-config.json` (mode 0600).

```sh
# Configure once per instance
blob postgres backup-config set my-pg \
  --s3-endpoint https://s3.amazonaws.com \
  --s3-region us-east-1 \
  --s3-bucket blob-backups \
  --s3-prefix my-pg/ \
  --s3-access-key-id <KEY> \
  --s3-secret-access-key <SECRET> \
  --schedule "0 3 * * *" \
  --retention-daily 7 --retention-weekly 4 --retention-monthly 6 \
  --enable

blob postgres backup-config get my-pg     # secret key shown as ***
blob postgres backup-config test my-pg    # HEAD bucket round-trip
blob postgres backups my-pg               # unified local + remote view with sha256
blob postgres restore my-pg latest --from s3 --force
```

Failure mode to watch: if the in-process scheduler can't reach the configured S3 endpoint, `journalctl -u blobd | grep "ship failed"` shows the exact error. Local backups continue to land at `/srv/blob/backups/postgres/<instance>/` regardless. See [`docs/managed-services.md#off-host-backup-shipping`](managed-services.md) for endpoint-tuning notes (Cloudflare in front of an S3 host mangles SigV4 — point at the origin host directly).

## Observability stack (v0.8 + v0.10)

Logs (Loki + Promtail), traces (Tempo), metrics (Prometheus), dashboards (Grafana) are managed services on the same control plane.

```sh
blob loki create platform-logs
blob promtail create platform-shipper --loki platform-logs
blob tempo create platform-tempo
blob prometheus create platform-prom
blob grafana create platform-graf \
  --loki platform-logs \
  --tempo platform-tempo \
  --prometheus platform-prom
blob grafana url platform-graf            # prints URL + admin password
```

Once a Loki is registered, `blob logs <app> --since 5m --grep ERROR` queries it via `/loki/api/v1/query_range` and returns chronological lines. Falls back to `nomad alloc logs --tail` when no Loki is registered or no `--since`/`--grep` is passed.

Once a Tempo is registered, blobd auto-exports OTLP traces for the deploy code paths. Confirm with `curl http://<host>:13200/api/search?tags=service.name%3Dblobd`.

Once a Prometheus is registered, `/metrics` on blobd is scraped automatically. Add cAdvisor (system job, see [`docs/observability.md`](observability.md)) to feed `cpu`/`memory` autoscale metrics.

### UFW for managed-service ports

`bootstrap-host.sh` only opens 22/80/443. Apply these once before creating any managed service — without them the docker bridge can't reach the data plane:

```sh
sudo ufw allow 13000:13400/tcp comment "blob-observability"   # Loki, Grafana, Tempo, Prometheus
sudo ufw allow 14222:14322/tcp comment "blob-nats"
sudo ufw allow from 172.17.0.0/16 to any port 8787 proto tcp  # blobd /metrics from Prometheus
sudo ufw allow from 172.17.0.0/16 to any port 4646 proto tcp  # Nomad SD from Prometheus
```

Memory budget: Loki 512 MiB cap (~85 MiB resident), Tempo 512 MiB (~120 MiB), Prometheus 512 MiB (~150 MiB at low cardinality), Grafana 384 MiB (~80 MiB), Promtail 128 MiB per node. Whole stack fits in ~1 GiB resident on a single host.

## Autoscaling (v0.11)

Per-app horizontal autoscaler. blobd ticks every 30s, queries the first registered Prometheus for the configured metric, runs Kubernetes-style ratio scaling (`desired = ceil(current * value/target)` clamped to `[min,max]`), applies cooldowns.

```sh
blob autoscale set my-app \
  --metric cpu \
  --target 50 \
  --min 1 --max 5 \
  --cooldown-up 60s --cooldown-down 180s
blob autoscale list
blob autoscale get my-app
blob autoscale unset my-app
```

Built-in metrics: `cpu` and `memory` (need cAdvisor), `http_qps` (needs the app to expose `blob_http_requests_total{app="<n>"}`). Raw PromQL with `__APP__` placeholder works for anything else. Metric outage is a no-op — blobd never scales to zero on a transient Prometheus failure. State at `/srv/blob/autoscale/<app>.json`. Full notes in [`docs/autoscaling.md`](autoscaling.md).

## Preview environments (v0.12)

Ephemeral per-branch deploys at `<app>-<branch>.<base-domain>`, reusing the parent app's source tarball.

```sh
blob preview create my-app --branch test1
blob preview list my-app
blob preview destroy my-app test1
```

Multi-component apps get one Nomad job per component under the branch namespace (since v0.13). State at `/srv/blob/previews/<app>/<branch>.json`. Full lifecycle in [`docs/preview-environments.md`](preview-environments.md).

## GitHub webhook for previews (v0.13)

`blob webhook github setup <app>` returns the URL and HMAC secret to paste into a GitHub repo's Webhooks settings. blobd auto-creates a preview at `<app>-pr-<number>.<base>` on `pull_request.opened` / `synchronize` and tears it down on `closed`. Secret stored at `/srv/blob/webhooks/<app>/github.json` mode 0600.

If a preview gets stuck (PR closed but the preview still up), `blob preview destroy <app> pr-<N>` clears it manually. Webhook receiver logs land in `journalctl -u blobd | grep webhook`.

## Doctor severity legend

`blob doctor` returns issues at four severities:

- **P1** — broken; deploys may already be failing. Fix immediately.
- **P2** — degraded; deploys still work but something's wrong (pending placements, queue lag).
- **P3** — drift; the registry/disk state diverged from running state. Usually safe but worth cleaning up.
- **info** — odd state worth knowing about.

A non-zero exit from `blob doctor` indicates at least one P1.

From v0.27 onward, `blob nodes list` and `blob doctor` rebuild `/srv/blob/resource-graph.json` from Nomad's node detail and node allocation APIs. The graph records total, reserved, and available CPU shares, memory MiB, and disk MiB per node. Pending-placement doctor findings use that graph to say whether the immediate blocker is RAM, CPU, disk, node eligibility, or a non-capacity constraint.

From v0.26 onward, every `blob deploy` writes a deterministic manifest projection hash into `/srv/blob/jobs/<job>.meta.json`, the rendered `/srv/blob/jobs/<job>.nomad`, and the live Nomad job's `meta.blob_projection_hash`. `blob doctor` compares all three. A mismatch means the live job or on-disk job file was changed outside Blob after the last accepted deploy; re-run `blob deploy` to restore the intended projection.

## Upgrades

Updating `blobd`:

```sh
curl -fsSL -o /tmp/blobd https://github.com/darvell/blob/releases/latest/download/blobd-linux-amd64
sudo install -m 0755 /tmp/blobd /usr/local/bin/blobd
sudo systemctl restart blobd
```

Updating `blobctl` (laptop):

```sh
curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/install.sh | sh
```

## Logs to look at when something's weird

| Symptom                          | First thing to look at                    |
|---                               |---                                        |
| Deploy hangs at "schedule"       | `nomad job status <id>` and `events`      |
| Deploy succeeds but URL 502/404  | `nomad service info <id>` and Traefik     |
| App restarts in a loop           | `blob logs <app>` and Docker exit codes   |
| `blob deploy` fails at `push`    | Registry container, registry creds        |
| Cert never issues                | Traefik logs (ACME), DNS resolution       |

## Things that don't exist yet

The spec describes much more (multi-region failover, autoscaling, observability stack, hot journal volumes, plugins, web console). The runtime in this repo is the deploy-and-operate core, deliberately small. See README.md for the explicit shipped vs. roadmap table.
