#!/bin/sh
# Bootstrap a Debian/Ubuntu host as a Nomad client and join an existing Blob.
#
# This is the static, env-var-driven companion to `blob nodes join`. Use
# it when the new node can't reach blobd yet (firewalls, fresh provision)
# but can reach the existing platform host's Nomad RPC port (4647).
#
#   curl -fsSL https://raw.githubusercontent.com/irrigationreal/blob/main/scripts/bootstrap-client.sh \
#     | sudo BLOB_SERVER_RPC=65.21.9.22:4647 sh
#
# Required env:
#   BLOB_SERVER_RPC — host:port of the existing Blob server's Nomad RPC
#                     endpoint. Must be reachable from this node on tcp/4647.
#
# Optional env:
#   DC                       — Nomad datacenter (default: dc1; set to whatever
#                              the existing server uses if not the default)
#   REGISTRY                 — registry hostname (e.g. registry.example.com)
#                              for docker login
#   REGISTRY_USER + REGISTRY_PASS
#                            — credentials for the private registry. If both are
#                              set, the script runs `docker login` after install
#                              so the first workload to schedule here can pull.
#                              Mirror what's in /etc/blob/registry-credentials.txt
#                              on the existing platform host.
#   ENABLE_KATA=1 + KATA_VERSION=3.30.0
#                            — install Kata Containers, configure Docker runtime
#                              kata-runtime, and advertise Nomad meta blob_kata=true.
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "must run as root" >&2; exit 1
fi
if [ -z "${BLOB_SERVER_RPC:-}" ]; then
  echo "BLOB_SERVER_RPC is required (e.g. BLOB_SERVER_RPC=65.21.9.22:4647)" >&2
  exit 1
fi
DC=${DC:-dc1}
ENABLE_KATA=${ENABLE_KATA:-0}
KATA_VERSION=${KATA_VERSION:-3.30.0}

install_kata() {
  if [ "$ENABLE_KATA" != "1" ]; then
    return 0
  fi
  echo "==> kata containers"
  if [ ! -e /dev/kvm ]; then
    echo "ENABLE_KATA=1 requires hardware virtualization exposed at /dev/kvm" >&2
    exit 1
  fi
  apt-get install -y zstd jq
  arch=$(dpkg --print-architecture)
  case "$arch" in
    amd64|arm64|ppc64le|s390x) kata_arch="$arch" ;;
    *) echo "unsupported Kata architecture: $arch" >&2; exit 1 ;;
  esac
  if [ ! -x /opt/kata/bin/kata-runtime ]; then
    url="https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}/kata-static-${KATA_VERSION}-${kata_arch}.tar.zst"
    tmp="/tmp/kata-static-${KATA_VERSION}-${kata_arch}.tar.zst"
    curl -fL "$url" -o "$tmp"
    tar --zstd -xf "$tmp" -C /
  fi
  mkdir -p /etc/docker
  if [ ! -s /etc/docker/daemon.json ]; then
    echo '{}' > /etc/docker/daemon.json
  fi
  tmp_json=$(mktemp)
  jq '.runtimes = (.runtimes // {}) | .runtimes["kata-runtime"] = {"runtimeType":"/opt/kata/bin/containerd-shim-kata-v2"}' \
    /etc/docker/daemon.json > "$tmp_json"
  mv "$tmp_json" /etc/docker/daemon.json
  systemctl restart docker || true
  /opt/kata/bin/kata-runtime check
}

kata_nomad_meta() {
  if [ "$ENABLE_KATA" = "1" ]; then
    cat <<'EOF'
  meta {
    blob_kata = "true"
  }
EOF
  fi
}

echo "==> apt prereqs"
apt-get update
# iproute2 is required by Nomad's network fingerprinter (fork/exec /sbin/ip).
# bootstrap-host.sh doesn't need this explicitly because Debian/Ubuntu
# server images ship it; minimal/container images don't.
apt-get install -y ca-certificates curl gnupg lsb-release iproute2

echo "==> docker"
if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  . /etc/os-release
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$ID $VERSION_CODENAME stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
fi

systemctl enable --now docker
install_kata
KATA_META=$(kata_nomad_meta)

echo "==> nomad"
if ! command -v nomad >/dev/null; then
  curl -fsSL https://apt.releases.hashicorp.com/gpg | gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" \
    > /etc/apt/sources.list.d/hashicorp.list
  apt-get update
  apt-get install -y nomad
fi

echo "==> nomad client config (server=$BLOB_SERVER_RPC dc=$DC)"
mkdir -p /etc/nomad.d /opt/nomad/data
# The hashicorp deb ships /etc/nomad.d/nomad.hcl with `server { enabled = true; bootstrap_expect = 1 }`
# which conflicts with our client-only intent (it would try to bootstrap a
# new cluster on this node). Disable it before writing our config so
# Nomad only loads the client-mode file.
if [ -f /etc/nomad.d/nomad.hcl ] && [ ! -f /etc/nomad.d/nomad.hcl.disabled ]; then
  mv /etc/nomad.d/nomad.hcl /etc/nomad.d/nomad.hcl.disabled
  echo "==> disabled the default /etc/nomad.d/nomad.hcl (server-mode); renamed to .disabled"
fi
cat > /etc/nomad.d/blob-client.hcl <<EOF
data_dir   = "/opt/nomad/data"
datacenter = "$DC"
bind_addr  = "0.0.0.0"
client {
  enabled = true
  servers = ["$BLOB_SERVER_RPC"]
$KATA_META
}
plugin "docker" {
  config {
    allow_privileged = false
    allow_runtimes   = ["runc", "kata-runtime"]
  }
}
EOF
# If the host had a server-mode config from bootstrap-host.sh (named
# blob.hcl, not the upstream-default nomad.hcl), refuse to clobber it
# silently — the operator may have a reason for running both modes on
# this host.
if [ -f /etc/nomad.d/blob.hcl ]; then
  echo "warning: /etc/nomad.d/blob.hcl exists (server-mode config from bootstrap-host.sh)"
  echo "         this script wrote a client-mode config; Nomad will load both."
  echo "         If you want this node as a client only, remove blob.hcl and restart nomad."
fi

echo "==> systemd"
systemctl enable --now docker nomad

if [ -n "${REGISTRY:-}" ] && [ -n "${REGISTRY_USER:-}" ] && [ -n "${REGISTRY_PASS:-}" ]; then
  echo "==> docker login $REGISTRY"
  echo "$REGISTRY_PASS" | docker login "$REGISTRY" -u "$REGISTRY_USER" --password-stdin
fi

echo
echo "blob client up. Run 'blob nodes list' on your laptop to see this node"
echo "appear (status=ready, eligible) within ~10 seconds."
