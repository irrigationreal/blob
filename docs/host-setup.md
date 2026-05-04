# Turn a server into a Blob

This guide turns a fresh Debian 12 / Ubuntu 22+ box into a one-node Blob: control plane + worker on the same machine. Once it's working, see [`joining-nodes.md`](joining-nodes.md) for adding more capacity.

The result: your `blob deploy` from a laptop produces `https://<name>.<your-domain>` with a real cert.

## What you need

- A Debian 12 / Ubuntu 22+ host with a public IP. 4 GiB RAM is enough; 2 GiB works for very small fleets. **Must run systemd as PID 1** (any real VM, bare metal, or Hetzner-style VPS does; bare Docker/LXC containers without `--init` do not — `bootstrap-host.sh`'s `systemctl enable --now` calls fail with "System has not been booted with systemd").
- DNS: a wildcard A/AAAA record `*.<base-domain>` AND the apex `<base-domain>` both pointing at the host's public IP. The wildcard is for **subdomain coverage** (every app you deploy lands at `<app>.<base-domain>`); it is NOT a wildcard cert. Let's Encrypt HTTP-01 (which is what `bootstrap-host.sh` uses) issues a fresh per-subdomain cert at first request — that works for `<app>.<base>` but cannot issue `*.<base>`.
- Ports 22 (SSH), 80 (HTTP), 443 (HTTPS), and 8787 (the API, optional if you proxy it) reachable.
- Root or passwordless sudo on the host.
- A workstation with `blob` installed.

## 1. Install the substrate

The Blob currently runs on top of Nomad + Docker + Traefik + a private OCI registry. The `scripts/bootstrap-host.sh` in this repo installs all four. Run it once on the host:

```sh
curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/bootstrap-host.sh | sudo BASE_DOMAIN=example.com sh
```

### What `bootstrap-host.sh` actually does

The script is ~150 lines and assumes nothing about your machine beyond root + Debian/Ubuntu. Read it once if you want to know exactly what hits disk. Inputs:

| env var | required | default | what it controls |
|---|---|---|---|
| `BASE_DOMAIN` | yes | — | wildcard root. Flows into Traefik's ACME registration, the registry hostname (`registry.$BASE_DOMAIN`), and every app's published URL. |
| `ACME_EMAIL` | no | `admin@$BASE_DOMAIN` | Let's Encrypt registration email. Override if `admin@` doesn't exist. |
| `REGISTRY_USER` | no | `blob` | bcrypt-hashed into the registry's htpasswd file. The matching password is auto-generated (24 hex bytes from openssl) and written to `/etc/blob/registry-credentials.txt` mode 0600. |
| `PROFILE` | no | `core` | `ultralight` skips the registry-on-Nomad step (use a public registry instead) for low-RAM hosts. |
| `DC` | no | `dc1` | Nomad datacenter name. Most users keep the default. |

What it installs / configures, in order:

1. APT prereqs: `ca-certificates`, `curl`, `gnupg`, `ufw`, `lsb-release`, `jq`, `apache2-utils` (for `htpasswd`).
2. Docker CE + Compose plugin from `download.docker.com`.
3. Nomad (latest stable) from `apt.releases.hashicorp.com`. Configures it as a single-node server-and-client at `/etc/nomad.d/blob.hcl`, data dir `/opt/nomad/data`. Enables and starts both `docker` and `nomad`.
4. UFW: opens 22/80/443 inbound only. **Note:** managed-service ports (Loki, Grafana, Tempo, Prometheus, NATS) are NOT opened here. See the [observability doc](observability.md#ufw) for the rules to add before running `blob loki create` etc. — the docker bridge needs `from 172.17.0.0/16` allowed on the relevant port ranges.
5. Generates the registry htpasswd at `/etc/blob/registry.htpasswd` and the matching plaintext credentials at `/etc/blob/registry-credentials.txt`.
6. Submits a `traefik:v3.6` Nomad job listening on host ports 80/443 with ACME via HTTP-01, providers.nomad enabled.
7. Submits a `registry:2` Nomad job under `registry.$BASE_DOMAIN` reading the htpasswd from step 5.

After it runs, the only file you'll need to hand-copy is the credentials file (the rest of step 3 below).

If you'd rather wire your own substrate, read the script — it documents every assumption.

## 2. Install blobd

```sh
# On the host
curl -fsSL -o /tmp/blobd https://github.com/darvell/blob/releases/latest/download/blobd-linux-amd64
sudo install -m 0755 /tmp/blobd /usr/local/bin/blobd
sudo /usr/local/bin/blobd --version
```

Use `arm64` instead of `amd64` on aarch64 hosts.

## 3. Configure blobd

```sh
sudo install -d -m 0700 -o $(id -un) /etc/blob
sudo install -d -m 0755 -o $(id -un) /srv/blob /srv/blob/{jobs,sources,secrets}

# /etc/blob/registry-credentials.txt was created by bootstrap-host.sh in
# step 1; chown it to the user blobd will run as.
sudo chown $(id -un) /etc/blob/registry-credentials.txt
sudo chmod 600 /etc/blob/registry-credentials.txt

# Generate a bearer token. blobd reads it from the BLOB_TOKEN env var —
# the variable name MUST be BLOB_TOKEN (not TOKEN, not API_TOKEN).
TOKEN=$(openssl rand -hex 24)
sudo bash -c "echo BLOB_TOKEN=$TOKEN > /etc/blob/env"
sudo chmod 600 /etc/blob/env
echo "Save this token — you need it on the laptop: $TOKEN"
```

Drop in the systemd unit (a copy is at [`scripts/blobd.service`](../scripts/blobd.service)):

```ini
[Unit]
Description=The Blob control plane (blobd)
After=network.target docker.service nomad.service
Wants=docker.service nomad.service

[Service]
Type=simple
User=<your-user>
Group=<your-user>
# /etc/blob/env must contain `BLOB_TOKEN=<hex>` — that exact key name.
EnvironmentFile=/etc/blob/env
ExecStart=/usr/local/bin/blobd \
  --listen 127.0.0.1:8787 \
  --base-domain example.com \
  --dc dc1 \
  --registry registry.example.com \
  --state /srv/blob \
  --registry-creds /etc/blob/registry-credentials.txt \
  --public-ip <your-public-ip>
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now blobd
sudo systemctl status blobd
```

## 4. Expose blobd through Traefik

`blobd` only listens on `127.0.0.1:8787`. To expose it at `https://blob.<base-domain>` you submit a tiny Nomad job that proxies through to it. A working file is [`scripts/blobd-edge.nomad`](../scripts/blobd-edge.nomad). Edit the hostname and run:

```sh
nomad job run /path/to/blobd-edge.nomad
```

Within seconds Traefik picks up the route and the API is reachable at `https://blob.<base-domain>`.

## 5. Point your CLI at it

On your laptop:

```sh
blob login --endpoint https://blob.example.com --token <BLOB_TOKEN-from-step-3>
blob whoami            # expects: <hostname>
blob doctor            # expects: 5 checks, no issues
```

Now `blob deploy` from any project folder works.

## Before you run `blob loki create` (or any managed service)

`bootstrap-host.sh` only opens 22/80/443 in UFW. Every managed-service driver (Loki, Grafana, Tempo, Prometheus, NATS) listens on a port range above 13000 and needs the docker bridge to reach it. Apply the rules in [`docs/observability.md#ufw`](observability.md) before creating any of them, or `blob loki create` will succeed in scheduling Nomad's job but the data plane will be unreachable from the rest of the fleet.

## What can break

- **`401 Unauthorized` from `blob login`** — `/etc/blob/env` has `TOKEN=` instead of `BLOB_TOKEN=`. blobd reads only `BLOB_TOKEN`. Fix and `sudo systemctl restart blobd`.
- **404 from Traefik on `https://blob.<base>`** — the Nomad job didn't get picked up. Check `nomad job status blobd-edge` and `nomad job logs blobd-edge`.
- **`registry creds: permission denied`** — `/etc/blob/registry-credentials.txt` is not owned by the user blobd runs as. `sudo chown <user>:<user> /etc/blob/*`.
- **`http: 502 Bad Gateway`** for app deploys — Traefik can't reach Nomad's service registry. Confirm `nomad service info <app>` returns one healthy entry.
- **Cert issuance fails** — Let's Encrypt rate-limited you, or HTTP-01 can't reach port 80. Check `nomad alloc logs <traefik-alloc-id>` for ACME errors.
- **`blob loki create` succeeds but the data plane is unreachable** — UFW doesn't allow the docker bridge to the managed-service port range. See observability.md.

## Profile floors

| Profile      | Master floor       | Use case                              |
|---           |---                 |---                                    |
| `ultralight` | 2 GiB RAM, 1 vCPU  | learning, very small homelab          |
| `core`       | Pi 4 4 GiB / VPS   | personal projects, small team         |
| `full`       | 3+ nodes, 8 GiB+   | production with managed services      |

`bootstrap-host.sh` defaults to `core`. Set `BLOB_PROFILE=ultralight` to skip the registry-on-Nomad step (use a public registry instead) and reduce RAM usage.

## Next

Read [`joining-nodes.md`](joining-nodes.md) to add more workers.
Read [`operator.md`](operator.md) for day-2 ops: backups, drains, upgrades, recovering from a dead node.
