package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// probeTestDeadline is the bound the cases below give a probed child. It is a test seam,
// not the shipped bound: the shipped one is the constant the probe verb applies, and a case
// that had to wait it out would prove nothing about a hang.
//
// It is not a tight one, because the staged executable is this test binary re-executed, and
// under the race detector a re-executed test binary takes long enough to start that a bound
// meant for an unrecoverable hang would read as one. It has to stay far below the bounds the
// cases wait on below, so those still mean what they say.
const probeTestDeadline = 3 * time.Second

// probeReapGrace is how long a case waits before concluding that a descendant the probe was
// supposed to terminate outlived it. It is far longer than the delay that descendant waits
// before recording itself, so a survivor is observed rather than raced against.
const probeReapGrace = 12 * time.Second

// descendantMarkDelay is how long the descendant of a hanging child waits before recording
// that it was still alive. It is far longer than the probe's deadline, and longer than the
// drain bound the probe applies to the stream that descendant inherited, so a marker appears
// if and only if the descendant was never terminated.
const descendantMarkDelay = 8 * time.Second

// descendantHoldBound is how long a descendant keeps holding the output stream after it has
// recorded itself.
//
// The recording and the holding are separate on purpose. A descendant that exited the moment it
// recorded itself would also stop holding the stream, and the probe would report that stream as
// drained - so whether the probe could reach that descendant would be decided by a race between
// its drain grace and the descendant's exit, instead of by whether the descendant was still
// there. It is bounded rather than endless so a case that is not cleaned up does not leave a
// process behind for the rest of the lane.
const descendantHoldBound = 60 * time.Second

// probeHarnessBound is how long a case waits for a probe to return at all. It is far longer
// than any probe here should take and far shorter than the Go test timeout, so a probe that
// never returns fails its case instead of stalling the lane.
const probeHarnessBound = 30 * time.Second

// TestProbe_ReportsTheProbedExitStatusAndOutput keeps a probe that runs exactly what it was
// pointed at.
//
// A probe exists so the packaging scripts can ask a staged executable a question and report
// what it answered. Losing the child's exit status, or reducing its two output streams to one
// arbitrary ordering, would leave the caller deciding from whatever it happened to receive, so
// both have to arrive unchanged.
func TestProbe_ReportsTheProbedExitStatusAndOutput(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		mode     string
		wantExit string
		want     []string
	}{
		{name: "succeeds", mode: "ok", wantExit: "0", want: []string{"v22.17.0"}},
		{name: "fails", mode: "fail", wantExit: "3", want: []string{"out:ok", "err:ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, status := runProbeChild(t, probeTestDeadline, tc.mode)
			require.Equal(t, tc.wantExit, probeStatusField(t, status, "exit"),
				"a probe has to report the exit status the child itself produced:\n%s", status)
			require.Equal(t, "false", probeStatusField(t, status, "timed_out"),
				"a child that answered in time is not a timeout:\n%s", status)
			for _, want := range tc.want {
				require.Contains(t, output, want,
					"the probe has to forward the child's own output:\n%s", status)
			}
		})
	}
}

// TestProbe_FailsAChildThatOutlivesItsDeadlineInsteadOfWaitingForIt keeps a replaced or
// wedged interactive binary a finding rather than a hang.
//
// The private runtime and the bridge launcher are executables this project ships but cannot
// vouch for at verification time: an operator's install can hold a stub, a wrong build, or
// anything else that never answers. Waiting for one is how a verification gate stops producing
// evidence at all, so the probe has a finite bound, reports that the bound was reached, and
// returns.
func TestProbe_FailsAChildThatOutlivesItsDeadlineInsteadOfWaitingForIt(t *testing.T) {
	t.Parallel()

	output, status := runProbeChild(t, probeTestDeadline, "hang")
	require.Equal(t, "true", probeStatusField(t, status, "timed_out"),
		"a child that never answered has to be reported as a timeout, not as an exit status:\n%s", status)
	require.Equal(t, strconv.Itoa(probeTimeoutExit), probeStatusField(t, status, "exit"),
		"a timed-out probe reports the conventional timeout status:\n%s", status)
	require.NotEmpty(t, probeStatusField(t, status, "deadline"),
		"the report has to name the bound that was applied, so a reader knows how long it waited:\n%s", status)
	require.Empty(t, strings.TrimSpace(output),
		"a child that answered nothing must not have its silence padded into content:\n%s", status)
}

// TestProbe_ReturnsWhenADescendantHoldsTheOutputAfterTheChildExited keeps a surviving
// descendant from holding the caller hostage, and says so rather than claiming otherwise.
//
// The probed command's output reaches the caller through this tool, so this tool has to own
// that pipe rather than hand the command its own stdout. If it hands its own stdout over, a
// descendant the command started inherits the pipe the calling shell is reading the answer out
// of, and the caller's read blocks until that descendant exits - which, for a staged executable
// nobody vouched for, may be never. The command here exits cleanly and immediately; only its
// descendant is still holding the stream, which is the case waiting on the command alone would
// not notice.
//
// The second half pins the limit. That descendant is inside the command's process tree, and a
// probe could terminate it - but the command has already been reaped, so its pid is no longer a
// handle on it and signalling that number again would be a guess at whatever now owns it. The
// probe therefore reports that the stream did not drain, and the descendant records that it
// was still alive. A probe that ever reported a survivor as cleaned up fails here.
func TestProbe_ReturnsWhenADescendantHoldsTheOutputAfterTheChildExited(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "descendant survived.txt")
	output, status := runProbeChild(t, probeTestDeadline, "exit-holding-stream",
		marker, strconv.Itoa(int(descendantMarkDelay/time.Second)))

	require.Equal(t, "0", probeStatusField(t, status, "exit"),
		"the command itself finished successfully, so that is the status to report:\n%s", status)
	require.Equal(t, "false", probeStatusField(t, status, "timed_out"),
		"the command did not time out; something it started outlived it:\n%s", status)
	require.Equal(t, "false", probeStatusField(t, status, "drained"),
		"an output stream still held after the command exited did not drain on its own:\n%s", status)
	require.Contains(t, output, "out:held",
		"what the command did print still has to reach the caller:\n%s", status)

	// The descendant writes the marker only if it outlived the probe.
	time.Sleep(probeReapGrace)
	_, err := os.Stat(marker)
	require.NoError(t, err,
		"a command that finished on its own must not be signalled after it was reaped: "+
			"its pid is no longer a handle on it, so a signal would be a guess")
}

// TestProbe_ReleasesTheOutputACommandItGaveUpOnStillHeld keeps the bounded wait from trading
// one hang for a leak.
//
// The child a probe gives up on is a launcher that may already own a runtime of its own, so
// terminating only the process the probe started would leave the one actually consuming the
// CPU. Terminating the tree has to release the output stream too, or the caller waits on a
// stream held by a process that is already gone. The descendant below records that it was still
// alive long after the deadline; a marker on disk is the only portable evidence that the probe
// terminated the whole tree.
func TestProbe_ReleasesTheOutputACommandItGaveUpOnStillHeld(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "descendant survived.txt")
	output, status := runProbeChild(t, probeTestDeadline, "hang-holding-stream",
		marker, strconv.Itoa(int(descendantMarkDelay/time.Second)))

	require.Equal(t, "true", probeStatusField(t, status, "timed_out"),
		"a command that never answered has to be reported as a timeout:\n%s", status)
	require.Equal(t, "true", probeStatusField(t, status, "drained"),
		"terminating the tree has to release the output stream it was holding:\n%s", status)
	require.Contains(t, output, "out:held",
		"what the command printed before the bound arrived still reaches the caller:\n%s", status)

	time.Sleep(probeReapGrace)
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist,
		"a descendant of a child the probe gave up on survived it, so the wait was bounded only for the direct child")
}

// TestProbe_ReportsADescendantThatLeftTheTreeInsteadOfClaimingItIsGone keeps the honest limit
// honest.
//
// A descendant that has detached from the probed command's process group is outside the
// process-group policy on POSIX and outside taskkill's ancestry walk on Windows, so the probe
// cannot promise it is gone. What it can promise is that its own caller stops reading. Here the
// descendant leaves the group, so the probe has to give up on the stream and say so.
//
// The escape is a POSIX property and the case is stated as one: Windows exposes no process
// group to leave, and taskkill's walk reaches everything the probed command started.
func TestProbe_ReportsADescendantThatLeftTheTreeInsteadOfClaimingItIsGone(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows has no process group for a descendant to leave, so this escape cannot be staged")
	}
	marker := filepath.Join(t.TempDir(), "descendant survived.txt")
	_, status := runProbeChild(t, probeTestDeadline, "hang-detached-stream",
		marker, strconv.Itoa(int(descendantMarkDelay/time.Second)))

	require.Equal(t, "true", probeStatusField(t, status, "timed_out"),
		"a command that never answered has to be reported as a timeout:\n%s", status)
	require.Equal(t, "false", probeStatusField(t, status, "drained"),
		"a stream held by a descendant outside the tree has to be reported as undrained:\n%s", status)

	time.Sleep(probeReapGrace)
	_, err := os.Stat(marker)
	require.NoError(t, err,
		"the descendant is expected to be out of reach here, which is why the probe reports rather than claims")
}

// TestProbe_KeepsItsOwnStatusStreamOutOfReachesOfTheProbedCommand keeps a probed command from
// writing the report the caller reads.
//
// The status line is the only channel the caller parses, so it cannot share a stream with the
// command's own output. A staged executable that printed a well-formed status line would
// otherwise be able to answer for itself. Here it prints one on both of its streams and exits
// with a status of its own; the parsed report has to be the probe's, and the imitation has to
// arrive as content.
func TestProbe_KeepsItsOwnStatusStreamOutOfReachesOfTheProbedCommand(t *testing.T) {
	t.Parallel()

	spoof := probeStatusPrefix + " exit=0 timed_out=false drained=true deadline=1ns"
	output, status := runProbeChild(t, probeTestDeadline, "spoof", spoof)

	require.Equal(t, "3", probeStatusField(t, status, "exit"),
		"the parsed status has to be the one the command really left behind:\n%s", status)
	require.Equal(t, probeTestDeadline.String(),
		probeStatusField(t, status, "deadline"),
		"the bound in the report has to be the bound the probe applied:\n%s", status)
	require.Contains(t, output, spoof,
		"what the command printed is content, and content has to survive as content:\n%s", status)
	require.Equal(t, 1, strings.Count(strings.TrimSpace(status), probeStatusPrefix),
		"the report stream carries the probe's own line and nothing the command wrote:\n%s", status)
	require.True(t, strings.HasPrefix(strings.TrimSpace(status), probeStatusPrefix),
		"the report line is found by its reserved prefix:\n%s", status)
}

// TestProbe_TerminatesTheDescendantsOfAChildItGaveUpOn keeps the bounded wait from trading
// one hang for a leak.
//
// The child a probe gives up on is a launcher that may already own a runtime of its own, so
// terminating only the process the probe started would leave the one actually consuming the
// CPU. The descendant below records that it was still alive long after the deadline; a marker
// on disk is the only portable evidence that the probe terminated the whole tree.
func TestProbe_TerminatesTheDescendantsOfAChildItGaveUpOn(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "descendant survived.txt")
	_, status := runProbeChild(t, probeTestDeadline, "hang-tree",
		marker, strconv.Itoa(int(descendantMarkDelay/time.Second)))
	require.Equal(t, "true", probeStatusField(t, status, "timed_out"),
		"the probe has to report the timeout it decided:\n%s", status)

	// The descendant writes the marker only if it outlived the probe, so waiting several
	// times the deadline is what turns "no marker" into evidence.
	time.Sleep(probeReapGrace)
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist,
		"a descendant of a child the probe gave up on survived it, so the wait was bounded only for the direct child")
}

// TestProbe_TakesItsTreeDownWhenItIsInterrupted keeps a terminal signal from stranding the
// probed command.
//
// The probed command is deliberately outside this process's signal group, so that terminating
// its tree does not terminate this tool. That makes a signal something this tool has to act
// on rather than something that reaches the command by itself: without it, interrupting a
// verification would leave the runtime it was waiting on running.
//
// Windows delivers no terminal signal to an unrelated process, so the channel is handed to the
// wait directly. That is the same decision probeTerminationSignalList makes on each platform,
// and it is what this case can exercise everywhere.
func TestProbe_TakesItsTreeDownWhenItIsInterrupted(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "descendant survived.txt")
	child := probeChildCommand("hang-tree", marker, strconv.Itoa(int(descendantMarkDelay/time.Second)))
	cmd := exec.Command(child[0], child[1:]...)
	var out, status bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &status
	// The signal path is handed to the wait directly rather than raised, because Windows
	// delivers no terminal signal to an unrelated process. Everything else about this child
	// has to be the child the probe really starts - including the configuration that puts it
	// outside this process's signal group, which is what makes the tree it owns reachable
	// without the signal reaching this test binary. A case that skipped it would be asserting
	// about a tree the probe cannot terminate, on POSIX.
	configureProbeChild(cmd)
	require.NoError(t, cmd.Start())

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	waitErr, outcome := awaitProbeChild(done, cmd, signals, time.Hour)

	require.Equal(t, probeSignalled, outcome,
		"an interrupted probe has to say it was interrupted rather than that it finished or timed out")
	require.Equal(t, probeSignalledExit, probeExitStatus(waitErr, outcome),
		"an interrupted probe reports the conventional signalled status")

	time.Sleep(probeReapGrace)
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist,
		"an interrupted probe has to take the tree it owns down with it")
}

// TestProbe_ReportsAChildItCannotStart keeps an unstartable probe a reportable answer.
//
// A staged executable that cannot be created is a prerequisite failure the operator has to
// read, not a crash of the tool that noticed it. The status line is the only channel the
// caller reads, so it has to carry that answer too.
func TestProbe_ReportsAChildItCannotStart(t *testing.T) {
	t.Parallel()

	var output, status bytes.Buffer
	require.NoError(t, probeCommand(probeTestDeadline,
		[]string{filepath.Join(t.TempDir(), "no-such-runtime")}, &output, &status))
	require.Equal(t, "false", probeStatusField(t, status.String(), "timed_out"),
		"a child that never started did not time out:\n%s", status.String())
	require.Equal(t, strconv.Itoa(probeNotFoundExit), probeStatusField(t, status.String(), "exit"),
		"a child that never started has no successful status:\n%s", status.String())
	// Nothing ran, so nothing was ever handed the stream and there is no descendant to
	// have outlived it. Reporting the stream as undrained here would be a claim about a
	// process that does not exist, and it is the claim a caller reads to decide whether
	// something was left behind.
	require.Equal(t, "true", probeStatusField(t, status.String(), "drained"),
		"a child that never started cannot have left its output open:\n%s", status.String())
	require.Contains(t, status.String(), "cannot start",
		"the report has to say the child could not be started:\n%s", status.String())
	require.Empty(t, output.String(), "a child that never started produced no output")
}

// TestProbe_DeliversTheProbedCommandVerbatim keeps the probed command out of this tool's own
// argument handling. The private runtime is probed with -p and --version and the launcher
// with a subcommand of its own, so a probed argument that reached anything but the child would
// answer for a question the caller never asked.
func TestProbe_DeliversTheProbedCommandVerbatim(t *testing.T) {
	t.Parallel()

	// The child decides its mode from the first argument after --, so this run only
	// answers "ok" if that separator reached it untouched.
	output, status := runProbeChild(t, probeTestDeadline, "ok")
	require.Equal(t, "0", probeStatusField(t, status, "exit"),
		"the probed command has to run with its own arguments:\n%s", status)
	require.Contains(t, output, "v22.17.0", "the probed command's own answer is the answer:\n%s", status)
}

// TestProbe_RefusesAnEmptyCommand keeps a probe with nothing to run out of the answer path.
// A status line describing a probe that never had a subject would be a claim about nothing.
func TestProbe_RefusesAnEmptyCommand(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{nil, {}} {
		var output, status bytes.Buffer
		err := probeCommand(probeTestDeadline, argv, &output, &status)
		require.Error(t, err, "argv %v has to be refused", argv)
		require.Contains(t, err.Error(), "probe")
		require.Empty(t, status.String(), "a refused probe reports through its own error, not through a status line")
	}
}

// runProbeChild runs one probe against the test binary acting as the staged executable and
// returns what a caller of the probe verb sees.
//
// The call is bounded on purpose: the failure this file exists for is a probe that never
// returns, so a case that only waited would stall the lane instead of failing.
func runProbeChild(tb testing.TB, deadline time.Duration, mode string, extra ...string) (output, status string) {
	tb.Helper()

	var outBuf, statusBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(tb, probeCommand(deadline, probeChildCommand(mode, extra...), &outBuf, &statusBuf))
	}()

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(probeHarnessBound):
		tb.Fatalf("the probe did not return within %s, so a caller reading its output would still be waiting:\n%s",
			probeHarnessBound, outBuf.String())
	}
	return outBuf.String(), statusBuf.String()
}

// probeChildCommand renders the argv that runs this test binary as a staged executable.
func probeChildCommand(mode string, extra ...string) []string {
	return append([]string{os.Args[0], "-test.run=TestProbeChildProcess", "--", mode}, extra...)
}

// probeStatusField reads one field of the probe status line.
func probeStatusField(tb testing.TB, status, name string) string {
	tb.Helper()

	for _, token := range strings.Fields(status) {
		if key, value, ok := strings.Cut(token, "="); ok && key == name {
			return value
		}
	}
	tb.Fatalf("the probe status line carries no %s field:\n%s", name, status)
	return ""
}

// TestProbeChildProcess is the staged executable the cases above probe. It is a child of this
// same test binary, so the probe contract needs no fixture executable built and staged, and
// the mode argument is what decides what the staged executable does.
func TestProbeChildProcess(t *testing.T) {
	mode, args := probeChildMode()
	switch mode {
	case "":
		// The ordinary test run: this binary is the test suite here, not a staged
		// executable, and there is nothing for it to stand in for.
		return
	case "ok":
		os.Stdout.WriteString("v22.17.0\n")
		os.Exit(0)
	case "fail":
		os.Stdout.WriteString("out:ok\n")
		os.Stderr.WriteString("err:ok\n")
		os.Exit(3)
	case "spoof":
		// A staged executable that imitates the probe's own report, on both of its
		// streams: the caller has to be reading the probe's line, not this one.
		line := ""
		if len(args) > 0 {
			line = args[0]
		}
		os.Stdout.WriteString(line + "\n")
		os.Stderr.WriteString(line + "\n")
		os.Exit(3)
	case "hang":
		blockProbeChild()
	case "hang-holding-stream":
		// Whatever the command printed before the bound arrived still has to reach the
		// caller, so this mode says something before it stops answering.
		startStreamHoldingDescendant(args, false)
		os.Stdout.WriteString("out:held\n")
		os.Stdout.Sync()
		blockProbeChild()
	case "hang-detached-stream":
		startStreamHoldingDescendant(args, true)
		blockProbeChild()
	case "block":
		// A descendant that inherits the output streams and never writes to them again, so
		// the stream stays open long after its parent is gone.
		blockProbeChild()
	case "exit-holding-stream":
		// The descendant inherits this process's output streams and outlives it, which is
		// the whole point: whatever this process was writing to stays open after it exits.
		startStreamHoldingDescendant(args, false)
		os.Stdout.WriteString("out:held\n")
		os.Stdout.Sync()
		os.Exit(0)
	case "hang-tree":
		if len(args) < 2 {
			os.Stderr.WriteString("probe child: hang-tree needs a marker path and a delay\n")
			os.Exit(2)
		}
		// The descendant outlives this process on purpose: a probe that terminated only
		// the child it started would leave it running, which is what the case observes.
		if err := exec.Command(os.Args[0], "-test.run=TestProbeChildProcess", "--",
			"mark-after", args[0], args[1]).Start(); err != nil {
			os.Stderr.WriteString("probe child: start descendant: " + err.Error() + "\n")
			os.Exit(2)
		}
		blockProbeChild()
	case "mark-after":
		if len(args) < 2 {
			os.Exit(2)
		}
		seconds, err := strconv.Atoi(args[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Duration(seconds) * time.Second)
		_ = os.WriteFile(args[0], []byte("descendant survived the probe\n"), 0o600)
		// The recording is not the end of this descendant. It still holds the output stream
		// it inherited, so whether the probe reports that stream as drained says whether the
		// probe could reach this process - not whether it happened to still be running.
		holdProbeDescendant(descendantHoldBound)
		os.Exit(0)
	default:
		os.Stderr.WriteString("probe child: unknown mode " + mode + "\n")
		os.Exit(2)
	}
}

// startStreamHoldingDescendant starts a descendant that keeps this process's output streams
// open after this process is gone, recording itself in the named file when the case asked for
// one.
//
// The streams are named explicitly rather than left unset, because a child with no output
// streams gets the null device: inheriting is what has to be stated to happen.
//
// detached puts the descendant in a session of its own where the platform has sessions, which
// is what makes it unreachable through the process group the probe terminates. That is the
// escape the probe has to report rather than promise to clean up.
func startStreamHoldingDescendant(args []string, detached bool) {
	child := exec.Command(os.Args[0], "-test.run=TestProbeChildProcess", "--", "mark-after", "unused", "3600")
	if len(args) > 1 {
		child = exec.Command(os.Args[0], "-test.run=TestProbeChildProcess", "--",
			"mark-after", args[0], args[1])
	}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if detached {
		child.SysProcAttr = detachedChildAttrs()
	}
	if err := child.Start(); err != nil {
		os.Stderr.WriteString("probe child: start stream holder: " + err.Error() + "\n")
		os.Exit(2)
	}
}

// holdProbeDescendant keeps a descendant alive, still holding the streams it inherited, for the
// given bound.
func holdProbeDescendant(bound time.Duration) {
	time.Sleep(bound)
}

// probeChildMode reads the mode a re-executed test binary was asked for. Everything after the
// -- separator belongs to the mode, because a probed command owns its own arguments.
func probeChildMode() (mode string, args []string) {
	for i, arg := range os.Args {
		if arg != "--" || i+1 >= len(os.Args) {
			continue
		}
		return os.Args[i+1], os.Args[i+2:]
	}
	return "", nil
}

// blockProbeChild keeps a staged executable alive without exiting, which is what a replaced
// or wedged private runtime looks like from outside.
func blockProbeChild() {
	time.Sleep(time.Hour)
	os.Exit(0)
}
