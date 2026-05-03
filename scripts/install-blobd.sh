#!/usr/bin/env bash
# Install blobd on a platform host that already has Nomad, Docker, Traefik,
# and a private registry running. Run as a user in the docker group with
# passwordless sudo.
set -euo pipefail

if [[ -z "${1:-}" ]]; then
  echo "usage: install-blobd.sh <path-to-blobd-binary>" >&2
  exit 1
fi
BIN="$1"

sudo install -m 0755 "$BIN" /usr/local/bin/blobd
sudo mkdir -p /etc/blob /srv/blob/{jobs,sources}
sudo chown "$(id -un)":"$(id -gn)" -R /srv/blob

if [[ ! -f /etc/blob/registry-credentials.txt ]]; then
  if [[ -f /etc/irrigate/registry-credentials.txt ]]; then
    sudo cp /etc/irrigate/registry-credentials.txt /etc/blob/registry-credentials.txt
  else
    echo "create /etc/blob/registry-credentials.txt with:"
    echo "  username: <user>"
    echo "  password: <pass>"
    exit 1
  fi
fi
sudo chown "$(id -un)":"$(id -gn)" /etc/blob/registry-credentials.txt
sudo chmod 600 /etc/blob/registry-credentials.txt

if [[ ! -f /etc/blob/token ]]; then
  TOKEN=$(openssl rand -hex 24)
  echo "$TOKEN" | sudo tee /etc/blob/token >/dev/null
  printf 'BLOB_TOKEN=%s\n' "$TOKEN" | sudo tee /etc/blob/env >/dev/null
  sudo chmod 600 /etc/blob/token /etc/blob/env
  echo "generated bearer token: $TOKEN"
fi

sudo cp "$(dirname "$0")/blobd.service" /etc/systemd/system/blobd.service
sudo systemctl daemon-reload
sudo systemctl enable --now blobd
sleep 1
sudo systemctl status blobd --no-pager -l | head -15
echo
echo "blobd is up. Now expose it through Traefik at blob.<base-domain>."
echo "See scripts/blobd-edge.nomad for a Nomad job that routes to it."
