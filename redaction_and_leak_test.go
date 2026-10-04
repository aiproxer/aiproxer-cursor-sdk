package cursorsdk_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// This file holds the two proofs that are about what the plugin does *after* the
// interesting event: it must not hand a secret to the host, and it must not leave a
// goroutine running. Both are easy to assert and easy to assert wrongly, so each states
// what would make it fail.

// TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire proves
// requirement 5.3 for the credential the plugin is actually configured with.
//
// The sweep the other proofs do cannot do this job: `frame.Diagnostic` is empty on a
// healthy run, so asserting the credential is absent from it proves nothing. So the
// fake bridge is made to misbehave the way a real runtime does - it echoes the
// configured credential into a warning, into a run error, and onto its own stderr -
// and the host-facing canonical stream is then read for it.
//
// The assertions are two-sided on purpose. Redaction has to be visible, so the
// redaction marker has to be present in the text that did travel; and the secret has to
// be absent from every string on the frame, not only from the field it came from. A test
// that only checked the first would pass against a connector that redacted the message
// and appended the raw key next to it.
func TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire(t *testing.T) {
	const instanceID = "redacted-diagnostics"
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(secretBearingBridgeScript(t)),
		instanceID, configYAML)
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	stream := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("redacted-diagnostics", certificationModel)),
		closeInputFrame(instanceID))
	// A run that ends in a provider error is a returned error on this ABI, and this
	// script ends in one on purpose. What matters is that it is the run's own failure
	// rather than a transport death: the frames still have to arrive, and the text on
	// them still has to be redacted.
	execErr := session.Execute(stream)
	if execErr != nil {
		var modeErr backendplugin.ModeError
		require.False(t, errors.As(execErr, &modeErr) && modeErr.Code == transportDeathCode,
			"the attempt has to fail as the run's own error, not as a dead transport: %v", execErr)
	}

	out := summarize(stream.frames())
	requireOrderedSingleTerminal(t, out)

	warning := findEventMessage(t, out, backendplugin.EventWarning, "warning")
	errorMessage := findEventMessage(t, out, backendplugin.EventError, "run error")

	for label, text := range map[string]string{"warning": warning, "run error": errorMessage} {
		assert.Contains(t, text, redactionMarker,
			"the %s text the host received has to show that the secret was redacted, not merely that it vanished", label)
		assert.NotContains(t, text, certificationAPIKey,
			"the %s text the host received carried the configured credential", label)
		assert.NotContains(t, text, keyShapedSecret,
			"the %s text the host received carried a key-shaped token", label)
	}

	// Nothing else on the frame may carry the secret either. A redacted message with
	// the raw credential left in an adjacent field would satisfy every assertion above.
	for _, frame := range out.Frames {
		require.NotContains(t, frame.Diagnostic, certificationAPIKey,
			"a frame diagnostic carried the configured credential")
		if frame.Event == nil {
			continue
		}
		require.NotContains(t, frame.Event.Opaque, certificationAPIKey,
			"an event's opaque payload carried the configured credential")
		if frame.Event.Error != nil {
			require.NotContains(t, frame.Event.Error.Message, certificationAPIKey,
				"an event error message carried the configured credential")
		}
	}
}

// TestPublicConformance_CancelThenCloseLeavesNoGoroutines proves requirement 5.1 for
// the plugin's own concurrency.
//
// Cancelling an attempt makes `backendplugin.ForwardExecute` start three goroutines -
// the control reader, the upstream reader, and the cancellation worker - and join all
// three before it returns. Cancelling is also what lets the instance close cleanly
// afterwards, so this one test covers both halves of the bounded-cleanup promise: the
// attempt ends, the instance closes, the bridge subprocess is reaped, and nothing is
// left running.
//
// The defer order is the whole trick and is not incidental. `defer` runs last-registered
// first, so the leak verification is registered before the teardown and therefore runs
// after it: by the time goleak looks, the transport is stopped and the instance is
// closed. Registering them the other way round would report this harness's own
// teardown as the leak it is meant to exclude.
func TestPublicConformance_CancelThenCloseLeavesNoGoroutines(t *testing.T) {
	// Registered first, so it runs last: after the teardown below.
	defer goleak.VerifyNone(t, append([]goleak.Option{goleak.IgnoreCurrent()}, ignoreGRPCInternals...)...)

	const instanceID = "leak-cancel"
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(cancellingBridgeScript(t)),
		instanceID, configYAML)
	// Registered second, so it runs first.
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	stream := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("leak-cancel", certificationModel)),
		cancelFrame(instanceID))
	require.NoError(t, session.Execute(stream))

	out := summarize(stream.frames())
	requireOrderedSingleTerminal(t, out)
	require.Equal(t, backendplugin.TerminalCancelled, out.Terminals[0].Status)
}

// TestPublicConformance_ShutdownClosesTheInstanceIdempotentlyAndLeavesNoGoroutines is
// the shutdown half.
//
// Closing is what releases the connector's owned resources: the session pool disposes
// its agents, the bridge process is killed and reaped, and the bridge's stdout, stderr,
// and wait goroutines end with it. The corpus requires a first and a second close to
// both succeed; this requires the second one to be a no-op rather than a second teardown,
// and requires none of that work to survive the test.
func TestPublicConformance_ShutdownClosesTheInstanceIdempotentlyAndLeavesNoGoroutines(t *testing.T) {
	// Registered first, so it runs last: after the teardown below.
	defer goleak.VerifyNone(t, append([]goleak.Option{goleak.IgnoreCurrent()}, ignoreGRPCInternals...)...)

	const instanceID = "leak-shutdown"
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(cancellingBridgeScript(t)),
		instanceID, configYAML)
	// Registered second, so it runs first. The teardown closes again, and that has to
	// be harmless: an idempotent close is part of what is being certified.
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	stream := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("leak-shutdown", certificationModel)),
		cancelFrame(instanceID))
	require.NoError(t, session.Execute(stream))
	requireOrderedSingleTerminal(t, summarize(stream.frames()))

	closeCtx, closeCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer closeCancel()
	require.NoError(t, session.Close(closeCtx), "the first shutdown has to succeed")
	require.NoError(t, session.Close(closeCtx), "a second shutdown has to be a no-op, not a second teardown")

	// A closed session refuses further work rather than serving from a torn-down
	// instance, which is what makes the second close safe to assert on.
	err := session.Execute(newCertStream(streamCtx, startFrame(instanceID,
		certificationInvocation("leak-shutdown-after", certificationModel)), closeInputFrame(instanceID)))
	require.Error(t, err, "a closed session must not start another attempt")
}

// TestPublicConformance_BoundedShutdownEndsAnInFlightAttemptWithoutHanging covers the
// path the two leak checks above deliberately cannot.
//
// The released session waits for an in-flight attempt to drain and, when its own
// deadline expires first, cancels that attempt and releases the transport instead of
// waiting forever. Exercising that path means the attempt never ends on its own, so
// there is no clean instance left for the harness to close afterwards: the released
// adapter deliberately abandons the configured instance for a later retrying Close,
// and its bridge subprocess legitimately outlives this test. A whole-process leak check
// here would therefore report that documented behaviour as a defect, which is why this
// test asserts the two things that are actually true - the shutdown reports its own
// deadline instead of pretending it succeeded, and the attempt it interrupted ends.
func TestPublicConformance_BoundedShutdownEndsAnInFlightAttemptWithoutHanging(t *testing.T) {
	const instanceID = "bounded-shutdown"
	configYAML := certificationConfigYAML(t, fakebridge.BuildExe(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	session, _, teardown := mountPublicHostSession(t, ctx, certificationService(cancellingBridgeScript(t)),
		instanceID, configYAML)
	defer teardown()

	streamCtx, streamCancel := context.WithTimeout(ctx, certificationStreamTimeout)
	defer streamCancel()

	// Start a held run and leave it in flight: the whole point is that shutdown, not
	// cancellation, is what ends it.
	stream := newCertStream(streamCtx,
		startFrame(instanceID, certificationInvocation("bounded-shutdown", certificationModel)))
	executed := make(chan error, 1)
	go func() { executed <- session.Execute(stream) }()

	require.Eventually(t, func() bool {
		return len(stream.frames()) > 0
	}, certificationStreamTimeout, 10*time.Millisecond,
		"the attempt never reached the plugin, so shutdown would have nothing in flight to stop")

	closeCtx, closeCancel := context.WithTimeout(ctx, shutdownDeadline)
	defer closeCancel()
	require.ErrorIs(t, session.Close(closeCtx), context.DeadlineExceeded,
		"a shutdown that runs out its own deadline while work is held has to report the deadline, "+
			"not pretend it closed cleanly")

	select {
	case err := <-executed:
		require.Error(t, err,
			"an attempt the shutdown had to cancel ends in a cancellation, never in success")
		var modeErr backendplugin.ModeError
		require.False(t, errors.As(err, &modeErr) && modeErr.Code == transportDeathCode,
			"the cancelled attempt must not be reported as a dead transport: %v", err)
	case <-time.After(certificationStreamTimeout):
		t.Fatal("the in-flight attempt never returned; shutdown left it running")
	}
}

// shutdownDeadline is the bound a bounded shutdown is given here. It is short on
// purpose: the run under test never finishes on its own, so any value works, and a short
// one keeps the proof cheap.
const shutdownDeadline = 250 * time.Millisecond

// transportDeathCode is the released host adapter's code for a plugin that stopped
// responding without a terminal. It is the failure this suite must not confuse with a
// provider run that ended in an error or a cancellation.
const transportDeathCode = "transport_death"

// redactionMarker is the connector's own replacement text for a secret it recognised.
const redactionMarker = "[REDACTED]"

// keyShapedSecret is a token the connector cannot recognise by value because it is not
// the configured credential. It matches the connector's key-shaped patterns, so it is
// redacted by pattern rather than by knowledge - which is exactly why it is worth
// putting on the wire next to the credential itself.
const keyShapedSecret = "crsr_abcdef0123456789"

// ignoreGRPCInternals are the gRPC transport goroutines that are allowed to outlive a
// closed ClientConn briefly. Everything else must be gone.
//
// They are named rather than ignored wholesale on purpose: `goleak.IgnoreCurrent()`
// alone would be enough if the harness were perfect, and being explicit here means a new
// leak in the plugin's own forwarding still fails, while a gRPC version bump that
// reshuffles its internals fails loudly in this list instead of silently masking
// something.
var ignoreGRPCInternals = []goleak.Option{
	goleak.IgnoreTopFunction("google.golang.org/grpc.(*ClientConn).applyServiceConfigAndBalancer"),
	goleak.IgnoreTopFunction("google.golang.org/grpc.(*ClientConn).resetTransport"),
	goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransport"),
	goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
	goleak.IgnoreTopFunction("google.golang.org/grpc/internal/idle.Enqueue"),
}

// findEventMessage returns the message the host received on the first event of kind,
// failing the test when the event never arrived.
//
// The existence check matters as much as the redaction check: a connector that dropped
// the warning entirely would produce no secret to find, and a redaction assertion alone
// would be satisfied by silence.
func findEventMessage(tb testing.TB, out execOutcome, kind backendplugin.EventKind, label string) string {
	tb.Helper()

	for _, frame := range out.Frames {
		if frame.Kind != backendplugin.ServerFrameEvent || frame.Event == nil || frame.Event.Kind != kind {
			continue
		}
		switch kind {
		case backendplugin.EventWarning:
			if frame.Event.Warning != nil {
				return *frame.Event.Warning
			}
		case backendplugin.EventError:
			if frame.Event.Error != nil {
				return frame.Event.Error.Message
			}
		}
	}
	tb.Fatalf("no %s (%s) reached the host, so there is nothing to have redacted", label, kind)
	return ""
}
