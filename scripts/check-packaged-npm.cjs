#!/usr/bin/env node
// Executed by the staged Node binary before packaging its separately pinned npm.
const fs = require("node:fs");
const path = require("node:path");

function validate(bridge, lock, npm, nodeVersion, semver) {
  const expected = bridge.devDependencies?.npm;
  const locked = lock.packages?.["node_modules/npm"];
  if (typeof expected !== "string" || !semver.valid(expected) ||
      expected !== npm.version || expected !== locked?.version || npm.name !== "npm") {
    throw new Error("packaged npm must exactly match the bridge manifest and lockfile");
  }
  if (typeof locked.integrity !== "string" || !locked.integrity.startsWith("sha512-")) {
    throw new Error("packaged npm must have a pinned sha512 lockfile integrity");
  }
  const engines = npm.engines?.node;
  if (typeof engines !== "string" || !semver.validRange(engines) || !semver.satisfies(nodeVersion, engines)) {
    throw new Error(`packaged npm ${expected} does not support Node ${nodeVersion}`);
  }
}

function main(npmRoot, manifestFile, lockFile) {
  if (!npmRoot || !manifestFile || !lockFile) throw new Error("expected npm root, bridge manifest and lockfile");
  const root = path.resolve(npmRoot);
  const readJSON = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
  const semver = require(path.join(root, "node_modules", "semver"));
  validate(readJSON(manifestFile), readJSON(lockFile), readJSON(path.join(root, "package.json")), process.versions.node, semver);
  for (const name of ["LICENSE", path.join("bin", "npm-cli.js")]) {
    if (!fs.statSync(path.join(root, name)).isFile()) throw new Error(`packaged npm has no ${name}`);
  }
}

module.exports = { validate };
if (require.main === module) {
  try { main(...process.argv.slice(2)); }
  catch (err) {
    console.error(`packaged npm check failed: ${err instanceof Error ? err.message : String(err)}`);
    process.exitCode = 1;
  }
}
