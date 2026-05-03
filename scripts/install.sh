#!/usr/bin/env sh
# Install blobctl. Detects OS/arch and downloads the matching release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/darvell/blob/main/scripts/install.sh | sh
#
set -e

REPO=darvell/blob
VERSION="${BLOB_VERSION:-latest}"
PREFIX="${PREFIX:-/usr/local}"

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

tmp=$(mktemp)
echo "downloading $url"
curl -fsSL -o "$tmp" "$url"
chmod +x "$tmp"

dest="$PREFIX/bin/blob"
if [ -w "$PREFIX/bin" ]; then
  mv "$tmp" "$dest"
else
  sudo mv "$tmp" "$dest"
fi

echo "installed $dest"
"$dest" version
