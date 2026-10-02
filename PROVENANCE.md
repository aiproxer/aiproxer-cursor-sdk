# Provenance

This repository was extracted from the Go-LIP host repository
[`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy). It is
governed by the Go-LIP specification `cursor-sdk-standalone` (`tasks.md` task 1.3 provisioned this
repository skeleton, task 2.1 relocated the Go connector, and task 2.2 relocated the SDK bridge; private
runtime packaging and release tooling follow in later tasks).

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

Private runtime packaging is not present here yet, and no plugin artifact has been released.

The plugin-private bridge launcher in `cmd/lip-cursor-sdk-bridge/` is original work added in this
repository and is MIT-licensed under the same terms as the rest of this repository. It is a small
Go program that resolves the packaged private Node runtime and bridge entry relative to its own
location and forwards the bridge protocol; it contains no relocated host code, no JavaScript, and no
bundled or vendored runtime. Redistribution of the private Node runtime and of the production
`@cursor/sdk` tree into a released archive, with its license and provenance notices, remains the
responsibility of the later packaging task; nothing in this launcher redistributes either one.
