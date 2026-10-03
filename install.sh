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

# Release download URLs need the full git tag (e.g. v0.2.50). `whis upgrade
# 0.2.50` passes a bare version, so normalise it before building the URL.
case "$VERSION" in
  v*) TAG="$VERSION" ;;
  *) TAG="v${VERSION}" ;;
esac

URL="https://github.com/${REPO}/releases/download/${TAG}/${TARGET}.tar.gz"
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

# Replace the binary at $2 with $1.
#
# Copying over a RUNNING executable fails with ETXTBSY on Linux/macOS, which is
# what made upgrades silently report success while `whis --version` stayed on
# the old build. Rename the live binary aside instead: on POSIX that keeps the
# running process alive (it holds an open inode) while freeing the path for the
# new build. The old file is removed on the next run once the process is gone.
install_over() {
  _src="$1"
  _dst="$2"
  [ -f "$_src" ] || return 1
  mkdir -p "$(dirname "$_dst")" 2>/dev/null || true
  if cp "$_src" "$_dst" 2>/dev/null; then
    return 0
  fi
  # Busy (running). Move it aside, then write the new build into place.
  if mv "$_dst" "$_dst.old" 2>/dev/null && cp "$_src" "$_dst" 2>/dev/null; then
    rm -f "$_dst.old" 2>/dev/null || true
    return 0
  fi
  # Could not replace it: stage the new build and clean up any previous attempt.
  cp "$_src" "$_dst.new" 2>/dev/null || true
  return 1
}

# If old WHIS copies live elsewhere on PATH, update them too so the freshly
# installed version isn't shadowed by a stale one.
# The executable that started this install (the one the user ran).
ORIGINAL_WHIS="${WHIS_ORIGINAL_EXE:-}"
# Also update the original exe's folder (e.g. ~/go/bin) so a new terminal always
# picks up the latest whis instead of a shadowed stale copy.
if [ -n "$ORIGINAL_WHIS" ] && [ -f "$ORIGINAL_WHIS" ] && [ "$ORIGINAL_WHIS" != "$DEST/whis" ]; then
  ORIG_DIR=$(dirname "$ORIGINAL_WHIS")
  if [ "$ORIG_DIR/whis" != "$DEST/whis" ] && [ -f "$ORIG_DIR/whis" ]; then
    if install_over "$DEST/whis" "$ORIG_DIR/whis"; then
      echo "  Also updated $ORIG_DIR/whis"
    else
      echo "  Could not update $ORIG_DIR/whis (it is running) - close whis and re-run"
    fi
  fi
fi

whis_path="$(command -v whis 2>/dev/null || true)"
if [ -n "$whis_path" ] && [ "$whis_path" != "$DEST/whis" ] && [ -f "$whis_path" ]; then
  if install_over "$DEST/whis" "$whis_path"; then
    echo "  Also updated $whis_path"
  else
    echo "  Could not update $whis_path (it is running) - close whis and re-run"
  fi
fi

# Prove the install actually took, so a silently-failed swap is never reported
# as success.
INSTALLED_VERSION="$("$DEST/whis" --version 2>/dev/null | head -n1 || true)"
EXPECTED_VERSION="$(printf '%s' "$VERSION" | sed 's/^v//')"
if [ -z "$INSTALLED_VERSION" ]; then
  echo "  Could not verify (whis may still be running) - check 'whis --version' after closing whis"
elif [ "$INSTALLED_VERSION" != "$EXPECTED_VERSION" ]; then
  echo "  WARNING: installed binary reports $INSTALLED_VERSION but $EXPECTED_VERSION was requested."
  echo "  Close whis and re-run the installer to finish the upgrade."
else
  echo "  Verified: whis --version reports $INSTALLED_VERSION"
fi

case ":$PATH:" in
  *":$DEST:"*) ;;
  *) echo "  Add it to your PATH:  export PATH=\"$DEST:\$PATH\"" ;;
esac
echo "  Run:  whis"
echo ""
