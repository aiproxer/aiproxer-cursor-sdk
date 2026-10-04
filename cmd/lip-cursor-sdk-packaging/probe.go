package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

// The bounds below are the probe contract, and they are constants rather than options on
// purpose.
//
// The private runtime and the bridge launcher are executables this project ships but cannot
// vouch for at verification time: an operator's install can hold a stub, a wrong build, or
// anything else that never answers. A caller that could choose how long such an executable is
// given would be choosing part of the verdict, so the bound lives here, where neither
// packaging script can reach it, and the scripts read it back out of the status line instead
// of restating it.
//
// The bound is generous because a verdict that depended on a tight one would be a verdict
// about the machine's speed rather than about the tree. What it has to buy is only that an
// executable which never answers ends the run with a finding.
const (
	// probeDeadline bounds one probed executable.
	probeDeadline = 120 * time.Second
	// probeDrainGrace bounds how long the probe waits for the command's output to finish
	// after the command itself is settled. It is short because the only thing it is waiting
	// for is a stream reaching its end, and a stream that does not is reported rather than
	// waited on.
	probeDrainGrace = 2 * time.Second
	// probeTerminateGrace bounds the wait for a terminated tree to be reaped, so
	// terminating a process that ignores termination still ends the run.
	probeTerminateGrace = 10 * time.Second
)

// The statuses below are the conventional ones a caller reading a probe already knows, so a
// caller that only compares against zero still sees a failure.
const (
	// probeTimeoutExit is the status timeout(1) reports for a command it gave up on.
	probeTimeoutExit = 124
	// probeNotFoundExit is the status a shell reports for a command it could not start.
	probeNotFoundExit = 127
)

// probeStatusPrefix is the one spelling of a probe status line.
//
// The line is the only channel the caller parses, and it is kept off the command's own output
// so that a staged executable cannot answer for itself: the command's two output streams are
// read through the probe's own pipe and reach the caller's output, while this line goes to the
// probe's report stream, which the command is never given.
const probeStatusPrefix = "lip-cursor-sdk-packaging probe: "

// probeUsage is the one spelling of the probe verb's arguments. There is no option parsing,
// so the probed command's own arguments reach it untouched: the runtime is probed with -p and
// --version, and the launcher with a subcommand of its own.
const probeUsage = "usage: lip-cursor-sdk-packaging probe COMMAND [ARG...]"

// runProbe is the probe verb. It is called with the arguments after the verb name.
func runProbe(args []string) error {
	if len(args) == 0 {
		return reportError("probe", errors.New(probeUsage))
	}
	return probeCommand(probeDeadline, args, os.Stdout, os.Stderr)
}

// probeCommand runs one command under a deadline and reports what it did.
//
// The command's output is read through a pipe this tool owns and is written to stdout as it
// arrives; the report goes to stderr, one line, so a caller can keep the answer and the
// answer's status apart. The report is a fixed set of key=value tokens: it is read by two
// scripts in two shells, and a sentence would be a string to parse rather than a value to read.
//
// Nothing here can wait forever. The command settles by exiting, by the deadline, or by a
// signal; its output settles by reaching its end or by this tool closing the read end. A caller
// reading this tool's output is therefore released whether the command cooperated or not.
func probeCommand(deadline time.Duration, argv []string, stdout, status io.Writer) error {
	if len(argv) == 0 {
		return errors.New(probeUsage)
	}

	pipe, err := newProbePipe()
	if err != nil {
		writeProbeStatus(status, probeNotFoundExit, false, true, deadline)
		fmt.Fprintf(status, "%scannot open a pipe for %s: %v\n", probeStatusPrefix, argv[0], err)
		return nil
	}
	defer pipe.close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = pipe.writer
	cmd.Stderr = pipe.writer
	configureProbeChild(cmd)

	if err := cmd.Start(); err != nil {
		// A staged executable that cannot be created is a prerequisite failure the
		// operator has to read, so it is reported rather than treated as this tool's
		// own failure. The stream counts as drained because nothing was ever handed it:
		// no command ran, so there is no descendant that could have outlived one, and
		// reporting an undrained stream here would be a statement about a process that
		// does not exist.
		pipe.release()
		writeProbeStatus(status, probeNotFoundExit, false, true, deadline)
		fmt.Fprintf(status, "%scannot start %s: %v\n", probeStatusPrefix, argv[0], err)
		return nil
	}
	pipe.releaseWriter()

	// The output is read on its own goroutine and finishes when the stream ends or when this
	// tool closes the read end. It never blocks the wait below and never blocks the exit.
	drain := startProbeDrain(pipe, stdout)

	// The waited-on channel decides when the run is over: the process exiting, the
	// deadline arriving, or this tool being interrupted. Nothing else can end the wait,
	// because the stream it forwards is the probe's own.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// A terminal signal ends this tool while the probed command is still running, and the
	// probed command is deliberately outside this process's signal group so the tree kill
	// can reach its descendants without reaching this tool. So a signal has to take the tree
	// with it rather than leave it behind.
	signals := probeTerminationSignals()
	signal.Notify(signals, probeTerminationSignalList()...)
	defer signal.Stop(signals)

	waitErr, outcome := awaitProbeChild(done, cmd, signals, deadline)
	drained := settleProbeDrain(drain, pipe)
	writeProbeStatus(status, probeExitStatus(waitErr, outcome), outcome == probeTimedOut, drained, deadline)
	return nil
}

// probePipe is the pipe a probed command's output travels through.
//
// The probe does not hand the command its own stdout. If it did, the command's descendants
// would inherit the stream the calling shell is reading the answer out of, and a descendant
// that outlived the command would keep that read open - so a staged executable nobody vouched
// for could stop the caller finishing, which is exactly what the bound exists to prevent.
type probePipe struct {
	reader *os.File
	writer *os.File
	closed sync.Once
}

func newProbePipe() (*probePipe, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &probePipe{reader: reader, writer: writer}, nil
}

// releaseWriter drops the probe's own copy of the write end, so the only holders left are the
// command and whatever it started.
func (p *probePipe) releaseWriter() {
	_ = p.writer.Close()
}

// release drops both ends of a pipe the command never held, which is the case where the
// command could not be started at all.
func (p *probePipe) release() {
	_ = p.writer.Close()
	_ = p.reader.Close()
}

// closeReader ends the read side. Anything still holding the write end gets a broken pipe on its
// next write rather than an answer nobody is left to read.
func (p *probePipe) closeReader() {
	_ = p.reader.Close()
}

// close releases both ends once, for the paths that leave the probe without settling the
// stream.
func (p *probePipe) close() {
	p.closed.Do(func() {
		p.releaseWriter()
		p.closeReader()
	})
}

// probeDrain is the probe's side of the command's output stream.
type probeDrain struct {
	done chan struct{}
	// ended reports whether the stream reached its end on its own, as opposed to the probe
	// giving up on it. A stream that did not is the caller's signal that something the
	// command started is still holding it.
	ended atomic.Bool
}

// startProbeDrain moves the command's output into the caller's stream.
func startProbeDrain(pipe *probePipe, stdout io.Writer) *probeDrain {
	drain := &probeDrain{done: make(chan struct{})}
	go func() {
		defer close(drain.done)
		_, err := io.Copy(stdout, pipe.reader)
		drain.ended.Store(err == nil)
	}()
	return drain
}

// settleProbeDrain finishes the command's output stream and reports whether it ended by itself.
//
// A stream that is still open after the command settled is held by something the command
// started. Terminating the tree may have released it; a descendant that left the tree will not
// be released by anything this probe can reach, and the probe does not claim otherwise. Either
// way the read end is closed, which is what guarantees the caller's read finishes.
//
// It reports whether the stream ended on its own, and nothing more.
func settleProbeDrain(drain *probeDrain, pipe *probePipe) bool {
	select {
	case <-drain.done:
		return drain.ended.Load()
	case <-time.After(probeDrainGrace):
	}

	// Closing the read end is what unblocks a reader waiting on a stream somebody else is
	// still holding, so the copy finishes rather than outliving this tool.
	pipe.closeReader()
	select {
	case <-drain.done:
	case <-time.After(probeDrainGrace):
	}
	return false
}

// probeOutcome is what ended a probe. They stay three answers because they are three different
// statements: a command that answered, one that ran out of time, and one whose caller stopped
// waiting for it.
type probeOutcome int

const (
	// probeExited is the command finishing on its own.
	probeExited probeOutcome = iota
	// probeTimedOut is the deadline arriving first.
	probeTimedOut
	// probeSignalled is this tool being interrupted first.
	probeSignalled
)

// awaitProbeChild settles one probed command.
//
// Every path out of it terminates the tree the probe owns, so a probe that gives up never
// leaves a process behind, and the wait after termination is itself bounded. The path where
// the command already exited does not signal anything: it has been reaped, so its process id is
// no longer a handle on it and signalling that number again would be a guess at whatever now
// owns it.
func awaitProbeChild(done <-chan error, cmd *exec.Cmd, signals <-chan os.Signal,
	deadline time.Duration,
) (error, probeOutcome) {
	select {
	case err := <-done:
		return err, probeExited
	case <-time.After(deadline):
		_ = terminateProbeTree(cmd)
		return awaitReaped(cmd, done), probeTimedOut
	case <-signals:
		_ = terminateProbeTree(cmd)
		return awaitReaped(cmd, done), probeSignalled
	}
}

// awaitReaped waits a bounded time for a terminated command to be reaped.
func awaitReaped(cmd *exec.Cmd, done <-chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(probeTerminateGrace):
		return nil
	}
}

// probeExitStatus maps a settled probe to the status it reports: the command's own status
// when it finished, and a conventional status when the deadline or a signal is what ended it.
func probeExitStatus(err error, outcome probeOutcome) int {
	switch outcome {
	case probeTimedOut:
		return probeTimeoutExit
	case probeSignalled:
		return probeSignalledExit
	case probeExited:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitCode(); code >= 0 {
				return code
			}
		}
		return probeNotFoundExit
	default:
		return probeNotFoundExit
	}
}

// writeProbeStatus reports what one probe did.
//
// The bound is part of the report so a reader of a finding that quotes it learns how long the
// verifier waited rather than trusting a number the caller chose, and whether the command's
// output ended by itself is part of it because a stream the probe had to break is a statement
// about the machine after the command exited.
func writeProbeStatus(status io.Writer, exit int, timedOut, drained bool, deadline time.Duration) {
	fmt.Fprintf(status, "%sexit=%d timed_out=%t drained=%t deadline=%s\n",
		probeStatusPrefix, exit, timedOut, drained, deadline)
}
