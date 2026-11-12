import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { test } from "node:test";
import { blocksRelease, inventoryNpm, matchAdvisories, npmRootForNode, queryAdvisories } from "./audit-bundled-npm.mjs";

const semver = {
  valid: (v) => /^\d+\.\d+\.\d+$/.test(v),
  validRange: (r) => r === "<2.0.0" ? r : null,
  satisfies: (v, r) => r === "<2.0.0" && v === "1.0.0",
};

test("finds bundled npm beside the runtime on Unix and Windows layouts", () => {
  const root = resolve("node-runtime");
  assert.equal(npmRootForNode(join(root, "bin", "node"), "linux"), join(root, "lib", "node_modules", "npm"));
  assert.equal(npmRootForNode(join(root, "node.exe"), "win32"), join(root, "node_modules", "npm"));
});

test("inventories nested versions but does not follow symlinks", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "bundled-npm-audit-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await writeFile(join(root, "package.json"), JSON.stringify({ name: "npm", version: "10.0.0" }));
  const nested = join(root, "node_modules", "example");
  await mkdir(nested, { recursive: true });
  await writeFile(join(nested, "package.json"), JSON.stringify({ name: "example", version: "1.0.0" }));
  await mkdir(join(nested, "dist"));
  await writeFile(join(nested, "dist", "package.json"), JSON.stringify({ name: "unversioned" }));
  await mkdir(join(nested, "dist", "esm"));
  await writeFile(join(nested, "dist", "esm", "package.json"), JSON.stringify({ type: "module" }));
  // Node's directory entries identify this as a symlink even when its target is a directory.
  try {
    await symlink(nested, join(root, "node_modules", "alias"), "dir");
  } catch (err) {
    if (process.platform !== "win32" || !["EPERM", "EACCES"].includes(err.code)) throw err;
    t.diagnostic("symlink check unavailable without Windows symlink privileges");
  }
  const inventory = await inventoryNpm(root, semver);
  assert.deepEqual(inventory.packages.map((pkg) => pkg.name).sort(), ["example", "npm"]);
  assert.deepEqual(inventory.skipped, [
    { path: join("node_modules", "example", "dist", "esm", "package.json"), reason: "missing name" },
    { path: join("node_modules", "example", "dist", "package.json"), reason: "missing or invalid version" },
  ]);
  await rm(join(root, "package.json"));
  await assert.rejects(inventoryNpm(root, semver), /no versioned npm package/);
});

test("queries unique versions and fails closed on service errors", async () => {
  const packages = [
    { name: "example", version: "1.0.0", path: "a" },
    { name: "example", version: "1.0.0", path: "b" },
    { name: "example", version: "2.0.0", path: "c" },
  ];
  const result = await queryAdvisories(packages, async (url, options) => {
    assert.equal(url, "https://registry.npmjs.org/-/npm/v1/security/advisories/bulk");
    assert.deepEqual(JSON.parse(options.body), { example: ["1.0.0", "2.0.0"] });
    return { ok: true, json: async () => ({ example: [] }) };
  });
  assert.deepEqual(result, { example: [] });
  await assert.rejects(queryAdvisories(packages, async () => ({ ok: false, status: 503 })), /HTTP 503/);
  await assert.rejects(queryAdvisories(packages, async () => ({ ok: true, json: async () => [] })), /invalid response/);
  await assert.rejects(queryAdvisories(packages, async () => ({ ok: true, json: async () => ({ error: "unavailable" }) })), /invalid advisory list/);
  await assert.rejects(queryAdvisories(packages, async () => ({ ok: true, json: async () => ({ example: {} }) })), /invalid advisory list/);
});

test("reports only affected versions and rejects malformed advisory ranges", () => {
  const packages = [
    { name: "example", version: "1.0.0", path: "a" },
    { name: "example", version: "2.0.0", path: "b" },
  ];
  const advisory = { vulnerable_versions: "<2.0.0", severity: "high", url: "https://example.invalid/advisory" };
  assert.deepEqual(matchAdvisories(packages, { example: [advisory] }, semver), [
    { ...packages[0], severity: "high", url: "https://example.invalid/advisory" },
  ]);
  assert.throws(() => matchAdvisories(packages, { example: [{ vulnerable_versions: "bad" }] }, semver), /invalid advisory version range/);
  assert.throws(() => matchAdvisories(packages, { example: [{ vulnerable_versions: "<2.0.0" }] }, semver), /invalid advisory severity/);
  assert.throws(() => matchAdvisories(packages, { example: {} }, semver), /invalid advisory list/);
});

test("the release gate blocks high and critical matches but reports moderate matches", () => {
  assert.equal(blocksRelease([]), false);
  assert.equal(blocksRelease([{ severity: "moderate" }]), false);
  assert.equal(blocksRelease([{ severity: "low" }, { severity: "high" }]), true);
  assert.equal(blocksRelease([{ severity: "critical" }]), true);
});

test("uses the runtime npm's real semver implementation for npm advisory ranges", () => {
  const root = npmRootForNode(process.execPath, process.platform);
  const actualSemver = createRequire(join(root, "package.json"))("semver");
  const packages = [
    { name: "example", version: "1.2.0", path: "a" },
    { name: "example", version: "2.0.0", path: "b" },
  ];
  const entries = { example: [{ vulnerable_versions: ">=1.0.0 <2.0.0", severity: "high", url: "https://example.invalid" }] };
  assert.deepEqual(matchAdvisories(packages, entries, actualSemver).map((item) => item.version), ["1.2.0"]);
});
