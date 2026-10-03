# Installing the Cursor SDK plugin

This is the operator guide for the `aiproxer-cursor-sdk` backend plugin. It covers
installation through Go-LIP's existing plugin mechanism and the one provisioning step the
plugin needs before it can serve a request.

- [What the archive contains](#what-the-archive-contains)
- [Is a system Node required](#is-a-system-node-required)
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
the manifest and the lockfile that pin the SDK and no third-party package code at all.
You provision that tree yourself; see below.

The host binary is unchanged by any of this. Cursor support is an optional plugin.

## Is a system Node required

**No.** The archive ships its own Node runtime, and the provisioning command runs *that*
runtime through *that* runtime's own bundled npm. You need:

- a Go toolchain only if you want to run `scripts/verify-package` yourself; and
- nothing else. No system Node, no global npm, no pnpm or yarn.

The archive does not contain `private/bridge/node_modules/`, and installing the plugin
does not populate it. Provisioning is a deliberate, separate step.

## Installing

1. Download the archive for your platform and check its digest against the published
   `*.sha256`.
2. Unpack the archive's single top-level directory into the Go-LIP plugin root, so that
   `plugin.backendplugin.json` sits directly in the plugin's own directory.
3. Install the plugin through Go-LIP's existing trusted plugin mechanism. The host reads
   the manifest, verifies the outer executable's digest, and starts
   `bin/lip-backend-cursorsdk` - it never runs anything from `private/`.
4. Put the plugin root somewhere only its owner can write. On Windows, restrict the
   directory ACL; on Linux and macOS, `chmod 700`. Verification reports the requirement
   rather than guessing when it cannot measure it, and a root any local user can write
   lets that user replace a checksummed companion after you verified it.

## Provisioning the Cursor SDK

Provisioning resolves `@cursor/sdk` at the pinned version from the shipped lockfile,
once, into `private/bridge/node_modules/`. Run it from the bridge package directory with
the shipped runtime:

```sh
cd <plugin-root>/private/bridge
../node/node[.exe] ../node/<npm-root>/bin/npm-cli.js ci --omit=dev
```

The exact command is recorded in three places, so you never have to reconstruct it: the
`sdk_provision_command` field of `compatibility.json`, the
`sdk provisioning command:` line of `scripts/verify-package`, and the launcher itself,
which prints it whenever the SDK is missing. It reads, per platform:

| Platform | Command (run from `<plugin-root>/private/bridge`) |
| --- | --- |
| Windows | `..\node\node.exe ..\node\node_modules\npm\bin\npm-cli.js ci --omit=dev` |
| Linux | `../node/node ../node/lib/node_modules/npm/bin/npm-cli.js ci --omit=dev` |

Both spellings work in PowerShell, `cmd`, and any POSIX shell; the two paths are
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
  third-party package code at all. A freshly unpacked archive is expected to *fail*
  installed-state verification with the provisioning command, and that is correct.

The report states the trust split explicitly: `checksums.sha256` covers the shipped files
only, so the plugin authenticates what it ships and you authenticate what you
provisioned.

## When something is missing

Every one of these is an explicit failure naming the remedy. None of them falls back to
another Cursor integration, to a system Node, or to another package manager.

| Symptom | What it means | What to do |
| --- | --- | --- |
| `the Cursor SDK is not provisioned` | `private/bridge/node_modules/@cursor/sdk` is absent | Run the provisioning command above. |
| `the provisioned Cursor SDK is X but the shipped ... pins Y` | The tree was provisioned against a different pin, or edited | Re-run the provisioning command. |
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

## Updating and rolling back

Upgrading the plugin replaces the shipped files; the provisioned tree stays where it is
but may hold the previous SDK version. Re-run the provisioning command after every
upgrade or reinstall so the tree matches the shipped lockfile again.

To roll back, install a previously released archive and re-run the provisioning command
against it. No SDK downgrade happens implicitly: the lockfile in that archive is what
decides the version you end up with, and verification tells you which one you have.

## Provenance and trust

- **The plugin authenticates what it ships.** `checksums.sha256` covers every shipped
  file, `compatibility.json` records the digests of the manifest and of the private
  runtime, and both are cross-checked against the staged files. The host's manifest
  digest authenticates the outer executable only, and nothing here claims more.
- **You authenticate what you provisioned.** `private/bridge/node_modules/` is outside
  the checksum record by design. `LICENSES/THIRD-PARTY-NOTICES.md` inside the archive
  states this and names the SDK as not redistributed.
- **The private runtime's provenance is recorded.** `compatibility.json`
  `private_runtime_source` says whether the shipped runtime came from an official Node
  distribution, a supplied executable, or the build machine's `PATH`, and an archive
  staged from `PATH` says so in its own notices.