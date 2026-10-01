# Provenance

This repository was extracted from the Go-LIP host repository
[`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy). It is
governed by the Go-LIP specification `cursor-sdk-standalone` (`tasks.md` task 1.3 provisioned this
repository skeleton; the connector and bridge relocation follows in later tasks).

## Extraction baseline

| Fact | Value |
| --- | --- |
| Source repository | `github.com/matdev83/go-llm-interactive-proxy` (Apache License 2.0) |
| Extraction baseline commit | `d40a091b` (publishes root and ACP module baselines `v0.1.0-rc.1`) |
| Specification inventory baseline | `14cdd0e9` (`.kiro/specs/cursor-sdk-standalone/migration-inventory.json`) |
| Original in-tree module | `github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk` |

## Identity and baseline to be carried by the bridge relocation

The extracted integration keeps its existing externally observable identity. The following values are
preserved by the later relocation tasks and are intentionally not encoded in this skeleton commit:

| Aspect | Preserved value |
| --- | --- |
| Plugin ID | `io.golip.backend.cursorsdk` |
| Factory kind / route prefix | `cursorsdk` |
| Credential mode / access scope / process sharing / execution class | `static` / `local_only` / `per_instance` / `agent_runtime` |
| Cursor SDK pin | `@cursor/sdk` 1.0.23 |
| JavaScript security override | `undici` 6.28.1 |
| JavaScript runtime development baseline | Node 22.22.3 |

## Licensing and attribution

This repository, including the Cursor plugin source that later tasks move here, is licensed under the MIT
License; see [LICENSE](LICENSE).

The host repository it is extracted from is **not** MIT-licensed: `matdev83/go-llm-interactive-proxy` is
distributed under the Apache License 2.0. Code, documentation, and configuration that originate in that
repository are therefore derived from an Apache-2.0 work, and this repository records that attribution
explicitly:

- Apache License 2.0 requires derived files to retain their copyright and license notices. Every relocated
  file keeps its original Apache-2.0 header, and its provenance is attributable to the host repository and
  the baseline commit above.
- Original work added in this repository (relocation scaffolding, the standalone module and its pinned
  contract guard, repository hygiene, and the plugin-specific bridge, release, and documentation changes)
  is MIT-licensed.
- Any redistributed JavaScript runtime or SDK dependency added by a later task must carry its own license
  and provenance notices in the released artifact.

Nothing in this file re-licenses the host project as MIT; it only states the license of this repository and
attributes the origin of derived material.

## Current commit scope

This commit is provisioning only: module path, pinned published dependencies, license, provenance,
repository hygiene, and independent Go plus Node verification workflows. No Cursor implementation
source, bridge code, npm manifest, lockfile, fixture, or test has been moved here yet, and no plugin
artifact has been released.