//go:build !windows

package cursorsdk_test

import (
	"os/exec"
	"syscall"
)

// terminateProbeHarnessTree terminates a verifier this harness gave up on, together with
// everything it started.
//
// The declared POSIX process-tree policy is the process group, so the harness puts the
// verifier in one of its own and signals that group: a verifier that leaked a probed
// executable would otherwise keep holding this test machine's process table open.
func terminateProbeHarnessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
