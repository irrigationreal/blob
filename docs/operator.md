# Day-2 ops

This is the runbook for keeping a Blob healthy. Pair it with `blob doctor`, which runs many of these checks automatically.

## Where state lives

| What                | Where                                            |
|---                  |---                                               |
| Nomad job specs     | `/srv/blob/jobs/<job>.nomad`                     |
| Job metadata        | `/srv/blob/jobs/<job>.meta.json`                 |
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

## Doctor severity legend

`blob doctor` returns issues at four severities:

- **P1** — broken; deploys may already be failing. Fix immediately.
- **P2** — degraded; deploys still work but something's wrong (pending placements, queue lag).
- **P3** — drift; the registry/disk state diverged from running state. Usually safe but worth cleaning up.
- **info** — odd state worth knowing about.

A non-zero exit from `blob doctor` indicates at least one P1.

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
