package main

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// startRuntime creates the owned private runtime descendant.
//
// The runtime deliberately stays in the launcher's own process group or process
// tree. The connector owns the launcher and kills the launcher's tree; only a
// runtime that is a descendant of the launcher can be reached by that policy, so
// giving the runtime a new process group would strand it whenever the launcher
// itself is killed outright.
func startRuntime(spec runtimeSpec, pipes childPipes) (runtimeChild, error) {
	if spec.path == "" || len(spec.args) == 0 {
		return nil, errors.New("empty private runtime invocation")
	}
	cmd := exec.Command(spec.path, spec.args[1:]...)
	cmd.Stdin = pipes.in
	cmd.Stdout = pipes.out
	cmd.Stderr = pipes.err
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.path, err)
	}
	return &execRuntime{cmd: cmd, status: exitStartFailed}, nil
}

// execRuntime is the production runtime descendant.
type execRuntime struct {
	cmd    *exec.Cmd
	status int
}

func (p *execRuntime) PID() int { return p.cmd.Process.Pid }

func (p *execRuntime) Kill() error { return terminateRuntimeTree(p.cmd) }

// Wait reaps the descendant and records the exit status it propagates.
func (p *execRuntime) Wait() error {
	err := p.cmd.Wait()
	p.status = exitStatusForWait(err)
	return err
}

// ExitStatus reports the status recorded by Wait.
func (p *execRuntime) ExitStatus() int { return p.status }

// exitStatusForWait maps a completed runtime wait to the exit status the
// launcher propagates: the runtime's own status, or the shell convention
// 128+signal for a runtime terminated by a signal.
func exitStatusForWait(err error) int {
	if err == nil {
		return exitOK
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
	}
	return exitStartFailed
}
