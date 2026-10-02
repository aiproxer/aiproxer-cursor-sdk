# Provenance

This repository was extracted from the Go-LIP host repository
[`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy). It is
governed by the Go-LIP specification `cursor-sdk-standalone` (`tasks.md` task 1.3 provisioned this
repository skeleton, task 2.1 relocated the Go connector, task 2.2 relocated the SDK bridge, task 2.3
wired plugin-local companion resolution, task 3.1 added the private Node launcher, and task 3.2 added
native archive assembly and verification; release metadata and publication follow in later tasks).

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
- Any redistributed JavaScript runtime or SDK dependency added by a later task must carry its own license
  and provenance notices in the released artifact. The relocated `bridge-node/` sources carry no npm
  licenses of their own; the installed SDK and runtime tree is assembled and licensed at packaging time.

Nothing in this file re-licenses the host project as MIT; it only states the license of this repository and
attributes the origin of derived material.

## Redistributed runtime and dependency notices

`scripts/package-plugin.{sh,ps1}` stages the components below into every archive and writes the collected
notices to `LICENSES/`, so the artifact carries its own attribution:

| Component | License | Notice staged |
| --- | --- | --- |
| Node.js private runtime (`private/node/node[.exe]`) | MIT, with the notices for the components Node bundles (ICU, OpenSSL, c-ares, libuv, ...) in the distribution `LICENSE` | `LICENSES/nodejs-LICENSE` |
| This repository's plugin sources | MIT | `LICENSES/plugin-LICENSE` |
| Staged production npm dependencies (`@bufbuild/protobuf`, `@connectrpc/*`, `@statsig/*`, `undici`, `zod`) | As declared by each package: Apache-2.0 and/or BSD-3-Clause, Apache-2.0, ISC, MIT, MIT | `LICENSES/THIRD-PARTY-NOTICES.md` |
| `@cursor/sdk` and its platform package | Proprietary. `LICENSE.md` states that use is subject to [Cursor's Terms of Service](https://cursor.com/terms-of-service) and grants no redistribution right. The platform package additionally ships bundled `rg` and `cursorsandbox` binaries whose own license texts the package does not redistribute. | `LICENSES/THIRD-PARTY-NOTICES.md` (factually recorded) |

The generated notice file records what the archive redistributes; it is evidence, not a license grant. It
states explicitly that a maintainer must confirm the redistribution rights for `@cursor/sdk` and its
bundled binaries before publication. **That confirmation does not exist yet, so this repository has
published nothing and must publish nothing until it does.** `compatibility.json` carries the same status
inside every archive, and `scripts/verify-package` reports the staged notices so an operator sees the same
question the maintainers have to answer.

The private Node runtime is a copy of an official Node.js distribution selected at packaging time, never
of a developer's working installation; `compatibility.json` records its version, digest, and provenance
label. Both artifacts are reproducible from the inputs recorded there, and neither is checked into this
repository.## Current commit scope

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
`scripts/package-plugin.{sh,ps1}`, `scripts/verify-package.{sh,ps1}`, and the packaging tests. They contain
no relocated host code and no JavaScript. **No plugin artifact has been released: there is no tag and no
GitHub release, and the redistribution rights above are still unconfirmed.**

The plugin-private bridge launcher in `cmd/lip-cursor-sdk-bridge/` is original work added in this
repository and is MIT-licensed under the same terms as the rest of this repository. It is a small
Go program that resolves the packaged private Node runtime and bridge entry relative to its own
location and forwards the bridge protocol; it contains no relocated host code, no JavaScript, and no
bundled or vendored runtime. It redistributes nothing by itself: the private Node runtime and the
production `@cursor/sdk` tree enter an archive only through the packaging scripts above, which carry the
notices recorded in this file.
