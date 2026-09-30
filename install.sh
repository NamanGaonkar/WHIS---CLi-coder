#!/bin/sh
# WHIS installer - https://github.com/NamanGaonkar/WHIS---CLi-coder
# Run with: curl -fsSL https://raw.githubusercontent.com/NamanGaonkar/WHIS---CLi-coder/main/install.sh | sh
set -e

REPO="NamanGaonkar/WHIS---CLi-coder"
DEST="${WHIS_INSTALL_DIR:-$HOME/.whis/bin}"

echo ""
echo "  ██╗    ██╗ ██╗  ██╗ ██╗ ███████╗"
echo "  ██║    ██║ ██║  ██║ ██║ ██╔════╝"
echo "  ██║ █╗ ██║ ███████║ ██║ ███████╗"
echo "  ██║███╗██║ ██╔══██║ ██║ ╚════██║"
echo "  ╚███╔███╔╝ ██║  ██║ ██║ ███████║"
echo "   ╚══╝╚══╝  ╚═╝  ╚═╝ ╚═╝ ╚══════╝"
echo "  Installing WHIS [Personal Edition]..."
echo ""

# Detect platform
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="x64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac
case "$OS" in
  linux) PLATFORM="linux" ;;
  darwin) PLATFORM="darwin" ;;
  *) echo "Unsupported OS: $OS (use install.ps1 on Windows)"; exit 1 ;;
esac

TARGET="whis-${PLATFORM}-${ARCH}"

# Resolve latest release version
if [ "${VERSION:-latest}" = "latest" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
  if [ -z "$VERSION" ]; then echo "Failed to resolve latest release"; exit 1; fi
fi
echo "  Version: ${VERSION}"

URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARGET}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "  Downloading ${TARGET}..."
curl -fsSL "$URL" -o "$TMP/whis.tar.gz"

# Verify checksum if available
if curl -fsSL "${URL}.sha256" -o "$TMP/whis.tar.gz.sha256" 2>/dev/null; then
  (cd "$TMP" && echo "  $(cat whis.tar.gz.sha256)  whis.tar.gz" | shasum -a 256 -c - >/dev/null 2>&1) \
    && echo "  Checksum OK" || { echo "  Checksum MISMATCH"; exit 1; }
fi

mkdir -p "$DEST"
tar -xzf "$TMP/whis.tar.gz" -C "$TMP"
find "$TMP" -name whis -type f -exec mv {} "$DEST/whis" \;
chmod +x "$DEST/whis"

echo ""
echo "  Installed to $DEST/whis"
case ":$PATH:" in
  *":$DEST:"*) ;;
  *) echo "  Add it to your PATH:  export PATH=\"$DEST:\$PATH\"" ;;
esac
echo "  Run:  whis"
echo ""
