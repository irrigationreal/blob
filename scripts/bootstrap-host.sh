#!/bin/sh
# Bootstrap a Debian/Ubuntu host into a one-node Blob.
#
#   curl -fsSL https://raw.githubusercontent.com/irrigationreal/blob/main/scripts/bootstrap-host.sh | sudo BASE_DOMAIN=example.com sh
#
# Required env:
#   BASE_DOMAIN   — wildcard DNS root (e.g. example.com). Must already point at this host.
#
# Optional env:
#   ACME_EMAIL    — email for Let's Encrypt registration (default admin@$BASE_DOMAIN)
#   REGISTRY_USER — registry username (default: blob)
#   PROFILE       — core | ultralight (default: core)
#   ENABLE_KATA   — 1 installs Kata Containers and marks this Nomad node blob_kata=true
#   KATA_VERSION  — Kata static release to install when ENABLE_KATA=1 (default: 3.30.0)
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "must run as root" >&2; exit 1
fi
if [ -z "${BASE_DOMAIN:-}" ]; then
  echo "BASE_DOMAIN is required (e.g. BASE_DOMAIN=example.com)" >&2; exit 1
fi

ACME_EMAIL=${ACME_EMAIL:-admin@$BASE_DOMAIN}
REGISTRY_USER=${REGISTRY_USER:-blob}
PROFILE=${PROFILE:-core}
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
apt-get install -y ca-certificates curl gnupg ufw lsb-release jq apache2-utils

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

echo "==> nomad config (server + client)"
DC=${DC:-dc1}
mkdir -p /etc/nomad.d /opt/nomad/data
cat > /etc/nomad.d/blob.hcl <<EOF
data_dir   = "/opt/nomad/data"
datacenter = "$DC"
bind_addr  = "0.0.0.0"
server {
  enabled          = true
  bootstrap_expect = 1
}
client {
  enabled = true
$KATA_META
}
plugin "docker" {
  config {
    allow_privileged = false
    allow_runtimes   = ["runc", "kata-runtime"]
  }
}
EOF
systemctl enable --now docker nomad
sleep 4

echo "==> firewall"
ufw allow 22/tcp || true
ufw allow 80/tcp || true
ufw allow 443/tcp || true
ufw allow 20000:20099/tcp || true
ufw --force enable || true

echo "==> registry credentials"
mkdir -p /etc/blob /srv/blob /srv/blob/jobs /srv/blob/sources /srv/blob/secrets
if [ ! -f /etc/blob/registry-credentials.txt ]; then
  REGISTRY_PASS=$(openssl rand -hex 24)
  htpasswd -Bbn "$REGISTRY_USER" "$REGISTRY_PASS" > /etc/blob/registry.htpasswd
  cat > /etc/blob/registry-credentials.txt <<EOF
username: $REGISTRY_USER
password: $REGISTRY_PASS
EOF
  chmod 600 /etc/blob/registry-credentials.txt /etc/blob/registry.htpasswd
fi

echo "==> traefik (Nomad job)"
mkdir -p /srv/traefik
touch /srv/traefik/acme.json && chmod 600 /srv/traefik/acme.json
cat > /tmp/edge-traefik.nomad <<EOF
job "edge-traefik" {
  datacenters = ["$DC"]
  type = "service"
  group "edge" {
    count = 1
    network {
      port "http"  { static = 80 }
      port "https" { static = 443 }
    }
    task "traefik" {
      driver = "docker"
      config {
        image        = "traefik:v3.6"
        network_mode = "host"
        volumes = ["/srv/traefik/acme.json:/acme.json"]
        args = [
          "--log.level=INFO",
          "--api.dashboard=false",
          "--entrypoints.web.address=:80",
          "--entrypoints.websecure.address=:443",
          "--providers.nomad=true",
          "--providers.nomad.endpoint.address=http://127.0.0.1:4646",
          "--providers.nomad.exposedbydefault=false",
          "--certificatesresolvers.le.acme.email=$ACME_EMAIL",
          "--certificatesresolvers.le.acme.storage=/acme.json",
          "--certificatesresolvers.le.acme.httpchallenge=true",
          "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web"
        ]
      }
      resources { cpu = 200  memory = 256 }
    }
  }
}
EOF
nomad job run /tmp/edge-traefik.nomad

echo "==> registry (Nomad job)"
cat > /tmp/registry.nomad <<EOF
job "registry" {
  datacenters = ["$DC"]
  type = "service"
  group "registry" {
    count = 1
    network {
      port "http" { to = 5000 }
    }
    service {
      name     = "registry"
      provider = "nomad"
      port     = "http"
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.registry-https.rule=Host(\`registry.$BASE_DOMAIN\`)",
        "traefik.http.routers.registry-https.entrypoints=websecure",
        "traefik.http.routers.registry-https.tls=true",
        "traefik.http.routers.registry-https.tls.certresolver=le",
        "traefik.http.routers.registry-https.middlewares=registry-auth",
        "traefik.http.middlewares.registry-auth.basicauth.usersfile=/etc/registry.htpasswd"
      ]
    }
    task "registry" {
      driver = "docker"
      config {
        image = "registry:2"
        ports = ["http"]
        volumes = ["/srv/registry:/var/lib/registry", "/etc/blob/registry.htpasswd:/etc/registry.htpasswd:ro"]
      }
      env { REGISTRY_HTTP_ADDR = ":5000" }
      resources { cpu = 200  memory = 256 }
    }
  }
}
EOF
nomad job run /tmp/registry.nomad || true

echo
echo "==> done"
echo "DNS to set:"
echo "  A   $BASE_DOMAIN          $(curl -s4 ifconfig.me)"
echo "  A   *.$BASE_DOMAIN         $(curl -s4 ifconfig.me)"
echo
echo "Then install blobd: see docs/host-setup.md step 2."
