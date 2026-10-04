package cursorsdk_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/service"
	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/contracttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
)

// This file certifies the plugin against the host's released conformance contract.
//
// `contracttest.Run` is the host's own shared corpus runner: it owns the scenario
// list, drives Describe/Negotiate/Configure/Resolve/Execute/Cancel/Close, and fails
// on a malformed frame, a non-monotonic sequence, a second terminal, a frame after a
// terminal, or a cancellation that was never observed. Running it here rather than
// reimplementing those checks is what makes this plugin's evidence the same evidence
// every other certified connector produces.
//
// The service under it is the real one - the real product layer, the real session
// pool, the real run stream, the real bridge process - with only the SDK bridge
// executable replaced by the deterministic fake, because a certification run must
// never spend a Cursor quota or need a credential.

// capabilityUnsupportedCode is the plugin's own capability-refusal code, asserted as
// it appears on the wire. It is the connector's stable operator-facing answer to "this
// request needs something this build does not have", so a certification that let it
// change shape without noticing would stop proving requirement 4.2.
const capabilityUnsupportedCode = "cursor_sdk_capability_unsupported"

// TestPublicConformance_SharedContractSuiteRunsOverTheReleasedHostAdapter runs the
// shared contract corpus against the plugin through the released public host adapter
// and fails on any drift from the behaviour the corpus states.
func TestPublicConformance_SharedContractSuiteRunsOverTheReleasedHostAdapter(t *testing.T) {
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	script := contractBridgeScript(t)

	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID,
		Version:     certificationVersion(t),
		FactoryKind: service.FactoryKind,
		ConfigYAML:  configYAML,
		Secrets:     certificationSecrets(),
		Timeout:     10 * time.Minute,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return certificationService(script), nil, nil
		},
		StartHost: func(ctx context.Context, svc backendplugin.Service) (contracttest.HostSession, func(), error) {
			// The corpus runner stamps its own instance id on every client frame and
			// routes the frames back to the instance that id names, so the certified
			// session is configured under that id rather than one of this package's.
			//
			// The teardown is handed to the corpus runner rather than deferred here:
			// `Run` owns the whole certification lifetime, so it has to own the
			// release too, and it runs its own Close before this one.
			session, _, teardown := mountPublicHostSession(t, ctx, svc, contractCorpusInstanceID, configYAML)
			return session, teardown, nil
		},
	})

	// The runner validates the artifact internally before returning it; re-asserting
	// it here records that the plugin's own suite consumed a complete artifact rather
	// than a partially populated one.
	require.NoError(t, result.Validate())

	assert.Equal(t, service.PluginID, result.PluginID)
	assert.True(t, result.Negotiated.Compatible, "the certified negotiation has to be compatible")
	assert.Empty(t, result.Failures, "the shared corpus reported failures")
	_, err := result.MarshalJSON()
	assert.NoError(t, err, "the certification artifact has to be serialisable as released evidence")

	// The corpus partition, measured rather than remembered. The runner decides which
	// scenarios are positive from the resolved capability summary, so the numbers here
	// are the concrete statement of what this connector does and does not serve - and
	// they are the numbers docs/certification.md publishes. If the host adds or removes
	// a scenario, this fails and the record has to be re-read rather than left stale.
	require.Len(t, result.ScenarioResults, len(contract.BaselineScenarioCorpus()))
	positive, negative := 0, 0
	for _, scenario := range result.ScenarioResults {
		if scenario.Positive {
			positive++
			continue
		}
		negative++
	}
	assert.Equal(t, corpusTotalScenarios, len(result.ScenarioResults),
		"the shared corpus changed size; docs/certification.md states its partition and has to be updated")
	assert.Equal(t, corpusExecutedScenarios, positive,
		"the number of scenarios this connector actually executes changed; the published partition is stale")
	assert.Equal(t, corpusRejectedScenarios, negative,
		"the number of scenarios this connector refuses changed; the published partition is stale")
	assert.Len(t, result.Negative, corpusRejectedScenarios,
		"the artifact's own negative list has to agree with the measured partition")
	assert.Equal(t, []lipapi.Capability{lipapi.CapabilityStreaming, lipapi.CapabilityReasoning},
		result.Capabilities,
		"the capabilities the corpus partition was derived from; the published partition assumes these two")
}

// The published partition of the shared contract corpus for this connector, restated
// as constants so the record and the measurement cannot drift apart silently. All of
// them are functions of one thing: the connector's resolved profile advertises exactly
// streaming and reasoning, so every scenario requiring anything else is a hard negative
// that the host refuses before Execute.
const (
	corpusTotalScenarios    = 26
	corpusExecutedScenarios = 10
	corpusRejectedScenarios = 16
)

// TestPublicConformance_CertifiesTheProtocolOfferThePluginActuallyDeclares pins the
// ABI this certification ran against.
//
// The corpus decides which scenarios are positive from the resolved capability
// summary, so a plugin that quietly stopped advertising reasoning - or started
// advertising tools it does not implement - would move scenarios between the positive
// and negative columns and still produce a passing artifact. Asserting the offer, the
// negotiation, and the resulting positive/negative partition means a widened or
// narrowed claim is a failure rather than a silently smaller test.
func TestPublicConformance_CertifiesTheProtocolOfferThePluginActuallyDeclares(t *testing.T) {
	t.Parallel()

	descriptor, err := service.New().Describe(t.Context())
	require.NoError(t, err)

	assert.Equal(t, backendplugin.ProtocolMajorV1, descriptor.ProtocolMajor)
	assert.Equal(t, backendplugin.ProtocolMinorCancellationHandshake, descriptor.ProtocolMinor,
		"the certified plugin minor changed; the ABI the corpus was run against has to be re-read")
	assert.Equal(t, []string{
		backendplugin.FeatureAccountingEvidence,
		backendplugin.FeatureCancellationHandshake,
	}, featureNames(descriptor.Features),
		"the certified protocol feature offer changed; re-read the ABI before trusting this suite")
	require.Len(t, descriptor.Factories, 1)
	assert.Equal(t, service.FactoryKind, descriptor.Factories[0].Kind)

	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	session, profile, teardown := mountPublicHostSession(t, ctx, certificationService(contractBridgeScript(t)),
		"offer-corpus", configYAML)
	defer teardown()

	assert.Equal(t, []string{
		backendplugin.FeatureAccountingEvidence,
		backendplugin.FeatureCancellationHandshake,
	}, session.Negotiation().EnabledFeatures,
		"the negotiated feature set is the intersection of the host offer and the plugin offer")
	assert.Equal(t, backendplugin.ProtocolMinorCancellationHandshake, session.Negotiation().NegotiatedMinor)

	assert.Equal(t, []lipapi.Capability{lipapi.CapabilityStreaming, lipapi.CapabilityReasoning},
		capabilityNames(profile.Capabilities),
		"the certified capability offer changed; the corpus partition below is derived from it")

	// Every scenario whose requirements this plugin does not meet has to be a hard
	// negative: it must be rejected by the host before Execute, which is the shape
	// requirement 4.2 asks for - an explicit capability refusal rather than a silently
	// narrowed request.
	require.NotEmpty(t, contract.BaselineScenarioCorpus())
	for _, scenario := range contract.BaselineScenarioCorpus() {
		applicable := scenarioMeetsProfile(scenario, profile)
		if applicable {
			continue
		}
		for _, required := range scenario.Requires.Capabilities {
			assert.NotContains(t, capabilityNames(profile.Capabilities), required,
				"scenario %q requires %s, which the plugin now claims; re-read the corpus partition",
				scenario.ID, required)
		}
	}
}

// TestPublicConformance_RequiredCapabilityIsRefusedExplicitly proves requirement 4.2
// on the wire the host sees.
//
// The resolved profile already refuses tools, so this covers the half the profile
// cannot: a request that nevertheless carries a tool. Two things have to hold. The
// plugin has to name the capability problem in the status it returns - a generic
// failure would leave a host with nothing to report to an operator. And the run has
// to end there: no terminal, no provider content, no narrowed request quietly
// streamed without the tool the caller required.
func TestPublicConformance_RequiredCapabilityIsRefusedExplicitly(t *testing.T) {
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	const instanceID = "capability-refusal"
	client, teardown := mountPublicPluginClient(t, ctx, certificationService(contractBridgeScript(t)), instanceID, configYAML)
	defer teardown()

	executeCtx, executeCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer executeCancel()

	stream, err := client.Execute(executeCtx)
	require.NoError(t, err)

	inv := certificationInvocation("capability-required", certificationModel)
	inv.Tools = []backendplugin.ToolDef{{
		Name:           "lookup",
		Description:    "a tool this connector does not implement",
		ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`)),
	}}
	require.NoError(t, stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind:       backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START,
		InstanceId: instanceID,
		Invocation: protoInvocation(t, inv),
	}))

	var providerFrames int
	for {
		frame, recvErr := stream.Recv()
		if recvErr != nil {
			require.Error(t, recvErr, "a refused request has to come back as an explicit failure")
			assert.Contains(t, status.Convert(recvErr).Message(), capabilityUnsupportedCode,
				"the refusal has to name the capability problem, not an opaque transport failure")
			break
		}
		// Accepting the attempt is not provider work, and it is the only frame a
		// refusal may produce: anything past it would be the plugin streaming a
		// request it just refused to serve.
		require.Equal(t, backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED, frame.GetKind(),
			"a refused request must not emit provider frames")
		providerFrames++
	}
	assert.LessOrEqual(t, providerFrames, 1, "a refused attempt is admitted at most once")
}

// TestPublicConformance_RequiredCapabilityIsNeverSilentlyNarrowed is the host
// adapter's half of requirement 4.2.
//
// The released session classifies any plugin error returned before a terminal as a
// transport failure, so it cannot be the place that reads the capability code, and an
// error on its own proves nothing: a plugin that died mid-attempt produces the same
// error. Two things make this a refusal rather than a death. The attempt was admitted -
// the plugin sent `accepted` and only then refused - and the session is still serving
// immediately afterwards, which a crashed or wedged attempt would not be.
func TestPublicConformance_RequiredCapabilityIsNeverSilentlyNarrowed(t *testing.T) {
	const instanceID = "capability-refusal-session"
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(contractBridgeScript(t)),
		instanceID, configYAML)
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	inv := certificationInvocation("capability-required-session", certificationModel)
	inv.Tools = []backendplugin.ToolDef{{
		Name:           "lookup",
		Description:    "a tool this connector does not implement",
		ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`)),
	}}
	stream := newCertStream(streamCtx,
		startFrame(instanceID, inv),
		closeInputFrame(instanceID))

	require.Error(t, session.Execute(stream), "a request requiring an unimplemented capability has to fail")

	out := summarize(stream.frames())
	require.Len(t, out.Frames, 1, "the plugin has to admit the attempt and then refuse it, nothing else")
	assert.Equal(t, backendplugin.ServerFrameAccepted, out.Frames[0].Kind,
		"a refusal that arrives without an admission is a failure, not a capability answer")
	assert.Empty(t, out.Terminals, "a refused request must not also report a terminal outcome")
	assert.Empty(t, out.Text, "a refused request must not emit provider content")
	requireNoCredentialLeak(t, out)

	// The instance is still serving, so the plugin refused this request rather than
	// ending the attempt or the instance with it.
	models, err := session.ListModels(streamCtx, 4)
	require.NoError(t, err)
	assert.NotEmpty(t, models.Models, "the instance stopped serving inventory after refusing a request")
}

// TestPublicConformance_ExecuteDeliversOrderedEventsAndOneTerminal proves
// requirements 4.1 and 4.3 on a real run: model discovery already happened during
// Configure, and now the canonical text, usage, and terminal evidence crosses the
// public boundary in order and exactly once.
func TestPublicConformance_ExecuteDeliversOrderedEventsAndOneTerminal(t *testing.T) {
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	const instanceID = "ordered-execute"
	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(contractBridgeScript(t)),
		instanceID, configYAML)
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	stream := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("ordered-execute", certificationModel)),
		closeInputFrame(instanceID))
	require.NoError(t, session.Execute(stream))

	out := summarize(stream.frames())
	requireOrderedSingleTerminal(t, out)
	require.Equal(t, backendplugin.TerminalSuccess, out.Terminals[0].Status)
	assert.Contains(t, out.EventKind, backendplugin.EventResponseStarted)
	assert.Contains(t, out.EventKind, backendplugin.EventTextDelta)
	assert.Contains(t, out.EventKind, backendplugin.EventResponseFinished)
	assert.NotEmpty(t, out.Text, "the certified run produced no canonical content")
	requireNoCredentialLeak(t, out)

	// A second, independent attempt on the same session has to behave the same way:
	// session reuse cannot leave the first run's terminal or its generation behind.
	second := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("ordered-execute-2", certificationModel)),
		closeInputFrame(instanceID))
	require.NoError(t, session.Execute(second))
	secondOut := summarize(second.frames())
	requireOrderedSingleTerminal(t, secondOut)
	assert.NotEmpty(t, secondOut.Text)
}

// TestPublicConformance_CancellationEndsTheRunWithoutLaterContent proves requirements
// 4.3 and 5.1 on the wire.
//
// The fake bridge holds the run open, so the cancelled terminal cannot be the tail of
// a run that had already finished: the host's cancel frame is the only thing that can
// end it. That is what makes "no content after the cancelled terminal" a real
// assertion rather than a race the fast path usually wins.
func TestPublicConformance_CancellationEndsTheRunWithoutLaterContent(t *testing.T) {
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	const instanceID = "cancelled-execute"
	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(cancellingBridgeScript(t)),
		instanceID, configYAML)
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	stream := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("cancelled-execute", certificationModel)),
		cancelFrame(instanceID))

	require.NoError(t, session.Execute(stream))

	out := summarize(stream.frames())
	requireOrderedSingleTerminal(t, out)
	require.Equal(t, backendplugin.TerminalCancelled, out.Terminals[0].Status,
		"a held run ended by the host's cancel has to report the cancelled terminal")
	require.Len(t, out.Cancels, 1, "a negotiated cancellation has to publish exactly one outcome")
	assert.True(t, out.Cancels[0].Acknowledged, "the plugin did not acknowledge the host's cancel")
	assert.Empty(t, out.Text, "a cancelled run must not deliver provider content")
	requireNoCredentialLeak(t, out)

	// The session stays usable after a cancelled attempt: a cancelled run releases its
	// resources rather than poisoning the instance.
	models, err := session.ListModels(streamCtx, 4)
	require.NoError(t, err)
	assert.NotEmpty(t, models.Models, "the instance stopped serving inventory after a cancelled run")
}

// certificationVersion reads the version this build declares, so the certification
// artifact identifies the plugin the way Describe does instead of a second literal
// that could drift from it.
func certificationVersion(tb testing.TB) string {
	tb.Helper()

	descriptor, err := service.New().Describe(tb.Context())
	require.NoError(tb, err)
	require.NotEmpty(tb, descriptor.Version)
	return descriptor.Version
}

// protoInvocation projects a public invocation onto the released wire DTO through the
// same converter the host uses, so the certification cannot send a shape the host
// would never have produced.
func protoInvocation(tb testing.TB, inv backendplugin.Invocation) *backendpluginv1.Invocation {
	tb.Helper()

	wire, err := backendplugin.InvocationToProto(inv)
	require.NoError(tb, err)
	return wire
}

func featureNames(features []backendplugin.Feature) []string {
	out := make([]string, 0, len(features))
	for _, f := range features {
		out = append(out, f.Name)
	}
	return out
}

func capabilityNames(summary backendplugin.CapabilitySummary) []lipapi.Capability {
	var out []lipapi.Capability
	if summary.Streaming {
		out = append(out, lipapi.CapabilityStreaming)
	}
	if summary.Tools {
		out = append(out, lipapi.CapabilityTools)
	}
	if summary.Vision {
		out = append(out, lipapi.CapabilityVision)
	}
	if summary.Documents {
		out = append(out, lipapi.CapabilityDocuments)
	}
	if summary.StructuredOutputs {
		out = append(out, lipapi.CapabilityStructuredOutputs)
	}
	if summary.Reasoning {
		out = append(out, lipapi.CapabilityReasoning)
	}
	return out
}

// scenarioMeetsProfile mirrors the corpus runner's own applicability rule for
// capability requirements, so the partition asserted here is the partition the runner
// used rather than a second opinion about it.
func scenarioMeetsProfile(scenario contract.ScenarioDescriptor, profile backendplugin.ResolvedProfile) bool {
	offered := capabilityNames(profile.Capabilities)
	for _, required := range scenario.Requires.Capabilities {
		if !slices.Contains(offered, required) {
			return false
		}
	}
	return len(scenario.Requires.ItemDialects) == 0 &&
		len(scenario.Requires.ReasoningDialects) == 0 &&
		len(scenario.Requires.CompactionDialects) == 0 &&
		len(scenario.Requires.ExtensionTypes) == 0
}
