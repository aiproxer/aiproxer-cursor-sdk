# Certification record

This is the record tasks 4.1 and 4.2 of the `cursor-sdk-standalone` specification produce:
for every requirement in scope, the test that proves it and the lane that runs it. It is a
record, not a summary of intent — a reader should be able to open the test, open the
lane, and re-derive every claim below.

Nothing here is certified that a machine did not check. Where a claim has no evidence
yet, this document says so instead of describing what the evidence would look like.

## How to reproduce

| What | Command |
| --- | --- |
| Full Go suite (Linux and Windows) | `GOWORK=off go test ./...` |
| Fake-bridge lifecycle smoke | `GOWORK=off go test -count=1 -timeout 20m -run '^TestPlatformSmoke_' ./internal/product` (`TestPlatformSmoke_FakeBridgeLane`) |
| Race detector (Linux) | `GOWORK=off go test -race ./cmd/lip-cursor-sdk-bridge/... ./internal/product/... .` |
| Bridge unit tests | `cd bridge-node && npm ci && npm test` |
| Bridge typecheck | `cd bridge-node && npm run typecheck` |
| Packaged archive, per platform | `LIP_PACKAGE_GATE=1 GOWORK=off go test -count=1 -timeout 40m -run TestPackageArchive -v .` |
| Real-host certification (opt-in) | see [Requirement 3.1, 3.2, 3.3, 3.4, 4.5, 6.1](#requirement-3123333445-61--real-host-install-trust-and-optional-activation) |

The CI lanes that run these are `go`, `go-windows`, `go-race`, `bridge-node`,
`go-macos-dev`, and `package`, all defined in `.github/workflows/verify.yml`.

## Requirement 3.1, 3.2, 3.3, 3.4, 4.5, 6.1 — real host install, trust, and optional activation

Everything above certifies the plugin against this repository's own released host
*contracts*. This section certifies it against a real, downloadable, versioned host
*binary*, which is the artifact an operator actually has. That boundary is where a host can
disagree with this repository's assumptions, so it is measured rather than reasoned about.

Measured against **`github.com/matdev83/go-llm-interactive-proxy` v0.1.0**, on **both**
platforms this project declares, by
`TestHostReleaseCertification_RealHostInstallTrustAndOptionalActivation`
(`host_release_certification_test.go`):

| Platform | Host binary digest | Plugin archive | Result |
| --- | --- | --- | --- |
| `windows/amd64` | `6a6f7462d94bd3c5fae16e799e10ec236bfc116c86baaca19544dc3fed6b20c4` | assembled natively, Windows | 13/13 pass, packaged default **unreachable** |
| `linux/amd64` | `ce52b7e3f02c12ce00f67eac6b0ee8a038af17d0f62b15ee8e11e34d130b7d3c` | assembled natively, linux/amd64, official Node 22.22.3 distribution | 13/13 pass, packaged default **reachable** |

The linux archive was assembled with `scripts/package-plugin.sh --node-dist
<node-v22.22.3-linux-x64>`, so its `compatibility.json` records
`node_source_kind: official-distribution` rather than a PATH fallback. Both install trees
were provisioned with the shipped runtime's own bundled npm and resolved the
operator-provided **`@cursor/sdk 1.0.23`**; both launchers report
`doctor: ok bridge=0.1.0 sdk=1.0.23`.

The gate is **opt-in** and never runs on a push, because it needs a host binary this
repository neither builds nor ships. A green default run is therefore not host
certification, and the suite that keeps the prose honest says so mechanically (see
[Requirement 6.2](#requirement-62--the-certification-itself)).

```text
LIP_HOST_CERT_GATE=1
LIP_HOST_CERT_HOST_BINARY=/path/to/lipstd[.exe]
LIP_HOST_CERT_HOST_SHA256=<sha256 of that binary>
LIP_HOST_CERT_PLUGIN_ROOT=/path/to/cursorsdk-<version>-<os>-<arch>
LIP_HOST_CERT_PLUGIN_ARCHIVE=/path/to/cursorsdk-<version>-<os>-<arch>[.zip|.tar.gz]  # optional
LIP_HOST_CERT_PLUGIN_ARCHIVE_SHA256=<sha256 of that archive>                          # required with it
GOWORK=off go test -count=1 -timeout 40m -run TestHostReleaseCertification -v .
```

One host-side precondition applies to running this on Linux: the released host refuses to
compose its request plane as an administrative user (`stdhttp: refusing to start as
administrative user on linux`), so the gate has to run as an unprivileged user. That is a
host policy about serving, not a plugin finding.

The gate refuses to run without the binary *and* its expected digest, re-checks that digest
when it finishes, verifies the install tree against the archive's own shipped
`checksums.sha256` before it starts and again at the end, and runs the host with a minimal
environment so nothing from the operator's machine leaks into the measurement. No Cursor
credential is forwarded and no provider is contacted: the deterministic bridge stands in for
the Cursor SDK, and the two cases that need the real shipped runtime are the two that end in
an explicit prerequisite failure.

| Requirement | What was verified | Case |
| --- | --- | --- |
| 3.1 | A plugin in a configured discovery path is **discovered**, not builtin, with the plugin id, kind, and `ok` reason the shipped manifest declares; the report is cross-checked against the artifact's own bytes. | `installed_plugin_is_trusted_and_discovered_with_manifest_identity` |
| 3.1 | The closed trust boundary is the one the host composes from: `static` credentials, `local_only`, `per_instance`, `agent_runtime`. | same |
| 3.2 | An installed but unconfigured plugin is reported as `discovered` with `activation_required: false`, and nothing is launched — proven differentially against an enabled control that does launch and does fail. | `inactive_install_is_reported_without_starting_the_runtime` |
| 3.3 | The instance activates over the approved secure channel: `doctor` reports `active`, `launched: true`, and that the channel was peer-authenticated. | `secure_negotiation_activates_the_configured_instance` |
| 3.3 | The host's own inventory lists the configured `cursorsdk` instance, so a route can select it. | `inventory_lists_the_configured_plugin_instance` |
| 3.4 | A canonical request through the host's `openai-responses` frontend returns a completed canonical response with content, and the host's attempt log shows one attempt opened on the plugin's own candidate key. | `canonical_execution_serves_a_canonical_response_over_http` |
| 4.5 | A capability the resolved profile does not serve is refused by name — HTTP 400 `missing required capabilities: tools` — with no provider content, and the same instance serves the plain request in the same run. | `a_capability_the_plugin_does_not_serve_is_refused_explicitly` |
| 6.1 | A local-only agent runtime is denied composition under `access.mode: multi_user`, before it starts listening, while the same artifact composes in single-user mode. | `multi_user_access_denies_the_local_only_agent_runtime` |
| 3.4 | A configured instance whose plugin artifact is absent fails closed (`enabled kind unresolved`), starts nothing, and provisions nothing. | `a_missing_plugin_artifact_fails_closed_without_automatic_installation` |
| 3.4 | An installed tree with the private runtime removed fails as an explicit prerequisite naming the packaged location and `reinstall`. | `a_missing_private_runtime_is_an_explicit_prerequisite` |
| 3.4 | A freshly installed, unprovisioned tree fails as an explicit prerequisite naming `@cursor/sdk`, the pinned version, and the exact per-platform `npm ci --omit=dev` invocation. | `an_unprovisioned_cursor_sdk_is_an_explicit_prerequisite` |

The last two cases ran the **real shipped launcher, the real shipped private Node runtime,
and the real operator-provisioned Cursor SDK 1.0.23** — the trees they used were unpacked
release archives with `node_modules/@cursor/sdk` provisioned by each archive's own bundled
npm. That is the whole private-runtime packaging decision exercised through a real host on
both platforms: the runtime starts, the SDK resolves at the pinned version, and a missing
prerequisite is reported rather than worked around.

### What the measurement found: the packaged default is platform-dependent

The archive's headline convenience is that an operator who configures nothing gets the
packaged private companion, resolved as `../private/bridge/lip-cursor-sdk-bridge[.exe]`
relative to the installed outer executable. **Measured, that works on `linux/amd64` and does
not work on `windows/amd64`** with host v0.1.0. The gate decides it from the host's own log
(`the_packaged_default_bridge_resolution_is_what_the_operator_gets`) and prints the verdict:

```text
MEASURED packaged default companion resolution on linux/amd64:   REACHABLE
MEASURED packaged default companion resolution on windows/amd64: UNREACHABLE
```

**linux/amd64 — reachable.** The host binds the verified executable by descriptor and execs
it, so `os.Executable()` inside the plugin is the install-root path. The default companion
started the packaged launcher, the packaged Node runtime loaded the provisioned SDK, and the
chain reached Cursor's own credential check. Every other case on that platform also passes
with an explicit `bridge_executable`.

**windows/amd64 — unreachable.** The host verifies the outer executable's digest, copies it
into a private digest-addressed staging directory, and launches *those staged bytes*. The
plugin then resolves its companion relative to that staging copy, which has no `private/`
tree beside it. Measured diagnostic:

```text
cursorsdk: private bridge launcher "...\Temp\private\bridge\lip-cursor-sdk-bridge.exe" not found
(expected ../private/bridge/lip-cursor-sdk-bridge.exe next to the installed plugin executable;
reinstall the Cursor plugin package or set bridge_executable to a direct bridge binary)
```

The failure itself is the correct behaviour — explicit, naming the packaged location, no
installation, no fallback to another Cursor integration — and the remedy it names is the
remedy every other measured Windows case uses: an explicit `bridge_executable` pointing at
the packaged launcher in the install tree. That is what
[`docs/installation.md`](installation.md#running-under-a-go-lip-host) documents.

This is therefore a **blocker on the packaged-default claim on Windows, not on the plugin
and not on Linux**. Two things are deliberately not done here:

- **The host is not patched.** Fixing it means either handing a discovered plugin its
  install tree or teaching plugins to locate it, both of which are the host's decisions and
  would change a trust boundary this repository does not own.
- **The posture is not flipped.** `release.yaml` stays `host_certification: uncertified` and
  records the measured reason; `compatibility.json` carries no host artifact digest.
  Certifying would require flipping it to `certified` and passing `-tested-host`, and
  certifying a release whose packaged default is broken on one of the two declared
  platforms would publish a claim the measurement contradicts.

Whether the packaged default must work on Windows before this release can ship, or whether
an explicit `bridge_executable` is an acceptable operator step there, is a maintainer
decision about the host contract. What this record establishes is that the behaviour is
platform-dependent, measured on both declared platforms, and has a tested workaround.

## Requirement 4.1 — provider and stream semantics survive extraction

Model discovery, configuration validation, text and reasoning events, tool activity,
warnings, and terminal usage are the behaviour the pre-extraction contract evidence
represented, and the relocated connector still produces them through the released host
contracts.

What the public-ABI proofs below actually observe is narrower than that sentence, and
this record does not widen it. The shared corpus drives the connector's own adapter, and
the fake bridge those proofs run against emits a text delta, a warning, and a run error.
Reasoning, usage counters, tool-call activity, prompt-cache observations, and the model
catalog semantics are proved where they live: in the relocated connector suite that
moved with the source. Both halves are listed, so a reader can tell which proof covers
which part of the requirement.

Public-ABI proofs (the released host adapter, the real service, the fake bridge):

- `TestPublicConformance_SharedContractSuiteRunsOverTheReleasedHostAdapter` runs the
  host's own shared contract corpus (`pkg/lipsdk/backendplugin/contracttest`) against the
  real plugin service mounted behind the released public host adapter.
  **26 scenarios: 10 executed, 16 hard negatives.** The 10 are `text-baseline`,
  `text-streaming`, `usage-present`, `usage-zero`, `reasoning-output`,
  `recoverable-error`, `terminal-error`, `cancellation`, `lifecycle-close`, and
  `close-idempotent`; each has to produce validated frames and a terminal. The 16 are
  every scenario that requires a capability, item dialect, reasoning dialect, compaction
  dialect, or extension type this connector does not advertise, and each is refused by
  the host *before* Execute — no attempt is made and no upstream work is started.
- The partition is a function of one measured thing: the resolved profile advertises
  exactly `streaming` and `reasoning`. That is why 16 scenarios are negatives, and the
  same test asserts the capability list, the 26/10/16 split, and the artifact's own
  negative list together, so the published numbers cannot drift from a real run.
- `TestPublicConformance_ExecuteDeliversOrderedEventsAndOneTerminal` asserts the
  canonical start/text/finished evidence crosses the boundary in order, that the run ends
  in exactly one successful terminal, and that a second attempt on the same session
  behaves identically. **This is the text path only.**
- `TestPublicConformance_CertifiesTheProtocolOfferThePluginActuallyDeclares` pins the
  protocol major, minor, feature offer, negotiated feature set, and resolved capability
  summary the corpus ran against.
- `TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin` repeats the execution
  proof from a real installed plugin tree, through the real outer executable, over its
  own listener.
- `TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire` proves the
  warning and run-error paths on the canonical wire; see Requirement 5.3.

Relocated connector proofs, for the semantics the public proofs above do not exercise:

- Reasoning: `internal/product/event_mapper_test.go` and
  `internal/product/reasoning_profile_test.go` — reasoning delta mapping, and the exact
  effort vocabulary per model profile (no aliases).
- Usage and terminal counters: `internal/product/event_mapper_test.go` and
  `internal/product/normalize_canonical_test.go` — usage mapping including zero counters
  and cache fields, and the canonical event envelope every mapped event must satisfy.
- Tool activity and warnings: `internal/product/event_mapper_test.go` — tool-call start,
  argument delta, and finish mapping, plus warning mapping and its bounded, sanitized
  message.
- Stream ordering and the terminal contract at the canonical layer:
  `internal/product/stream_test.go` and `internal/product/run_sub_test.go`.
- Model discovery and the catalog: `internal/product/inventory_test.go`,
  `internal/product/classify_test.go`, `internal/product/model_index.go`, and
  `internal/product/reasoning_profile_test.go`.

### Baseline fixtures, and which language consumes each

| Fixture | Consumed by |
| --- | --- |
| `internal/product/testdata/fixtures/sdk_contract.json` | Go (`internal/product/protocol/protocol_test.go`) and TypeScript (`bridge-node/src/protocol.test.ts`) |
| `internal/product/testdata/fixtures/models_sanitized.json` | Go (`inventory_test.go`, `reasoning_profile_test.go`, `protocol/protocol_test.go`, `coexist/modelregistry_coexistence_test.go`) and TypeScript (`models.test.ts`, `protocol.test.ts`, `sdkMock.test.ts`, `server.test.ts`) |
| `internal/product/testdata/fixtures/protocol/methods.json` | Go (`protocol/protocol_test.go`) and TypeScript (`protocol.test.ts`) |
| `internal/product/testdata/fixtures/protocol/valid_frames.ndjson` | Go (`protocol/protocol_test.go`, `protocol/params_test.go`) and TypeScript (`protocol.test.ts`) |
| `internal/product/testdata/fixtures/protocol/invalid_frames.json` | Go (`protocol/protocol_test.go`) and TypeScript (`protocol.test.ts`) |
| `internal/product/testdata/fixtures/protocol/event_sequences.json` | Go (`protocol/protocol_test.go`) and TypeScript (`protocol.test.ts`) |
| `internal/product/testdata/fixtures/sdk_setting_sources_1.0.23.txt` | **Go only** (`internal/product/config_test.go`) — it pins the SDK setting-source names the Go configuration accepts; the JavaScript side never parses it |
| `internal/product/testdata/fixtures/models_sdk_variants_missing_id.json` | **TypeScript only** (`bridge-node/src/models.test.ts`) — it pins how the JavaScript bridge rejects a model row with no id; the Go normalizer has its own equivalent assertions in `internal/product/inventory_test.go` and does not read this file |

Two of the eight fixtures are single-language. The rest are shared, and both suites read
the same bytes, so a protocol or model-format change cannot be accommodated on one side
only.

## Requirement 4.2 — a capability the build cannot serve is refused explicitly

The resolved profile advertises streaming and reasoning and nothing else, so the host
refuses tools, vision, documents, and structured output before it ever calls Execute.
The remaining half is that a request which carries such a requirement anyway is refused
by name rather than quietly narrowed.

- `TestPublicConformance_RequiredCapabilityIsRefusedExplicitly` reads the status the
  plugin returns on the released wire and requires the connector's own
  `cursor_sdk_capability_unsupported` code in it. The run emits no provider frames.
- `TestPublicConformance_RequiredCapabilityIsNeverSilentlyNarrowed` asserts the same
  refusal through the released host session: Execute fails, with no terminal and no
  content.
- `TestPublicConformance_CertifiesTheProtocolOfferThePluginActuallyDeclares` asserts the
  corpus partition follows from the advertised capabilities, so widening the claim would
  move scenarios and fail rather than shrink the test silently.

## Requirement 4.3 — ordered events and a single terminal outcome

- `requireOrderedSingleTerminal` is the shared assertion: strictly increasing sequence
  numbers for every frame but `accepted`, exactly one terminal, and no frame after it.
  It is applied by `TestPublicConformance_ExecuteDeliversOrderedEventsAndOneTerminal`,
  `TestPublicConformance_CancellationEndsTheRunWithoutLaterContent`,
  `TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire`, and both
  `TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin` cases.
- The shared corpus enforces the same rule independently, for the **10 of its 26
  scenarios that execute**. The other 16 are never executed at all, so no terminal is
  expected of them; their evidence is that the host refused them before Execute.

## Requirement 4.4 — session reuse, isolation, and configured resource limits

- `TestPublicConformance_ExecuteDeliversOrderedEventsAndOneTerminal` runs a second
  attempt on the same configured session and requires the same outcome, so one attempt's
  terminal and generation cannot leak into the next.
- `TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin` requires the instance to
  still serve its inventory after a completed run and after a cancelled one.
- The connector-level limits stay covered where they were relocated:
  `internal/product/session_pool_test.go`, `pending_queue_test.go`,
  `concurrency_leak_test.go`, `parallel_race_history_test.go`, `generation_isolation_test.go`,
  and `internal/product/comparison/`.

## Requirement 4.5 — no provider-local replay after downstream content

No retry or failover path was added, and none was removed. The connector has exactly one
attempt per Execute: `internal/service.Execute` opens one upstream stream through
`backendplugin.ForwardExecute` and has no loop. `TestOpen_NoCommitOnPrepareFailure`
(`internal/product/open_test.go`) pins that a rejected attempt leaves nothing behind.

## Requirement 5.1 — bounded cancellation and shutdown

- `TestPublicConformance_CancellationEndsTheRunWithoutLaterContent` holds the run open
  in the fake bridge so only the host's cancel frame can end it, then requires exactly
  one acknowledged cancel outcome, exactly one cancelled terminal, and no content.
- `TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin` repeats that from an
  installed tree, so the bounded cleanup path is proven where an operator meets it.
- `TestPublicConformance_CancelThenCloseLeavesNoGoroutines` cancels an attempt and then
  closes the instance, and verifies with `goleak` that nothing survives: the forwarding
  goroutines `backendplugin.ForwardExecute` starts on a negotiated cancel are joined,
  and the bridge subprocess is reaped.
- `TestPublicConformance_ShutdownClosesTheInstanceIdempotentlyAndLeavesNoGoroutines`
  requires a first and a second close to both succeed, requires the closed session to
  refuse further work rather than serve from a torn-down instance, and verifies with
  `goleak` that shutdown left nothing running.
- `TestPublicConformance_BoundedShutdownEndsAnInFlightAttemptWithoutHanging` covers the
  deadline-bound shutdown: work still held when `Close` arrives makes the released
  session report `context.DeadlineExceeded` rather than pretend it closed, and the
  interrupted attempt ends instead of hanging. It deliberately does **not** run a
  whole-process leak check, because the released adapter abandons the configured
  instance for a later retrying `Close` in that path. The retry is owed by the transport
  owner; this harness has no replacement transport, and its teardown cannot retry over
  the closed session. The bridge subprocess and its `readStdout`, `readStderr`, and
  `waitProc` goroutines therefore outlive this test. This path proves the deadline bound,
  not resource reclamation.
- The configured deadlines stay covered by `internal/product/cancel_timeout_kill_test.go`,
  `close_join_test.go`, `run_stream_close_cancel_test.go`, `reap.go`, and the shipped
  `cancel_timeout_seconds` / `shutdown_timeout_seconds` configuration.

## Requirement 5.2 — generation fencing

- `internal/product/generation_isolation_test.go` proves a stale run, stream close, or
  cancel from a replaced runtime generation cannot affect a newer one, across reused run
  ids.
- `TestPublicConformance_CancellationEndsTheRunWithoutLaterContent` requires the
  instance to keep serving inventory after a cancellation, which is the observable half
  of a released run.

## Requirement 5.3 — credential redaction and bounded diagnostics

- `TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire` is the
  proof, and it is built to be falsifiable rather than decorative. The fake bridge is
  made to misbehave the way a real runtime does: it echoes the credential this instance
  was configured with into a warning, into a run error, and onto its own stderr. The
  test then reads the host-facing canonical stream and requires, for both the warning and
  the run error, that the redaction marker is **present** — so it cannot be satisfied by
  silence — that the configured credential is **absent**, that a key-shaped token the
  connector cannot know by value is **absent**, and that no other field on any frame
  carries either secret.
  Removing the by-value replacement from `sanitizeBridgeDiag` makes this fail, which is
  how the assertion was checked rather than assumed.
- The stderr line in that script exercises the connector's retained-stderr sanitiser in
  the same run, but stderr is not carried on the canonical wire, so its assertions stay
  where the output is: `internal/product/bridge_sanitize.go` and
  `internal/product/event_mapper_apikey_sanitize_test.go`.
- `requireNoCredentialLeak` sweeps every public-ABI proof in this record for the
  credential in a frame diagnostic. It is a cheap net, not the proof: `frame.Diagnostic`
  is empty on a healthy run, so on its own it would pass against a connector that never
  redacts anything.
- `internal/product/bridge_sanitize.go` and its tests keep the retained stderr bounded,
  control-character stripped, path-redacted, and free of prompt- and tool-shaped text;
  `internal/product/open_test.go`'s argv/env checks keep the credential out of process
  arguments and the forwarded environment, which is the other half of requirement 5.3.

## Requirement 5.4 — health, readiness, and runtime version without a host branch

Readiness, runtime version, and failure information are still the connector's own
diagnostics over the same released ABI, so the host needs no Cursor-specific branch.

- `TestPublicConformance_SharedContractSuiteRunsOverTheReleasedHostAdapter` negotiates,
  configures, and resolves through the released adapter only.
- `internal/product/diag_ops_test.go`, `diag_ops.go`, and `platform_smoke_run_test.go`
  cover the readiness and runtime-version reporting; `internal/product/platform_native.go`
  keeps the SDK-version and runtime probe.

## Requirement 5.5 — sandboxing is refused when unavailable

- `internal/product/security_policy_test.go` proves a required sandbox with unverified
  support is refused with `cursor_sdk_capability_unsupported` before any agent is
  created, including when the bridge omits the sandbox field entirely.
- The certification configuration used here sets `sandbox_mode: "off"`, so no proof in
  this record depends on sandbox support being present on the runner.

## Requirement 6.2 — the certification itself

This document, plus:

- `TestCertificationEvidence_DocumentNamesEveryCertifiedRequirementAndItsProof` keeps
  this file a record: it fails if a requirement in scope, a named proof, the published
  corpus partition, or one of the non-claims below disappears.
- `TestCertificationEvidence_LiveScenariosStayOutOfDefaultVerification` keeps live
  provider work out of default verification.
- `TestCertificationEvidence_NativeWindowsLaneRunsTheFullGoAndPlatformSuites` keeps the
  Windows platform evidence real rather than a packaging subset.
- `TestCertificationEvidence_MacOSLaneIsDevelopmentEvidenceOnly` keeps the macOS lane
  honest about what it may claim.
- `TestStandaloneModule_HasNoReplaceDirective` and
  `TestStandaloneModule_HasNoWorkspaceAboveIt` keep the certified build resolving only
  published host contracts. `go mod tidy -diff` does not enforce either: it accepts a
  `replace` directive and resolves a workspace without mentioning it.

The real-host gate is opt-in and therefore does not run on a push, which means the prose
around it could drift away from it with nothing failing. `host_certification_record_test.go`
closes that gap: it fails if the declared certification posture stops naming the measured
result, if this document stops naming the gate and the environment it needs, if the measured
incompatibility stops being described together with its remedy, if the installation guide
stops printing the per-platform remedy path derived from the archive layout, or if any of
the non-claims below disappears.

## Known boundary constraints

These are properties of the released boundary that this certification works within. They
are recorded so a future reader does not mistake them for something the connector decides.

- **A provider error code outside the ABI's closed set cannot cross the wire.** The
  connector forwards the bridge's error code verbatim (`internal/product/event_mapper.go`),
  and `backendplugin.PluginError.Code` only encodes the closed ABI set. A bridge that
  reports a provider-specific code therefore produces a frame the conversion rejects, and
  the attempt ends without a terminal — the host sees a transport failure, not the error.
  Nothing in this task changes that; it is called out because
  `TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire` has to stay
  inside the code set to observe redaction at all, and because an operator debugging a
  provider error would otherwise see a transport death with no explanation. Mapping
  unknown codes onto the ABI set belongs to a separate, production change.

## Platform evidence

| Platform | Lane | What it proves | Production claim |
| --- | --- | --- | --- |
| linux/amd64 | `go`, `go-race`, `package` | full suite, race detector, assembled archive | declared in the manifest template and `internal/packagelayout` |
| windows/amd64 | `go-windows`, `package` | full suite, fake-bridge lifecycle, assembled archive | declared in the manifest template and `internal/packagelayout` |
| darwin | `go-macos-dev` | fake-bridge lifecycle only | **no claim**: no assembled macOS archive, no macOS entry in the `package` matrix, no darwin entry in the manifest |

`TestManifestScope_TemplateDeclaresExactlyThePlatformsThePipelineAssembles` is what keeps
the third row from becoming a declaration by accident.

## What this record does not claim

- **No live provider evidence is in the default verification, and there never will be.**
  Live scenarios are opt-in through `CURSOR_SDK_LIVE=1` together with a
  `CURSOR_API_KEY` credential, and they run only through
  `scripts/test-cursor-sdk-live.sh`, `scripts/test-cursor-sdk-live-bridge.sh`, and their
  PowerShell equivalents, plus `npm run live-probe` and `npm run live-scenarios`. Each of
  those reports `BLOCKED` and exits successfully when it is not opted in, so a blocked run
  can never be mistaken for a green one. They spend real Cursor quota against a real
  credential and are run deliberately, never on a push.
- **This plugin is not certified against a host, and that is a measured result rather
  than a missing measurement.** A downloadable host release exists and it was measured on
  both platforms this project declares:
  `github.com/matdev83/go-llm-interactive-proxy` v0.1.0 on windows/amd64 and
  linux/amd64. Everything in
  [the section above](#requirement-3123333445-61--real-host-install-trust-and-optional-activation)
  passed on both except the packaged-default companion resolution, which the host's private
  staging makes unreachable on Windows. `release.yaml` therefore declares
  `host_certification: uncertified` with that measured reason, `compatibility.json` carries
  no host artifact digest, and no digest is invented anywhere.
- **No Linux host evidence is missing, and none is claimed beyond what was run.** The
  linux/amd64 run used the published linux `lipstd` from the same release and a
  natively assembled linux archive. It covered the same thirteen cases. It did **not** run
  with `-race`, and it ran as an unprivileged user because the released host refuses to
  compose its request plane as an administrative user on Linux.
- **The real-host runs are not live provider runs.** No Cursor credential is used and no
  provider quota is spent. The deterministic bridge stands in for the Cursor SDK, and the
  cases that exercise the real shipped runtime end in an explicit prerequisite failure or
  at Cursor's own credential rejection. Live provider behaviour remains the opt-in
  `CURSOR_SDK_LIVE=1` lanes described above.
- **The real-host gate is not part of default verification.** It needs a host binary this
  repository does not build, so it runs only when an operator opts in with a binary and its
  expected digest. A green `go test ./...` is not host certification.
- **No plugin artifact has been released.** There is no tag and no GitHub release.
- **Per release verification status is a future step.** Every archive records
  `package_verification_state: not-performed`, because the packager writes the record
  before verification can run. Verification is performed afterwards by
  `scripts/verify-package`, and it is run per release rather than continuously; no
  standing claim in this repository says an archive was verified.
- **The macOS lane is not macOS support.** It is development evidence about process
  supervision on that kernel, and it does not appear in any place a platform is
  published.
