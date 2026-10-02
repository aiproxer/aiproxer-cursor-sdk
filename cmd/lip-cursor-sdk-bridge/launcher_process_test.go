package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/protocol"
	"github.com/stretchr/testify/require"
)

var (
	buildLauncherOnce sync.Once
	buildLauncherPath string
	buildLauncherErr  error
)

// TestLauncherProcess_PropagatesRuntimeExitStatus runs the real launcher
// executable over the packaged private layout: protocol bytes must cross the
// launcher unchanged and the runtime's exit status must become the launcher's
// exit status.
func TestLauncherProcess_PropagatesRuntimeExitStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the launcher executable")
	}

	lay := installedLauncherLayout(t, true)
	writeBridgeScript(t, lay, `{"mode":"echo","exit":7}`)
	const payload = "{\"type\":\"request\"}\n{\"type\":\"request\"}\n"

	proc := startLauncherProcess(t, lay.launcher)
	require.NoError(t, writeAndClose(proc, payload))
	require.Equal(t, payload, readAll(t, proc.stdout))
	requireExitCode(t, proc, 7)
}

// TestLauncherProcess_ForwardsArgumentsToRuntime keeps the operator-facing
// `--version` and `doctor` invocations reaching the bridge entry unchanged,
// after the fixed private runtime and entry arguments.
func TestLauncherProcess_ForwardsArgumentsToRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the launcher executable")
	}

	for _, args := range [][]string{{"--version"}, {"doctor"}} {
		lay := installedLauncherLayout(t, true)
		argvLog := filepath.Join(t.TempDir(), "argv.log")
		writeBridgeScript(t, lay, `{"mode":"echo","argvLog":`+strconvQuote(argvLog)+`}`)

		proc := startLauncherProcess(t, lay.launcher, args...)
		require.NoError(t, writeAndClose(proc, ""))
		readAll(t, proc.stdout)
		requireExitCode(t, proc, 0)
		require.Equal(t, append([]string{lay.entry}, args...), readLines(t, argvLog))
	}
}

// TestLauncherProcess_ServesBridgeProtocolThroughPrivateRuntime proves the
// launcher is transparent to the NDJSON handshake the connector performs: a
// real initialize request crosses the launcher and its runtime unchanged.
func TestLauncherProcess_ServesBridgeProtocolThroughPrivateRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the launcher executable")
	}

	lay := installedLauncherLayout(t, true)
	writeBridgeScript(t, lay, `{"mode":"bridge"}`)

	proc := startLauncherProcess(t, lay.launcher)
	request := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            "c1",
		Method:        protocol.MethodInitialize,
		Params:        json.RawMessage(`{"implVersion":"go-cursorsdk/0.1.0"}`),
	}
	require.NoError(t, writeTo(proc, mustFrameLine(t, request)+"\n"))

	reader := bufio.NewReader(proc.stdout)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	frame, err := protocol.DecodeLine([]byte(strings.TrimRight(line, "\n")))
	require.NoError(t, err)
	require.Equal(t, protocol.TypeResponse, frame.Type)
	require.Equal(t, "c1", frame.ID)
	require.Nil(t, frame.Error)

	var result protocol.InitializeResult
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	require.Equal(t, protocol.SchemaVersion, result.SchemaVersion)
	require.NotEmpty(t, result.ImplVersion)
	require.Equal(t, protocol.PinnedSDKVersion, result.SDKVersion)

	require.NoError(t, writeAndClose(proc, ""))
	requireExitCode(t, proc, 0)
}

// TestLauncherProcess_MissingPrivateRuntimeFailsExplicitlyWithoutFallback keeps
// the packaged prerequisite explicit: with the private runtime absent, the
// launcher exits with the prerequisite status and names the packaged location,
// even though a startable `node` is reachable through PATH and the working
// directory, and without ever running a shell, npm, or a download.
func TestLauncherProcess_MissingPrivateRuntimeFailsExplicitlyWithoutFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the launcher executable")
	}

	lay := installedLauncherLayout(t, false)
	decoyDir := t.TempDir()
	require.NoError(t, copyFile(fakebridge.BuildNodeExe(t), filepath.Join(decoyDir, "node"+platformExeSuffix())))
	wd := t.TempDir()
	require.NoError(t, copyFile(fakebridge.BuildNodeExe(t), filepath.Join(wd, "node"+platformExeSuffix())))

	proc := newLauncherProcess(t, lay.launcher)
	proc.cmd.Dir = wd
	proc.cmd.Env = append(os.Environ(), "PATH="+decoyDir)
	proc.start(t)
	stderr := readAll(t, proc.stderr)
	requireExitCode(t, proc, exitPrerequisite)
	require.Contains(t, stderr, "private/node/node")
	require.Contains(t, stderr, "not found")
	require.NotContains(t, stderr, "npm")
	require.NotContains(t, stderr, "http")
}

// TestLauncherProcess_ConnectorTreeKillReapsRuntimeDescendants proves the
// private runtime stays inside the launcher's own process tree. The connector
// owns the launcher and kills the launcher's tree; that policy must also reach
// the private runtime and anything the runtime started, so a launcher that is
// killed outright still cannot strand a private Node process.
func TestLauncherProcess_ConnectorTreeKillReapsRuntimeDescendants(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the launcher executable")
	}

	lay := installedLauncherLayout(t, true)
	dir := t.TempDir()
	runtimePIDFile := filepath.Join(dir, "runtime.pid")
	descendantPIDFile := filepath.Join(dir, "descendant.pid")
	writeBridgeScript(t, lay, fmt.Sprintf(
		`{"mode":"hold","spawnChild":true,"pidFile":%s,"childPidFile":%s}`,
		strconvQuote(runtimePIDFile), strconvQuote(descendantPIDFile)))

	proc := startLauncherProcess(t, lay.launcher)
	waitForFile(t, runtimePIDFile)
	waitForFile(t, descendantPIDFile)
	runtimePID := readPID(t, runtimePIDFile)
	descendantPID := readPID(t, descendantPIDFile)
	require.NotEqual(t, runtimePID, descendantPID)
	require.True(t, processAlive(t, runtimePID), "the private runtime must be running before the tree kill")
	require.True(t, processAlive(t, descendantPID), "a runtime descendant must be running before the tree kill")

	killProcessTreeLikeConnector(t, proc.cmd)
	_ = proc.cmd.Wait()

	requireEventuallyDead(t, runtimePID)
	requireEventuallyDead(t, descendantPID)
}

// launcherProcess is a running launcher executable with its own pipes.
type launcherProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func startLauncherProcess(tb testing.TB, path string, args ...string) *launcherProcess {
	tb.Helper()
	proc := newLauncherProcess(tb, path, args...)
	proc.start(tb)
	return proc
}

// newLauncherProcess prepares a launcher invocation with its own pipes so a test
// can adjust the environment or working directory before starting it.
func newLauncherProcess(tb testing.TB, path string, args ...string) *launcherProcess {
	tb.Helper()
	cmd := exec.Command(path, args...)
	stdin, err := cmd.StdinPipe()
	require.NoError(tb, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(tb, err)
	stderr, err := cmd.StderrPipe()
	require.NoError(tb, err)
	proc := &launcherProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}
	tb.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return proc
}

// start runs the launcher the way the connector spawns its companion.
func (p *launcherProcess) start(tb testing.TB) {
	tb.Helper()
	setProcessGroupForTest(p.cmd)
	require.NoError(tb, p.cmd.Start())
}

func writeTo(proc *launcherProcess, payload string) error {
	_, err := io.WriteString(proc.stdin, payload)
	return err
}

func writeAndClose(proc *launcherProcess, payload string) error {
	if err := writeTo(proc, payload); err != nil {
		_ = proc.stdin.Close()
		return err
	}
	return proc.stdin.Close()
}

func readAll(tb testing.TB, r io.Reader) string {
	tb.Helper()
	raw, err := io.ReadAll(r)
	require.NoError(tb, err)
	return string(raw)
}

func requireExitCode(tb testing.TB, proc *launcherProcess, want int) {
	tb.Helper()
	err := proc.cmd.Wait()
	if want == 0 {
		require.NoError(tb, err)
		return
	}
	var exitErr *exec.ExitError
	require.ErrorAs(tb, err, &exitErr)
	require.Equal(tb, want, exitErr.ExitCode())
}

func mustFrameLine(tb testing.TB, frame *protocol.Frame) string {
	tb.Helper()
	var sb strings.Builder
	require.NoError(tb, protocol.WriteFrame(&sb, frame))
	return strings.TrimRight(sb.String(), "\n")
}

// installedLauncherLayout builds the packaged private layout around the real
// launcher executable and the deterministic fake private runtime.
func installedLauncherLayout(tb testing.TB, withRuntime bool) installedPrivateLayout {
	tb.Helper()
	lay := installPrivateLayout(tb, withRuntime, true)
	require.NoError(tb, copyFile(buildLauncherExe(tb), lay.launcher))
	if withRuntime {
		require.NoError(tb, copyFile(fakebridge.BuildNodeExe(tb), lay.runtime))
	}
	return lay
}

func buildLauncherExe(tb testing.TB) string {
	tb.Helper()
	buildLauncherOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			buildLauncherErr = errors.New("runtime.Caller failed")
			return
		}
		dir, err := os.MkdirTemp("", "lip-cursor-sdk-bridge-build-")
		if err != nil {
			buildLauncherErr = err
			return
		}
		exe := filepath.Join(dir, launcherName+platformExeSuffix())
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, ".")
		cmd.Dir = filepath.Dir(thisFile)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildLauncherErr = fmt.Errorf("go build lip-cursor-sdk-bridge: %w\n%s", err, out)
			return
		}
		buildLauncherPath = exe
	})
	if buildLauncherErr != nil {
		tb.Fatal(buildLauncherErr)
	}
	return buildLauncherPath
}
