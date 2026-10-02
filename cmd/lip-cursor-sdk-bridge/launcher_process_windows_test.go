//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// stillActive is the Windows process exit code reported for a live process.
const stillActive = 0x00000103

// processQueryLimitedInformation is the least-privilege process query access
// this test needs to observe whether a descendant is still alive.
const processQueryLimitedInformation = 0x1000

// setProcessGroupForTest mirrors the connector's own process-group isolation for
// the launcher it spawns, so the tree-kill test exercises the production
// arrangement.
func setProcessGroupForTest(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessTreeLikeConnector is the connector's declared Windows process-tree
// policy: taskkill on the launcher and everything below it.
func killProcessTreeLikeConnector(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	require.NotNil(t, cmd.Process)
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").CombinedOutput()
	require.NoError(t, err, "taskkill: %s", out)
}

func processAlive(t testing.TB, pid int) bool {
	t.Helper()
	require.Greater(t, pid, 1)
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}
