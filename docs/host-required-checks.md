# Host required status checks touching the Cursor SDK lane

Recorded by spec task 3.4 (`cursor-sdk-standalone`) while relocating the plugin's own tooling. This is a
read-only inspection of the Go-LIP host repository; nothing in the host's branch protection, rulesets, or
workflows was changed. Re-run the commands below before acting on this record - required contexts are
external configuration and can change independently of this repository.

Inspected: `matdev83/go-llm-interactive-proxy`, branch `main`, host worktree on `spec/cursor-sdk-standalone`
at the `3.2` merge of this plugin (`c803d4b`).

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

## Also host-side, for the cutover batches

The complete set of **active** host references that break or go stale when
`.github/workflows/cursor-sdk-platform.yml` and the relocated `scripts/test-cursor-sdk-*.{sh,ps1}` leave the host.
None of them is a plugin prerequisite, so none of them gates this repository's own release; every one of them is
the host's problem to land in the task 5.2 batch.

The list was reconciled against the recorded baseline by grepping the host for `cursor-sdk-platform`,
`bridge-node-tests`, the `cursorsdk` go-cache lane, and `test-cursor-sdk-`. Re-run those four greps before acting;
a host-side reference the migration inventory missed is a red `internal/qa` lane or a silently unbound cache lane,
not a documentation nit.

**UPDATE means the file stays and its expectations are rewritten. DELETE means the whole reference goes.** The
distinction is load-bearing in one row: the QA contract tests are updated, never deleted, because each one also
carries a generic host-boundary assertion that must survive.

### Must be updated in the same change

| Host reference | What it references | Why it is not a deletion |
| --- | --- | --- |
| `internal/qa/remote_ci_performance_contract_test.go` (~149-152) | Hard-reads `cursor-sdk-platform.yml` and asserts the `bridge-node-tests` job's `name`, `if`, and `needs`. | **The item that breaks loudest.** `read` fatals on a missing file, so following this document's own ordering turns `go test ./internal/qa` red. Task 5.2 drops the dedicated-lane expectation and keeps the generic assertion it also carries. |
| `internal/qa/ci_iteration_speed_contract_test.go` (~91, ~101) | Names `cursor-sdk-platform.yml` in two workflow lists: the Makefile-relevance probe for expensive matrices, and the bounded go-cache consumer/producer set. | `readRepositoryFile` fatals on a missing file. Its cache-contract assertions are generic and must stay, minus this workflow. |
| `internal/qa/development_iteration_contract_test.go` (~20, ~69) | Names the same workflow in the bounded-cache/`cache: true` set and in the retention-trigger cross-check. | Same hard read; the retention cross-check is what keeps `go-cache-maintenance.yml` honest, so the row below moves with it. |
| `internal/qa/main_push_lane_scope_test.go` (~24) | Lane-table row `{cursor-sdk-platform.yml, changes, filter, cursorsdk, connectors/cursorsdk/example.go, true, true}`. | Same hard read. The generic scoped-lane rows stay. |
| `.github/workflows/go-cache-maintenance.yml` (~7) | Retention trigger list names `Cursor SDK Platform Smoke`. | Removing the workflow without this leaves a retention trigger for a workflow that can no longer run. `development_iteration_contract_test.go` cross-checks these names against each workflow's `name:` line, so the two have to land together. |
| `scripts/prune-go-caches.py` (~10, ~14) | `cursorsdk` is an alternative in both the `SNAPSHOT` and `LEGACY` retention regexes. | Stale coupling, not a break: `scripts/test_prune_go_caches.py` never names the lane, so the extra alternative is inert. It goes because no workflow produces that cache prefix any more. |
| `docs/remote-ci-performance.md` (~110, ~129) | Prose describing the independent `bridge-node-tests` status and the "Cursor" entry in the gate list. | This is the host runbook for the lane being deleted. Stale prose here is what makes the next operator believe a retired job is still reporting. |
| `docs/cursor-sdk-backend.md` (whole file) | The active in-tree runbook, including `make test-cursor-sdk-*` invocations (~130-131, ~156, ~171). | Replaced by an external install/migration pointer with no local npm instructions, per the design's File Structure Plan row for `docs/cursor-sdk-backend.md`, `README.md`. It is not deleted. |
| `README.md` (~33, ~120, ~128) | Names `connectors/cursorsdk`, `docs/cursor-sdk-backend.md`, `config/examples/cursor-sdk-experimental.yaml`, and `make test-cursor-sdk-comparison-report`. | Same external-pointer rewrite, same design row. The `cursorcliacp` mentions in this file are a separate product line and are out of scope. |

### Deleted outright

- `.github/workflows/cursor-sdk-platform.yml`. The ordering constraint above applies to it.
- `Makefile` targets `test-cursor-sdk-live`, `test-cursor-sdk-live-bridge`, `test-cursor-sdk-platform`, and
  `test-cursor-sdk-comparison-report`, plus their `.PHONY` (~line 1) and `help` (~line 88) lines.
- `scripts/makefile-scope.sh`, whose `cursorsdk` scope keyword (~18, ~31) and its self-test fixtures (~94-100,
  ~122-137) exist only to gate the 3-OS smoke.
- `scripts/test-cursor-sdk-*.{sh,ps1}`. All four pairs now live in this repository under `scripts/`, so the
  cutover deletes host copies that already have an owner.
- The `cursorsdk` entry in `.github/actions/go-cache/policy.json` (~42-45), which is bound to
  `job: platform-smoke` and `workflow: Cursor SDK Platform Smoke`. `scripts/ci-go-cache.py` resolves the lane by
  name from the workflow's `lane:` input, so an orphaned entry is inert rather than fatal; it goes with the
  workflow it configures.
- The `/connectors/cursorsdk` directory entry in `.github/dependabot.yml` (~25), which disappears with
  `connectors/cursorsdk/**`.

### Deliberately not in this list

- `.kiro/specs/archive/windows-task-reliability/design.md` (~220) records `test-cursor-sdk-platform` in a
  completed specification. Archived specs are retained historical record and are never rewritten.
- References inside `connectors/cursorsdk/**` disappear with the directory itself, and the host-side inventories
  that enumerate that directory (`pkg/lipsdk/backendplugin/contracttest/coverage.go`,
  `scripts/fuzz-targets.tsv`, `scripts/check-adhoc-goroutines.{sh,ps1}`, `.golangci.yml`,
  `config/config.yaml`, `internal/archtest/cursor_sdk*`) are a different cutover batch from the workflow and
  script removal. Task 5.2's own scope text owns them; this record covers what breaks when the workflow and the
  relocated scripts go.