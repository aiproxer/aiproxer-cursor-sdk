# lip-cursor-sdk-bridge

Plugin-owned Node companion for this repository's `cursorsdk` backend plugin.

Operator documentation: [`docs/installation.md`](../docs/installation.md) in this repository.

## Pins

- `@cursor/sdk` exact `1.3.1`
- `zod` exact `4.6.6` (SDK peer)
- `undici` override `6.29.1` (retained even though absent from the current dependency tree)
- Node `>=22.13` for SDK compatibility; CI and release packaging pin `22.23.5`
- npm `12.2.2` as a build dependency for separate staging into the archive

The SDK's minimum Node version is not a security guarantee. If supplying a runtime rather than using
a release archive, use a currently supported, patched Node release.

The npm audit result covers the installed dependency graph, not code bundled inside the SDK or
the separately packaged npm tree. In particular, the SDK bundles a custom-patched MCP SDK 1.25.1; its security
status is not established by npm audit or the SDK upgrade. See the
[MCP OAuth security guidance](../README.md#mcp-oauth-security). Existing host certification
evidence predates this pin.

## Layout

- `bin/lip-cursor-sdk-bridge.js` — executable entry (`--version`, `doctor`, or NDJSON server)
- `src/protocol.ts` — versioned bounded NDJSON bridge contract (shared fixtures with Go)
- `src/sdkMock.ts` — deterministic SDK mock for default tests
- `src/liveProbe.ts` — opt-in live probe (`CURSOR_SDK_LIVE_PROBE=1` + `CURSOR_API_KEY`)
- `src/liveScenarios.ts` — opt-in live scenarios (`CURSOR_SDK_LIVE=1` + `CURSOR_API_KEY`)

Shared fixtures live in `../internal/product/testdata/fixtures` and are consumed by both Go and TypeScript tests.

## Release smoke (repository root)

```bash
scripts/test-cursor-sdk-live-bridge.sh   # opt-in Go→Node lifecycle (-tags=cursorsdk_live_bridge); BLOCKED exit 0 without flag/key
pwsh -NoProfile -File scripts/test-cursor-sdk-live-bridge.ps1   # Windows equivalent
```

The platform, live SDK scenario, and ACP-versus-SDK comparison smoke tooling lives here
too, under `scripts/test-cursor-sdk-*`. This repository owns its verification and
needs no sibling checkout; retiring the original host-side tooling is a later cutover task. The Go
side of the certification is recorded in [`docs/certification.md`](../docs/certification.md).

## Commands

```bash
npm ci
npm audit --audit-level=high
npm run typecheck
npm test
npm run build
npm pack --dry-run
```

`npm test` auto-discovers all `src/*.test.ts` files via Node's test-runner glob, so new test files are picked up without editing `package.json`.

Safe local checks (no credentials, no agent creation):

```bash
node bin/lip-cursor-sdk-bridge.js --version
node bin/lip-cursor-sdk-bridge.js doctor
```

Live probe (optional, not part of default tests):

```bash
set CURSOR_SDK_LIVE_PROBE=1
set CURSOR_API_KEY=...
npm run live-probe
```

## Installation policy

Go-LIP never runs `npm install`, `npm ci`, or other package lifecycle scripts at config validation, startup, model discovery, or request handling. Operators (or CI) install and build this companion package separately, then point `bridge_executable` at the resulting binary on `PATH` or an explicit path. The package declares no `preinstall` / `install` / `postinstall` product hooks.
