const assert = require("node:assert/strict");
const { test } = require("node:test");
const path = require("node:path");
const { validate } = require("./check-packaged-npm.cjs");
const npmRoot = path.resolve(__dirname, "../bridge-node/node_modules/npm");
const semver = require(path.join(npmRoot, "node_modules", "semver"));
const bridge = { devDependencies: { npm: "12.2.2" } };
const lock = { packages: { "node_modules/npm": { version: "12.2.2", integrity: "sha512-example" } } };
const npm = { name: "npm", version: "12.2.2", engines: { node: "^22.22.2 || ^24.15.0 || >=26.0.0" } };

test("the pinned npm must be compatible with the staged runtime", () => {
  assert.doesNotThrow(() => validate(bridge, lock, npm, "22.23.5", semver));
  assert.throws(() => validate(bridge, lock, npm, "22.13.0", semver), /does not support Node/);
  assert.throws(() => validate(bridge, lock, npm, "23.0.0", semver), /does not support Node/);
});

test("manifest, lock, package and integrity must agree", () => {
  assert.throws(() => validate(bridge, lock, { ...npm, version: "10.9.9" }, "22.23.5", semver), /exactly match/);
  assert.throws(() => validate({ devDependencies: { npm: "^12.2.2" } }, lock, npm, "22.23.5", semver), /exactly match/);
  assert.throws(() => validate(bridge, { packages: { "node_modules/npm": { version: "12.2.2" } } }, npm, "22.23.5", semver), /sha512/);
});
