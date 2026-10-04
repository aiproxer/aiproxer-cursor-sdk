# aiproxer-cursor-sdk

Standalone [Go-LIP](https://github.com/matdev83/go-llm-interactive-proxy) backend plugin that integrates the
Cursor SDK.

This repository is the Cursor-specific product project. It owns the Cursor connector, the Cursor SDK
bridge, Cursor instrumentation, fixtures, tests, operator documentation, and release tooling, so that
Cursor's JavaScript toolchain and dependencies evolve and release independently of the Go-LIP host.

The standard Go-LIP distribution does not depend on this repository. Cursor support is optional: an
operator installs a released plugin archive through Go-LIP's existing trusted plugin mechanism, and the
Go-LIP host binary is unchanged.

Full host decoupling is the intended end state, not the current one. Node and npm are now required to
build and test the Cursor bridge only inside this repository, and this repository verifies its own bridge
on Node 22.22.3 with `npm ci`, `npm test`, and `npm run typecheck`. Relocation is still in progress: the
Go-LIP host repository still carries its own copy of the Cursor SDK source under `connectors/cursorsdk/`
and still verifies its bridge with its own Node 22.22.3 `npm ci`/`npm test` lane in
[`.github/workflows/cursor-sdk-platform.yml`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/.github/workflows/cursor-sdk-platform.yml).
That host-side copy and its Node/npm tooling are removed when the cutover completes.

## Status

Relocation is in progress. The Go connector lives here: `cmd/lip-backend-cursorsdk/`,
`internal/service/`, `internal/product/` (provider adapter, lifecycle, diagnostics, protocol, fixtures,
and the Go test suite), the root-level `service_test.go` and `manifest_posture_parity_test.go`, plus
`release.yaml`, `manifest/template.backendplugin.json`, the relocated example configuration
`config/examples/cursor-sdk-experimental.yaml`, and the relocated live-bridge harness scripts
`scripts/test-cursor-sdk-live-bridge.ps1` and `scripts/test-cursor-sdk-live-bridge.sh`.

The SDK bridge now lives here as well, under [`bridge-node/`](bridge-node), with its source, production
lockfile, SDK fixtures/tests, and Node tooling: `@cursor/sdk` pinned to exact `1.0.23`, the `undici`
`6.28.1` security override, `engines.node >=22.13`, and a CI lane pinned to Node `22.22.3`. Its 108 hermetic
bridge tests pass unchanged from this repository, and the shared Go/TypeScript fixtures it reads from
`internal/product/testdata/fixtures` were relocated with the connector. The Go-LIP host still carries its
own copy of the bridge until the cutover completes.

The plugin-private bridge launcher exists as source in `cmd/lip-cursor-sdk-bridge/`; see
[Private bridge launcher](#private-bridge-launcher).

Native archive assembly exists as tooling in this repository; see
[Native archives](#native-archives). **No plugin release has been published and none is published from
this branch:** there is no tag and no GitHub release, no host binary release exists to certify this plugin
against - so the release is **uncertified** with respect to the host, and `compatibility.json` in a built
archive says exactly that, with the reason and no invented host artifact hash - and no redistribution right is
claimed for the proprietary Cursor SDK, because the archive does not ship it at all - see
[Redistribution posture](#redistribution-posture) and
[Host certification](docs/packaging.md#host-certification).

The packaging decision behind the archive shape, including what was measured and what was only reasoned
about, is in [`docs/packaging.md`](docs/packaging.md).

## Installing and provisioning (operators)

Start at [`docs/installation.md`](docs/installation.md). In short:

1. Unpack the released archive's single directory into the Go-LIP plugin root and install
   it through Go-LIP's existing trusted plugin mechanism. The host binary is unchanged.
2. Provision the Cursor SDK once, out of band, with the runtime the archive ships:
   `cd <plugin-root>/private/bridge && ../node/node[.exe] ../node/<npm-root>/bin/npm-cli.js ci --omit=dev`.

**A system Node is not required, and neither is a global npm.** The archive carries the
runtime *and* that runtime's own npm, and the command above needs neither from `PATH`.
npm specifically, not any package manager: the bridge pins `undici` to the security-fixed
`6.28.1` through npm `overrides`, whose semantics other package managers resolve
differently.

## Native archives

`scripts/package-plugin.{sh,ps1}` assembles one install tree and one per-platform archive for the machine
it runs on, and `scripts/verify-package.{sh,ps1}` verifies an install tree and reports its exact shipped
files, their checksums, its private runtime metadata, and its SDK provisioning state.

```text
plugin.backendplugin.json
bin/lip-backend-cursorsdk[.exe]
private/bridge/lip-cursor-sdk-bridge[.exe]
private/bridge/bin/lip-cursor-sdk-bridge.js
private/bridge/dist/
private/bridge/package.json
private/bridge/package-lock.json
private/node/node[.exe]
private/node/{lib/,}node_modules/npm/          that runtime's own bundled npm
compatibility.json
checksums.sha256
LICENSES/
```

There is no `private/bridge/node_modules/` in an archive: the Cursor SDK is not
redistributed, and the operator provisions it against the shipped runtime. See
[Redistribution posture](#redistribution-posture).

That layout lives in exactly one place, [`internal/packagelayout`](internal/packagelayout), which the
packaging scripts read through `cmd/lip-cursor-sdk-packaging` and the private launcher consumes directly.
Neither packager spells out a layout name: every archive file name, the plugin-private prefix, and every
staging directory - the bridge entry, the built `dist/`, the lockfile that pins the SDK, and the runtime's
bundled npm - is read from the report, as is the operator-provisioned prefix the verifier keeps out of the
checksum record. `TestPackageArchive_ScriptsDoNotRestateTheArchiveLayout` fails when one of those literals
appears in a packaging script, and it requires both packagers to derive their staging paths from the report
and both verifiers to read the provisioning contract from it; it is a guard over that fixed literal list,
not a proof that no script could restate anything. That is why the launcher resolves the same files the
archive stages instead of keeping a second copy of the layout.

What the packager stages, and only this:

- **Production JavaScript only.** `npm ci` and `npm run build` run in `bridge-node/`; the dev toolchain
  (TypeScript, tsx, esbuild) never enters the archive. `TestPackageArchive` fails if any dev dependency is
  staged.
- **The bridge manifest and the lockfile, and no Cursor SDK dependency closure.**
  `private/bridge/package.json` pins the SDK version the bridge verifies at run time, and
  `private/bridge/package-lock.json` pins the exact closure and the `undici` override, so an operator's
  `npm ci` resolves the versions this release was built against. `cmd/lip-cursor-sdk-packaging render`
  refuses to describe a staged tree that carries that closure, so a packager that staged one fails instead
  of publishing a bundle it must not ship.
- **The private Node runtime together with its own bundled npm**, taken from an official Node
  distribution when one is supplied (`--node-dist <zip|tar.gz|dir>`, digest-checked against
  `nodejs.org/dist/<version>/SHASUMS256.txt` by the release operator), otherwise from
  `--node-runtime`/`LIP_PACKAGE_NODE_RUNTIME`, otherwise from the `node` on `PATH` at build time.
  `compatibility.json` records which of the three it was, in `private_runtime_source`:
  `nodejs-official-distribution:<artifact>` only for a distribution, and
  `nodejs-supplied-runtime:<file>` or `nodejs-path-fallback:<file>` otherwise. An archive whose runtime
  came from a `PATH` fallback says so in its own `LICENSES/THIRD-PARTY-NOTICES.md` too, because no
  distribution digest backs that runtime; re-stage it with `--node-dist` before publishing. The bundled npm
  is staged at the path its own distribution keeps it (`lib/node_modules/npm` on POSIX,
  `node_modules/npm` on Windows), so nothing inside it is renamed and the entry point the provisioning
  command names is the real one. A runtime staged with no npm beside it is a packaging failure naming
  `--node-dist`, because without it the documented command would need a global package manager.
  **This npm tree and the packages npm bundles inside it are the third-party package code an archive
  actually ships** — the great majority of its files by count. They are **not** covered by the Node.js
  MIT grant. npm is separately licensed: its own `LICENSE` ships inside the staged tree at
  `private/node/{lib/,}node_modules/npm/LICENSE` and licenses the npm application under the Artistic
  License 2.0 while stating that npm's bundled Node package dependencies are licensed on their
  respective license terms. Each bundled package under `private/node/{lib/,}node_modules/npm/node_modules/`
  carries its own license text in its own package directory where it ships one, and where it ships none,
  the generated notice says so by name instead of implying coverage. They ship for exactly the
  reason above. No artifact here claims an archive is free of third-party package code; it is not.
- **License and provenance notices** under `LICENSES/`: the Node distribution `LICENSE` (which contains
  the Node grant together with the notices for the components Node bundles, npm's Artistic-2.0 notice
  among them), this repository's `LICENSE`, and a generated `THIRD-PARTY-NOTICES.md` recording what the
  runtime reports about itself, the license text each bundled npm package ships under its own directory
  plus the ones that ship none, the locked dependency closure the operator will provision, and the
  non-redistribution position on the Cursor SDK — scoped by name to the Cursor SDK and its closure,
  because the archive does ship third-party code: that npm tree. Staging a runtime without its license
  notices is a packaging failure, not a warning, and both verifiers fail an install tree whose
  `LICENSES/` carries no notice rather than reporting a count of zero as evidence that they looked.
- **Checksums over every archive file**, plugin-private files included, in `sha256sum` line order so an
  operator can check them with the platform tool of their choice.

Trust scope is stated rather than implied, and the split is the point. `checksums.sha256` covers every
**shipped** file - the manifest, the outer executable, and every plugin-private file - so a tampered or
missing companion is detected. It deliberately does not cover `private/bridge/node_modules/`: the plugin
authenticates what it ships, and the operator authenticates what they provisioned. Both verifiers print
that split in their report. The host's manifest digest remains the authority for the outer process only;
nothing here claims the host authenticates companion files. `compatibility.json` records the SDK as
**required, not bundled**, with the exact provisioning command and an explicit non-redistribution
statement, and records the digest of the manifest and of the private runtime, which both verifiers compare
against the staged files rather than printing a provenance claim nothing has ever checked.

The rest of that record is release evidence, and every value in it is derived from a validated build input
rather than typed in: the exact published Go-LIP module versions are read from this module's own `go.mod`
(the manifest `go build` resolved, and a `replace` of either module is a packaging failure); the source
revision comes from one of two bases, stated in `source_stamp_evidence` for every archive - the Go build
VCS stamp the toolchain writes inside the outer executable when it builds in a primary version-control
checkout, or, because this project builds in linked work trees where that stamp is written by nobody, the
revision `scripts/package-plugin` resolved from the tree it built in. When both exist they are
cross-checked and a disagreement fails packaging. Whether the build came from a tree with uncommitted
changes is a separate, third state: a dirty tree and a clean tree each state themselves, and a state
nothing established is left out of the record entirely and reported as `unknown` rather than recorded as
clean; the declared platform set comes from the manifest template, alongside the platforms this single
archive is not evidence for; and the host certification posture is declared in `release.yaml` and
validated against any digest supplied to `scripts/package-plugin --tested-host <sha256>`.
`package_verification_state` is `not-performed` in every archive, because the packager writes the record
before verification can run; the record names the two runs that have to be performed, and their reports are
the evidence. Nothing in it labels a check as passed that has not run. Both verifiers read the layout
contract and `release.yaml` from the repository they ship in - the parent of their own
`scripts/` directory - and there is no option to substitute another one: one report decides what an install
tree is, so a caller-supplied one would let the caller decide it, which is the same hole as asking the
working directory with a switch. Verifying a tree that lives outside a checkout is what `--package-root` is
for; it was verified on both implementations from a working directory that is neither the repository nor the
install tree. The scripts also assemble and verify from any working directory, because every `go` invocation
in them runs in the repository rather than in the caller's. Install into a protected plugin root: the
archive is only as trustworthy as the directory it is unpacked into.

### Verified platforms

`scripts/package-plugin` refuses to assemble anything but its own platform, because cross-compilation is
not native validation. Each assembled archive narrows the manifest's `platforms` to the single platform it
was assembled on, so an artifact cannot advertise support that was never tested, and
`scripts/verify-package` rejects a tree whose manifest claims any other platform.

`manifest/template.backendplugin.json` declares exactly the platforms this pipeline can natively assemble,
and nothing else. A platform nobody assembles and runs natively is not declared here, is not resolvable
as an archive layout, and is not a claim this project makes.

| Platform | State |
| --- | --- |
| `windows/amd64` | Declared, natively assembled and verified on Windows/amd64. |
| `linux/amd64` | Declared, natively assembled and verified on Linux/amd64. |
| `windows/arm64`, `linux/arm64` | Not a declared plugin platform: no native runner assembles or verifies them, so there is no evidence for the claim. |
| `darwin/*` | Not a declared plugin platform. |

Both declared platforms have enforced evidence rather than a maintainer's word: the `package` lane in
[`.github/workflows/verify.yml`](.github/workflows/verify.yml) is a matrix over `windows-latest` and
`ubuntu-latest`, and each leg assembles, verifies, and runs the private runtime on its own runner. A
platform claim is a fact about a native run, so a new platform needs a new native runner.
`TestManifestScope_TemplateDeclaresExactlyThePlatformsThePipelineAssembles` holds the declared set, the
`internal/packagelayout` contract, and that runner matrix to the same set, so the three cannot drift apart.

Each natively validated platform's evidence is `TestPackageArchive_NativeArchiveIsInstallableAndVerifiable`,
which covers an archive content audit (no Cursor SDK and no SDK dependency closure in it), checksum coverage of
shipped private files, a tampered shipped file, a missing companion, a missing private runtime (in the
verifier and in the launcher, which exits 78), an installed tree that was never provisioned, a tree
provisioned with the shipped runtime and verified end to end, a provisioned tree at the wrong SDK version,
unlisted and missing shipped files, a platform overclaim, install roots containing spaces, extracted-archive
round trips, deterministic checksum-record ordering, the staged runtime starting as a direct process, and
verification with no `node` reachable on `PATH`. On a POSIX runner that lane also drives the PowerShell
verifier over the same tree, so both verifier implementations reach the same verdicts. The declared
Linux/Windows claims in the manifest survive only for the platforms in the table above.

The always-on half runs in the default unit lane and holds on every platform, whichever script this host
happens to use: the layout contract comes from the repository the verifier ships in rather than from the
caller's working directory, and it comes from the repository with no override; the PowerShell
install-ownership predicate classifies every permission mode; the PowerShell verifier turns a
writable-beyond-owner verdict into exactly one finding more than an owner-only root does; a tree with an
empty `LICENSES/` is a finding and one with notices is not; both verifiers cross-check the recorded
release digests; both verifiers reject a shipped archive that carries the SDK, fail an unprovisioned
installed tree with the provisioning command, fail a provisioned tree at the wrong version, keep
operator-provisioned files out of the "present but not listed" finding, and reject a checksum record that
covers them; and no script restates the archive layout or reaches a global Node.

### Redistribution posture

**The Cursor SDK is not redistributed, so the archive ships no Cursor SDK and none of its dependency
closure.** `@cursor/sdk` is proprietary: its `LICENSE.md` states that use is subject to
[Cursor's Terms of Service](https://cursor.com/terms-of-service) and grants no redistribution right, and its
platform package ships bundled `rg` and `cursorsandbox` binaries whose own license texts the package does not
redistribute. No redistribution right could be verified, so the decision is not to ship it: an archive that
staged the dependency closure would assert a right nobody has confirmed. The archive ships
`private/bridge/package.json` and `private/bridge/package-lock.json` - the manifest and the lockfile that pin
the SDK at `1.0.23` and `undici` at `6.28.1` - and the operator resolves the tree themselves, accepting
Cursor's terms. `cmd/lip-cursor-sdk-packaging render` refuses to describe a staged tree that carries that
closure, `scripts/verify-package --tree-state shipped` fails an archive that contains it, and
`compatibility.json` records `cursor_sdk_bundled: false` next to an explicit non-redistribution statement and
`LICENSES/THIRD-PARTY-NOTICES.md` restates it.

The claim is scoped to the Cursor SDK on purpose. The archive **does** ship third-party package code: the
private runtime's own bundled npm and the packages npm bundles inside it, staged whole from the pinned Node
distribution. npm is licensed separately from Node.js — its own `LICENSE` ships inside the staged tree and
licenses the npm application under the Artistic License 2.0 — and each package npm bundles is licensed on its
own terms by the license that package declares. Where such a package ships its own license text, it ships it
in its own package directory. `LICENSES/THIRD-PARTY-NOTICES.md` is generated from the staged tree, reads
every dependency directory in it including the nested ones, and names every bundled package that ships no
license text of its own, so the notice never implies coverage the archive does not
carry. By file count that npm tree is the bulk of an archive, and it is what makes operator provisioning
work without a global toolchain. "This archive ships no third-party package code" is false of the archive
these scripts build, so no document here says it; what each artifact states instead is which third-party
code ships, under which license, and that the Cursor SDK is not among it.

Because the tree is the operator's, it is also **outside the shipped checksum record**: the plugin
authenticates what it ships, the operator authenticates what they provisioned, and both verifiers say so in
their report. That split has an exact edge worth stating: the plugin ships a lockfile and compares one declared
`version` string, and nothing more — no digest, hash, or signature of the provisioned tree is recorded
anywhere in an archive. A substituted SDK that *declares* the pinned version passes the verifier and `doctor`.
Verifying the actual content of the tree you provisioned is your responsibility; see
[Provenance and trust](docs/installation.md#provenance-and-trust).

**No plugin release has been published and none may be published from this branch:** there is no tag and no
GitHub release, and no host binary release exists to certify the plugin against, so `compatibility.json`
records the release as uncertified with the reason and no host artifact digest.

### Verification limitations

Stated rather than left to be discovered:

- **A probe has no timeout.** Both verifiers run the staged private runtime and the launcher by absolute
  path and wait for them, so a staged executable that never returns hangs the verification instead of
  failing it. That is not a way past a check: the private runtime is covered by `checksums.sha256` and
  cross-checked against `private_runtime_sha256` in `compatibility.json`, so a replaced binary is already
  a finding, and CI bounds the hang with the job timeout. Making a probe time out needs process control
  with different semantics on each platform, and is not implemented.
- **Path comparison does not normalize Unicode.** `Test-SamePath`/`same_path` fold case on a
  case-insensitive filesystem and drop the Windows extended-length prefix, but they compare the rest
  byte for byte. Two spellings of one path that differ only in Unicode normalization therefore read as
  different paths, and verification fails. It fails closed.
- **The PowerShell install-ownership branch is driven end to end only where permission bits are
  readable.** On Windows the check reports `not machine-checkable (Windows ACL)` rather than guessing, so a
  Windows-only checkout proves the predicate for every permission mode
  (`TestPackageArchive_PowerShellOwnershipCheckRejectsWritableInstallRoot`, which runs wherever `pwsh`
  does) but not the script's FAIL branch. The `ubuntu-latest` package leg drives that branch: on a POSIX
  host `verifierImplementations` runs the PowerShell verifier over the same tree, chmods the root to
  `0777`, and requires a non-zero exit from it.

### Licensing status

The private Node runtime is MIT and its notices ship with it in `LICENSES/nodejs-LICENSE`. The npm it bundles
and the packages npm bundles inside that npm are shipped too, and they are **not** under that MIT grant: npm
licenses its own application under the Artistic License 2.0 in a `LICENSE` inside the staged npm tree, and each
package npm bundles is licensed on its own terms by the license its own manifest declares, which it ships in
its own package directory where it has one. That npm tree is the third-party package code this archive
redistributes. The Cursor SDK is proprietary and is **not**
shipped; see [Redistribution posture](#redistribution-posture) for the decision, the command, and where the
position is recorded inside every archive.

### Building and verifying an archive

```sh
# assemble for this machine (Node 22.22.3 recommended; npm and Node are build-time only)
scripts/package-plugin.sh --out-dir dist --node-dist node-v22.22.3-linux-x64.tar.gz   # or .ps1 on Windows

# audit the archive as released: it must contain no Cursor SDK and no SDK dependency closure
scripts/verify-package.sh --package-root dist/cursorsdk-0.1.0-linux-amd64 --tree-state shipped   # or .ps1

# provision the SDK exactly as an operator does, then verify the installed tree
cd dist/cursorsdk-0.1.0-linux-amd64/private/bridge
../node/node ../node/lib/node_modules/npm/bin/npm-cli.js ci --omit=dev
cd -
scripts/verify-package.sh --package-root dist/cursorsdk-0.1.0-linux-amd64             # or .ps1 on Windows
```

`GOWORK=off` is set by the scripts themselves. The archive is written to
`<out-dir>/cursorsdk-<version>-<os>-<arch>.{zip,tar.gz}` with its own `.sha256`, next to the unpacked
install tree of the same name; unpack exactly that one directory into the host plugin root. The
provisioning step is the operator's, exactly as [`docs/installation.md`](docs/installation.md) documents it,
and it is the only download the plugin ever performs.

## Default bridge resolution

With no `bridge_executable` in the plugin configuration, the connector resolves its private
companion launcher as a direct path relative to the running outer plugin executable:
`../private/bridge/lip-cursor-sdk-bridge[.exe]` next to `bin/lip-backend-cursorsdk[.exe]`,
matching the release archive layout. Resolution never consults `PATH`, a shell, npm, a global
binary directory, or the current working directory, and it never installs or downloads
anything. If that file is absent, configuration fails with an explicit prerequisite error that
names the expected relative location; there is no fallback to another Cursor integration.

An explicit `bridge_executable` keeps its previous meaning: a direct bridge binary given as an
absolute path or as a `PATH` name, with the same rejection of shell and npm launchers. Set it
explicitly when running from a source checkout, for example
`bridge_executable: lip-cursor-sdk-bridge` resolved through `PATH` after building the bridge in
`bridge-node/`.

## Private bridge launcher

`cmd/lip-cursor-sdk-bridge/` builds the plugin-private launcher executable that the packaged
default resolves. It is a small Go program, not a shell script: it locates the private Node
executable and the bridge entry relative to its own location, starts them as a direct process,
forwards the bridge protocol streams and the exit status unchanged, and owns the runtime process
it creates. It never runs a shell, npm, or any other package manager, never downloads anything,
and never resolves a runtime through `PATH`, a global binary directory, or the current working
directory.

To run the launcher from a source checkout, build it and lay out the private files next to it:

```text
private/bridge/lip-cursor-sdk-bridge[.exe]   go build ./cmd/lip-cursor-sdk-bridge
private/bridge/bin/lip-cursor-sdk-bridge.js  bridge-node/bin/lip-cursor-sdk-bridge.js
private/bridge/package.json                 bridge-node/package.json
private/bridge/dist/                        bridge-node/dist/
private/bridge/node_modules/                bridge-node/node_modules/  (bridge-node's own `npm ci`)
private/node/node[.exe]                     the private Node runtime
```

The bridge entry is the bridge package's `bin/lip-cursor-sdk-bridge.js` rather than
`dist/main.js`, because `--version` and `doctor` are handled there; the shim resolves `dist/`,
`node_modules/`, and `package.json` relative to its own parent directory, which is exactly
`private/bridge/`. Arguments are forwarded unchanged, so `--version`, `doctor`, and the plain
NDJSON server invocation all behave as they do under `node`.

A missing private runtime or bridge entry is an explicit prerequisite failure: the launcher exits
with status 78 (`EX_CONFIG`) and names the expected archive location. An unprovisioned SDK is
the same kind of failure, and it prints the provisioning command rather than letting the bridge
fail later with a module-resolution stack: the launcher reads the pinned version from the
shipped bridge manifest, compares it against the operator-provisioned package metadata, and
starts nothing when they disagree or the tree is absent. It falls back to nothing in either
case - no `PATH` lookup, no system-wide Node, no npm invocation, and no other Cursor
integration. The bridge's own `doctor` and SDK loader keep the same check as defense in depth.

Cleanup is owned and bounded. The launcher closes the runtime's stdin first, escalates to the
platform's process-tree termination when the runtime does not exit, always reaps what it started,
and treats repeated cleanup as the same single completion. The runtime deliberately stays inside
the launcher's own process tree, so the connector's existing process-tree kill of the launcher
also reaches the runtime and anything the runtime started.

Two limits of that model are operator-visible, so they are stated rather than implied:

- **A launcher-only escalation reaches the direct child, not a tree the runtime forked.** When the
  launcher is stopped by itself - its own stdin closes, or it is sent a termination signal the launcher
  registers, which is `SIGINT` or `SIGTERM` on Windows and `SIGINT`, `SIGTERM`, or `SIGHUP` on POSIX -
  it closes the runtime's stdin. The shipped bridge reads that as end of input, drains its in-flight
  work, and exits by itself; the launcher terminates the private runtime process only when the bridge
  has not exited on its own by the end of the graceful cleanup bound. On POSIX that escalation is
  aimed at the direct child only, so processes the runtime itself started can outlive it; on Windows
  it walks the runtime's own descendants with `taskkill /T /F`. The connector's kill is the wider one
  on both platforms: the launcher's POSIX process group, or that same `taskkill /T /F` ancestry walk on
  Windows.
- **A launcher killed by its own process handle alone can strand the private runtime.** A crash, an
  external kill, or the connector's narrow process-identity-mismatch downgrade - which kills only the
  launcher handle so a reused process id can never widen the blast radius - runs none of the cleanup
  above, because the launcher executes no more code. The stranded runtime then sees its stdin pipe
  close, since the launcher held the only write end, and the shipped bridge stops on that EOF
  (`bridge-node/src/main.ts` serves until its input ends). A runtime that ignored the EOF would have
  to be ended by the operator: close the instance to release everything the connector owns, and
  terminate the leftover `private/node/node[.exe]` process.

The second limit is a stated supervision boundary, not a claim of full ownership. No launcher code can
close it, because it needs an OS-level parent-death mechanism - a parent-death signal on POSIX, a
kill-on-close job object on Windows - that this launcher does not install.
`TestLauncherProcess_AbruptLauncherDeathByHandleAloneStrandsPrivateRuntime` measures that boundary and
`TestLauncherProcess_ConnectorTreeKillReapsRuntimeDescendants` proves the connector's tree kill does
reach the same runtime.

## Pinned host contract baseline

The module depends only on published Go-LIP modules. There are no `replace` directives, no `go.work`
file, and no sibling checkout requirement.

| Module | Pinned version |
| --- | --- |
| `github.com/matdev83/go-llm-interactive-proxy` | `v0.1.0-rc.1` |
| `github.com/matdev83/go-llm-interactive-proxy/connector-support/acp` | `v0.1.0-rc.1` |

The relocated connector imports only the public contract packages of those modules — `pkg/lipapi`,
`pkg/lipsdk`, `pkg/lipsdk/backendplugin`, `pkg/lipsdk/modelinventory`, `api/backendplugin/v1`, and
`connector-support/acp` — so `go build ./...` and `go test ./...` prove the pins resolve from a clean
checkout. The connector never imports host `internal/` packages, and no generated protobuf code is
vendored here: `api/backendplugin/v1` comes from the released root module.

## Verifying

The Go checks require only a Go toolchain; Node and npm are not needed for them.

```sh
GOWORK=off go mod download
GOWORK=off go mod verify
GOWORK=off go mod tidy -diff
GOWORK=off go build ./...
GOWORK=off go test ./...
```

The bridge checks require Node `>=22.13` (CI pins `22.22.3`) and run in `bridge-node/`:

```sh
cd bridge-node
npm ci
npm test
npm run typecheck
```

Both lanes run in CI. SDK and JavaScript dependency maintenance, including the pinned `@cursor/sdk`
version and the `undici` security override, belongs here and never to the Go-LIP host.

A third lane validates the archive natively on its runner - the same gate as
`TestPackageArchive_NativeArchiveIsInstallableAndVerifiable`, run on both `windows-latest` and
`ubuntu-latest` in CI:

```sh
LIP_PACKAGE_GATE=1 GOWORK=off go test -run TestPackageArchive .
```

It assembles a real archive (production JavaScript, the private Node runtime) and resolves the Cursor SDK
over the network exactly as an operator does, so it needs the build-time toolchain (`go`, `npm`, `node`),
network access, and several minutes; it is opt-in through `LIP_PACKAGE_GATE=1` rather than part of the
default unit lane, and it skips itself in `-short` mode. Running `scripts/package-plugin` and
`scripts/verify-package` by hand proves the same things.

Budget for it accordingly. The gate verifies an assembled tree about twenty times, and each verification
walks and digests the whole staged tree; it resolves the SDK once over the network and copies the
provisioned tree for the cases that need one. On `windows-latest` that is a couple of minutes, and on a
slow POSIX host it can run to fifteen minutes or more - well past Go's 10m default, which is not a
comfortable margin. CI runs it as `go test -count=1 -timeout 40m -run TestPackageArchive -v .` inside a
45-minute job, so a slow run is a failure with a real report rather than a kill; pass `-timeout` yourself
if you run the gate locally and it panics on the default.

### Cursor SDK test tooling

`scripts/test-cursor-sdk-*.{sh,ps1}` are this plugin's own Cursor SDK development and evidence tooling, and
they are owned here. They moved out of the Go-LIP host repository with the rest of the integration; the host
no longer carries Cursor SDK source, and a script that exercised Cursor SDK behaviour has no reason to live
in a repository that no longer contains it. Each one resolves the repository root from its own location, so
run it from anywhere:

| Script | What it does | Needs |
| --- | --- | --- |
| `test-cursor-sdk-platform.{sh,ps1}` | Fake-bridge platform smoke for the current OS: `TestPlatformSmoke_*` and `TestProbeNativeBridgeLane_*` in `internal/product`. | Go toolchain only. No credentials, no network. |
| `test-cursor-sdk-comparison-report.{sh,ps1}` | ACP-versus-Cursor-SDK comparison matrix, synthetic and blocked rows only. | Go toolchain only. No credentials, no network. |
| `test-cursor-sdk-live.{sh,ps1}` | Opt-in Node live scenarios (`npm run live-scenarios`) in `bridge-node/`. | `CURSOR_SDK_LIVE=1`, `CURSOR_API_KEY`, and a provisioned bridge tree. |
| `test-cursor-sdk-live-bridge.{sh,ps1}` | Opt-in Go-to-Node live bridge lifecycle harness (`-tags=cursorsdk_live_bridge`). | `CURSOR_SDK_LIVE=1`, `CURSOR_API_KEY`. |

```sh
bash scripts/test-cursor-sdk-platform.sh
bash scripts/test-cursor-sdk-comparison-report.sh
CURSOR_SDK_LIVE=1 CURSOR_API_KEY=... bash scripts/test-cursor-sdk-live.sh
```

The two credential-free scripts are the ones to run by hand; they are the same suites the default
`go test ./...` lane already runs, wrapped so the run pattern and the working directory are explicit. Both
live scripts print `BLOCKED` and exit `0` unless they are explicitly opted in, which is a skip and not a
green live proof - the live rows of the comparison report say `blocked` for the same reason. Live scenarios
consume provider quota, so they stay out of the default verification path.

`comparison-report` is a provider-comparison tool, not a claim about the plugin: it reports synthetic and
blocked rows when no credentials are opted in, and its own output says so.

## Documentation

- Packaging decision and its evidence, including what was only reasoned about:
  [`docs/packaging.md`](docs/packaging.md).
- Operator installation and provisioning: [`docs/installation.md`](docs/installation.md).
- Host required status checks touching the Cursor lane, and the order in which they have to be retired:
  [`docs/host-required-checks.md`](docs/host-required-checks.md).
- Plugin authoring and the executable backend-plugin ABI: Go-LIP
  [`docs/backend-plugins/authoring.md`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/docs/backend-plugins/authoring.md).
- Go-LIP host repository: [`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy).

## License

MIT. See [LICENSE](LICENSE). Extraction provenance and attribution of host-derived code are recorded in
[PROVENANCE.md](PROVENANCE.md).