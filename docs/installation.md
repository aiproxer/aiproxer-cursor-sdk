# Installing the Cursor SDK plugin

This is the operator guide for the `aiproxer-cursor-sdk` backend plugin. It covers
installation through Go-LIP's existing plugin mechanism and the one provisioning step the
plugin needs before it can serve a request.

- [What the archive contains](#what-the-archive-contains)
- [Is a system Node required](#is-a-system-node-required)
- [Supported platforms](#supported-platforms)
- [Installing](#installing)
- [Provisioning the Cursor SDK](#provisioning-the-cursor-sdk)
- [Verifying the install](#verifying-the-install)
- [When something is missing](#when-something-is-missing)
- [Updating and rolling back](#updating-and-rolling-back)
- [Provenance and trust](#provenance-and-trust)

## What the archive contains

A released plugin archive is one directory. Unpack it into the Go-LIP plugin root and
nothing else:

```text
plugin.backendplugin.json                     closed host manifest
bin/lip-backend-cursorsdk[.exe]               the plugin executable the host starts
compatibility.json                            plugin release metadata
checksums.sha256                              sha256 of every shipped file
LICENSES/                                     license and provenance notices
private/bridge/lip-cursor-sdk-bridge[.exe]    plugin-private launcher
private/bridge/bin/lip-cursor-sdk-bridge.js   bridge CLI shim (--version, doctor)
private/bridge/dist/                          built bridge JavaScript
private/bridge/package.json                   bridge manifest; pins the SDK version
private/bridge/package-lock.json              exact lockfile the operator provisions from
private/node/node[.exe]                       private Node runtime
private/node/{lib/,}node_modules/npm/         that runtime's own bundled npm
```

There is **no** `private/bridge/node_modules/`. The Cursor SDK is proprietary, its
platform package bundles native binaries whose license texts it does not redistribute,
and no redistribution right for any of it has been verified. The plugin therefore ships
the manifest and the lockfile that pin the SDK, and neither the SDK nor any package that
exists only to satisfy it. You provision that tree yourself; see below.

To be precise about what third-party code this archive does ship, because most of it by
file count is the private Node runtime's own bundled npm and the packages npm bundles
inside it (`private/node/{lib/,}node_modules/npm/node_modules/`). Those ship on purpose —
they are what lets the provisioning command run with no global Node and no global package
manager. They are **not** covered by the Node.js MIT grant: npm ships its own license text
inside the tree at `private/node/{lib/,}node_modules/npm/LICENSE`, which licenses the npm
application under the Artistic License 2.0 and states that npm's bundled Node package
dependencies are licensed on their respective license terms, and each bundled package
carries its own license text in its own package directory where it ships one.
`LICENSES/THIRD-PARTY-NOTICES.md` is generated from the staged tree, reads every
dependency directory in it including the nested ones, and names any bundled
package that ships no license text of its own, so the notice never claims coverage the
archive does not carry. The archive is not free of third-party package code; it ships
exactly that, and no notice in it claims otherwise.

The host binary is unchanged by any of this. Cursor support is an optional plugin.

## Is a system Node required

**No. A system Node is not required, and neither is a global npm.** The archive ships its
own Node runtime, and the provisioning command runs *that* runtime through *that*
runtime's own bundled npm. You need:

- a Go toolchain only if you want to run `scripts/verify-package` yourself; and
- nothing else. No system Node, no global npm, no pnpm or yarn.

The archive does not contain `private/bridge/node_modules/`, and installing the plugin
does not populate it. Provisioning is a deliberate, separate step.

## Supported platforms

Two platforms are shipped, and they are the only two. Each one is assembled, verified,
and has its private runtime run natively on a runner of its own; there is no cross-compiled
artifact and no unverified claim.

| Platform | Archive | Bundled npm inside the runtime | Evidence |
| --- | --- | --- | --- |
| `windows/amd64` | `cursorsdk-<version>-windows-amd64.zip` | `../node/node_modules/npm/bin/npm-cli.js` | `windows-latest` leg of the `package` lane in [`.github/workflows/verify.yml`](../.github/workflows/verify.yml) |
| `linux/amd64` | `cursorsdk-<version>-linux-amd64.tar.gz` | `../node/lib/node_modules/npm/bin/npm-cli.js` | `ubuntu-latest` leg of the same lane |

The two commands differ only because each Node distribution keeps its bundled npm
somewhere different; the forward-slash spelling of both works in PowerShell and `cmd` as
well, since Windows accepts forward slashes in a path.

`windows/arm64`, `linux/arm64`, and `darwin/*` are **not** declared platforms. No native
runner assembles or verifies them, so there is no evidence for them and no archive to
install. macOS has fake-bridge development smoke in this repository; that is connector
evidence, not a production claim.

## Installing

1. Download the archive for your platform and check its digest against the published
   `*.sha256`.
2. Unpack the archive's single top-level directory into the Go-LIP plugin root, so that
   `plugin.backendplugin.json` sits directly in the plugin's own directory.
3. Install the plugin through Go-LIP's existing trusted plugin mechanism. The host reads
   the manifest, verifies the outer executable's digest, and starts
   `bin/lip-backend-cursorsdk` - it never runs anything from `private/`.
4. Put the plugin root somewhere only its owner can write. On Windows, restrict the
   directory ACL; on Linux, `chmod 700`. Verification reports the requirement rather than
   guessing when it cannot measure it, and a root any local user can write lets that user
   replace a checksummed companion after you verified it.
5. Provision the SDK (next section). The plugin is inert until you do: a configured
   instance fails with the provisioning command rather than serving anything.

## Running under a Go-LIP host

Install the plugin directory as a discovery path in the host configuration. Each
`paths` entry is one **install directory** — the one that contains
`plugin.backendplugin.json` — not a parent directory holding several of them:

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/go-lip/plugins/cursorsdk      # the directory holding plugin.backendplugin.json
    strict: true
    development_mode: false
```

Then point the host at it:

```sh
lipstd --config ./config/config.yaml check-config
lipstd --config ./config/config.yaml inspect      # the plugin is reported as `discovered`
lipstd --config ./config/config.yaml doctor --instance <your-instance-id>
```

An installed plugin is reported without being started, so `inspect` is the command that
answers "is my plugin trusted and recognised". Nothing starts until you configure and enable
an instance.

### Set `bridge_executable` explicitly on Windows

**Measured against Go-LIP v0.1.0 and adopted as this plugin's installation contract: the
packaged default works on `linux/amd64` and does not work on `windows/amd64`. Set the field
explicitly on Windows; it is optional on Linux.** The real-host certification gate enforces
both halves of that per platform, and the same per-platform contract is what every archive
records in `compatibility.json` under `host_certification_platforms`, so the table below is
also what the release you installed declares for itself.

| Platform | Packaged default | Do you need `bridge_executable`? | Recorded contract |
| --- | --- | --- | --- |
| `linux/amd64` | measured working | no | `packaged-default-supported` |
| `windows/amd64` | measured **not** working | **yes** | `explicit-bridge_executable-required` |

On Windows the host verifies the outer executable's digest, copies it into a private
digest-addressed staging directory, and launches those staged bytes. The connector then
looks for its companion at `../private/bridge/lip-cursor-sdk-bridge[.exe]` relative to the
*running* executable, which is now the staging copy — and there is no `private/` tree beside
it. The failure is explicit and names this very setting:

```text
cursorsdk: private bridge launcher "...\private\bridge\lip-cursor-sdk-bridge.exe" not found
(expected ../private/bridge/lip-cursor-sdk-bridge.exe next to the installed plugin
executable; reinstall the Cursor plugin package or set bridge_executable to a direct bridge
binary)
```

On Linux the host binds the verified executable by descriptor and execs it, so the running
executable is the one in your install root and the default resolves on its own.

Set the field either way if you prefer not to depend on that difference — it costs nothing
and it is the same field on both platforms:

```yaml
- kind: cursorsdk
  id: cursor-sdk
  enabled: true
  config:
    bridge_executable: "C:/go-lip/plugins/cursorsdk/private/bridge/lip-cursor-sdk-bridge.exe"  # windows/amd64
    # bridge_executable: "/opt/go-lip/plugins/cursorsdk/private/bridge/lip-cursor-sdk-bridge"  # linux/amd64
```

| Platform | `bridge_executable` |
| --- | --- |
| `windows/amd64` | `<plugin-root>/private/bridge/lip-cursor-sdk-bridge.exe` |
| `linux/amd64` | `<plugin-root>/private/bridge/lip-cursor-sdk-bridge` |

An absolute path is the right spelling: the plugin resolves a `PATH` name too, but a path
avoids depending on the host's environment. The launcher is a direct executable, not a shell
or npm wrapper, so it passes the connector's own validation unchanged — the same field, and
the same rules, that a source checkout uses.

This was exercised end to end against the released host on both platforms: the packaged
launcher started the packaged private Node runtime, that runtime loaded the
operator-provisioned `@cursor/sdk 1.0.23`, and the plugin's own diagnostics proved each step.
The measurement, and how it was made, is in
[`docs/certification.md`](certification.md#requirement-3123333445-61--real-host-install-trust-and-optional-activation).

### Access mode

This plugin declares `access_scope: local_only` and `execution_class: agent_runtime`, and
its kind is not in the host's multi-user approval registry. Under
`access.mode: multi_user` it is therefore denied at composition, before it starts:

```text
bootstrap failed: runtimebundle: local-only backend is not allowed when access.mode is
multi_user (instance "<id>" factory "cursorsdk")
```

That is the correct and intended behaviour for a local-only agent runtime. Single-user
loopback deployments are unaffected.

## Provisioning the Cursor SDK

Provisioning resolves `@cursor/sdk` at the pinned version from the shipped lockfile,
once, into `private/bridge/node_modules/`. Run it from the bridge package directory with
the shipped runtime:

```sh
cd <plugin-root>/private/bridge
../node/node[.exe] ../node/<npm-root>/bin/npm-cli.js ci --omit=dev
```

The exact command is recorded in three places, so you never have to reconstruct it: the
`cursor_sdk_provisioning_command` field of `compatibility.json`, the
`sdk provisioning command:` line of `scripts/verify-package`, and the launcher itself,
which prints it whenever the SDK is missing. It reads, per platform:

| Platform | Command (run from `<plugin-root>/private/bridge`) |
| --- | --- |
| Windows | `../node/node.exe ../node/node_modules/npm/bin/npm-cli.js ci --omit=dev` |
| Linux | `../node/node ../node/lib/node_modules/npm/bin/npm-cli.js ci --omit=dev` |

The slash spelling is the one the record and the launcher print: it works in every POSIX
shell, and on Windows in PowerShell and `cmd` too, since Windows accepts forward slashes in
a path. A backslash spelling (`..\node\node.exe ..\node\node_modules\npm\bin\npm-cli.js ci
--omit=dev`) works on Windows too and is Windows-only — a POSIX shell reads `\` as an
escape character, so `..\node\node.exe` is not a path there. Either way the paths are
relative to the directory you just changed into, so nothing outside the plugin root is
involved.

Notes that matter:

- **Use npm, not another package manager.** The bridge manifest pins `undici` to the
  security-fixed `6.28.1` through npm `overrides`. `overrides` is npm semantics, and
  other package managers resolve or ignore it differently, so a tree provisioned with
  pnpm or yarn is unsupported and is reported as such.
- **This is the only download the plugin ever performs, and you perform it.** Nothing in
  the plugin or the host installs packages, shells out to npm, or fetches anything. The
  download happens under your own acceptance of
  [Cursor's Terms of Service](https://cursor.com/terms-of-service).
- **Re-run it whenever you reinstall or upgrade the plugin.** `npm ci` deletes the tree
  first, so re-provisioning after an upgrade is idempotent and pins the tree to the new
  lockfile.
- **It needs network access**, once.

## Verifying the install

From a checkout of this repository:

```sh
scripts/verify-package.sh --package-root <plugin-root>              # or verify-package.ps1
```

Two tree states are checked, and both are enforced rather than advisory:

- `--tree-state installed` (the default) treats the tree as an installed one: the
  provisioned SDK has to resolve at the pinned version, and the private runtime and
  launcher have to run.
- `--tree-state shipped` audits a released archive as it arrives: it must contain no
  Cursor SDK and nothing that exists only to satisfy it. (The private runtime's bundled
  npm is third-party package code and is expected — it is what the provisioning command
  runs.) A freshly unpacked archive is expected to *fail* installed-state verification
  with the provisioning command, and that is correct.

The report states the trust split explicitly: `checksums.sha256` covers the shipped files
only, so the plugin authenticates what it ships and you authenticate what you
provisioned. It also prints the record's own evidence rather than a summary of it: the
source identity of the build (either the Go build VCS stamp the toolchain wrote inside the
outer executable, or the revision the packager resolved from the tree it built in, with the
record naming which), whether that build came from a tree with uncommitted changes - or
`unknown` when nothing established it - the exact published Go-LIP module versions the
archive was built against, the declared platforms and the ones this archive is not evidence
for, the host certification posture with its reason, and the fact that the record itself
says the package has not been verified (`package_verification_state: not-performed`, with
the two runs that have to be performed). Nothing in the report claims a verification that
did not happen: `compatibility.json` is written while the archive is assembled, before
verification can run, so the finished report is the evidence and the record is not.

That report is also where the platform claims are checked. Each archive narrows its
manifest to the one platform it was assembled on, and a tree whose manifest claims any
other platform is a finding.

### What was actually tested, and where

The per-platform behaviour described above is not a claim to be taken on trust:

- `TestPackageArchive_NativeArchiveIsInstallableAndVerifiable` assembles a real archive on
  the runner it runs on, provisions the SDK from the registry exactly as you do, and
  verifies the installed tree end to end. It is the evidence behind the
  [supported platforms](#supported-platforms) table, and it runs in CI as the `package`
  matrix in [`.github/workflows/verify.yml`](../.github/workflows/verify.yml) on
  `windows-latest` and `ubuntu-latest`.
- You can run the same evidence yourself with `LIP_PACKAGE_GATE=1 GOWORK=off go test -run
  TestPackageArchive .`, or by hand with `scripts/package-plugin` followed by
  `scripts/verify-package`.

Not covered by that gate, so not claimed anywhere in these instructions: a real
`@cursor/sdk` import from an installed archive (it needs live provider credentials; see
`scripts/test-cursor-sdk-live.{sh,ps1}`), `cursorsandbox` execution, code signing, and any
platform other than the two above. The packaging decision and its evidence, including the
alternatives that were reasoned about rather than measured, are in
[`docs/packaging.md`](packaging.md).

## When something is missing

Every one of these is an explicit failure naming the remedy. None of them falls back to
another Cursor integration, to a system Node, or to another package manager.

| Symptom | What it means | What to do |
| --- | --- | --- |
| `the Cursor SDK is not provisioned` | `private/bridge/node_modules/@cursor/sdk` is absent | Run the provisioning command above. |
| `the provisioned Cursor SDK is X but the shipped ... pins Y` | The declared version in `private/bridge/node_modules/@cursor/sdk/package.json` is not the version the shipped bridge manifest pins | Re-run the provisioning command. |
| `private runtime file "..." not found` | The archive is incomplete | Reinstall from a complete archive. |
| `the private runtime or the private bridge launcher is missing` | The archive is incomplete | Reinstall from a complete archive. |
| `install root ... is writable beyond its owner` | Any local user can replace a companion | Move the plugin root somewhere owner-only. |
| `file is present but not listed in checksums.sha256` | A shipped file is unaccounted for | Reinstall from a complete archive. |

The bridge's own `doctor` reaches the same answers without the Go tooling:

```sh
<plugin-root>/private/bridge/lip-cursor-sdk-bridge[.exe] doctor
```

It exits 78 (`EX_CONFIG`) when a prerequisite is missing, and 0 with `doctor: ok` when
the tree is provisioned and the private runtime answers.

The version comparison behind that second row is a **string comparison of one declared
`version` field** against the pin in the shipped bridge manifest. It is not a content
check: it does not hash the provisioned tree, and nothing in this archive records a
digest for it. A tree whose `@cursor/sdk` declares the pinned version therefore passes
both the verifier and `doctor` whatever else is in it. Treat a version match as "the
right version is declared", not as "the right bytes are installed".

## Updating and rolling back

Upgrading the plugin replaces the shipped files; the provisioned tree stays where it is
but may hold the previous SDK version. Re-run the provisioning command after every
upgrade or reinstall so the tree matches the shipped lockfile again.

To roll back, install a previously released archive and re-run the provisioning command
against it. No SDK downgrade happens implicitly: the lockfile in that archive is what
decides the version you end up with, and verification tells you which one you have.

**Which artifact to roll back to.** `cursorsdk-v0.1.0` is this plugin's first release, so
when you install it there is no earlier plugin archive to return to, and this guide does not
name one: it would be naming an artifact that does not exist. From the next release onward,
the rollback target is the previous `cursorsdk-v*` tag, and the procedure is the same for
every one of them:

1. Install the earlier archive's shipped files over the plugin directory, or unpack it into a
   second plugin directory and point the host's `backend_discovery.paths` at that one. Both
   directories keep their own `private/bridge/node_modules/`, so an upgrade and a rollback
   can sit side by side.
2. Restore the configuration that release expects — in particular `bridge_executable` on
   `windows/amd64`, per the contract above, which is unchanged across these releases but is
   recorded per artifact rather than assumed.
3. Run **that archive's** `cursor_sdk_provisioning_command` (its own `compatibility.json`
   carries it) so the provisioned tree matches the lockfile you just installed. Do not skip
   this and assume the tree is still right: the SDK version in the tree is decided by that
   archive's lockfile, and nothing downgrades it for you.
4. Run `scripts/verify-package --package-root <plugin-root>` and read
   `cursor_sdk_required_version` against the version it reports as provisioned.

If you are moving off the plugin entirely and back to the integration built into the Go-LIP
host, that is a host-side change rather than a plugin rollback: install the host release that
carries the in-tree Cursor connector and remove the plugin from `backend_discovery.paths`.
Nothing in the plugin performs that for you, and nothing in it fails if you do.

## Provenance and trust

- **The plugin authenticates what it ships.** `checksums.sha256` covers every shipped
  file, `compatibility.json` records the digests of the manifest and of the private
  runtime, and both are cross-checked against the staged files. The host's manifest
  digest authenticates the outer executable only, and nothing here claims more.
- **You authenticate what you provisioned.** `private/bridge/node_modules/` is outside
  the checksum record by design. `LICENSES/THIRD-PARTY-NOTICES.md` inside the archive
  states this and names the SDK as not redistributed.
- **The content authenticity of the tree you provision is yours to establish.** The
  split above is not a formality and it does not extend further than it reads: this
  project ships a lockfile and a version string, and nothing more. It never signs,
  hashes, or otherwise attests to a single byte of the provisioned tree, so no verdict
  either verifier prints is evidence about what is actually on disk there. If that
  matters to you, verify the tree yourself against a source you trust — npm's own
  integrity data in the lockfile is the natural starting point — and re-provision from a
  clean tree whenever you cannot. Provisioning under your own acceptance of
  [Cursor's Terms of Service](https://cursor.com/terms-of-service) is where that
  responsibility starts; it does not end there.
- **The private runtime's provenance is recorded.** `compatibility.json`
  `private_runtime_source` says whether the shipped runtime came from an official Node
  distribution, a supplied executable, or the build machine's `PATH`, and an archive
  staged from `PATH` says so in its own notices.
- **The host this release was certified against is named, per platform.**
  `compatibility.json` records `host_certification_state: certified` and, in
  `host_certification_platforms`, the Go-LIP host release, the asset of it, the host binary,
  and the sha256 that binary was measured at for your platform. The release workflow
  re-checks both the host asset and the extracted binary against those digests before it
  certifies, so the record names the host your archive was measured against rather than
  "some host". A host release other than the recorded one is **not** certified by this
  archive: the digests are there to be checked, and both verifiers print them.
  `docs/certification.md` has the measurement and the release commands - see
  [`docs/packaging.md`](packaging.md#host-certification).
