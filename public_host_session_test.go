package cursorsdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/protocol"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/service"
	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/host"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// This file is the plugin's own public conformance seam.
//
// The plugin is certified against the contracts a host actually links, so the tests
// in this package deliberately import nothing from the host's internals and copy no
// generated ABI file: they use `pkg/lipsdk/backendplugin`, its `host` session, and
// `contracttest`, all resolved from the released module the plugin pins in go.mod.
// A certification that reached past those packages would pass on a host seam this
// plugin can never ship against, so the seam is the point rather than a convenience.
//
// Everything a certified run needs lives here: a scriptable fake bridge starter, the
// in-memory transport, the mount that puts the real service behind the real host
// adapter, and the frame assertions the Execute contract is stated in.

const (
	// certificationAPIKey is the static credential the certified instance is
	// configured with. It is a fixed non-secret string: a certification must never read
	// a real credential, and it has to be able to assert that the credential never
	// reaches anything an operator can read.
	certificationAPIKey = "certification-not-a-real-credential"

	// certificationModel is the model the fake bridge advertises and the certified
	// invocations select. The shared contract corpus uses its own model id, so the
	// script below advertises that id too; see contractBridgeScript.
	certificationModel = "gpt-5.3-codex"

	// certificationContractModel is the model id the shared contract corpus sends.
	// The corpus is owned by the host and is not negotiable, so the fake bridge
	// advertises this id rather than the certified instance being rewritten to suit
	// the test.
	certificationContractModel = "contract-model"

	// certificationStreamTimeout bounds one certified Execute. It is generous
	// because a Windows runner builds and launches a subprocess per test, but it is
	// far below `go test`'s own timeout so a stuck stream reports a real failure
	// instead of wedging the package.
	certificationStreamTimeout = 90 * time.Second

	// contractCorpusInstanceID is the instance id the released corpus runner stamps
	// on the client frames it builds. Configure has to use the same id, because the
	// plugin routes an Execute frame to the instance that frame names.
	contractCorpusInstanceID = "contract"
)

// contractBridgeScript is the fake-bridge script the shared corpus runs against.
//
// Only the advertised model list differs from fakebridge.DefaultScript: every
// positive corpus scenario sends `contract-model`, and an unaccepted model is a
// legitimate rejection rather than a stream, so the certified run would measure the
// model's absence instead of the contract. The run behaviour itself is the default
// one - respond with a run id, emit one text delta, then finish - which is the same
// sequence the in-tree connector tests already certify.
func contractBridgeScript(tb testing.TB) string {
	tb.Helper()

	script := fakebridge.DefaultScript()
	script.Models = json.RawMessage(`[` +
		`{"id":"` + certificationContractModel + `","displayName":"Contract Model",` +
		`"parameters":[{"id":"reasoning","values":["low","medium","high","xhigh"]}]},` +
		`{"id":"` + certificationModel + `","displayName":"Certified Model",` +
		`"parameters":[{"id":"reasoning","values":["low","medium","high","xhigh"]}]}` +
		`]`)
	return marshalBridgeScript(tb, script)
}

// cancellingBridgeScript is the fake-bridge script for the cancellation proof.
//
// The first send registers the run as held and emits nothing else, so the run cannot
// finish before the host's cancel frame is observed. Without that, a cancellation
// assertion would be a race the fast default path can win, and the suite would report
// a cancelled run it never caused.
func cancellingBridgeScript(tb testing.TB) string {
	tb.Helper()

	script := fakebridge.DefaultScript()
	script.Models = json.RawMessage(`[{"id":"` + certificationModel +
		`","displayName":"Certified Model","parameters":[{"id":"reasoning","values":["low","medium","high","xhigh"]}]}]`)
	script.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-certification"}`)},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-certification"},
	}}
	return marshalBridgeScript(tb, script)
}

// secretBearingBridgeScript is the fake-bridge script for the redaction proof.
//
// It makes the bridge misbehave the way a real provider runtime does when it echoes a
// credential back into its own diagnostics: a warning and a run error whose text
// carries the exact credential this instance was configured with, a key-shaped token
// that the connector cannot know the shape of, and a stderr line with the same
// secret. Without something this hostile on the wire, a redaction assertion proves
// nothing - there is simply no secret in the output to have leaked.
//
// The credential is the configured one, not a key-shaped string, because the connector
// redacts the configured credential by value and key-shaped tokens by pattern. Only the
// first rule is what this test is about, so only the first is load-bearing.
//
// The run error carries an ABI error code rather than a provider-specific one. The
// connector forwards the bridge's code verbatim, and the ABI's error-code set is closed,
// so a code outside it cannot be encoded and the frame never reaches the host at all.
// That is a property of the boundary, not of redaction, so this script stays inside the
// code set and leaves the message - the part being redacted - fully under test.
func secretBearingBridgeScript(tb testing.TB) string {
	tb.Helper()

	script := fakebridge.DefaultScript()
	script.Models = json.RawMessage(`[{"id":"` + certificationModel +
		`","displayName":"Certified Model","parameters":[{"id":"reasoning","values":["low","medium","high","xhigh"]}]}]`)
	script.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionStderr, Text: "bridge boot rejected key " + certificationAPIKey},
		{
			Type:   fakebridge.ActionRespond,
			Result: json.RawMessage(`{"runId":"run-redaction"}`),
		},
		{
			Type:    fakebridge.ActionEvent,
			RunID:   "run-redaction",
			Seq:     1,
			Kind:    protocol.KindWarning,
			Payload: json.RawMessage(`{"message":"upstream rejected key ` + certificationAPIKey + ` (also crsr_abcdef0123456789)"}`),
		},
		{
			Type:    fakebridge.ActionEvent,
			RunID:   "run-redaction",
			Seq:     2,
			Kind:    protocol.KindError,
			Payload: json.RawMessage(`{"message":"run failed with ` + certificationAPIKey + `","code":"provider_terminal"}`),
		},
	}}
	return marshalBridgeScript(tb, script)
}

func marshalBridgeScript(tb testing.TB, script fakebridge.Script) string {
	tb.Helper()

	raw, err := json.Marshal(script)
	require.NoError(tb, err)
	return string(raw)
}

// scriptStarter is the injected ProcessStarter that runs the deterministic fake
// bridge instead of the packaged private runtime.
//
// It is a test double for one thing only: which bridge executable the certified
// instance starts, and with which script. It deliberately does not stand in for the
// connector's own process supervision - the real bridge_process, session pool, run
// stream, and cancel path all run unmodified above it - so a lifecycle or redaction
// regression still fails this suite.
type scriptStarter struct {
	inner product.ProcessStarter
	extra []string
}

// Start launches the injected bridge. The connector normally forwards a host
// environment it was given; nothing in the plugin process hands one to the connector,
// so the child would inherit this test process's entire environment. Narrow it to the
// platform minimum the connector is itself allowed to forward, then add the fake
// bridge's script, so the certified child starts on every runner with nothing from the
// test machine leaking in.
func (s scriptStarter) Start(cmd []string, _ string, env []string) (product.Process, error) {
	merged := append([]string(nil), env...)
	if len(merged) == 0 {
		merged = product.SelectHostEnv(os.Environ(), product.PlatformMinimumEnvNames())
	}
	merged = append(merged, s.extra...)
	inner := s.inner
	if inner == nil {
		inner = product.OSProcessStarter{}
	}
	return inner.Start(cmd, "", merged)
}

// certificationService builds the real plugin service over the fake bridge.
func certificationService(scriptJSON string) *service.Service {
	return &service.Service{
		Starter: scriptStarter{
			inner: product.OSProcessStarter{},
			extra: []string{"FAKE_BRIDGE_SCRIPT=" + scriptJSON},
		},
	}
}

// certificationConfigYAML is the opaque plugin configuration a host would supply.
func certificationConfigYAML(tb testing.TB, bridgeExecutable string) []byte {
	tb.Helper()

	return []byte(strings.Join([]string{
		"bridge_executable: " + strconv.Quote(bridgeExecutable),
		"default_workspace: " + strconv.Quote(tb.TempDir()),
		"sandbox_mode: \"off\"",
		"max_agents: 4",
		"max_concurrent_runs: 2",
		"bridge_start_timeout_seconds: 30",
		"cancel_timeout_seconds: 10",
		"shutdown_timeout_seconds: 15",
	}, "\n") + "\n")
}

func certificationSecrets() backendplugin.SecretBundle {
	return backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte(certificationAPIKey)}}
}

func certificationPolicy() backendplugin.RuntimePolicy {
	return backendplugin.RuntimePolicy{
		DisableTransportRetries: true,
		MaxPendingEvents:        32,
		CancelDeadlineMS:        10_000,
	}
}

// mountPublicHostSession puts svc behind the released public host adapter over an
// in-memory listener and returns the host-facing session every certification
// operation runs through, plus the teardown that releases it.
//
// Teardown is returned rather than registered as a t.Cleanup on purpose. A goroutine
// leak check has to observe the transport already gone, and t.Cleanup callbacks run
// after the test body's deferred functions - so a registered teardown would still be
// live when `defer goleak.VerifyNone(...)` fires and would be reported as the leak it
// is meant to rule out. Callers own the order.
func mountPublicHostSession(
	tb testing.TB,
	ctx context.Context,
	svc backendplugin.Service,
	instanceID string,
	configYAML []byte,
) (*host.Session, backendplugin.ResolvedProfile, func()) {
	tb.Helper()

	conn, stopTransport := mountPublicPluginTransport(tb, svc)

	session, profile, err := host.DialConfiguredSession(
		ctx, conn, instanceID, service.FactoryKind, configYAML, certificationSecrets(), certificationPolicy(),
	)
	if err != nil {
		stopTransport()
		require.NoError(tb, err, "the released host adapter refused to mount the plugin service")
	}

	var once sync.Once
	teardown := func() {
		once.Do(func() {
			// Close the configured instance before the transport goes away: the point
			// of the check is that plugin work ends, not that the pipe is pulled.
			closeCtx, cancel := context.WithTimeout(context.Background(), certificationStreamTimeout)
			defer cancel()
			_ = session.Close(closeCtx)
			stopTransport()
		})
	}
	return session, profile, teardown
}

// mountPublicPluginClient negotiates and configures the plugin over the released
// public gRPC surface and returns the raw client and its teardown.
//
// It exists for the one assertion the host session cannot make. The host adapter
// classifies a plugin error returned before any terminal as a transport failure,
// which is the right thing for a host to do with an opaque error but which erases the
// plugin's own capability code. The wire still carries it, and the wire is a released
// public contract, so the certification reads the status the plugin actually returns
// instead of inferring it.
func mountPublicPluginClient(
	tb testing.TB,
	ctx context.Context,
	svc backendplugin.Service,
	instanceID string,
	configYAML []byte,
) (backendpluginv1.BackendPluginClient, func()) {
	tb.Helper()

	conn, stopTransport := mountPublicPluginTransport(tb, svc)
	gc, err := grpc.NewClient("passthrough:///backendplugin",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return conn, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(tb, err)

	client := backendpluginv1.NewBackendPluginClient(gc)
	negotiated, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: backendplugin.ProtocolMajorV1, HostMinor: certifiedProtocolMinor(),
		HostFeatures:            certifiedHostFeatures(),
		DisableTransportRetries: true,
	})
	require.NoError(tb, err)
	require.True(tb, negotiated.GetCompatible(),
		"the plugin refused the certified host offer: %s", negotiated.GetRejectReason())
	require.NotEmpty(tb, negotiated.GetNegotiationToken())

	_, err = client.Configure(ctx, &backendpluginv1.ConfigureRequest{
		InstanceId: instanceID, FactoryKind: service.FactoryKind, ConfigYaml: configYAML,
		Secrets:          &backendpluginv1.SecretBundle{Values: map[string][]byte{"api_key": []byte(certificationAPIKey)}},
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 32, CancelDeadlineMs: 10_000},
		NegotiationToken: negotiated.GetNegotiationToken(),
	})
	require.NoError(tb, err)

	var once sync.Once
	return client, func() {
		once.Do(func() {
			_ = gc.Close()
			stopTransport()
		})
	}
}

// mountPublicPluginTransport serves svc behind the released plugin ABI over an
// in-memory listener and returns one connection to it plus the teardown that stops
// the server and closes the listener.
//
// The offer is the one the plugin actually declares. Asserting it here rather than
// copying whatever a test happens to want keeps the certified negotiation honest: if
// Describe widened or narrowed its protocol offer, this mount would stop being
// compatible and the suite would say so instead of quietly testing a narrower ABI.
func mountPublicPluginTransport(tb testing.TB, svc backendplugin.Service) (net.Conn, func()) {
	tb.Helper()

	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(certifiedProtocolOffer(), svc))
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(lis)
	}()

	conn, err := lis.Dial()
	require.NoError(tb, err)

	var once sync.Once
	return conn, func() {
		once.Do(func() {
			server.Stop()
			// Serve returns once Stop has run, so waiting for it is what makes the
			// transport goroutine observably gone rather than merely asked to stop.
			<-served
			_ = lis.Close()
		})
	}
}

func certifiedProtocolOffer() backendplugin.ProtocolOffer {
	return backendplugin.ProtocolOffer{
		Major: backendplugin.ProtocolMajorV1, Minor: certifiedProtocolMinor(), DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
	}
}

func certifiedProtocolMinor() uint32 {
	return backendplugin.ProtocolMinorCancellationHandshake
}

func certifiedHostFeatures() []*backendpluginv1.Feature {
	return []*backendpluginv1.Feature{
		{Name: backendplugin.FeatureAccountingEvidence},
		{Name: backendplugin.FeatureCancellationHandshake},
	}
}

// certStream is the in-memory ExecuteStream the host adapter forwards through. It
// records every plugin frame so a test can assert the ordering and terminal contract
// rather than only that Execute returned.
type certStream struct {
	ctx context.Context

	mu     sync.Mutex
	inbox  []backendplugin.ClientFrame
	pos    int
	outbox []backendplugin.ServerFrame
}

func newCertStream(ctx context.Context, frames ...backendplugin.ClientFrame) *certStream {
	return &certStream{ctx: ctx, inbox: frames}
}

func (s *certStream) Context() context.Context { return s.ctx }

func (s *certStream) Recv() (backendplugin.ClientFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= len(s.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	frame := s.inbox[s.pos]
	s.pos++
	return frame, nil
}

func (s *certStream) Send(frame backendplugin.ServerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbox = append(s.outbox, frame)
	return nil
}

func (s *certStream) frames() []backendplugin.ServerFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]backendplugin.ServerFrame(nil), s.outbox...)
}

// startFrame and closeInputFrame build the ordinary single-attempt client sequence.
func startFrame(instanceID string, inv backendplugin.Invocation) backendplugin.ClientFrame {
	return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart, InstanceID: instanceID, Invocation: &inv}
}

func closeInputFrame(instanceID string) backendplugin.ClientFrame {
	return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCloseInput, InstanceID: instanceID}
}

func cancelFrame(instanceID string) backendplugin.ClientFrame {
	return backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameCancel, InstanceID: instanceID, CancelReason: backendplugin.CancelReasonClient,
	}
}

// certificationInvocation is the canonical request the certified Execute runs.
func certificationInvocation(requestID, modelID string) backendplugin.Invocation {
	text := "certification request"
	return backendplugin.Invocation{
		RequestID: requestID, AttemptID: requestID + "-attempt",
		ALegID: "certification-a", BLegID: "certification-b",
		CanonicalModelID: modelID, NativeModelID: modelID,
		Operation:     string(lipapi.OperationOpenAIResponses),
		DeliveryMode:  string(lipapi.DeliveryModeStreaming),
		TransportMode: string(lipapi.TransportModeStreaming),
		Messages:      []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
		Options:       backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
}

// execOutcome is the shape a certified Execute is judged on.
type execOutcome struct {
	Frames    []backendplugin.ServerFrame
	Text      string
	EventKind []backendplugin.EventKind
	Terminals []backendplugin.Terminal
	Cancels   []backendplugin.CancelOutcome
}

// summarize reduces a frame stream to the contract terms the certification asserts.
func summarize(frames []backendplugin.ServerFrame) execOutcome {
	out := execOutcome{Frames: frames}
	var text strings.Builder
	for _, frame := range frames {
		switch frame.Kind {
		case backendplugin.ServerFrameEvent:
			if frame.Event == nil {
				continue
			}
			out.EventKind = append(out.EventKind, frame.Event.Kind)
			if frame.Event.Kind == backendplugin.EventTextDelta && frame.Event.Delta != nil {
				text.WriteString(*frame.Event.Delta)
			}
		case backendplugin.ServerFrameTerminal:
			if frame.Terminal != nil {
				out.Terminals = append(out.Terminals, *frame.Terminal)
			}
		case backendplugin.ServerFrameCancelOutcome:
			if frame.CancelOutcome != nil {
				out.Cancels = append(out.Cancels, *frame.CancelOutcome)
			}
		}
	}
	out.Text = text.String()
	return out
}

// requireOrderedSingleTerminal asserts the frame-stream contract a certified run has
// to satisfy: strictly increasing sequence numbers, exactly one terminal, and no frame
// after it. Requirement 4.3 is stated in exactly these terms.
func requireOrderedSingleTerminal(tb testing.TB, out execOutcome) {
	tb.Helper()

	require.NotEmpty(tb, out.Frames, "the certified run produced no frames at all")
	var lastSeq uint64
	terminals := 0
	for _, frame := range out.Frames {
		require.NoError(tb, frame.ValidateShape(), "the certified run emitted a malformed frame")
		if frame.Kind != backendplugin.ServerFrameAccepted {
			require.Greater(tb, frame.Sequence, lastSeq,
				"frame sequence went backwards: %d after %d", frame.Sequence, lastSeq)
			lastSeq = frame.Sequence
		}
		if terminals > 0 {
			tb.Fatalf("a frame arrived after the run's terminal: kind=%s sequence=%d", frame.Kind, frame.Sequence)
		}
		if frame.Kind == backendplugin.ServerFrameTerminal {
			terminals++
		}
	}
	require.Equal(tb, 1, terminals, "a certified run has to end in exactly one terminal outcome")
}

// requireNoCredentialLeak keeps the static credential out of everything a certified
// run can be observed through. Requirement 5.3 names this directly.
//
// This is the cheap sweep over every proof in this package; it is not the redaction
// proof. It can only fail if something already emitted the credential, so on its own it
// would pass against a connector that never redacts anything.
// TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire is the proof
// that a secret on the wire comes out redacted.
func requireNoCredentialLeak(tb testing.TB, out execOutcome) {
	tb.Helper()

	for _, frame := range out.Frames {
		require.NotContains(tb, frame.Diagnostic, certificationAPIKey,
			"the plugin diagnostic carried the credential")
	}
}
