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
- optionally installs Kata Containers and marks the node `blob_kata=true` when run with `ENABLE_KATA=1`
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

For a worker that should accept `isolation: kata` workloads, run the same script with `/dev/kvm` exposed and `ENABLE_KATA=1`:

```sh
ssh root@new-node.example.com 'ENABLE_KATA=1 sh /tmp/join.sh'
```

Or paste it into a remote console session.

### Alternative: bootstrap-client.sh (no laptop required)

If you don't have a laptop with `blob` installed (e.g. provisioning the second node from CI, or the operator who set up the platform isn't around), use the static `scripts/bootstrap-client.sh` companion to `bootstrap-host.sh`:

```sh
curl -fsSL https://raw.githubusercontent.com/irrigationreal/blob/main/scripts/bootstrap-client.sh \
  | sudo BLOB_SERVER_RPC=65.21.9.22:4647 \
         REGISTRY=registry.example.com \
         REGISTRY_USER=blob \
         REGISTRY_PASS=$(ssh existing-host 'sudo grep ^password: /etc/blob/registry-credentials.txt | awk "{print \$2}"') \
         sh
```

Required env: `BLOB_SERVER_RPC` (host:port of the existing platform's Nomad RPC, default port 4647). Optional: `DC` (default `dc1`), `REGISTRY` + `REGISTRY_USER` + `REGISTRY_PASS` (skip docker login if absent — the first workload's pull will fail with `unauthorized` if you skip and don't `docker login` manually later), `ENABLE_KATA=1`, and `KATA_VERSION` (default `3.30.0`).

Both paths produce the same outcome: a Nomad client at `/etc/nomad.d/blob-client.hcl` (static script) or `/etc/nomad.d/client.hcl` (`blob nodes join`) pointing at the existing platform's Nomad RPC, registered with `client { enabled = true }`. With `ENABLE_KATA=1`, the node also has Docker runtime `kata-runtime` and Nomad meta `blob_kata=true`.

## 3. Verify

Back on your laptop:

```sh
blob nodes list
```

The new node appears within a few seconds with `STATUS=ready` and `ELIGIBLE=eligible`. The table also shows CPU, memory, and disk as reserved / available / total, which is the resource graph Blob uses for placement preflight. Workloads deployed from now on are eligible to land on either host.

```
ID         NAME       ADDR         STATUS   ELIGIBLE   DC   CPU R/A/T        MEM R/A/T             DISK R/A/T             ALLOC
639cb577   platform   65.21.9.22   ready    eligible   pve  18450/13550/32000 22368/1674/24042MiB  12600/491152/503752MiB 42
4d1a8c33   scout-01   10.0.5.42    ready    eligible   pve  300/7700/8000     512/14872/15384MiB   300/232700/233000MiB  1
```

Before sending a large workload, ask the graph where it fits:

```sh
blob nodes recommend --memory 2048 --cpu 500
```

If it cannot fit, the deploy path refuses before `nomad job run` and prints the same remediation as `blob doctor`.

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

## Kata-capable workers

Kata support is explicit per node. A `blob.yaml` with `isolation: kata` renders a Nomad constraint for `blob_kata=true`, so only nodes joined with `ENABLE_KATA=1` can run it. Verify a joined worker with:

```sh
ssh root@new-node.example.com 'docker run --rm --runtime kata-runtime hello-world'
nomad node status -verbose | grep 'blob_kata = true'
```

If the fleet has no matching node, the deploy stays pending with a constraint mismatch. It will not silently downgrade to normal Docker isolation.

## Heterogeneous fleets

The Blob places work based on architecture, RAM, isolation capability, and free disk. Mixing Pi 4s, VPSes, and bare metal works as long as image targets are compatible.

- Pi 4s are arm64; most VPSes are amd64. Either build multi-arch images (`docker buildx build --platform linux/amd64,linux/arm64`) or pin a workload to one arch with `arch:` in `blob.yaml` (planned for v0.4 — for now use Nomad constraints if needed).
- Storage-heavy nodes can be labeled and used for stateful workloads only.

## Gotchas

- **Nomad clients need outbound 4647/tcp** to the Blob server. If the new node is behind NAT, set up a tunnel or VPN first.
- **Time skew** breaks Nomad heartbeats. Make sure the new host has NTP working.
- **Same IP twice**: Nomad refuses to register two clients with the same `bind_addr`. The default config uses the host's first non-loopback IP, which is normally fine.
