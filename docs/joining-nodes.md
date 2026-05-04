# Adding a node to the Blob

Once you have one Blob host running, adding more capacity is a single command. New nodes become Nomad clients that the existing scheduler places workloads on.

## What you need

- Existing Blob host that's running and healthy (`blob doctor` returns no issues).
- A second Debian 12 / Ubuntu 22+ host. Anywhere with outbound connectivity to the existing Blob server's Nomad RPC port (4647) works — same data center, different cloud, your laptop, an old Pi.
- Root or passwordless sudo on the new host.

## 1. Generate the join script

On your laptop:

```sh
blob nodes join > join.sh
```

This prints a self-contained shell script that:

- installs Docker (latest from docker.com)
- installs Nomad (latest from HashiCorp APT)
- writes `/etc/nomad.d/client.hcl` pointing at your existing Blob server
- enables and starts both services
- runs `docker login registry.<base-domain>` using the credentials from the existing Blob host's `/etc/blob/registry-credentials.txt`, so the first workload to schedule on this node can pull from the private registry without manual setup

The script is **idempotent** — safe to re-run if a previous attempt half-finished.

The credentials are embedded directly in the script body. `/v1/join` is auth-gated by the same bearer token that lets you deploy, so this doesn't widen exposure beyond who already has the keys to the kingdom. Treat the generated `join.sh` as a secret — don't commit it.

## 2. Run it on the new host

Copy `join.sh` to the new node and run as root:

```sh
scp join.sh root@new-node.example.com:/tmp/
ssh root@new-node.example.com 'sh /tmp/join.sh'
```

Or paste it into a remote console session.

## 3. Verify

Back on your laptop:

```sh
blob nodes list
```

The new node appears within a few seconds with `STATUS=ready` and `ELIGIBLE=eligible`. Workloads deployed from now on are eligible to land on either host.

```
ID           NAME                 ADDR            STATUS     ELIGIBLE   DC
639cb577     platform             65.21.9.22      ready      eligible   pve
4d1a8c33     scout-01             10.0.5.42       ready      eligible   pve
```

## What still needs to be on each node

- **Storage**: the new node needs to be able to access workloads' Docker volumes. For now (v0.3) this means workloads using `volumes:` in `blob.yaml` should be either pinned to a single node, or be okay with re-creation if rescheduled. A future version will use a CSI plugin so volumes follow the workload.
- **Registry pull credentials**: handled by `blob nodes join` since v0.13 — the script runs `docker login registry.<base-domain>` with the credentials from the existing Blob host's `/etc/blob/registry-credentials.txt`. If you brought a node up before v0.13 (no docker login baked in) and it's failing pulls, re-run `sh /tmp/join.sh` from a freshly-generated `blob nodes join`, or `ssh new-node 'docker login registry.<base-domain>'` once with the creds.

## Draining a node

To temporarily move workloads off a node (for maintenance, replacement, etc.):

```sh
blob nodes drain <node-id>
```

Workloads reschedule to other eligible nodes. The node stays in the fleet but won't receive new placements.

When done:

```sh
blob nodes undrain <node-id>
```

## Removing a node permanently

1. Drain it: `blob nodes drain <id>`.
2. Wait for `blob nodes list` to show the node has zero allocations.
3. On the node itself: `sudo systemctl stop nomad && sudo systemctl disable nomad`.
4. Wait one more minute for the heartbeat to expire on the server. The node will drop out of `blob nodes list`.

## Heterogeneous fleets

The Blob places work based on architecture, RAM, and free disk. Mixing Pi 4s, VPSes, and bare metal works as long as image targets are compatible.

- Pi 4s are arm64; most VPSes are amd64. Either build multi-arch images (`docker buildx build --platform linux/amd64,linux/arm64`) or pin a workload to one arch with `arch:` in `blob.yaml` (planned for v0.4 — for now use Nomad constraints if needed).
- Storage-heavy nodes can be labeled and used for stateful workloads only.

## Gotchas

- **Nomad clients need outbound 4647/tcp** to the Blob server. If the new node is behind NAT, set up a tunnel or VPN first.
- **Time skew** breaks Nomad heartbeats. Make sure the new host has NTP working.
- **Same IP twice**: Nomad refuses to register two clients with the same `bind_addr`. The default config uses the host's first non-loopback IP, which is normally fine.
