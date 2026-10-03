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
3. Delete `.github/workflows/cursor-sdk-platform.yml` in the same PR as step 2, or a later one.

Deleting the workflow first would leave `bridge-node-tests` required and permanently unreportable: with
`strict = true` every subsequent PR would be blocked with no way to satisfy the context. This is the
"never leave PRs blocked on a deleted required job" case in the design's Integration & Migration Notes
step 4, and it is why no required check may be retired before its replacement reports.

## Also host-side, for the cutover batches

These host references disappear with the workflow and the scripts, and none of them are plugin
prerequisites:

- `Makefile` targets `test-cursor-sdk-live`, `test-cursor-sdk-live-bridge`, `test-cursor-sdk-platform`, and
  `test-cursor-sdk-comparison-report`, plus their `.PHONY` and `help` lines.
- `scripts/makefile-scope.sh`, whose `cursorsdk` scope keyword exists to gate the 3-OS smoke.
- `docs/cursor-sdk-backend.md`, the active in-tree Cursor SDK runbook.
- `scripts/test-cursor-sdk-*.{sh,ps1}`. All four pairs now live in this repository under
  `scripts/`, so the cutover deletes host copies that already have an owner.