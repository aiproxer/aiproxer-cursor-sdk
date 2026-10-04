package cursorsdk_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file certifies that a packaged plugin layout can actually serve provider work,
// not only that it can be configured and asked for its model list.
//
// The installed-layout suite above proves the layout contract: the plugin resolves its
// own private companion, honours an explicit override, and fails explicitly when the
// companion or the provisioned SDK is missing. All of that stops at model discovery.
// An installed tree that discovers models and then cannot execute a request, or cannot
// stop one, is broken in the only way an operator would actually notice, so the two
// proofs below drive a real Execute and a real cancellation through the real outer
// plugin executable over its own loopback listener.

// installedExecuteStream is the released public client-stream type of the plugin ABI.
type installedExecuteStream = backendpluginv1.BackendPlugin_ExecuteClient

// TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin proves requirements 4.3
// and 5.1 against the installed artifact rather than an in-process service.
func TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("installed-layout execution builds and runs the plugin executable")
	}

	t.Run("default_installed_layout_serves_ordered_events_with_one_terminal", func(t *testing.T) {
		layout := newInstalledLayout(t, true)
		decoys := newBridgeDecoys(t, false)
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		instanceID := "installed-execute"
		require.NoError(t, configureInstance(t, pluginProc, negotiateToken(t, pluginProc), instanceID,
			instanceConfigYAML("", t.TempDir())))

		frames, err := installedExecute(t, pluginProc, instanceID,
			certificationInvocation("installed-execute", certificationModel),
			func(stream installedExecuteStream) { sendInstalledCloseInput(t, stream, instanceID) })
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())

		out := summarize(frames)
		requireOrderedSingleTerminal(t, out)
		require.Equal(t, backendplugin.TerminalSuccess, out.Terminals[0].Status)
		assert.Contains(t, out.EventKind, backendplugin.EventTextDelta)
		assert.NotEmpty(t, out.Text, "the installed layout served no canonical content")
		requireNoCredentialLeak(t, out)

		// The instance is still serving after the run, so the run released its
		// resources instead of ending the instance with them.
		models, err := pluginProc.listModels(t, instanceID)
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())
		require.NotEmpty(t, models.GetModels())
	})

	t.Run("default_installed_layout_cancels_a_held_run_as_cancelled", func(t *testing.T) {
		// The fake bridge holds the run open until the host cancels it. Without the
		// hold, the default bridge finishes first and a cancelled terminal could be
		// reported for a run that had already ended - the assertion would be a race
		// rather than proof that the plugin stopped the work.
		layout := newInstalledLayout(t, true)
		decoys := newBridgeDecoys(t, false)
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, installedEnvWithScript(t, decoys.pathDir))

		instanceID := "installed-cancel"
		require.NoError(t, configureInstance(t, pluginProc, negotiateToken(t, pluginProc), instanceID,
			installedConfigYAMLWithScriptEnv(t, "")))

		frames, err := installedExecute(t, pluginProc, instanceID,
			certificationInvocation("installed-cancel", certificationModel),
			func(stream installedExecuteStream) { sendInstalledCancel(t, stream, instanceID) })
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())

		out := summarize(frames)
		requireOrderedSingleTerminal(t, out)
		require.Equal(t, backendplugin.TerminalCancelled, out.Terminals[0].Status,
			"a held run the host cancelled has to end as cancelled")
		require.Len(t, out.Cancels, 1, "a cancelled attempt has to publish exactly one cancel outcome")
		assert.True(t, out.Cancels[0].Acknowledged, "the plugin did not acknowledge the host's cancel")
		assert.Empty(t, out.Text, "a cancelled run must not deliver provider content")
		requireNoCredentialLeak(t, out)
	})
}

// installedExecute drives one Execute attempt over the installed plugin's own gRPC
// client and returns the public frames it produced.
//
// The client is the released wire contract rather than the host adapter on purpose:
// this proof is about the plugin executable serving work from an installed tree, and
// the host adapter's own handling of the same stream is certified separately. Reading
// the wire directly also keeps a plugin-side failure legible instead of collapsing it
// into a transport verdict.
func installedExecute(
	t *testing.T,
	proc *installedPlugin,
	instanceID string,
	inv backendplugin.Invocation,
	afterStart func(stream installedExecuteStream),
) ([]backendplugin.ServerFrame, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), installedRPCDeadline)
	defer cancel()

	stream, err := proc.client.Execute(ctx)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind:       backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START,
		InstanceId: instanceID,
		Invocation: protoInvocation(t, inv),
	}); err != nil {
		return nil, err
	}
	afterStart(stream)

	var frames []backendplugin.ServerFrame
	for {
		wire, recvErr := stream.Recv()
		if recvErr != nil {
			return frames, recvErr
		}
		frame, convErr := backendplugin.ServerFrameFromProto(wire)
		if convErr != nil {
			return frames, convErr
		}
		frames = append(frames, frame)
		if frame.Kind == backendplugin.ServerFrameTerminal {
			return frames, nil
		}
	}
}

func sendInstalledCloseInput(t *testing.T, stream installedExecuteStream, instanceID string) {
	t.Helper()

	require.NoError(t, stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind:       backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CLOSE_INPUT,
		InstanceId: instanceID,
	}), "the host could not close the attempt's input")
	require.NoError(t, stream.CloseSend(), "the host could not half-close the attempt's input")
}

func sendInstalledCancel(t *testing.T, stream installedExecuteStream, instanceID string) {
	t.Helper()

	require.NoError(t, stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind:         backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CANCEL,
		InstanceId:   instanceID,
		CancelReason: backendpluginv1.CancelReason_CANCEL_REASON_CLIENT,
	}), "the host could not cancel the attempt")
	require.NoError(t, stream.CloseSend(), "the host could not half-close the attempt's input")
}

// installedEnvWithScript gives the installed plugin process an environment carrying
// the fake bridge's script.
//
// The connector forwards only the environment names it is allowed to, so the script
// has to be named in `bridge_env_allowlist` as well as present here. That is the
// operator-facing mechanism the connector already offers, so this proof needs no
// test-only production seam.
func installedEnvWithScript(tb testing.TB, pathDir string) []string {
	tb.Helper()

	raw, err := json.Marshal(installedHeldRunScript())
	if err != nil {
		tb.Fatal(err)
	}
	return append(childEnvWithPathOnly(pathDir), "FAKE_BRIDGE_SCRIPT="+string(raw))
}

// installedConfigYAMLWithScriptEnv is the installed instance configuration for the
// held-run proof.
func installedConfigYAMLWithScriptEnv(tb testing.TB, bridgeExecutable string) string {
	tb.Helper()

	var sb strings.Builder
	if bridgeExecutable != "" {
		sb.WriteString("bridge_executable: " + strconv.Quote(bridgeExecutable) + "\n")
	}
	sb.WriteString("default_workspace: " + strconv.Quote(tb.TempDir()) + "\n")
	sb.WriteString("sandbox_mode: \"off\"\n")
	sb.WriteString("max_agents: 4\nmax_concurrent_runs: 2\nbridge_start_timeout_seconds: 20\n")
	sb.WriteString("bridge_env_allowlist:\n  - FAKE_BRIDGE_SCRIPT\n")
	return sb.String()
}

// installedHeldRunScript advertises the model the installed proofs select and holds the
// run open so only the host's cancel can end it.
func installedHeldRunScript() fakebridge.Script {
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-installed"}`)},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-installed"},
	}}
	return script
}
