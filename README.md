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
this branch:** there is no tag and no GitHub release, `compatibility.json` in a built archive records no
tested host artifact, and the redistribution rights for the proprietary Cursor SDK tree are still
unconfirmed - see [Licensing status](#licensing-status).

## Native archives

`scripts/package-plugin.{sh,ps1}` assembles one install tree and one per-platform archive for the machine
it runs on, and `scripts/verify-package.{sh,ps1}` verifies an install tree and reports its exact files,
their checksums, and its private runtime metadata.

```text
plugin.backendplugin.json
bin/lip-backend-cursorsdk[.exe]
private/bridge/lip-cursor-sdk-bridge[.exe]
private/bridge/bin/lip-cursor-sdk-bridge.js
private/bridge/dist/
private/bridge/node_modules/
private/bridge/package.json
private/node/node[.exe]
compatibility.json
checksums.sha256
LICENSES/
```

That layout lives in exactly one place, [`internal/packagelayout`](internal/packagelayout), which the
packaging scripts read through `cmd/lip-cursor-sdk-packaging` and the private launcher consumes directly.
Neither packager spells out a layout name: every archive file name, the plugin-private prefix, and every
staging directory - the bridge entry, the built `dist/`, and the production `node_modules/` - is read from
the report. `TestPackageArchive_ScriptsDoNotRestateTheArchiveLayout` fails when one of those literals appears
in a packaging script, and it requires both packagers to derive their staging paths from the report; it is a
guard over that fixed literal list, not a proof that no script could restate anything. That is why the
launcher resolves the same files the archive stages instead of keeping a second copy of the layout.

What the packager stages, and only this:

- **Production JavaScript only.** `npm ci` and `npm run build` run in `bridge-node/`; the dev toolchain
  (TypeScript, tsx, esbuild) never enters the archive. `TestPackageArchive` fails if any dev dependency is
  staged.
- **Production npm dependencies only.** `npm ci --omit=dev` runs against the bridge's own
  `package.json` and `package-lock.json` in the staging directory, and the lockfile is removed afterwards:
  nothing in the archive needs a package manager at run time.
- **SDK-version metadata.** `private/bridge/package.json` and the staged
  `node_modules/@cursor/sdk/package.json` are what the bridge entry reads to resolve its own version and
  to verify the pinned SDK. `cmd/lip-cursor-sdk-packaging render` refuses to describe a tree whose staged
  SDK version does not match the bridge's pin.
- **The private Node runtime**, taken from an official Node distribution when one is supplied
  (`--node-dist <zip|tar.gz|dir>`, digest-checked against `nodejs.org/dist/<version>/SHASUMS256.txt` by
  the release operator), otherwise from `--node-runtime`/`LIP_PACKAGE_NODE_RUNTIME`, otherwise from the
  `node` on `PATH` at build time. `compatibility.json` records which of the three it was, in
  `private_runtime_source`: `nodejs-official-distribution:<artifact>` only for a distribution, and
  `nodejs-supplied-runtime:<file>` or `nodejs-path-fallback:<file>` otherwise. An archive whose runtime
  came from a `PATH` fallback says so in its own `LICENSES/THIRD-PARTY-NOTICES.md` too, because no
  distribution digest backs that runtime; re-stage it with `--node-dist` before publishing.
- **License and provenance notices** under `LICENSES/`: the Node distribution `LICENSE` (which contains
  the Node grant together with the notices for the components Node bundles), this repository's `LICENSE`,
  and a generated `THIRD-PARTY-NOTICES.md` recording the staged production dependency inventory, the
  components the private runtime reports about itself, and the unresolved Cursor SDK question. Staging a
  runtime without its license notices is a packaging failure, not a warning.
- **Checksums over every archive file**, plugin-private files included, in `sha256sum` line order so an
  operator can check them with the platform tool of their choice.

Trust scope is stated rather than implied. `checksums.sha256` covers the manifest, the outer executable,
and every plugin-private file, so a tampered or missing companion is detected. The host's manifest digest
remains the authority for the outer process only; nothing here claims the host authenticates companion
files. `compatibility.json` records the digest of the manifest and of the private runtime, and both
verifiers compare those against the staged files rather than printing a provenance claim nothing has ever
checked. Both verifiers read the layout contract from the repository they ship in (`--repo-root`), never
from the directory they were started in: that one report decides what an install tree is, so a caller's
working directory must not decide it. Install into a protected plugin root: the archive is only as
trustworthy as the directory it is unpacked into.

### Verified platforms

`scripts/package-plugin` refuses to assemble anything but its own platform, because cross-compilation is
not native validation. Each assembled archive narrows the manifest's `platforms` to the single platform it
was assembled on, so an artifact cannot advertise support that was never tested, and
`scripts/verify-package` rejects a tree whose manifest claims any other platform.

| Platform | State |
| --- | --- |
| `windows/amd64` | Natively assembled and verified on Windows/amd64. |
| `linux/amd64` | Natively assembled and verified on Linux/amd64. |
| `windows/arm64`, `linux/arm64` | Declared in `manifest/template.backendplugin.json`, not assembled, not verified, no artifact. |
| `darwin/*` | Not a declared plugin platform. |

Both assembled platforms have enforced evidence rather than a maintainer's word: the `package` lane in
[`.github/workflows/verify.yml`](.github/workflows/verify.yml) is a matrix over `windows-latest` and
`ubuntu-latest`, and each leg assembles, verifies, and runs the private runtime on its own runner. A
platform claim is a fact about a native run, so a new platform needs a new native runner.

Each natively validated platform's evidence is `TestPackageArchive_NativeArchiveIsInstallableAndVerifiable`,
which covers checksum coverage of private files, a tampered private file, a missing companion, a missing
private runtime (in the verifier and in the launcher, which exits 78), unlisted and missing files, a
platform overclaim, install roots containing spaces, extracted-archive round trips, deterministic
checksum-record ordering, the staged runtime starting as a direct process, and verification with no
`node` reachable on `PATH`. On a POSIX runner that lane also drives the PowerShell verifier over the same
tree, so both verifier implementations reach the same verdicts. The public Linux/Windows claims in the
manifest survive only for the platforms in the table above.

### Licensing status

**This is a release blocker, and nothing may be published until it is resolved.** The private Node
runtime is MIT and its notices ship with it. `@cursor/sdk` is proprietary: its `LICENSE.md` states that
use is subject to
[Cursor's Terms of Service](https://cursor.com/terms-of-service) and grants no redistribution right, and
its platform package ships bundled `rg` and `cursorsandbox` binaries whose own license texts the package
does not redistribute and which are absent from the staged tree. A maintainer has to confirm the
redistribution rights for the SDK and its bundled binaries before any archive is published.
`compatibility.json` records that status inside every archive, and `LICENSES/THIRD-PARTY-NOTICES.md`
restates it.

### Building and verifying an archive

```sh
# assemble for this machine (Node 22.22.3 recommended; npm and Node are build-time only)
scripts/package-plugin.sh --out-dir dist --node-dist node-v22.22.3-linux-x64.tar.gz   # or .ps1 on Windows

# verify an install tree: reports exact files, checksums, and runtime metadata
scripts/verify-package.sh --package-root dist/cursorsdk-0.1.0-linux-amd64             # or .ps1 on Windows
```

`GOWORK=off` is set by the scripts themselves. The archive is written to
`<out-dir>/cursorsdk-<version>-<os>-<arch>.{zip,tar.gz}` with its own `.sha256`, next to the unpacked
install tree of the same name; unpack exactly that one directory into the host plugin root.

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
private/bridge/node_modules/                bridge-node/node_modules/
private/node/node[.exe]                     the private Node runtime
```

The bridge entry is the bridge package's `bin/lip-cursor-sdk-bridge.js` rather than
`dist/main.js`, because `--version` and `doctor` are handled there; the shim resolves `dist/`,
`node_modules/`, and `package.json` relative to its own parent directory, which is exactly
`private/bridge/`. Arguments are forwarded unchanged, so `--version`, `doctor`, and the plain
NDJSON server invocation all behave as they do under `node`.

A missing private runtime or bridge entry is an explicit prerequisite failure: the launcher exits
with status 78 (`EX_CONFIG`) and names the expected archive location. It falls back to nothing -
no `PATH` lookup, no system-wide Node, and no other Cursor integration.

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

It assembles a real archive (production JavaScript, the production dependency tree over the network, a
private Node runtime), so it needs the build-time toolchain (`go`, `npm`, `node`), network access, and
several minutes; it is opt-in through `LIP_PACKAGE_GATE=1` rather than part of the default unit lane, and
it skips itself in `-short` mode. Running `scripts/package-plugin` and `scripts/verify-package` by hand
proves the same things.

## Documentation

- Plugin authoring and the executable backend-plugin ABI: Go-LIP
  [`docs/backend-plugins/authoring.md`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/docs/backend-plugins/authoring.md).
- Go-LIP host repository: [`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy).

## License

MIT. See [LICENSE](LICENSE). Extraction provenance and attribution of host-derived code are recorded in
[PROVENANCE.md](PROVENANCE.md).