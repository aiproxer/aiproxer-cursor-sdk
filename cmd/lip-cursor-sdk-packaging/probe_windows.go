//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
)

// configureProbeChild leaves the process creation attributes alone.
//
// Windows exposes no process group a child can be put in, so the declared tree policy there is
// process ancestry, which taskkill walks. Grouping the child would separate it from its
// descendants' ancestry without making them reachable.
func configureProbeChild(*exec.Cmd) {}

// terminateProbeTree terminates the probed command and everything it started.
func terminateProbeTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// /T walks the descendants and /F forces, which is the same policy and the same
	// escalation the private launcher uses for the runtime it owns: termination is forced
	// rather than graceful because this path is taken precisely because the command did
	// not settle on its own.
	return exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
}

// probeTerminationSignals are the channels a terminal signal arrives on.
func probeTerminationSignals() chan os.Signal {
	return make(chan os.Signal, 1)
}

// probeTerminationSignalList is what a terminal signal has to be observed through. Windows
// delivers no SIGTERM to an unrelated console application, so an interrupt is the only
// terminal signal there is to answer.
func probeTerminationSignalList() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// probeSignalledExit is the status a probe interrupted by a terminal signal reports.
const probeSignalledExit = 128 + 2
