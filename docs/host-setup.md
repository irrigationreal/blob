# Turn a server into a Blob

This guide turns a fresh Debian 12 / Ubuntu 22+ box into a one-node Blob: control plane + worker on the same machine. Once it's working, see [`joining-nodes.md`](joining-nodes.md) for adding more capacity.

The result: your `blob deploy` from a laptop produces `https://<name>.<your-domain>` with a real cert.

## What you need

- A Debian 12 / Ubuntu 22+ host with a public IP. 4 GiB RAM is enough; 2 GiB works for very small fleets.
- A wildcard DNS record `*.<base-domain>` and the apex `<base-domain>` both pointing at the host's public IP.
- Ports 22 (SSH), 80 (HTTP), 443 (HTTPS), and 8787 (the API, optional if you proxy it) reachable.
- Root or passwordless sudo on the host.
- A workstation with `blob` installed.

## 1. Install the substrate

The Blob currently runs on top of Nomad + Docker + Traefik + a private OCI registry. The `scripts/bootstrap-host.sh` in this repo installs all four. Run it once on the host:

```sh
curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/bootstrap-host.sh | sudo BASE_DOMAIN=example.com sh
```

What the script does, briefly:

1. Installs Docker, Docker Compose, Nomad (server + client), and `jq`.
2. Configures Nomad as a single-node server-and-client.
3. Brings up Traefik as a Nomad job, terminating TLS via Let's Encrypt HTTP-01.
4. Brings up an authenticated container registry as a Nomad job under `registry.<base-domain>`.
5. Generates registry credentials in `/etc/blob/registry-credentials.txt`.
6. Tightens UFW to only allow 22 / 80 / 443 inbound.

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

# Copy your registry credentials to the location blobd expects
sudo cp /etc/irrigate/registry-credentials.txt /etc/blob/registry-credentials.txt
sudo chown $(id -un) /etc/blob/registry-credentials.txt
sudo chmod 600 /etc/blob/registry-credentials.txt

# Generate a bearer token
TOKEN=$(openssl rand -hex 24)
sudo bash -c "echo BLOB_TOKEN=$TOKEN > /etc/blob/env"
sudo chmod 600 /etc/blob/env
echo "Save this token: $TOKEN"
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
blob login --endpoint https://blob.example.com --token <the-token-from-step-3>
blob whoami            # expects: <hostname>
blob doctor            # expects: 5 checks, no issues
```

Now `blob deploy` from any project folder works.

## What can break

- **404 from Traefik on `https://blob.<base>`** — the Nomad job didn't get picked up. Check `nomad job status blobd-edge` and `nomad job logs blobd-edge`.
- **`registry creds: permission denied`** — `/etc/blob/registry-credentials.txt` is not owned by the user blobd runs as. `sudo chown <user>:<user> /etc/blob/*`.
- **`http: 502 Bad Gateway`** for app deploys — Traefik can't reach Nomad's service registry. Confirm `nomad service info <app>` returns one healthy entry.
- **Cert issuance fails** — Let's Encrypt rate-limited you, or HTTP-01 can't reach port 80. Check `nomad alloc logs <traefik-alloc-id>` for ACME errors.

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
