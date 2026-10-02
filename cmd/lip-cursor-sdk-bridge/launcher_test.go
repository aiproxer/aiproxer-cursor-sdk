package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestLauncher_ForwardsStreamsAndPropagatesExitStatus proves the launcher is a
// transparent protocol pipe: a payload larger than any internal buffer reaches
// the runtime and comes back byte-identical, and the runtime's exit status is
// propagated unchanged.
func TestLauncher_ForwardsStreamsAndPropagatesExitStatus(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	payload := strings.Repeat("{\"seq\":1}\n", 64*1024) // ~0.5 MiB, well past any copy buffer
	lay := installRunnablePrivateLayout(t, `{"mode":"echo","exit":7}`)
	stdio, stdout, _ := testStdio(t, payload)

	l := newLauncher(mustLayout(t, lay), nil, stdio, testLauncherOptions())
	require.NoError(t, l.Start(context.Background()))
	require.Equal(t, 7, l.Wait())
	require.NoError(t, l.Close())
	require.Equal(t, payload, stdout())
}

// TestLauncher_ForwardsArgumentsToRuntime keeps operator and tooling arguments
// (`--version`, `doctor`) reaching the bridge entry unchanged, after the fixed
// private runtime and entry arguments.
func TestLauncher_ForwardsArgumentsToRuntime(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	for _, args := range [][]string{{"--version"}, {"doctor"}, {"--", "extra"}} {
		argvLog := filepathJoin(t.TempDir(), "argv.log")
		lay := installRunnablePrivateLayout(t, `{"mode":"echo","argvLog":`+strconvQuote(argvLog)+`}`)
		stdio, _, _ := testStdio(t, "")

		l := newLauncher(mustLayout(t, lay), args, stdio, testLauncherOptions())
		require.NoError(t, l.Start(context.Background()))
		require.Equal(t, 0, l.Wait())
		require.NoError(t, l.Close())

		require.Equal(t, append([]string{lay.entry}, args...), readLines(t, argvLog))
	}
}

// TestLauncher_MissingPrivateRuntimeStartsNothing keeps a prerequisite failure
// before any resource escape: with the private runtime or entry missing, the
// launcher starts no process at all.
func TestLauncher_MissingPrivateRuntimeStartsNothing(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	for _, tc := range []struct {
		name        string
		withRuntime bool
		withEntry   bool
	}{
		{name: "missing_private_runtime", withEntry: true},
		{name: "missing_bridge_entry", withRuntime: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lay := installPrivateLayout(t, tc.withRuntime, tc.withEntry)
			started := atomic.Int32{}
			opts := testLauncherOptions()
			opts.start = func(runtimeSpec, childPipes) (runtimeChild, error) {
				started.Add(1)
				return nil, errors.New("start must not be attempted")
			}
			stdio, _, _ := testStdio(t, "")

			// The layout cannot resolve by design here: the launcher must fail
			// before any process is created.
			l := newLauncher(unresolvedLayoutFor(lay), nil, stdio, opts)
			err := l.Start(context.Background())
			require.Error(t, err)
			require.Equal(t, int32(0), started.Load())
			// A failed start settles immediately instead of hanging a waiter.
			require.Equal(t, exitStartFailed, l.Wait())
			require.NoError(t, l.Close())
		})
	}
}

// TestLauncher_StartupFailureReleasesAcquiredRuntime pins "release partial
// acquisitions on startup failure": a starter that hands back a live runtime
// together with an error must not leak that process.
func TestLauncher_StartupFailureReleasesAcquiredRuntime(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	lay := installRunnablePrivateLayout(t, `{"mode":"hold"}`)
	stdio, _, _ := testStdio(t, "")

	rec := newRecordingChild(t)
	opts := testLauncherOptions()
	opts.start = func(spec runtimeSpec, pipes childPipes) (runtimeChild, error) {
		child, err := startRuntime(spec, pipes)
		if err != nil {
			return nil, err
		}
		rec.attach(child)
		return rec, errors.New("injected start failure after the runtime was created")
	}

	l := newLauncher(mustLayout(t, lay), nil, stdio, opts)
	err := l.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "injected start failure")

	select {
	case <-rec.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime acquired before the startup failure was never terminated")
	}
	select {
	case <-rec.reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime acquired before the startup failure was never reaped")
	}
	require.Equal(t, exitStartFailed, l.Wait())
	require.NoError(t, l.Close())
}

// TestLauncher_ShutdownDuringStartTerminatesAndReapsRuntime covers the blocked
// start versus shutdown race: cleanup ownership is established before the
// runtime escapes, so a shutdown that wins the race still terminates and reaps
// it, and the start reports the closed launcher instead of leaking a process.
func TestLauncher_ShutdownDuringStartTerminatesAndReapsRuntime(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	lay := installRunnablePrivateLayout(t, `{"mode":"hold"}`)
	stdio, _, _ := testStdio(t, "")

	rec := newRecordingChild(t)
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	opts := testLauncherOptions()
	opts.start = func(spec runtimeSpec, pipes childPipes) (runtimeChild, error) {
		child, err := startRuntime(spec, pipes)
		if err != nil {
			return nil, err
		}
		rec.attach(child)
		close(startEntered)
		<-releaseStart
		return rec, nil
	}

	l := newLauncher(mustLayout(t, lay), nil, stdio, opts)
	startErr := make(chan error, 1)
	go func() { startErr <- l.Start(context.Background()) }()

	<-startEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- l.Close() }()
	// Let Close reach the in-flight start before the start is allowed to finish.
	require.Eventually(t, func() bool { return launcherIsStopping(l) }, 5*time.Second, time.Millisecond,
		"close never observed an in-flight start")
	close(releaseStart)

	require.ErrorIs(t, <-startErr, errLauncherClosed)
	require.NoError(t, <-closeDone)

	select {
	case <-rec.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime created during shutdown was never terminated")
	}
	select {
	case <-rec.reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime created during shutdown was never reaped")
	}
	require.Equal(t, int32(1), rec.waitCalls.Load(), "the runtime must be reaped exactly once")
}

// TestLauncher_CloseTerminatesAndReapsRuntimeIgnoringStdinEOF covers bounded
// cleanup of a runtime that ignores the graceful stdin EOF: the launcher must
// escalate to its declared process-tree kill and reap the descendant.
func TestLauncher_CloseTerminatesAndReapsRuntimeIgnoringStdinEOF(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	pidFile := filepathJoin(t.TempDir(), "runtime.pid")
	lay := installRunnablePrivateLayout(t, `{"mode":"hold","pidFile":`+strconvQuote(pidFile)+`}`)
	stdio, stdout, _ := testStdio(t, "graceful payload\n")

	rec := newRecordingChild(t)
	opts := testLauncherOptions()
	opts.start = recordingStart(rec)

	l := newLauncher(mustLayout(t, lay), nil, stdio, opts)
	require.NoError(t, l.Start(context.Background()))
	waitForFile(t, pidFile)

	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("Close did not finish within the bounded cleanup policy")
	}

	require.Equal(t, int32(1), rec.killCalls.Load(), "a runtime ignoring stdin EOF must be killed exactly once")
	require.Equal(t, int32(1), rec.waitCalls.Load())
	require.NotEqual(t, 0, l.Wait(), "a killed runtime must not report success")
	require.Equal(t, "graceful payload\n", stdout(), "stream forwarding must survive cleanup")
	require.NoError(t, l.Close())
}

// TestLauncher_LateSettlementAfterTerminationIsObservedOnce covers a runtime
// that settles after cleanup already started: the settlement is observed
// exactly once, later waiters observe the same completion, and repeated close
// stays idempotent.
func TestLauncher_LateSettlementAfterTerminationIsObservedOnce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	pidFile := filepathJoin(t.TempDir(), "runtime.pid")
	lay := installRunnablePrivateLayout(t, `{"mode":"hold","pidFile":`+strconvQuote(pidFile)+`}`)
	stdio, _, _ := testStdio(t, "")

	rec := newRecordingChild(t)
	opts := testLauncherOptions()
	opts.start = recordingStart(rec)
	// Close must give up on the graceful window while the runtime is still
	// running, so the kill and the settlement race.
	opts.graceTimeout = time.Millisecond

	l := newLauncher(mustLayout(t, lay), nil, stdio, opts)
	require.NoError(t, l.Start(context.Background()))
	waitForFile(t, pidFile)

	var wg sync.WaitGroup
	closes := make([]error, 8)
	waits := make([]int, 8)
	for i := range closes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			closes[i] = l.Close()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			waits[i] = l.Wait()
		}()
	}
	wg.Wait()

	for i := range closes {
		require.NoError(t, closes[i], "close %d", i)
		require.Equal(t, waits[0], waits[i], "wait %d observed a different settlement", i)
		require.NotEqual(t, 0, waits[i])
	}
	require.Equal(t, int32(1), rec.waitCalls.Load(), "the runtime must be reaped exactly once")
	require.LessOrEqual(t, rec.killCalls.Load(), int32(1), "repeated close must not kill twice")
	require.NoError(t, l.Close())
}

// TestLauncher_RepeatedCloseSharesOneCompletion pins idempotent cleanup: every
// caller observes the same result, only one termination and one reap happen,
// and closing an unstarted launcher is a no-op.
func TestLauncher_RepeatedCloseSharesOneCompletion(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	lay := installRunnablePrivateLayout(t, `{"mode":"echo","exit":3}`)
	stdio, _, _ := testStdio(t, "payload\n")

	rec := newRecordingChild(t)
	opts := testLauncherOptions()
	opts.start = recordingStart(rec)

	l := newLauncher(mustLayout(t, lay), nil, stdio, opts)
	require.NoError(t, l.Start(context.Background()))
	require.Equal(t, 3, l.Wait())

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = l.Close()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "close %d", i)
	}
	require.Equal(t, int32(0), rec.killCalls.Load(), "a runtime that already exited must not be killed")
	require.Equal(t, int32(1), rec.waitCalls.Load())
	require.NoError(t, l.Close())

	fresh := newLauncher(mustLayout(t, lay), nil, stdio, testLauncherOptions())
	require.NoError(t, fresh.Close(), "closing a launcher that never started must be a no-op")
}

// TestLauncher_ContextCancellationBeforeStartOwnsNothing keeps a cancelled start
// free of resources.
func TestLauncher_ContextCancellationBeforeStartOwnsNothing(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	lay := installRunnablePrivateLayout(t, `{"mode":"hold"}`)
	stdio, _, _ := testStdio(t, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := newLauncher(mustLayout(t, lay), nil, stdio, testLauncherOptions())
	require.ErrorIs(t, l.Start(ctx), context.Canceled)
	require.NoError(t, l.Close())
	require.Equal(t, exitStartFailed, l.Wait())
}

// TestTerminationHandler_ReleasesItsGoroutine keeps the forwarding handler
// itself bounded: stopping it releases the watcher goroutine and further stops
// stay harmless.
func TestTerminationHandler_ReleasesItsGoroutine(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	lay := installRunnablePrivateLayout(t, `{"mode":"hold"}`)
	stdio, _, _ := testStdio(t, "")

	l := newLauncher(mustLayout(t, lay), nil, stdio, testLauncherOptions())
	require.NoError(t, l.Start(context.Background()))
	stop := installTerminationHandler(l)
	stop()
	stop()
	require.NoError(t, l.Close())
}

// --- test doubles ---------------------------------------------------------

// recordingChild observes termination and reaping of the owned runtime
// descendant without changing its behavior.
type recordingChild struct {
	runtimeChild

	killCalls atomic.Int32
	waitCalls atomic.Int32
	killed    chan struct{}
	reaped    chan struct{}
}

func newRecordingChild(tb testing.TB) *recordingChild {
	tb.Helper()
	return &recordingChild{killed: make(chan struct{}), reaped: make(chan struct{})}
}

func (r *recordingChild) attach(child runtimeChild) {
	r.runtimeChild = child
}

func (r *recordingChild) Kill() error {
	r.killCalls.Add(1)
	close(r.killed)
	return r.runtimeChild.Kill()
}

func (r *recordingChild) Wait() error {
	r.waitCalls.Add(1)
	close(r.reaped)
	return r.runtimeChild.Wait()
}

// recordingStart wraps the production start seam so termination and reaping of
// the owned runtime descendant are observable.
func recordingStart(rec *recordingChild) startFunc {
	return func(spec runtimeSpec, pipes childPipes) (runtimeChild, error) {
		child, err := startRuntime(spec, pipes)
		if err != nil {
			return nil, err
		}
		rec.attach(child)
		return rec, nil
	}
}

// launcherIsStopping observes the launcher's own state for a deterministic
// shutdown-during-start barrier.
func launcherIsStopping(l *Launcher) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == launcherStopping
}

func testLauncherOptions() launcherOptions {
	return launcherOptions{
		graceTimeout: 100 * time.Millisecond,
		killTimeout:  10 * time.Second,
	}
}

// testStdio gives the launcher real files for its own streams so stream
// forwarding is deterministic and no test goroutine blocks on a pipe.
func testStdio(tb testing.TB, stdin string) (stdioSet, func() string, func() string) {
	tb.Helper()
	dir := tb.TempDir()
	in := filepathJoin(dir, "stdin")
	require.NoError(tb, os.WriteFile(in, []byte(stdin), 0o600))
	inFile, err := os.Open(in)
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = inFile.Close() })

	out := filepathJoin(dir, "stdout")
	errFile := filepathJoin(dir, "stderr")
	for _, path := range []string{out, errFile} {
		f, openErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		require.NoError(tb, openErr)
		tb.Cleanup(func() { _ = f.Close() })
	}
	outFile, err := os.OpenFile(out, os.O_RDWR, 0o600)
	require.NoError(tb, err)
	errOut, err := os.OpenFile(errFile, os.O_RDWR, 0o600)
	require.NoError(tb, err)
	tb.Cleanup(func() {
		_ = outFile.Close()
		_ = errOut.Close()
	})
	return stdioSet{in: inFile, out: outFile, err: errOut},
		func() string { return readFileString(tb, out) },
		func() string { return readFileString(tb, errFile) }
}
