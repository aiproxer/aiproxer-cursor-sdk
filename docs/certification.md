# Certification record

This is the record task 4.1 of the `cursor-sdk-standalone` specification produces: for
every requirement in scope, the test that proves it and the CI lane that runs it. It is
a record, not a summary of intent — a reader should be able to open the test, open the
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

The CI lanes that run these are `go`, `go-windows`, `go-race`, `bridge-node`,
`go-macos-dev`, and `package`, all defined in `.github/workflows/verify.yml`.

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
- **No host artifact has been certified against, so this plugin is not certified
  against a host.** There is no downloadable Go-LIP host binary release, so
  `release.yaml` declares `host_certification: uncertified` with its reason and
  `compatibility.json` carries no host artifact digest. No digest is invented
  anywhere. Certifying a release is task 4.2 and remains blocked pending host publication.
- **No plugin artifact has been released.** There is no tag and no GitHub release.
- **Per release verification status is a future step.** Every archive records
  `package_verification_state: not-performed`, because the packager writes the record
  before verification can run. Verification is performed afterwards by
  `scripts/verify-package`, and it is run per release rather than continuously; no
  standing claim in this repository says an archive was verified.
- **The macOS lane is not macOS support.** It is development evidence about process
  supervision on that kernel, and it does not appear in any place a platform is
  published.
