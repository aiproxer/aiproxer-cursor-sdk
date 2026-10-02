//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// setProcessGroupForTest mirrors the connector's own process-group isolation for
// the launcher it spawns, so the tree-kill test exercises the production
// arrangement.
func setProcessGroupForTest(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTreeLikeConnector is the connector's declared POSIX process-tree
// policy: kill the whole process group led by the launcher.
func killProcessTreeLikeConnector(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	require.NotNil(t, cmd.Process)
	require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
}

func processAlive(t testing.TB, pid int) bool {
	t.Helper()
	require.Greater(t, pid, 1)
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// TestLauncherProcess_TerminationSignalReapsRuntime covers termination
// forwarding: a termination signal aimed at the launcher must reach its owned
// private runtime descendant, which must then be reaped rather than left behind.
// The Windows tree path is covered by
// TestLauncherProcess_ConnectorTreeKillReapsRuntimeDescendants, because Windows
// has no deliverable graceful termination signal for this arrangement.
func TestLauncherProcess_TerminationSignalReapsRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the launcher executable")
	}

	lay := installedLauncherLayout(t, true)
	runtimePIDFile := filepath.Join(t.TempDir(), "runtime.pid")
	// The runtime keeps running after stdin EOF, so the launcher has to escalate
	// from the graceful stdin close to its declared process-tree kill.
	writeBridgeScript(t, lay, `{"mode":"hold","pidFile":`+strconvQuote(runtimePIDFile)+`}`)

	proc := startLauncherProcess(t, lay.launcher)
	waitForFile(t, runtimePIDFile)
	runtimePID := readPID(t, runtimePIDFile)
	require.True(t, processAlive(t, runtimePID))

	require.NoError(t, proc.cmd.Process.Signal(syscall.SIGTERM))
	requireExitCode(t, proc, 128+int(syscall.SIGKILL))
	requireEventuallyDead(t, runtimePID)
}
