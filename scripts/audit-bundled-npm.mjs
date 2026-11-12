#!/usr/bin/env node
// Version-match inventory of the separately shipped npm tree. This is distinct
// from the bridge lockfile audit and cannot see code bundled into JavaScript.
import { readdir, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const advisoryEndpoint = "https://registry.npmjs.org/-/npm/v1/security/advisories/bulk";

export function npmRootForNode(nodePath, platform) {
  const nodeDir = dirname(nodePath);
  return platform === "win32"
    ? join(nodeDir, "node_modules", "npm")
    : join(nodeDir, "..", "lib", "node_modules", "npm");
}

export async function inventoryNpm(root, semver) {
  const packages = [];
  const skipped = [];
  async function walk(dir) {
    for (const entry of await readdir(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      // Do not follow symlinks outside the distribution or visit a tree twice.
      if (entry.isDirectory()) {
        await walk(path);
      } else if (entry.isFile() && entry.name === "package.json") {
        const manifest = JSON.parse(await readFile(path, "utf8"));
        const manifestPath = relative(root, path);
        if (typeof manifest.name !== "string" || manifest.name.length === 0) {
          skipped.push({ path: manifestPath, reason: "missing name" });
          continue;
        }
        if (typeof manifest.version !== "string" || !semver.valid(manifest.version)) {
          skipped.push({ path: manifestPath, reason: "missing or invalid version" });
          continue;
        }
        packages.push({ name: manifest.name, version: manifest.version, path: manifestPath });
      }
    }
  }
  await walk(root);
  if (!packages.some((pkg) => pkg.name === "npm" && pkg.path === "package.json")) {
    throw new Error("npm root has no versioned npm package.json");
  }
  skipped.sort((a, b) => a.path.localeCompare(b.path));
  return { packages, skipped };
}

export async function queryAdvisories(packages, fetchFn = fetch) {
  const versions = new Map();
  for (const pkg of packages) {
    if (!versions.has(pkg.name)) versions.set(pkg.name, new Set());
    versions.get(pkg.name).add(pkg.version);
  }
  const payload = Object.fromEntries([...versions].map(([name, values]) => [name, [...values]]));
  const response = await fetchFn(advisoryEndpoint, {
    method: "POST",
    headers: { "content-type": "application/json", "npm-command": "audit" },
    body: JSON.stringify(payload),
    signal: AbortSignal.timeout(30_000),
  });
  if (!response.ok) throw new Error(`npm advisory service returned HTTP ${response.status}`);
  const data = await response.json();
  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    throw new Error("npm advisory service returned an invalid response");
  }
  for (const [name, entries] of Object.entries(data)) {
    if (!versions.has(name) || !Array.isArray(entries)) {
      throw new Error("npm advisory service returned an invalid advisory list");
    }
  }
  return data;
}

export function matchAdvisories(packages, advisories, semver) {
  const matches = [];
  for (const pkg of packages) {
    const entries = Object.hasOwn(advisories, pkg.name) ? advisories[pkg.name] : [];
    if (!Array.isArray(entries)) throw new Error(`invalid advisory list for ${pkg.name}`);
    for (const advisory of entries) {
      const range = advisory?.vulnerable_versions;
      if (typeof range !== "string" || !semver.validRange(range)) {
        throw new Error(`invalid advisory version range for ${pkg.name}`);
      }
      if (semver.satisfies(pkg.version, range, { includePrerelease: true })) {
        const severity = advisory.severity;
        if (!["low", "moderate", "high", "critical"].includes(severity)) {
          throw new Error(`invalid advisory severity for ${pkg.name}`);
        }
        matches.push({ ...pkg, severity, url: advisory.url ?? "unknown" });
      }
    }
  }
  return matches.sort((a, b) =>
    a.name.localeCompare(b.name) || a.version.localeCompare(b.version) ||
    a.path.localeCompare(b.path) || String(a.url).localeCompare(String(b.url)),
  );
}

export function blocksRelease(matches) {
  return matches.some((item) => item.severity === "high" || item.severity === "critical");
}

function printable(value) {
  return String(value).replace(/[\x00-\x1f\x7f]/g, "?");
}

async function main() {
  const args = process.argv.slice(2);
  const gate = args.includes("--fail-on-high");
  const roots = args.filter((arg) => arg !== "--fail-on-high");
  if (roots.length > 1 || roots.some((arg) => arg.startsWith("--"))) {
    throw new Error("usage: audit-bundled-npm.mjs [npm-root] [--fail-on-high]");
  }
  const root = resolve(roots[0] ?? npmRootForNode(process.execPath, process.platform));
  // npm ships its own semver; use that exact copy to interpret npm's ranges.
  const semver = createRequire(join(root, "package.json"))("semver");
  const { packages, skipped } = await inventoryNpm(root, semver);
  const matches = matchAdvisories(packages, await queryAdvisories(packages), semver);
  console.log(`Bundled npm: ${packages.length} versioned package manifests, ${skipped.length} skipped manifests, ${matches.length} advisory matches.`);
  console.log("Version matches only: triage reachability and fixes; this does not scan SDK bundles or code without package metadata.");
  for (const item of skipped) {
    console.log(`[skipped: ${printable(item.reason)}] ${printable(item.path)}`);
  }
  for (const item of matches) {
    console.log(`[${printable(item.severity)}] ${printable(item.name)}@${printable(item.version)} ${printable(item.url)} (${printable(item.path)})`);
  }
  if (gate && blocksRelease(matches)) {
    throw new Error("bundled npm has high or critical advisory matches");
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((err) => {
    console.error(`bundled npm advisory scan failed: ${err instanceof Error ? err.message : String(err)}`);
    process.exitCode = 1;
  });
}
