#!/bin/sh
# whis installer (macOS / Linux) — https://github.com/NamanGaonkar/WHIS---CLi-coder
# usage: curl -fsSL https://raw.githubusercontent.com/NamanGaonkar/WHIS---CLi-coder/main/install.sh | sh
set -e

REPO="NamanGaonkar/WHIS---CLi-coder"

# --- fetch helper: curl or wget, both fine ---
fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$2" "$1"
    else
        echo "error: need curl or wget to download" >&2
        exit 1
    fi
}

# --- platform detection (must match release asset names) ---
OS=$(uname -s)
ARCH=$(uname -m)
case "$OS" in
    Darwin) os="darwin" ;;
    Linux)  os="linux" ;;
    *) echo "error: unsupported OS '$OS' (windows: use the install.ps1 one-liner)" >&2; exit 1 ;;
esac
case "$ARCH" in
    x86_64|amd64)  arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) echo "error: unsupported architecture '$ARCH'" >&2; exit 1 ;;
esac

ASSET="whis-${os}-${arch}"
BASE="https://github.com/${REPO}/releases/latest/download"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "==> downloading whis (${os}/${arch}, latest release)..."
fetch "${BASE}/${ASSET}" "${TMP}/whis"
fetch "${BASE}/${ASSET}.sha256" "${TMP}/whis.sha256"

# --- verify checksum (format: "<hash>  <filename>", tolerant of CRLF) ---
expected=$(tr -d '\r' < "${TMP}/whis.sha256" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${TMP}/whis" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "${TMP}/whis" | cut -d' ' -f1)
else
    echo "warning: no sha256 tool found, skipping checksum verification" >&2
    actual="$expected"
fi
if [ "$actual" != "$expected" ]; then
    echo "error: checksum mismatch (want $expected, got $actual) — download corrupted, aborting" >&2
    exit 1
fi
echo "==> checksum ok"

# --- install (user-local bin dir, no sudo needed) ---
DEST="${WHIS_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$DEST"
mv "${TMP}/whis" "${DEST}/whis"
chmod +x "${DEST}/whis"

case ":$PATH:" in
    *":$DEST:"*) ;;
    *)
        echo ""
        echo "NOTE: $DEST is not on your PATH."
        echo "add this to your ~/.zshrc or ~/.bashrc:"
        echo "    export PATH=\"\$PATH:$DEST\""
        ;;
esac

echo "==> installed: ${DEST}/whis ($( "${DEST}/whis" -version 2>/dev/null || echo 'run it to start coding' ))"
echo "==> start: cd into a project and run: whis"
