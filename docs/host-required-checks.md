# Host required status checks touching the Cursor SDK lane

Recorded by spec task 3.4 (`cursor-sdk-standalone`) while relocating the plugin's own tooling. This is a
read-only inspection of the Go-LIP host repository; nothing in the host's branch protection, rulesets, or
workflows was changed. Re-run the commands below before acting on this record - required contexts are
external configuration and can change independently of this repository.

Inspected: `matdev83/go-llm-interactive-proxy`, host worktree on `spec/cursor-sdk-standalone` at
`3887c146` - the commit that records this plugin's `3.2` merge (`c803d4b`). Every line number below was
read from that tree, so re-derive them there rather than from `origin/main`.

## Current state

`gh api repos/matdev83/go-llm-interactive-proxy/branches/main/protection` reports classic branch
protection on `main` with `required_status_checks.strict = true` and these seven contexts:

| Context | Source |
| --- | --- |
| `Repo hygiene` | host workflow (not Cursor-specific) |
| `Test (ubuntu-latest)` | host `ci.yml` |
| `Test (windows-latest)` | host `ci.yml` |
| `Test (macos-latest)` | host `ci.yml` |
| `Analyze (Go)` | host `codeql.yml` |
| `Go vulnerability check` | host `security.yml` |
| `bridge-node-tests` | **`.github/workflows/cursor-sdk-platform.yml`**, job `bridge-node-tests` |

Exactly one required context comes from the Cursor workflow: `bridge-node-tests`.

`platform-smoke (${{ matrix.os }})` - the three-OS job of the same workflow - is **not** a required
context. It is additionally gated on the workflow's own `changes` classifier, so it is simply absent from a
run that does not touch Cursor SDK paths. GitHub tolerates an absent non-required check, so its removal
creates no dangling requirement.

`gh api repos/matdev83/go-llm-interactive-proxy/rulesets` returns one ruleset, `protector`
(id `15600845`), with `enforcement: "disabled"` and rules limited to deletion, non-fast-forward, creation,
update, and required linear history. It carries no `required_status_checks` rule, so it gates nothing today
and the classic branch protection above is the only live authority.

## What `bridge-node-tests` was actually gating

The job is deliberately unconditional: its `if: always() && !cancelled()` guard exists so the required
context always reports, and on a PR that touches nothing Cursor-related it succeeds after echoing
"Bridge tests bypassed; no Cursor SDK changes." Only when the classifier reports a Cursor SDK change does it
check out, install `connectors/cursorsdk/bridge-node` with Node 22.22.3, and run `npm ci`, `npm test`, and
`npm run typecheck`.

So for the great majority of host PRs it was a no-op gate whose only real effect was to keep a required
context alive. Its substantive evidence - that the Cursor SDK JavaScript bridge builds, tests, and
typechecks against its own pinned lockfile - is now owned by this repository: the `bridge-node` job in
`.github/workflows/verify.yml` runs `npm ci` -> `npm test` -> `npm run typecheck` on the same pinned
Node 22.22.3 against `bridge-node/package-lock.json` (relocated in task 2.2).

## Replacement invariant

The host-relevant property the Cursor lane was standing in for is **host Node-independence**: building the
standard Go-LIP distribution and running its standard verification must not require Node or npm, because
Node now exists only inside this plugin's archive. The generic replacement is the host no-Node guard of spec
task 5.1:

- `.github/workflows/node-independence.yml`
- `scripts/check-node-independence.py`
- `internal/qa/node_independence_contract_test.go`

That lane replaces the host-relevant invariant rather than the Cursor lane's own evidence: the Cursor
bridge evidence does not come back to the host, it stays here.

## Ordering constraint

`bridge-node-tests` must leave the required contexts **before** `.github/workflows/cursor-sdk-platform.yml`
is deleted, in this order:

1. Land task 5.1's host no-Node lane and its QA regression, and let it report green on `main`. If the
   no-Node invariant is to be enforced by branch protection rather than merely observed, add the lane's
   context to `required_status_checks` in the same change. No `node-independence` context exists in the
   required list yet, so this is an open decision for the maintainer.
2. Remove `bridge-node-tests` from `required_status_checks` on `main`
   (`PUT /repos/matdev83/go-llm-interactive-proxy/branches/main/protection/required_status_checks`).
3. Land the four `internal/qa` contract-test updates listed under "Must be updated in the same change"
   below, and confirm `go test ./internal/qa` is green.
4. Delete `.github/workflows/cursor-sdk-platform.yml` in the same PR as step 2, or a later one.

Steps 3 and 4 are not interchangeable. Four host QA contracts read `cursor-sdk-platform.yml` by name and
`t.Fatalf` when the file is missing, so deleting the workflow before those expectations are rewritten turns
`go test ./internal/qa` red - the deletion breaks a gate instead of retiring one. `internal/qa` is not a
required status context today, so a red lane here blocks a PR rather than `main` outright, but it is a
failing host gate either way.

Deleting the workflow first would leave `bridge-node-tests` required and permanently unreportable: with
`strict = true` every subsequent PR would be blocked with no way to satisfy the context. This is the
"never leave PRs blocked on a deleted required job" case in the design's Integration & Migration Notes
step 4, and it is why no required check may be retired before its replacement reports.

### Second ordering constraint: the relocated scripts outlive batch-1

Deleting the host's `scripts/test-cursor-sdk-*.{sh,ps1}` must land **no earlier than** batch-1, the Cursor
test and verification ownership transfer (`migration-inventory.json` `batches.batch-1`, task 6.1, 59
`*_test.go` files).

The reason is a relocated host test that reads the live-bridge scripts **from the repository root**, not from
the connector directory. At the inspected baseline, `connectors/cursorsdk/internal/product/live_bridge_scripts_test.go`
resolves `repoRoot` by walking **four** levels up from its own directory (~18) - i.e. to the host root - and
then `require.NoError`s on reading `scripts/test-cursor-sdk-live-bridge.ps1` and `.sh` from there (~20-25).

So the earlier rationale, that those references "disappear with the directory itself", holds for the scripts'
*callers* inside `connectors/cursorsdk/**` but **not** for `_test.go` files: `go test` discovers tests by file,
not by directory, and a test is not deleted because its parent tree is deleted one commit later. As long as
that test lives anywhere in the host module, the host copies of
`scripts/test-cursor-sdk-live-bridge.{sh,ps1}` are load-bearing and their deletion is a `require.NoError`
failure. The batch-1 transfer is what removes the reader; only then may the host copies go.

This is a sequencing constraint, not a scope change: the script rows are nominally task 5.2 work, and
`tasks.md` records 6.1 as depending on 5.2. The host-side ordering inside that graph is still
`transfer (6.1) -> delete host copies`, so the deletion step must be held back into the 6.1 delivery or later.
Its own copy in this repository already rewrote the same walk to **two** levels up, which is why the plugin's
`go test ./...` passes here with no host root at all.

## Also host-side, for the cutover batches

The complete set of **active** host references that break or go stale when
`.github/workflows/cursor-sdk-platform.yml` and the relocated `scripts/test-cursor-sdk-*.{sh,ps1}` leave the host.
None of them is a plugin prerequisite, so none of them gates this repository's own release; every one of them is
the host's problem. Almost all land in task 5.2's batch; the relocated-script deletions are held to the batch-1
ordering constraint above.

### How this list was derived

The list is the union of two passes, run against the `3887c146` tree with
`.kiro/specs/archive/**`, `.kiro/specs/cursor-sdk-standalone/**`, and the connector source trees excluded
(archived specs are historical record; the active spec owns its own inventory).

**Pass 1 - mechanical, by identifier.** One `git grep -l -i -F` per pattern, over the whole worktree:

| Pattern | Catches |
| --- | --- |
| `cursor-sdk-platform` | the workflow file name, wherever it is read or named |
| `Cursor SDK Platform Smoke` | the workflow's **display** `name:`, and so every prose and cache-policy reference to the lane |
| `bridge-node-tests` | the required job by name |
| `test-cursor-sdk-` | the eight relocated script files and every caller |
| `cursorsdk` | the cache-lane key, the `makefile-scope` scope keyword, and every kind/prefix enumeration |
| `cursor_sdk` | the underscore spelling, which the other patterns miss |
| `Cursor SDK` | prose-only references that name the product rather than a path |
| `makefile-scope` | every consumer of the shared Makefile-relevance probe, Cursor-related or not |

**Pass 2 - by workflow file name, not by content.** `git ls-files` for `cursor-sdk-platform`,
`test-cursor-sdk-`, and `makefile-scope`, so a file that *is* the artifact is never missed because it does
not spell its own name.

**Why both passes.** Pass 1 alone finds `.github/workflows/backend-plugin-release-gates.yml`, which names
the lane only in a two-line comment and shares no identifier with the file it references; the earlier
four-pattern reconciliation could not see it. Pass 2 alone finds nothing prose. A reference is only
reconciled when both passes have run, which is also why the table below carries a "Deliberately not in this
list" section: the excluded hits have to be named to prove they were checked rather than missed.

Re-run both passes before acting. A host-side reference the migration inventory missed is a red
`internal/qa` lane or a silently unbound cache lane, not a documentation nit.

**UPDATE means the file stays and its expectations are rewritten. DELETE means the whole reference goes.** The
distinction is load-bearing in two rows. The QA contract tests are updated, never deleted, because each one also
carries a generic host-boundary assertion that must survive. And `scripts/makefile-scope.sh` is updated, never
deleted, because its `acp` and `backend-plugin` scopes serve two live non-Cursor matrices - a reader that treats
it as Cursor-only tooling takes those matrices with it.

### Must be updated in the same change

| Host reference | What it references | Why it is not a deletion |
| --- | --- | --- |
| `internal/qa/remote_ci_performance_contract_test.go` (~149-152) | Hard-reads `cursor-sdk-platform.yml` and asserts the `bridge-node-tests` job's `name`, `if`, and `needs`. | **The item that breaks loudest.** `read` fatals on a missing file, so following this document's own ordering turns `go test ./internal/qa` red. Task 5.2 drops the dedicated-lane expectation and keeps the generic assertion it also carries. |
| `internal/qa/ci_iteration_speed_contract_test.go` (~72, ~78-82, ~91, ~101) | **Three** distinct Cursor couplings. (a) ~72 reads `scripts/makefile-scope.sh` through `readRepositoryFile`, which `t.Fatalf`s on error. (b) ~78-82 asserts the hard needle `"cursor-sdk\|cursorsdk"` against that script and `t.Fatalf`s when it is absent. (c) ~91 and ~101 name `cursor-sdk-platform.yml` in the Makefile-relevance probe list and in the bounded go-cache consumer/producer set, each read through the same fatal helper. | (a) and (b) mean the script cannot be deleted at all - see the `makefile-scope.sh` row. Only the `"cursor-sdk\|cursorsdk"` needle drops; `"acp\|cursorcliacp"` and `"backend-plugin"` must survive, or the assertion weakens. Its remaining cache-contract assertions are generic and stay, minus this workflow. |
| `internal/qa/development_iteration_contract_test.go` (~20, ~69) | Names the same workflow in the bounded-cache/`cache: true` set and in the retention-trigger cross-check. | Same hard read; the retention cross-check is what keeps `go-cache-maintenance.yml` honest, so the row below moves with it. |
| `internal/qa/main_push_lane_scope_test.go` (~24, ~68-69) | **Two** couplings. (a) ~24 is the lane-table row `{cursor-sdk-platform.yml, changes, filter, cursorsdk, connectors/cursorsdk/example.go, true, true}`. (b) ~68-69 copies `makefile-scope.sh` out of the real repo into a throwaway git fixture via `readRepositoryFile` **inside every lane subtest**, including the `acp-process-tree`, `backend-plugin-cross-platform`, `taskrunner-process-tree` and `codeql`/`security`/`qa` rows that have nothing to do with Cursor. | (a) is the Cursor lane row. (b) is why deleting `scripts/makefile-scope.sh` turns *this whole file* red, not just one subtest: a missing script fails the fixture setup of all **ten** lane subtests in the ~18-27 table. Only the ~24 row drops. |
| `scripts/makefile-scope.sh` (~16-18, ~29-31, ~96-104, ~122-137) | **UPDATE, not delete - this file must survive.** Its `acp` and `backend-plugin` scopes (~29-30) have nothing to do with the Cursor smoke, and they are precisely why the file stays. Only the `cursorsdk` keyword is removed: the scope name at ~18, the case arm and pattern at ~31, and the self-test assertions at ~96-104 and ~122-137. | Deleting it strands the cutover three ways. **1. Two live workflow consumers.** `.github/workflows/acp-process-tree.yml` (~77) and `.github/workflows/backend-plugin-cross-platform.yml` (~104) both shell out to `bash scripts/makefile-scope.sh --relevant "$BASE_SHA" HEAD {acp,backend-plugin}` under `probe_rc=0; ... || probe_rc=$?` followed by `if [[ "$probe_rc" != "1" ]]`. Both are **fail-open on exit 1 only**: a missing script makes bash return 127, which is `!= "1"`, so both native matrices would flip to `relevant=true` and run on *every* Makefile edit - silently burning the ACP process-tree and backend-plugin cross-platform matrices while still reporting success. **2. A fatal QA read.** `internal/qa/ci_iteration_speed_contract_test.go` ~72 reads it through the fatal helper. **3. A per-subtest read.** `internal/qa/main_push_lane_scope_test.go` ~68-69 reads it in every lane subtest. Note the design's File Structure Plan row for `Makefile`, `scripts/makefile-scope.sh` says "retire Cursor targets, source-specific exceptions and enumerations" - enumerations and Cursor-specific entries, never the shared script. |
| `.github/workflows/go-cache-maintenance.yml` (~7) | Retention trigger list names `Cursor SDK Platform Smoke`. | Removing the workflow without this leaves a retention trigger for a workflow that can no longer run. `development_iteration_contract_test.go` cross-checks these names against each workflow's `name:` line, so the two have to land together. |
| `.github/workflows/backend-plugin-release-gates.yml` (~5-6) | A comment enumerating the workflows that hold shared evidence: "...remains in focused cross-platform, process-tree, and Cursor SDK smoke workflows (backend-plugin-cross-platform, ACP process-tree, **Cursor SDK Platform Smoke**)." | **Prose-only, found by pass 1's `Cursor SDK Platform Smoke` pattern, not by the earlier four.** It names the lane's display name in a comment and shares no identifier with the workflow file, so a search for `cursor-sdk-platform` cannot see it. Nothing breaks: it is a comment, not a trigger, and no test reads it. It goes stale the moment the workflow does, and this document's own scope is stale prose. Delete the clause, keep the surviving two workflows named. |
| `scripts/prune-go-caches.py` (~10, ~14) | `cursorsdk` is an alternative in both the `SNAPSHOT` and `LEGACY` retention regexes. | Stale coupling, not a break: `scripts/test_prune_go_caches.py` never names the lane, so the extra alternative is inert. It goes because no workflow produces that cache prefix any more. |
| `docs/remote-ci-performance.md` (~110, ~129) | Prose describing the independent `bridge-node-tests` status and the "Cursor" entry in the gate list. | This is the host runbook for the lane being deleted. Stale prose here is what makes the next operator believe a retired job is still reporting. |
| `docs/cursor-sdk-backend.md` (whole file) | The active in-tree runbook, including the `make test-cursor-sdk-*` invocations (~130-133, ~156-162, ~171, ~179). | Replaced by an external install/migration pointer with no local npm instructions, per the design's File Structure Plan row for `docs/cursor-sdk-backend.md`, `README.md`. It is not deleted. |
| `README.md` (~33, ~120, ~128) | Names `connectors/cursorsdk`, `docs/cursor-sdk-backend.md`, `config/examples/cursor-sdk-experimental.yaml`, and `make test-cursor-sdk-comparison-report`. | Same external-pointer rewrite, same design row. The `cursorcliacp` mentions in this file are a separate product line and are out of scope. |

### Deleted outright

- `.github/workflows/cursor-sdk-platform.yml`. The ordering constraint above applies to it.
- `Makefile` targets `test-cursor-sdk-live`, `test-cursor-sdk-live-bridge`, `test-cursor-sdk-platform`, and
  `test-cursor-sdk-comparison-report`, plus their `.PHONY` (~line 1) and `help` (~lines 86-89) entries.
- `scripts/test-cursor-sdk-*.{sh,ps1}` (all eight files). They now live in this repository under `scripts/`,
  so the cutover deletes host copies that already have an owner - **but not before batch-1**, see the second
  ordering constraint above. `scripts/makefile-scope.sh` is deliberately **not** in this list; see its UPDATE
  row above.
- The `cursorsdk` entry in `.github/actions/go-cache/policy.json` (~42-46), which is bound to
  `job: platform-smoke` and `workflow: Cursor SDK Platform Smoke`. `scripts/ci-go-cache.py` resolves the lane by
  name from the workflow's `lane:` input, so an orphaned entry is inert rather than fatal; it goes with the
  workflow it configures.
- The `/connectors/cursorsdk` directory entry in `.github/dependabot.yml` (~25), which disappears with
  `connectors/cursorsdk/**`.

### Deliberately not in this list

Every remaining hit from the two derivation passes, with the reason it is out of scope. These were checked,
not missed.

- `.kiro/specs/archive/windows-task-reliability/design.md` (~220, ~258-263) records `test-cursor-sdk-platform`
  and the connector's test fixtures in a completed specification. Archived specs are retained historical record
  and are never rewritten. `.kiro/specs/cursor-sdk-standalone/**` is the live spec and owns its own inventory.
- References inside `connectors/cursorsdk/**` disappear with the directory itself, and the host-side inventories
  that enumerate that directory are a different cutover batch from the workflow and script removal. Task 5.2's
  own scope text owns them; this record covers what breaks when the workflow and the relocated scripts go. The
  full set, as swept: `pkg/lipsdk/backendplugin/contracttest/coverage.go`, `scripts/fuzz-targets.tsv`,
  `scripts/check-adhoc-goroutines.{sh,ps1}`, `.golangci.yml`, `config/config.yaml`,
  `config/examples/cursor-sdk-experimental.yaml`, `internal/archtest/cursor_sdk_external_gates_test.go`,
  `internal/archtest/cursorsdk_boundaries_test.go`, `internal/archtest/backend_multi_user_policy_test.go`,
  `internal/archtest/testdata/backend_multi_user_policy/authority/connector_recreates_multi_user_authority.go.txt`,
  `internal/standardplugins/{backend_prefix_inventory,multi_user_backend_policy,standard_bundle_posture}_test.go`,
  and `internal/providerprofiles/{expected_inventory,catalog_population}_test.go`. These are batch-2
  implementation-removal work, named in the design's File Structure Plan row for
  `internal/standardplugins/...`/`internal/providerprofiles/...`.
- Prose that names Cursor SDK as an *example product* rather than as this lane, in
  `docs/backend-plugins/authoring.md` (~15) and `docs/backend-plugins/operator.md` (~140, ~218). Both describe
  the generic `agent_runtime` execution-composition posture and cite "Cursor SDK agents" the way they cite
  `openai-codex`; neither references the workflow, the scripts, the cache lane, or the Makefile targets. They
  stay correct after the cutover. Found only by pass 1's `Cursor SDK` pattern.
- `internal/core/routing/execution_composition_test.go` (~385, ~391, ~395) uses `cursor_sdk` as a synthetic
  route prefix inside generic selector-parsing fixtures. No host reference; found only by the `cursor_sdk`
  pattern.
- `.github/workflows/acp-process-tree.yml` (~134, ~150) has steps named `Cursor connector (Unix)` and
  `Cursor connector (Windows)`, but both set `working-directory: connectors/cursorcliacp` - the separate
  `cursorcliacp` product line, which this cutover leaves untouched. A case-insensitive `Cursor` search hits
  them; they are false positives, not stale references.
- `internal/infra/runtimebundle/candidate_concurrent_lifecycle_test.go` (~17) mentions "cursor SDK" in a
  comment naming a class of candidate compile/rollback test. No host reference.
- `scripts/test-cursor-sdk-*.{sh,ps1}` themselves, and `scripts/makefile-scope.sh` (~96-104, ~122-137) self-test
  fixtures. The scripts are the artifacts being removed; the `makefile-scope.sh` self-test edits are part of its
  UPDATE row, not a separate deletion.