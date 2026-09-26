#!/usr/bin/env node
// whis npm shim: forwards to the platform binary fetched at install time.
const { spawn } = require("child_process");
const path = require("path");
const fs = require("fs");

const platform = process.platform === "win32" ? "windows" : process.platform;
const archMap = { x64: "amd64", arm64: "arm64" };
const arch = archMap[process.arch];
if (!arch) {
  console.error(`whis: unsupported arch ${process.arch}`);
  process.exit(1);
}

const ext = platform === "windows" ? ".exe" : "";
const bin = path.join(__dirname, "whis", `whis-${platform}-${arch}${ext}`);

if (!fs.existsSync(bin)) {
  console.error("whis binary missing — reinstall via: npm i -g whis-cli");
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
child.on("exit", (code) => process.exit(code ?? 0));
