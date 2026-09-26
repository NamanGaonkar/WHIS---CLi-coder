#!/usr/bin/env node
// npm postinstall: fetch the matching whis release binary.
const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const https = require("https");

const pkg = require("../package.json");
const VERSION = pkg.version;
const REPO = "whis-cli/whis";

const platform = process.platform === "win32" ? "windows" : process.platform;
const archMap = { x64: "amd64", arm64: "arm64" };
const arch = archMap[process.arch];
if (!arch) {
  console.warn(`whis: no binary for ${process.arch}; use 'go install github.com/whis-cli/whis/cmd/whis@latest'`);
  process.exit(0);
}

const ext = platform === "windows" ? ".exe" : "";
const dir = path.join(__dirname, "whis");
const file = path.join(dir, `whis-${platform}-${arch}${ext}`);
if (fs.existsSync(file)) process.exit(0);

fs.mkdirSync(dir, { recursive: true });
const url = `https://github.com/${REPO}/releases/download/v${VERSION}/whis-${platform}-${arch}${ext}`;
console.log(`fetching whis v${VERSION} (${platform}/${arch})…`);

function get(u, cb) {
  https.get(u, (res) => {
    if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) return get(res.headers.location, cb);
    if (res.statusCode !== 200) { console.error(`download failed: HTTP ${res.statusCode}`); process.exit(1); }
    const ws = fs.createWriteStream(file, { mode: 0o755 });
    res.pipe(ws);
    ws.on("finish", () => { ws.close(cb); });
  }).on("error", (e) => { console.error(e.message); process.exit(1); });
}

get(url, () => {
  try { fs.chmodSync(file, 0o755); } catch {}
  console.log("whis installed — run: whis init");
});
