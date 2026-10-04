//go:build windows

package cursorsdk_test

import (
	"os/exec"
	"strconv"
)

// terminateProbeHarnessTree terminates a verifier this harness gave up on, together with
// everything it started.
//
// Windows exposes no process group to signal, and the declared tree policy there is process
// ancestry, so taskkill walks the descendants of the verifier - including the probed
// executables it started - without ever touching this test process.
func terminateProbeHarnessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
