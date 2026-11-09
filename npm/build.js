#!/usr/bin/env node
"use strict";

// Builds the npm packages into dist/npm: one package per platform holding its
// binary, and a main package that depends on them optionally, so npm installs
// only the one matching the machine.
//
// Usage: node npm/build.js <version>

const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const version = process.argv[2];
if (!/^\d+\.\d+\.\d+(-[\w.]+)?$/.test(version ?? "")) {
  console.error("usage: node npm/build.js <version>");
  process.exit(1);
}

const root = path.join(__dirname, "..");
const output = path.join(root, "dist", "npm");
const targets = [
  { platform: "win32", arch: "x64", goos: "windows", goarch: "amd64" },
  { platform: "linux", arch: "x64", goos: "linux", goarch: "amd64" },
  { platform: "linux", arch: "arm64", goos: "linux", goarch: "arm64" },
  { platform: "darwin", arch: "x64", goos: "darwin", goarch: "amd64" },
  { platform: "darwin", arch: "arm64", goos: "darwin", goarch: "arm64" },
];
const common = {
  version,
  license: "Apache-2.0",
  repository: { type: "git", url: "git+https://github.com/andifirdauss/8memory.git" },
  homepage: "https://github.com/andifirdauss/8memory#readme",
};

function writePackage(directory, manifest) {
  fs.writeFileSync(path.join(directory, "package.json"), JSON.stringify(manifest, null, 2) + "\n");
  fs.copyFileSync(path.join(root, "LICENSE"), path.join(directory, "LICENSE"));
}

function buildPlatform({ platform, arch, goos, goarch }) {
  const name = `8memory-${platform}-${arch}`;
  const directory = path.join(output, name);
  const executable = platform === "win32" ? "8memory.exe" : "8memory";
  fs.mkdirSync(path.join(directory, "bin"), { recursive: true });
  execFileSync(
    "go",
    ["build", "-trimpath", "-ldflags", "-s -w", "-o", path.join(directory, "bin", executable), "./cmd/8memory"],
    { cwd: root, stdio: "inherit", env: { ...process.env, GOOS: goos, GOARCH: goarch, CGO_ENABLED: "0" } },
  );
  const manifest = {
    name,
    ...common,
    description: `The 8memory binary for ${platform}-${arch}. Install the 8memory package instead.`,
    os: [platform],
    cpu: [arch],
    files: ["bin"],
  };
  // npm marks bin entries executable, which a tarball packed on Windows cannot.
  if (platform !== "win32") {
    manifest.bin = { "8memory": `bin/${executable}` };
  }
  writePackage(directory, manifest);
  fs.writeFileSync(path.join(directory, "README.md"), `The 8memory binary for ${platform}-${arch}. Install [8memory](https://www.npmjs.com/package/8memory) instead.\n`);
  return name;
}

fs.rmSync(output, { recursive: true, force: true });
const platforms = targets.map(buildPlatform);

const main = path.join(output, "8memory");
fs.mkdirSync(path.join(main, "bin"), { recursive: true });
fs.copyFileSync(path.join(__dirname, "bin", "8memory.js"), path.join(main, "bin", "8memory.js"));
fs.copyFileSync(path.join(root, "README.md"), path.join(main, "README.md"));
writePackage(main, {
  name: "8memory",
  ...common,
  description: "Shared memory and prompt journey for AI coding tools, with a chat TUI over Claude Code and Codex.",
  keywords: ["mcp", "memory", "claude-code", "codex", "tui"],
  bin: { "8memory": "bin/8memory.js" },
  files: ["bin"],
  engines: { node: ">=18" },
  optionalDependencies: Object.fromEntries(platforms.map((name) => [name, version])),
});

console.log(`built ${platforms.length + 1} packages in ${path.relative(root, output)}`);
