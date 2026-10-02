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
echo "  Installing WHIS (no edition tagging)"
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
# The executable that started this install (the one the user ran).
ORIGINAL_WHIS="${WHIS_ORIGINAL_EXE:-}"
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

# If old WHIS copies live elsewhere on PATH, update them too so the freshly
# installed version isn't shadowed by a stale one.
# The executable that started this install (the one the user ran).
ORIGINAL_WHIS="${WHIS_ORIGINAL_EXE:-}"
# Also update the original exe's folder (e.g. ~/go/bin) so a new terminal always
# picks up the latest whis instead of a shadowed stale copy.
if [ -n "$ORIGINAL_WHIS" ] && [ -f "$ORIGINAL_WHIS" ] && [ "$ORIGINAL_WHIS" != "$DEST/whis" ]; then
  ORIG_DIR=$(dirname "$ORIGINAL_WHIS")
  if [ "$ORIG_DIR/whis" != "$DEST/whis" ] && [ -f "$ORIG_DIR/whis" ]; then
    cp "$DEST/whis" "$ORIG_DIR/whis" 2>/dev/null \
      && echo "  Also updated $ORIG_DIR/whis" \
      || echo "  (could not update $ORIG_DIR/whis - remove it manually)"
  fi
fi

whis_path="$(command -v whis 2>/dev/null || true)"
if [ -n "$whis_path" ] && [ "$whis_path" != "$DEST/whis" ] && [ -f "$whis_path" ]; then
  cp "$DEST/whis" "$whis_path" 2>/dev/null \
    && echo "  Also updated $whis_path" \
    || echo "  (could not update $whis_path - remove it manually)"
fi

case ":$PATH:" in
  *":$DEST:"*) ;;
  *) echo "  Add it to your PATH:  export PATH=\"$DEST:\$PATH\"" ;;
esac
echo "  Run:  whis"
echo ""
