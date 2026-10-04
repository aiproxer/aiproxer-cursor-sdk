//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProbeChild puts the probed command in a process group of its own.
//
// The declared POSIX process-tree policy is the process group, and the probed executable is
// the root of the tree a probe is responsible for: the launcher it starts owns a runtime of
// its own. Giving the command its own group is what lets terminateProbeTree reach all of that
// without signalling this tool or, worse, the verifier that started it.
func configureProbeChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProbeTree terminates the probed command and everything it started.
func terminateProbeTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// The negative pid addresses the process group, so the command's descendants are
	// terminated with it. Termination is forced rather than graceful: this is the path
	// taken precisely because the command did not settle on its own, and a graceful
	// request to a wedged executable is another wait.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// probeTerminationSignals are the channels a terminal signal arrives on.
func probeTerminationSignals() chan os.Signal {
	return make(chan os.Signal, 1)
}

// probeTerminationSignalList is what a terminal signal has to be observed through.
func probeTerminationSignalList() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// probeSignalledExit is the status a probe interrupted by a terminal signal reports. It is the
// shell's convention for a signalled command, and the shell that ran this tool is being
// signalled too, so the value is reported rather than chosen for its meaning.
const probeSignalledExit = 128 + 2
