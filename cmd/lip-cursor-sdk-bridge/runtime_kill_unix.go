//go:build !windows

package main

import "os/exec"

// terminateRuntimeTree escalates from the graceful stdin close to termination of
// the owned descendant.
//
// On POSIX the declared process-tree policy is the process group, and the runtime
// shares the launcher's group on purpose so the connector's group kill still
// reaches it. The launcher therefore must not group-kill itself out of existence
// and terminates the descendant it owns by handle instead. Anything the runtime
// started stays in the same group and is reached by the connector's kill, which
// remains valid after this launcher has exited.
func terminateRuntimeTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
