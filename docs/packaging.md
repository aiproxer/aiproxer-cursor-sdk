# Packaging evaluation: single executable or private runtime

This is the packaging decision for the `aiproxer-cursor-sdk` release, and the evidence
behind it. It exists because the decision has to be auditable without build logs: an
operator, and the next maintainer, has to be able to read what was measured and what was
reasoned about, and never confuse the two.

- [Decision](#decision)
- [What is being packaged](#what-is-being-packaged)
- [Evaluation](#evaluation)
- [Tested](#tested)
- [Not tested](#not-tested)
- [Why not a Single Executable Application](#why-not-a-single-executable-application)
- [Platform evidence](#platform-evidence)
- [Host certification](#host-certification)
- [Release process](#release-process)
- [If the private runtime ever fails validation](#if-the-private-runtime-ever-fails-validation)

## Decision

**The release ships the `private-runtime` archive: a directory containing the outer plugin
executable, a plugin-private bridge launcher, the built bridge, its manifest and lockfile,
and a pinned private Node runtime together with that runtime's own bundled npm. The
Cursor SDK is not shipped; the operator provisions it once with the runtime the archive
carries.**

The alternative evaluated here was a Node **Single Executable Application** (SEA): one
blob per platform containing the bridge, the runtime, and everything the bridge needs,
with no directory of private files at all. It is not what this project ships.

The decision is a release decision about packaging shape. It does not change any
behaviour of the plugin: the same bridge, the same pinned SDK, the same lifecycle, the
same diagnostics, and the same closed host manifest are involved either way.

## What is being packaged

The bridge is a Node program. Node's own loader does the work that decides this
question, and the four facts below are what makes the choice between shipping a runtime
directory and embedding a runtime in one file a real question rather than a preference:

1. `@cursor/sdk` is loaded at run time by name: the bridge does
   `import("@cursor/sdk")` and Node resolves it from `private/bridge/node_modules/`.
2. The bridge reads package metadata by walking to the SDK package root, because the
   SDK's export map does not expose `./package.json`.
3. The SDK's platform package bundles native executables (`rg` and `cursorsandbox`)
   that have to exist as files on disk to be spawned.
4. `overrides` in the bridge manifest pins `undici` to the security-fixed `6.28.1`,
   which is npm semantics and therefore a package-manager step, not a bundler flag.

## Evaluation

Every row states what each shape would mean, and what its evidence status is *in this
repository*. "Tested" means a test in this repository ran the shipped shape on a native
runner. "Reasoned" means the conclusion comes from documented Node and SDK behaviour and
from the four facts above, and nothing here measures it.

| Subject | `private-runtime` (shipped) | Single Executable Application | Evidence status here |
| --- | --- | --- | --- |
| SDK loading | The launcher starts the shipped runtime, the runtime resolves `@cursor/sdk` from `private/bridge/node_modules/`, and the bridge loads it on the first request that needs it. | The SDK tree would have to be inside the blob. Node's SEA support embeds one script; `import()` of a package resolved from a `node_modules` tree is not a thing an SEA blob resolves. | `private-runtime` package **resolution** is tested (see [Tested](#tested)); a real SDK **import** is not, and the SEA column is reasoned. |
| Dynamic imports | `import("@cursor/sdk")` and `import("dist/main.js")` are ordinary runtime resolutions inside the shipped runtime. Nothing is pre-bundled, so there is no second copy of the SDK. | A blob has no `node_modules` to resolve against, and the bridge's own `dist/` is dynamic too. Bundling it would mean a build step that duplicates the operator-provisioned tree. | `private-runtime` dynamic import is exercised by the bridge test suite with an injected importer; against the real package it is reasoned plus live-only. SEA is reasoned. |
| Package metadata lookup | `readInstalledCursorSdkVersion` resolves the SDK entry and walks to its package root. On the installed layout the launcher and `doctor` read the same `package.json` the bridge walks to. | The metadata would have to come from the blob's own copy, which is the one thing that cannot be the operator's provisioned tree. | **Tested** hermetically in `bridge-node` (`sdk_runtime.test.ts`, including the export-map omission and unreadable/missing metadata cases) and on the installed layout by the launcher's preflight and `doctor`. |
| Native assets | None are shipped. `rg` and `cursorsandbox` arrive with the operator's provisioned SDK and run from `private/bridge/node_modules/`, where the SDK expects them. | A blob would have to carry them or extract them at run time, and their license texts are not redistributed by the SDK itself. | `private-runtime` staging is **tested** (the archive content audit fails if the SDK tree is present). Whether `cursorsandbox` executes correctly is **not tested** here: it needs live provider credentials. |
| Sandbox | The plugin rejects execution when configured sandbox support is not verified to be available, before it starts a run. Nothing about sandboxing is baked into the archive. | Same check, but the blob's child processes would have to be located inside the blob. | The **rejection** is tested (hermetic, with the fake bridge). The sandbox **executing** is not tested in this repository; live scenarios with credentials are the only path that exercises it. |
| Signatures | `checksums.sha256` covers every shipped file, including the plugin-private ones, and both verifiers cross-check the recorded manifest and runtime digests. The record is unsigned, and this document does not claim otherwise: protected install-root ownership plus the host's manifest digest are the stated mitigations. | A single blob would be one signature target, which is a genuine advantage, and a genuine problem: the blob would contain the proprietary SDK, which is exactly what cannot be redistributed. | Shipped **checksum coverage and cross-checks are tested**. Code signing (Authenticode) and macOS notarization are **not performed and not claimed**. |
| Platform limits | Assembled natively per platform, and only for the platforms the manifest declares. | A SEA blob is per platform and per architecture, and the same non-redistribution problem applies to it. | `private-runtime` is **tested** on `windows/amd64` and `linux/amd64`. SEA is reasoned. |

## Tested

The shipped shape is validated natively, on the runner of each platform it claims, by
`TestPackageArchive_NativeArchiveIsInstallableAndVerifiable`. That gate is driven by
`scripts/package-plugin` and checked by `scripts/verify-package`, and it runs as the
`package` matrix in [`.github/workflows/verify.yml`](.github/workflows/verify.yml) on
`windows-latest` and `ubuntu-latest`. What it establishes about the shipped shape:

- An archive assembles from a real tree: production JavaScript, the private runtime, that
  runtime's bundled npm, the closed manifest, and the compatibility record.
- The archive contains no Cursor SDK and no SDK dependency closure, and the renderer
  refuses to describe a tree that does.
- The shipped checksum record covers every shipped file; a tampered shipped file, an
  unlisted shipped file, and a missing shipped file are all findings.
- The private runtime starts as a direct process with no `node` reachable on `PATH`, and
  the launcher starts the runtime and runs the bridge's `doctor` through it.
- The provisioned SDK resolves at the pinned version through the shipped lockfile and the
  shipped runtime; an unprovisioned tree and a wrong-version tree both fail with the
  provisioning command.
- The compatibility record's claims about the archive itself are cross-checked against the
  staged bytes: the manifest digest, the private runtime digest, the SDK pin, the platform
  claim, the source identity, and the host certification posture. The source revision comes
  from one of two bases - the Go build VCS stamp inside the outer executable, or, for a
  build in a linked work tree where the toolchain stamps nothing, the revision the packager
  resolved from the tree it built in - and the record states which basis it used. Whether the
  build came from a tree with uncommitted changes is a third state: dirty, clean, or, when
  nothing established it, absent from the record and reported as unknown rather than clean.

The bridge test suite (`bridge-node`) covers the bridge's own behaviour hermetically,
including SDK loading and metadata lookup with an injected importer, and the launcher and
connector lifecycle suites cover process ownership. Those are bridge evidence, not archive
evidence, and are not counted as packaging evidence here.

## Not tested

Stated plainly, because a decision document that only lists what worked is not evidence:

- **No Single Executable Application was built, measured, or benchmarked.** Every SEA
  statement in this document is reasoned from Node's documented SEA behaviour and from the
  loader facts above. Nothing here should be read as a measurement of a SEA build, because
  there is none in this repository.
- **A real `@cursor/sdk` import from an installed archive** is not exercised by the
  packaging gate. `doctor` and the launcher resolve the SDK's package metadata; the import
  itself happens when a request needs the SDK, which requires live provider credentials.
  The opt-in live tooling (`scripts/test-cursor-sdk-live.{sh,ps1}`) is where that is
  exercised, and it is deliberately outside the default verification path because it
  consumes provider quota.
- **`cursorsandbox` and `rg` execution** is not exercised by the packaging gate; it comes
  with the operator's provisioned SDK and needs a live run.
- **Code signing and notarization** are not performed. The archive ships checksums, not
  signatures.
- **macOS** is not a declared platform. The fake-bridge platform smoke on macOS is
  development evidence about the connector, and it is not a production claim.
- **`windows/arm64` and `linux/arm64`** are not declared platforms: no native runner
  assembles or verifies them, so there is no evidence for the claim and the manifest does
  not make it.

## Why not a Single Executable Application

The decision comes down to one fact: the proprietary SDK cannot be in the artifact, and a
SEA blob has nowhere to put a package tree that the operator resolves themselves.

- A SEA blob embeds the bridge script. The SDK arrives after the operator runs a package
  manager, into a `node_modules` directory beside the plugin. Node resolves the SDK by
  walking that directory; a blob is not a directory in that walk.
- The bridge's own entry (`dist/main.js`) is loaded dynamically, and its package metadata
  is read from `private/bridge/package.json`. Both need the directory shape.
- The SDK's bundled native executables need real paths. A blob cannot hand them a stable
  path without extracting at run time, which would move third-party binaries into the
  artifact - the redistribution problem the whole packaging design exists to avoid.
- Signing a blob would be simpler, not different: the thing being signed would still have
  to contain the SDK, or would still need the same provisioned tree next to it.

A SEA variant is therefore not a packaging improvement here. If the SDK ever became
redistributable, this evaluation would have to be redone, because the blocker above would
be gone; nothing in this repository's design would have to change to allow it.

## Platform evidence

| Platform | State | Evidence |
| --- | --- | --- |
| `windows/amd64` | Declared, shipped | Assembled, verified, and the private runtime run natively on `windows-latest`. |
| `linux/amd64` | Declared, shipped | Assembled, verified, and the private runtime run natively on `ubuntu-latest`; the PowerShell verifier is driven over the same POSIX tree there. |
| `windows/arm64`, `linux/arm64` | Not declared | No native runner assembles them, so no evidence exists and no claim is made. |
| `darwin/*` | Not declared | Development smoke only; not a production claim. |

`internal/packagelayout` is the single declaration of that set, the manifest template holds
the same set, and the CI matrix holds it too;
`TestManifestScope_TemplateDeclaresExactlyThePlatformsThePipelineAssembles` keeps the three
from drifting. `scripts/package-plugin` refuses to assemble anything but its own platform,
because cross-compilation is not native validation. Each archive narrows its manifest to
the one platform it was assembled on, and `compatibility.json` records the declared set
alongside the platforms that archive is *not* evidence for.

## Host certification

**This release is `certified` with respect to the Go-LIP host binary.** A downloadable host
release exists — `matdev83/go-llm-interactive-proxy` `v0.1.0` — and this plugin was certified
against it on both platforms this project declares, `windows/amd64` and `linux/amd64`, by
`TestHostReleaseCertification_RealHostInstallTrustAndOptionalActivation`, 15/15 cases on each.
Trusted discovery, manifest identity, secure negotiation, inventory listing, canonical
execution, explicit capability errors, inactive discovery, default-deny multi-user denial, the
per-platform companion-path contract, and explicit prerequisite failures for a missing plugin,
a missing private runtime, and an unprovisioned Cursor SDK all passed on both. The full table,
with the diagnostics each case observed, is in
[`docs/certification.md`](certification.md#requirement-3123333445-61--real-host-install-trust-and-optional-activation).

The certified artifacts are recorded per platform, because the measurement was per platform:
each leg downloaded its own host archive from that release and measured the binary inside it.

| Platform | Host artifact | Host binary digest | Companion contract |
| --- | --- | --- | --- |
| `windows/amd64` | `go-llm-interactive-proxy_0.1.0_windows_amd64.zip` | `6a6f7462d94bd3c5fae16e799e10ec236bfc116c86baaca19544dc3fed6b20c4` | `explicit-bridge_executable-required` |
| `linux/amd64` | `go-llm-interactive-proxy_0.1.0_linux_amd64.tar.gz` | `ce52b7e3f02c12ce00f67eac6b0ee8a038af17d0f62b15ee8e11e34d130b7d3c` | `packaged-default-supported` |

One behaviour is platform-dependent, and it is the archive's own convenience: the
**packaged-default** companion resolution. Measured, it works on linux/amd64 and does not
work on windows/amd64, because there the host verifies the outer executable's digest, copies
it into a private digest-addressed staging directory, and launches those staged bytes, so
`../private/bridge/lip-cursor-sdk-bridge[.exe]` relative to the installed executable resolves
into the staging directory, which has no `private/` tree beside it. That measured difference
is the **adopted installation contract** rather than a defect: on windows/amd64 the
supported configuration sets `bridge_executable` to the full installed launcher path, and on
linux/amd64 the packaged default stays supported and the field is optional. The gate
enforces it per platform, and where the field is required an unset value fails as an
explicit prerequisite naming `bridge_executable` — no search, no rewrite, no provisioning.
`docs/installation.md` prints the per-platform remedy.

`compatibility.json` records that evidence per platform in `host_certification_platforms`,
next to `host_certification_state: certified` and the flat `tested_host_artifacts` list, and
both verifiers print all of it. The evidence is read from `release.yaml`'s
`certified_host_artifacts` rather than typed into a packaging command, and the renderer
refuses a certified posture with a missing, malformed, duplicated, or undeclared-platform
entry. `--tested-host` on `scripts/package-plugin` remains an assertion a run makes about
what it measured, checked against that declaration rather than recorded in place of it, so no
run can certify against a host this repository does not record. No host artifact hash is
invented anywhere — not in the record, not in this document, not in a verifier's report.

## Release process

This release is **prepared for tag `cursorsdk-v0.1.0` and is not yet published.** The process
that will publish it is [`.github/workflows/release.yml`](.github/workflows/release.yml), which is
the only path from a commit to a published archive; nothing here is a claim that a run has
happened. The tag exists as a declaration in `release.yaml`, and until that workflow completes
on it there is no tag, no release, and no downloadable artifact — what exists today is the
metadata, the packagers, the verifiers, and the recorded certification. Every sentence below
describes what the process *will* require of an archive, not what a released artifact has
already been shown to be.

Each leg of that workflow's matrix will assemble one platform's archive **natively**, from a
clean primary checkout of the tagged commit, and will refuse to continue unless all of this
holds for that archive:

- `source_revision` is the commit the tag points at, `source_modified` is `false`, and the Go
  build VCS stamp is the stated basis — so a published archive is revision-bound and its build
  tree was clean, rather than saying "unknown", naming a revision the packager resolved, or
  naming a revision the packager could not establish at all;
- `scripts/verify-package --tree-state shipped` passes on the **staged** tree, which is the run
  that fails when the archive as assembled carries the Cursor SDK or its closure;
- the finished archive **unpacks into a fresh directory**, and that unpacked tree is audited as
  shipped, provisioned with the command the archive itself records, and verified installed — so
  the tree the archive actually delivers is the one that was checked, and the staged tree is
  left untouched for the no-SDK claim;
- `LIP_HOST_CERT_GATE=1 go test -run TestHostReleaseCertification` passes **against that
  unpacked tree and that archive's recorded digest** on that platform, so the gate is evidence
  about the published bytes rather than about a development build or about a staging directory.

Publication will then wait for every leg, re-check each archive against the digest its own leg
recorded, publish `checksums.txt`, attest build provenance for each asset, and create the
release with `gh release create --verify-tag`. It will wait for those gates and for nothing
else: no step of the process stops for an approval, and the publish job's only privilege is the
permission it needs to write the release and the identity it needs to attest it. The tag will
be the maintainer's: the guard checks the tagged commit out before it reads the declaration out
of it, and refuses to run when a release already exists for the tag, so a published tag is
immutable and its assets are never replaced. The same workflow can be run by hand with
`publish` unset, which rehearses every gate and stops before publication.

`docs/certification.md` records the measured evidence, the release commands, and what the gate's
case count covers, in full.

## If the private runtime ever fails validation

The specification allows a clearly labelled **tested external-Node** artifact as the
fallback if the private runtime turns out not to work. That path is not exercised here, and
this project does not claim it: no external-Node artifact has been built or tested. If the
private runtime ever fails validation on a platform, the honest outcome is a release that
says so on that platform - `external_node_required: true` in the record, an installation
guide that requires a system Node for it, and no claim that the private runtime works
there. It is a release decision to be made and tested at that point, not a silent runtime
fallback, and nothing in the plugin falls back at run time today.
