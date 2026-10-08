# Provenance

This repository was extracted from the Go-LIP host repository
[`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy). It is
governed by the Go-LIP specification `cursor-sdk-standalone` (`tasks.md` task 1.3 provisioned this
repository skeleton, task 2.1 relocated the Go connector, task 2.2 relocated the SDK bridge, task 2.3
wired plugin-local companion resolution, task 3.1 added the private Node launcher, task 3.2 added
native archive assembly and verification, and task 3.3 stopped redistributing the SDK and made it
operator-provisioned; release metadata and publication follow in later tasks).

## Extraction baseline

| Fact | Value |
| --- | --- |
| Source repository | `github.com/matdev83/go-llm-interactive-proxy` (Apache License 2.0) |
| Extraction baseline commit | `d40a091b` (publishes root and ACP module baselines `v0.1.0-rc.1`) |
| Specification inventory baseline | `14cdd0e9` (`.kiro/specs/cursor-sdk-standalone/migration-inventory.json`) |
| Original in-tree module | `github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk` |

## Identity and carried baseline

The extracted integration keeps its existing externally observable identity. The following values are
preserved by the relocated sources here and are intentionally not encoded in the manifest, the module
metadata, or the release metadata:

| Aspect | Preserved value |
| --- | --- |
| Plugin ID | `io.golip.backend.cursorsdk` |
| Factory kind / route prefix | `cursorsdk` |
| Credential mode / access scope / process sharing / execution class | `static` / `local_only` / `per_instance` / `agent_runtime` |
| Cursor SDK pin | `@cursor/sdk` 1.0.23 |
| JavaScript security override | `undici` 6.28.1 |
| JavaScript runtime development baseline | Node 22.22.3 |

## Licensing and attribution

This repository, including the Cursor connector and SDK bridge source relocated here, is licensed under the
MIT License; see [LICENSE](LICENSE).

The host repository it is extracted from is **not** MIT-licensed: `matdev83/go-llm-interactive-proxy` is
distributed under the Apache License 2.0. Code, documentation, and configuration that originate in that
repository are therefore derived from an Apache-2.0 work, and this repository records that attribution
explicitly:

- Apache License 2.0 preserves the copyright, patent, trademark, and attribution notices a work actually
  carries. The relocated files carry no per-file copyright or license header — none carried one in the
  host repository before the move either — so there is no per-file notice for them to keep. The host
  repository is licensed Apache-2.0, and the attribution for derived material is recorded here, in this
  file and the table above, rather than in per-file headers. No license header has been added to the
  relocated sources.
- Original work added in this repository (relocation scaffolding, the standalone module, repository
  hygiene, and the plugin-specific bridge, release, and documentation changes) is MIT-licensed. The
  scaffold-only `internal/pinnedcontracts` guard was deleted once the real connector source arrived; the
  relocated connector itself is derived from the host module above.
- Any redistributed JavaScript runtime must carry its own license and provenance notices in the released
  artifact, and no redistributed dependency closure may enter one without a verified redistribution right.
  The relocated `bridge-node/` sources carry no npm licenses of their own; the private runtime tree is
  assembled and licensed at packaging time, and the SDK dependency closure is not redistributed at all - it
  is operator-provisioned from the shipped lockfile. The one third-party dependency closure the archive does
  redistribute is the one the pinned runtime ships with its own distribution: the bundled npm and the packages
  npm bundles inside it, staged whole from that distribution and attributed to their own license texts as the
  table below records.

Nothing in this file re-licenses the host project as MIT; it only states the license of this repository and
attributes the origin of derived material.

## Redistributed runtime and dependency notices

`scripts/package-plugin.{sh,ps1}` stages the components below into every archive and writes the collected
notices to `LICENSES/`, so the artifact carries its own attribution:

| Component | License | Notice staged |
| --- | --- | --- |
| Node.js private runtime (`private/node/node[.exe]`) | MIT, with the notices for the components Node bundles (ICU, OpenSSL, c-ares, libuv, ...) in the distribution `LICENSE` | `LICENSES/nodejs-LICENSE` |
| The runtime's own bundled npm (`private/node/{lib/,}node_modules/npm/`) | **Not** under the Node.js MIT grant. npm's own `LICENSE` licenses the npm application under the Artistic License 2.0 and states that its bundled Node package dependencies are licensed on their respective license terms | `private/node/{lib/,}node_modules/npm/LICENSE`, shipped inside the tree |
| The packages npm bundles inside its own npm tree (`private/node/{lib/,}node_modules/npm/node_modules/`) | Each package on its own terms, as its own `package.json` declares. A package that ships its own license text ships it in its own package directory; `LICENSES/THIRD-PARTY-NOTICES.md` names every bundled package that ships none, with the license it declares, and counts the whole tree, nested `node_modules` included | the per-package license file where the package ships one, or the generated notice |
| This repository's plugin sources | MIT | `LICENSES/plugin-LICENSE` |
| `@cursor/sdk` and its dependency closure | Proprietary and **not redistributed**. `LICENSE.md` states that use is subject to [Cursor's Terms of Service](https://cursor.com/terms-of-service) and grants no redistribution right. The platform package additionally ships bundled `rg` and `cursorsandbox` binaries whose own license texts the package does not redistribute. No archive ships the Cursor SDK or any package that exists only to satisfy it; the third-party package code an archive does ship is the runtime's own bundled npm, listed above. | `LICENSES/THIRD-PARTY-NOTICES.md` (the pinned closure is listed from the shipped lockfile; the packages themselves are absent) |

The third-party-code boundary is worth stating plainly, because an archive is mostly third-party code by
volume: the runtime's bundled npm and the dependencies npm bundles with it are the great majority of the files
an archive carries. That is deliberate — it is what lets an operator provision the SDK with no global Node and
no global package manager — and it is attributed to the license text each component actually ships, not to a
single distribution-wide grant: npm's own license is its own, and each package it bundles is licensed on its
own terms by the text in its own package directory where the package ships one. The distribution `LICENSE`
collected into `LICENSES/`
carries Node's grant and Node's component notices; it does not carry those per-package texts, and nothing
here claims it does. What an archive never ships is the Cursor SDK or its dependency closure. Neither
statement is a claim about the other, and no artifact in this repository claims that an archive is free of
third-party package code: it is not.

The generated notice file records what the archive redistributes; it is evidence, not a license grant. It
states explicitly that `@cursor/sdk` is not redistributed, prints the one command that provisions it with the
shipped runtime, and records that the resulting tree is **operator-attributable**: resolved by the operator's
npm from the shipped lockfile, outside this project's checksum record, and covered by no notice in the
archive. `compatibility.json` carries the same position inside every archive as
`cursor_sdk_bundled: false` plus an explicit non-redistribution statement, and `scripts/verify-package`
reports it.

Because the dependency closure is not shipped, the shipped checksum record covers **shipped files only**, and
the trust claim narrows accordingly: the plugin authenticates what it ships, and the operator authenticates
what they provisioned. `scripts/verify-package` prints that split, rejects a checksum record that covers the
provisioned tree, and requires the provisioned tree to resolve `@cursor/sdk` at the pinned version the
shipped bridge manifest names. `scripts/verify-package --tree-state shipped` additionally rejects an archive
that contains the provisioned tree at all, so the no-redistribution rule is checkable on the bytes an
operator receives rather than only asserted by the packager.

**The host certification evidence is recorded, per platform, and nothing is invented in it.**
`release.yaml` declares `host_certification: certified` with one `certified_host_artifacts`
entry per declared platform, naming the Go-LIP host release
(`matdev83/go-llm-interactive-proxy` `v0.1.0`), the asset of it that was downloaded, the
checksum file the host published beside it, the host binary inside it, the sha256 that binary
was measured at, and the companion contract that platform supports. The renderer copies that
into every archive's `host_certification_platforms` and `tested_host_artifacts` and refuses a
certified posture with a missing, malformed, duplicated, or undeclared-platform entry, so a
published artifact cannot name host evidence this repository does not record. Those digests are
measurements of downloaded host binaries; none of them is a claim about this repository's own
code, and no digest here was produced by anything other than hashing the artifact it names.

The private Node runtime is staged from one of three sources, and `compatibility.json` records which one
in `private_runtime_source`, so an archive never overstates where its runtime came from:

| Recorded source | What was staged |
| --- | --- |
| `nodejs-official-distribution:<artifact>` | A copy of the official Node.js distribution given with `-NodeDist` (or `--node-dist`), whose artifact digest the release operator checks against `nodejs.org/dist/<version>/SHASUMS256.txt`. |
| `nodejs-supplied-runtime:<file>` | A runtime given with `-NodeRuntime` (`--node-runtime`) or through `LIP_PACKAGE_NODE_RUNTIME`. |
| `nodejs-path-fallback:<file>` | The `node` found on `PATH` on the build machine, because the packager was given neither a distribution nor a runtime. |

The third case is a copy of a developer's or CI runner's working installation, not of an official
distribution: no distribution digest backs it, and `LICENSES/THIRD-PARTY-NOTICES.md` says so inside the
archive itself. **An archive whose `private_runtime_source` is a PATH fallback or a supplied runtime is
not release evidence of a redistributable runtime; re-stage it from an official distribution before
publishing.** The Node license notice that ships with the runtime is collected from the same directory
or a parent of it either way, so the MIT grant travels with the file regardless of its source. The
runtime's own bundled npm is staged from that same installation, at the location the platform's
distribution keeps it, so the provisioning command an operator is documented to run resolves to a real
entry point rather than to an invented path; a runtime staged with no npm beside it is a packaging
failure naming `-NodeDist`, because a global package manager is not an acceptable substitute.

`compatibility.json` also records the runtime's version and digest, the exact published Go-LIP module versions
the archive was built against (read from this module's own `go.mod`, the manifest the build resolved), and
the source identity of the build. The source revision comes from one of two bases, and `source_stamp_evidence`
states which one every archive carries: the Go build VCS stamp the toolchain writes inside the outer
executable when the build happens in a primary version-control checkout, or - because this project builds in
linked work trees, where that stamp is written by nobody - the revision `scripts/package-plugin` resolved from
the tree it built in. Where both are available they are cross-checked and a disagreement fails packaging.
Whether the build came from a work tree with uncommitted changes is recorded in its own right, as dirty or
clean, or as `unknown`: a state nothing established is left out of the record and reported as unknown, because
recording it as clean would assert something about the artifact that nobody checked. The record also carries
the declared platform set alongside the platforms that one archive is not evidence for, and the package
verification state - which is `not-performed` in every archive, because the packager writes the record before
verification can run. It never records a verification outcome that has not happened.
`release_tag_declared` records the tag `release.yaml` declares for this publication -
`cursorsdk-v0.1.0` - and the release itself is produced by
`.github/workflows/release.yml` from that tag, which is where the source identity above is
established for a published archive: each matrix leg builds in a primary checkout of the tagged
commit, so the toolchain's VCS stamp is the basis the record states, and the leg refuses to
continue unless `source_revision` is the tagged commit with `source_modified: false`. No
release artifact is checked into this repository.

## Current commit scope

The Go connector is relocated: `cmd/lip-backend-cursorsdk/`, `internal/service/`, `internal/product/`
(including its Go test suite and `testdata` fixtures), the root-level `service_test.go` and
`manifest_posture_parity_test.go`, `release.yaml`, `manifest/template.backendplugin.json`, the relocated
`config/examples/` entry, and the relocated live bridge harness scripts. Changes relative to the in-tree
module are limited to module-relative import paths, repository-root path assumptions in relocated tests
and scripts, the `release.yaml` module and `replace_policy` fields, and the deletion of the
now-redundant scaffold guard. No provider behavior, error mapping, configuration semantics, or
credential handling was altered.

The SDK bridge is relocated as `bridge-node/`: bridge source, the production `package-lock.json`, the
TypeScript tsconfigs, the `bin/` entry, the bridge README, and the SDK fixtures/tests it shares with Go.
The bridge moved unmodified, so its baseline is byte-identical to the in-tree
`connectors/cursorsdk/bridge-node`: `@cursor/sdk` exact `1.0.23`, the `undici` `6.28.1` security
override, `engines.node >=22.13`, no npm lifecycle hooks, and 108 hermetic tests passing on Node
`22.22.3`. Only the bridge README's repository-relative documentation and smoke-tooling links changed,
because they pointed at host-only paths. The bridge now owns its own CI lane and npm Dependabot entry in
this repository, and its Node tooling no longer depends on the Go-LIP host.

Native archive assembly and verification are original work added in this repository and are MIT-licensed
under the same terms as the rest of it: `internal/packagelayout` (the single archive-layout contract),
`cmd/lip-cursor-sdk-packaging` (its build-time reporting and metadata-rendering surface),
`scripts/package-plugin.{sh,ps1}`, `scripts/verify-package.{sh,ps1}`, `scripts/lib/install-ownership.ps1`,
`cmd/lip-cursor-sdk-bridge/sdk.go` (the run-time SDK preflight), `docs/installation.md` (the operator
guide), and the packaging tests. They contain no relocated host code and no JavaScript. The
release process itself is original work as well: `.github/workflows/release.yml` assembles,
audits, and certifies each platform's archive natively before publication and never creates or
moves a tag.

The plugin-private bridge launcher in `cmd/lip-cursor-sdk-bridge/` is original work added in this
repository and is MIT-licensed under the same terms as the rest of this repository. It is a small
Go program that resolves the packaged private Node runtime and bridge entry relative to its own
location, checks the operator-provisioned SDK tree against the pin the shipped bridge manifest
carries, and forwards the bridge protocol; it contains no relocated host code, no JavaScript, and no
bundled or vendored runtime. It redistributes nothing by itself, installs nothing, and runs no
package manager: it resolves the private Node runtime and its bundled npm, reads the provisioned
package metadata, and prints the provisioning command when the tree is missing or at the wrong
version. Those two components enter an archive only through the packaging scripts above, which carry
the notices recorded in this file.
