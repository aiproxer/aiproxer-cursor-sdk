//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// terminateRuntimeTree escalates from the graceful stdin close to termination of
// the owned descendant.
//
// On Windows the declared process-tree policy is process ancestry, and taskkill
// walks the runtime's own descendants, so it reaches everything the runtime
// started without ever touching this launcher, which is the runtime's parent.
func terminateRuntimeTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
}
