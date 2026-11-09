#!/usr/bin/env node
"use strict";

// Starts the prebuilt 8memory binary that npm installed for this machine.

const { spawnSync } = require("node:child_process");

const { platform, arch } = process;
const executable = platform === "win32" ? "8memory.exe" : "8memory";

let binary;
try {
  binary = require.resolve(`8memory-${platform}-${arch}/bin/${executable}`);
} catch {
  console.error(
    `8memory: no prebuilt binary found for ${platform}-${arch}.\n` +
      "Reinstall without --omit=optional, or build from source: https://github.com/andifirdauss/8memory",
  );
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`8memory: ${result.error.message}`);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
