#!/bin/sh
# WHIS installer: curl -fsSL https://raw.githubusercontent.com/whis-cli/whis/main/install.sh | sh
set -e

REPO="whis-cli/whis"
BIN="whis"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  mingw* | msys* | cygwin*) os=windows ;;
  darwin) os=darwin ;;
  linux) os=linux ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

# resolve latest release tag
ver="${WHIS_VERSION:-latest}"
if [ "$ver" = "latest" ]; then
  ver=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')
  if [ -z "$ver" ]; then echo "could not resolve latest release" >&2; exit 1; fi
fi

asset="whis-${os}-${arch}"
if [ "$os" = "windows" ]; then asset="${asset}.exe"; fi
url="https://github.com/${REPO}/releases/download/${ver}/${asset}"

dest="${WHIS_INSTALL_DIR:-/usr/local/bin}"
if [ "$os" = "windows" ]; then dest="${WHIS_INSTALL_DIR:-$HOME/bin}"; fi
mkdir -p "$dest"

echo "↓ whis ${ver} (${os}/${arch})"
curl -fsSL "$url" -o "$dest/$BIN" || { echo "download failed: $url" >&2; exit 1; }
chmod +x "$dest/$BIN"

echo "[ok] installed to $dest/$BIN"
case ":$PATH:" in
  *":$dest:"*) ;;
  *) echo "note: add $dest to your PATH" ;;
esac
echo "next: whis init"
