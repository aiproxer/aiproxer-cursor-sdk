package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// The launcher owns exactly one private runtime descendant for its whole
// lifetime: one physical owner, cleanup ownership established before the
// runtime can escape, and one terminal settlement observed by every caller.
//
// Stream forwarding is a byte-for-byte pipe, not a protocol interpreter: the
// launcher adds no framing, ordering, or buffering semantics of its own, so
// whatever the connector sends and the bridge answers crosses unchanged.
const (
	// defaultGraceTimeout bounds the graceful cleanup step, in which the
	// runtime observes stdin EOF and is expected to exit by itself.
	defaultGraceTimeout = 2 * time.Second
	// defaultKillTimeout bounds termination and the reap that must follow it.
	defaultKillTimeout = 5 * time.Second
)

// errLauncherClosed reports a start that lost the race against cleanup.
var errLauncherClosed = errors.New("lip-cursor-sdk-bridge: launcher closed")

// stdioSet are the launcher's own standard streams. They stay *os.File so the
// protocol streams cross the launcher as raw bytes on a pipe.
type stdioSet struct {
	in  *os.File
	out *os.File
	err *os.File
}

// childPipes are the runtime-side ends of the pipes the launcher owns.
type childPipes struct {
	in  *os.File // the runtime's stdin
	out *os.File // the runtime's stdout
	err *os.File // the runtime's stderr
}

// runtimeChild is the owned private runtime descendant.
//
// Wait may be called exactly once per child: the launcher owns the single reap,
// so a second settlement can never race a first one.
type runtimeChild interface {
	// PID reports the descendant process id.
	PID() int
	// Kill terminates the descendant under the platform process-tree policy.
	Kill() error
	// Wait reaps the descendant and settles its exit status.
	Wait() error
	// ExitStatus reports the propagated exit status after Wait returned.
	ExitStatus() int
}

// runtimeSpec is the fixed private runtime invocation.
type runtimeSpec struct {
	path string
	args []string
}

// startFunc creates the owned descendant. Production is [startRuntime]; tests
// substitute barriers so startup and termination schedules are deterministic
// instead of timing-dependent.
type startFunc func(spec runtimeSpec, pipes childPipes) (runtimeChild, error)

type launcherState int

const (
	// launcherNew owns nothing: no runtime, no pipe, no goroutine.
	launcherNew launcherState = iota
	// launcherStarting is acquiring the runtime and its pipes.
	launcherStarting
	// launcherRunning owns a live descendant and its streams.
	launcherRunning
	// launcherStopping is releasing what the launcher owns.
	launcherStopping
	// launcherStopped owns nothing anymore and never starts again.
	launcherStopped
)

func launcherStateName(state launcherState) string {
	switch state {
	case launcherNew:
		return "new"
	case launcherStarting:
		return "starting"
	case launcherRunning:
		return "running"
	case launcherStopping:
		return "stopping"
	case launcherStopped:
		return "stopped"
	}
	return "unknown"
}

// launcherOptions are the process seam and the bounded cleanup policy. Zero
// values select production defaults.
type launcherOptions struct {
	start        startFunc
	graceTimeout time.Duration
	killTimeout  time.Duration
}

// Launcher owns the plugin-private runtime descendant.
type Launcher struct {
	layout privateLayout
	args   []string
	stdio  stdioSet
	start  startFunc
	grace  time.Duration
	kill   time.Duration

	// startDone is closed exactly once when a Start call has finished, whether
	// it acquired a runtime or not, so cleanup can wait for an in-flight start.
	startDone chan struct{}

	mu          sync.Mutex
	state       launcherState
	child       runtimeChild
	settlement  chan struct{}
	forwardDone chan struct{}
	childStdin  *os.File
	childStdout *os.File
	childStderr *os.File
	waitErr     error
	exitStatus  int

	closeOnce sync.Once
	closeErr  error
}

// newLauncher builds a launcher for the resolved private layout.
func newLauncher(lay privateLayout, args []string, stdio stdioSet, opts launcherOptions) *Launcher {
	start := opts.start
	if start == nil {
		start = startRuntime
	}
	grace := opts.graceTimeout
	if grace <= 0 {
		grace = defaultGraceTimeout
	}
	kill := opts.killTimeout
	if kill <= 0 {
		kill = defaultKillTimeout
	}
	return &Launcher{
		layout:    lay,
		args:      append([]string(nil), args...),
		stdio:     stdio,
		start:     start,
		grace:     grace,
		kill:      kill,
		startDone: make(chan struct{}),
		state:     launcherNew,
	}
}

// Start acquires the private runtime and its streams. Every failure unwinds the
// resources acquired so far in reverse order, and a start that loses the race
// against cleanup terminates and reaps what it created instead of leaking it.
func (l *Launcher) Start(ctx context.Context) (err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// An incomplete private layout is a prerequisite failure, so it is reported
	// before anything is acquired rather than as a start error.
	if err := l.layout.validate(); err != nil {
		return err
	}
	l.mu.Lock()
	if l.state != launcherNew {
		state := l.state
		l.mu.Unlock()
		return fmt.Errorf("lip-cursor-sdk-bridge: launcher already %s", launcherStateName(state))
	}
	settlement := make(chan struct{})
	l.state = launcherStarting
	l.settlement = settlement
	l.mu.Unlock()

	defer func() {
		if err != nil {
			l.finishStartFailure()
		}
		close(l.startDone)
	}()

	// Acquire the runtime streams. Everything from here on is released in
	// reverse acquisition order until the runtime is owned.
	runtimeIn, childIn, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("lip-cursor-sdk-bridge: private runtime stdin pipe: %w", err)
	}
	childOut, runtimeOut, err := os.Pipe()
	if err != nil {
		closeFiles(runtimeIn, childIn)
		return fmt.Errorf("lip-cursor-sdk-bridge: private runtime stdout pipe: %w", err)
	}
	childErr, runtimeErr, err := os.Pipe()
	if err != nil {
		closeFiles(runtimeIn, childIn, runtimeOut, childOut)
		return fmt.Errorf("lip-cursor-sdk-bridge: private runtime stderr pipe: %w", err)
	}

	spec := runtimeSpec{path: l.layout.Runtime, args: l.layout.argv(l.args)}
	child, startErr := l.start(spec, childPipes{in: runtimeIn, out: runtimeOut, err: runtimeErr})
	if startErr != nil {
		closeFiles(runtimeIn, childIn, runtimeOut, childOut, runtimeErr, childErr)
		if child != nil {
			// A starter that already created the runtime still handed back an
			// unowned live process: release it here.
			terminateUnstarted(child, l.kill)
		}
		return fmt.Errorf("lip-cursor-sdk-bridge: start private runtime: %w", startErr)
	}
	if child == nil {
		closeFiles(runtimeIn, childIn, runtimeOut, childOut, runtimeErr, childErr)
		return errors.New("lip-cursor-sdk-bridge: start private runtime: no runtime process was created")
	}
	// The runtime now holds its own copies of the runtime-side handles. Dropping
	// this process's copies is what lets the stream forwarders observe EOF when
	// the runtime exits, instead of waiting on a pipe this process still holds
	// open.
	closeFiles(runtimeIn, runtimeOut, runtimeErr)

	forwardDone := make(chan struct{})
	l.mu.Lock()
	stopping := l.state == launcherStopping || l.state == launcherStopped
	if !stopping {
		// Ownership is established under the same lock that Close takes, so a
		// concurrent cleanup either finds this descendant here or has already
		// claimed the shutdown and is rejected below.
		l.child = child
		l.childStdin, l.childStdout, l.childStderr = childIn, childOut, childErr
		l.forwardDone = forwardDone
		l.state = launcherRunning
		go l.settle(child)
	}
	l.mu.Unlock()

	if stopping {
		terminateUnstarted(child, l.kill)
		closeFiles(runtimeIn, childIn, runtimeOut, childOut, runtimeErr, childErr)
		return errLauncherClosed
	}

	l.forward(childIn, childOut, childErr, forwardDone)
	return nil
}

// forward pipes the launcher's own streams to and from the runtime. The output
// side is part of the awaited completion; the stdin side is process-scoped,
// because it can be blocked reading the launcher's own stdin when the runtime is
// already gone.
func (l *Launcher) forward(childIn, childOut, childErr *os.File, forwardDone chan struct{}) {
	var outputs sync.WaitGroup
	outputs.Add(2)
	go func() {
		defer outputs.Done()
		_, _ = io.Copy(l.stdio.out, childOut)
	}()
	go func() {
		defer outputs.Done()
		_, _ = io.Copy(l.stdio.err, childErr)
	}()
	go func() {
		_, _ = io.Copy(childIn, l.stdio.in)
		_ = childIn.Close()
	}()
	go func() {
		outputs.Wait()
		close(forwardDone)
	}()
}

// settle is the single owner of the descendant reap: it records the terminal
// outcome and closes the settlement channel that every caller observes.
func (l *Launcher) settle(child runtimeChild) {
	waitErr := child.Wait()
	status := child.ExitStatus()
	l.mu.Lock()
	l.waitErr = waitErr
	l.exitStatus = status
	l.mu.Unlock()
	close(l.settlement)
}

// finishStartFailure settles a start that acquired nothing, so a waiter is never
// left blocked on a runtime that does not exist. The start error itself is the
// caller's own return value, so only the settlement has to be recorded here.
func (l *Launcher) finishStartFailure() {
	l.mu.Lock()
	if l.state != launcherStopping && l.state != launcherStopped {
		l.state = launcherStopped
	}
	settlement := l.settlement
	l.exitStatus = exitStartFailed
	l.mu.Unlock()
	if settlement != nil {
		close(settlement)
	}
}

// Wait blocks until the owned runtime is reaped and reports the propagated exit
// status. It is safe to call repeatedly and concurrently: all callers observe the
// same single settlement. A failed start settles with the start-failure status
// instead of blocking.
func (l *Launcher) Wait() int {
	l.mu.Lock()
	settlement := l.settlement
	l.mu.Unlock()
	if settlement == nil {
		return exitStartFailed
	}
	<-settlement
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exitStatus
}

// ExitError reports the runtime's wait error, when it had one.
func (l *Launcher) ExitError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waitErr
}

// Close releases everything the launcher owns within the bounded cleanup policy:
// a graceful stdin close, then the declared process-tree termination, then the
// reap. It is idempotent, and concurrent callers share one completion.
func (l *Launcher) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		switch l.state {
		case launcherNew:
			l.state = launcherStopped
			l.mu.Unlock()
			return
		case launcherStarting:
			// Claim the shutdown and let the in-flight start settle its own
			// acquisitions: ownership is established before the runtime escapes.
			l.state = launcherStopping
			startDone := l.startDone
			l.mu.Unlock()
			<-startDone
			l.mu.Lock()
		}
		if l.state != launcherRunning {
			l.state = launcherStopped
			l.mu.Unlock()
			return
		}
		l.state = launcherStopping
		child := l.child
		childStdin, childStdout, childStderr := l.childStdin, l.childStdout, l.childStderr
		settlement, forwardDone := l.settlement, l.forwardDone
		l.child = nil
		l.childStdin, l.childStdout, l.childStderr = nil, nil, nil
		l.mu.Unlock()

		l.closeErr = l.cleanup(child, childStdin, childStdout, childStderr, settlement, forwardDone)
		l.mu.Lock()
		l.state = launcherStopped
		l.mu.Unlock()
	})
	return l.closeErr
}

// cleanup performs bounded cleanup of the owned descendant and its streams.
func (l *Launcher) cleanup(child runtimeChild, childStdin, childStdout, childStderr *os.File, settlement, forwardDone <-chan struct{}) error {
	var errs []error
	// Graceful first: stdin EOF is the same termination the runtime would
	// observe from the connector, and it never needs a signal.
	_ = childStdin.Close()
	if !settledWithin(settlement, l.grace) {
		if err := child.Kill(); err != nil {
			errs = append(errs, fmt.Errorf("lip-cursor-sdk-bridge: terminate private runtime pid %d: %w", child.PID(), err))
		}
		if !settledWithin(settlement, l.kill) {
			errs = append(errs, errors.New("lip-cursor-sdk-bridge: private runtime did not exit after termination"))
		}
	}
	if forwardDone != nil && !settledWithin(forwardDone, l.kill) {
		errs = append(errs, errors.New("lip-cursor-sdk-bridge: stream forwarding did not finish within the cleanup bound"))
	}
	// Release the remaining launcher-side handles in reverse acquisition order.
	closeFiles(childStdin, childStdout, childStderr)
	return errors.Join(errs...)
}

// terminateUnstarted releases a runtime that exists but that no caller owns. It
// is the reverse-order unwind for a start that lost the race against cleanup or
// failed after the descendant was already created. Termination is bounded, so a
// runtime that refuses to die can never block the start failure itself.
func terminateUnstarted(child runtimeChild, bound time.Duration) {
	if child == nil {
		return
	}
	if bound <= 0 {
		bound = defaultKillTimeout
	}
	_ = child.Kill()
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case <-waited:
	case <-time.After(bound):
	}
}

func settledWithin(done <-chan struct{}, bound time.Duration) bool {
	if done == nil {
		return true
	}
	if bound <= 0 {
		bound = defaultKillTimeout
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
