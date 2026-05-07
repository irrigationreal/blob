#!/usr/bin/env sh
# Install blobctl. Detects OS/arch and downloads the matching release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/irrigationreal/blob/main/scripts/install.sh | sh
#
# By default, installs to /usr/local/bin when it is writable or sudo can be
# used non-interactively. Otherwise falls back to $HOME/.local/bin. Override
# with PREFIX=/path.
set -e

REPO=irrigationreal/blob
VERSION="${BLOB_VERSION:-latest}"
DEFAULT_PREFIX=/usr/local
PREFIX_WAS_SET=0
if [ -n "${PREFIX:-}" ]; then
  PREFIX_WAS_SET=1
else
  PREFIX=$DEFAULT_PREFIX
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin|linux) ;;
  *) echo "unsupported os: $os" >&2; exit 1 ;;
esac

if [ "$VERSION" = latest ]; then
  url=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep browser_download_url \
    | grep "blobctl-$os-$arch" \
    | head -1 \
    | sed -E 's/.*"([^"]+)".*/\1/')
else
  url="https://github.com/$REPO/releases/download/$VERSION/blobctl-$os-$arch"
fi

if [ -z "$url" ]; then
  echo "could not find blobctl-$os-$arch in releases" >&2
  exit 1
fi

can_write_prefix() {
  dir="$1/bin"
  if [ -d "$dir" ]; then
    [ -w "$dir" ]
  else
    mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]
  fi
}

can_sudo() {
  command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1
}

install_file() {
  src="$1"
  dest="$2"
  dir=$(dirname "$dest")
  if can_write_prefix "$PREFIX"; then
    mkdir -p "$dir"
    mv "$src" "$dest"
  elif can_sudo; then
    sudo mkdir -p "$dir"
    sudo install -m 0755 "$src" "$dest"
    rm -f "$src"
  else
    if [ "$PREFIX_WAS_SET" -eq 0 ] && [ -n "${HOME:-}" ]; then
      PREFIX="$HOME/.local"
      dest="$PREFIX/bin/blob"
      mkdir -p "$PREFIX/bin"
      mv "$src" "$dest"
      echo "note: /usr/local/bin is not writable and sudo is unavailable; installed to $dest" >&2
      echo "note: add $PREFIX/bin to PATH if your shell does not already include it" >&2
    else
      echo "cannot write to $dir and sudo is unavailable" >&2
      echo "try: PREFIX=\"\$HOME/.local\" sh scripts/install.sh" >&2
      exit 1
    fi
  fi
  printf '%s\n' "$dest"
}

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT HUP INT TERM

echo "downloading $url"
curl -fsSL -o "$tmp" "$url"
chmod 0755 "$tmp"

dest=$(install_file "$tmp" "$PREFIX/bin/blob")
trap - EXIT HUP INT TERM

echo "installed $dest"
"$dest" version
