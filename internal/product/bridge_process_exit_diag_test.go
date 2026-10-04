package product

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bridge that rejects a startup precondition reports it on stderr and exits,
// and the packaged launcher does exactly that for an unprovisioned SDK tree. The
// connector therefore has to survive a child that is already gone before it
// writes the initialize frame: the write fails with the pipe's own error, which
// names neither the exit status nor the diagnostic the child wrote on its way
// out. These tests pin that contract deterministically: the owner's terminal
// fault wins over the pipe error, the fault is stamped only after the stderr
// reader has drained what the child wrote, a call whose deadline expired is not
// held open by that observation, and the generation keeps exactly one wait owner.

// exitedBridgeProc is a process that exited before the connector wrote to it.
// Nothing holds the read end of its stdin pipe, its stdout is at EOF, and its
// stderr already carries the whole diagnostic. The stderr reader and Wait are
// held by test-controlled gates, so a test decides exactly when the owner can
// observe the exit instead of racing the scheduler for it.
//
// Wait closes the stderr read end, which is what exec.Cmd.Wait does once the
// child is gone. That is the whole reason the retained stderr has to be drained
// before the child is waited on: a reader that has not copied the child's output
// yet loses it to that close, and the fault is stamped without the remedy.
type exitedBridgeProc struct {
	stdin  *observedStdin
	stdout io.ReadCloser
	stderr *gatedStderr

	drainGate  chan struct{}
	readHooked chan struct{}
	waitGate   chan struct{}

	drainOnce   sync.Once
	readOnce    sync.Once
	waitOnce    sync.Once
	waitEndOnce sync.Once
	wroteOnce   sync.Once
}

func newExitedBridgeProc(t *testing.T, diagnostic string) *exitedBridgeProc {
	t.Helper()
	// The child end of stdin is already closed, so a frame write fails with the
	// platform's broken-pipe error exactly as it does for a dead child.
	inR, inW, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, inR.Close())
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, outW.Close())
	errR, errW, err := os.Pipe()
	require.NoError(t, err)
	// The child writes and closes from its own goroutine: a diagnostic larger
	// than the platform pipe buffer would otherwise block the test before the
	// connector starts reading, which is also how a real child behaves.
	go func() {
		_, _ = io.WriteString(errW, diagnostic)
		_ = errW.Close()
	}()

	p := &exitedBridgeProc{
		stdout:     outR,
		drainGate:  make(chan struct{}),
		readHooked: make(chan struct{}),
		waitGate:   make(chan struct{}),
	}
	p.stdin = &observedStdin{w: inW, wrote: make(chan struct{}), owner: p}
	p.stderr = &gatedStderr{r: errR, gate: p.drainGate, hooked: p.readHooked}
	t.Cleanup(p.openDrain)
	t.Cleanup(p.openWait)
	return p
}

func (p *exitedBridgeProc) PID() int              { return 970001 }
func (p *exitedBridgeProc) Stdin() io.WriteCloser { return p.stdin }
func (p *exitedBridgeProc) Stdout() io.ReadCloser { return p.stdout }
func (p *exitedBridgeProc) Stderr() io.ReadCloser { return p.stderr }
func (p *exitedBridgeProc) Kill() error           { return nil }

func (p *exitedBridgeProc) Wait() error {
	<-p.waitGate
	p.waitEndOnce.Do(func() { _ = p.stderr.Close() })
	return errors.New("exit status 78")
}

func (p *exitedBridgeProc) openDrain() { p.drainOnce.Do(func() { close(p.drainGate) }) }
func (p *exitedBridgeProc) openWait()  { p.waitOnce.Do(func() { close(p.waitGate) }) }

func (p *exitedBridgeProc) writeReturned() <-chan struct{} { return p.stdin.wrote }

// readStarted reports that the connector's stderr reader has reached its first
// read. Until the test releases the drain, the reader owns nothing the child
// wrote, so an owner that stamps a fault before the drain has provably stamped it
// from an empty buffer.
func (p *exitedBridgeProc) readStarted() <-chan struct{} { return p.readHooked }

// observedStdin reports when a frame write has returned, so a test can act on
// the pipe error instead of guessing when the connector produced it.
type observedStdin struct {
	w     io.Writer
	wrote chan struct{}
	owner *exitedBridgeProc
}

func (o *observedStdin) Write(p []byte) (int, error) {
	n, err := o.w.Write(p)
	o.owner.wroteOnce.Do(func() { close(o.wrote) })
	return n, err
}

func (o *observedStdin) Close() error { return nil }

// gatedStderr parks the connector's stderr reader at its first read until the
// test releases it, then reads the child's whole diagnostic and reports EOF.
type gatedStderr struct {
	r      io.ReadCloser
	gate   <-chan struct{}
	hooked chan struct{}
	once   sync.Once
}

func (g *gatedStderr) Read(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.hooked)
		select {
		case <-g.gate:
		case <-time.After(10 * time.Second):
		}
	})
	return g.r.Read(p)
}

func (g *gatedStderr) Close() error { return g.r.Close() }

// unprovisionedSDKDiagnostic mirrors the launcher's prerequisite failure: it
// names what is missing, the pinned version, and the one command that fixes it.
const unprovisionedSDKDiagnostic = "lip-cursor-sdk-bridge: @cursor/sdk is not provisioned: " +
	"\"[PATH]/node_modules/@cursor/sdk/package.json\" not found, and this archive ships no @cursor/sdk; " +
	"required version 1.0.23. Provision it once with:\n  cd [PATH] && ../node/node ci --omit=dev\n"

func TestBridgeProcess_ExitedBridgeWriteReportsExitAndDiagnostic(t *testing.T) {
	proc := newExitedBridgeProc(t, unprovisionedSDKDiagnostic)
	proc.openDrain()
	proc.openWait()
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: &recordingStarter{next: func([]string, string, []string) (Process, error) {
			return proc, nil
		}},
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not provisioned")
	assert.Contains(t, err.Error(), "1.0.23")
	assert.Contains(t, err.Error(), "ci --omit=dev")
	var fault *BridgeFault
	require.ErrorAs(t, err, &fault)
	assert.Equal(t, CodeBridgeExited, fault.Code)
	assert.ErrorIs(t, err, ErrBridgeExited)
}

func TestBridgeProcess_ExitDiagnosticIsDrainedBeforeTheFaultIsStamped(t *testing.T) {
	// The diagnostic spans several reads but stays inside the retained-stderr
	// bound, so a drain that stops early loses the tail marker.
	const head = "lip-cursor-sdk-bridge: private runtime is not provisioned\n"
	const tail = "\nlip-cursor-sdk-bridge: provision it once, then retry\n"
	proc := newExitedBridgeProc(t, head+strings.Repeat("x", 5000)+tail)
	proc.openWait()
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: &recordingStarter{next: func([]string, string, []string) (Process, error) {
			return proc, nil
		}},
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	errCh := make(chan error, 1)
	go func() {
		_, err := bp.EnsureReady(context.Background())
		errCh <- err
	}()

	select {
	case <-proc.writeReturned():
	case <-time.After(5 * time.Second):
		t.Fatal("the connector never wrote the initialize frame")
	}
	// The child's diagnostic is unread while the reader is parked here. An owner
	// that waits for the child first lets Wait close this read end underneath it,
	// and the fault it stamps then carries a bare exit status instead of the
	// remedy. Releasing the reader only once it is provably parked is what makes
	// that difference observable instead of a scheduling accident.
	select {
	case <-proc.readStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("the connector never read the bridge's stderr")
	}
	proc.openDrain()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "private runtime is not provisioned")
		assert.Contains(t, err.Error(), "provision it once, then retry")
	case <-time.After(10 * time.Second):
		t.Fatal("the failed start never settled")
	}
}

func TestBridgeProcess_OwnerExitFaultIsRecordedAndReportedToItsOwnGeneration(t *testing.T) {
	// The exit fault of a generation is produced by the process owner observing the
	// child exit, and by nothing else. This drives that whole path: a child that
	// wrote its diagnostic and exited before the handshake reached it, the owner's
	// settlement of that generation, and then a call against the settled
	// generation. Seeding the recorded fault here instead would prove only that a
	// field is read back, which is why the fault is asserted as the owner's output
	// of a real exit rather than as an input to the fence.
	const head = "lip-cursor-sdk-bridge: private runtime is not provisioned\n"
	const tail = "\nlip-cursor-sdk-bridge: provision it once, then retry\n"
	proc := newExitedBridgeProc(t, head+strings.Repeat("x", 5000)+tail)
	proc.openDrain()
	proc.openWait()
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: &recordingStarter{next: func([]string, string, []string) (Process, error) {
			return proc, nil
		}},
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	// The handshake fails on a bridge that is already gone, and the reason it
	// reports is the owner's fault: the child's own diagnostic, not a pipe error.
	_, startErr := bp.EnsureReady(context.Background())
	require.Error(t, startErr)
	assert.Contains(t, startErr.Error(), "private runtime is not provisioned")
	assert.Contains(t, startErr.Error(), "provision it once, then retry")

	gen := bp.Generation()
	require.Positive(t, gen)
	bp.mu.Lock()
	settled := bp.waitDone
	bp.mu.Unlock()
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("the process owner never settled the exited generation")
	}

	bp.mu.Lock()
	recorded := bp.exitFault
	bp.mu.Unlock()
	require.NotNil(t, recorded, "the process owner must record the exit fault it observed")
	assert.Equal(t, CodeBridgeExited, recorded.Code)
	assert.Contains(t, recorded.Diag, "private runtime is not provisioned")
	assert.Contains(t, recorded.Diag, "provision it once, then retry")

	// A call against that settled generation reports the owner's fault rather than
	// a generation fence the caller can do nothing with.
	frame, err := bp.callOnProc(context.Background(), proc, gen, protocol.MethodModelsList, json.RawMessage(`{}`))
	require.Nil(t, frame)
	require.Error(t, err)
	var fault *BridgeFault
	require.ErrorAs(t, err, &fault)
	assert.Equal(t, CodeBridgeExited, fault.Code)
	assert.Contains(t, err.Error(), "private runtime is not provisioned")
	assert.EqualValues(t, 0, bp.pendingCount())

	// The fault belongs to the generation that produced it, so a call that lost its
	// generation stays fenced and never reads the fault of the runtime that
	// replaced it.
	_, stale := bp.callOnProc(context.Background(), proc, gen-1, protocol.MethodModelsList, json.RawMessage(`{}`))
	require.Error(t, stale)
	assert.EqualError(t, stale, "cursorsdk: bridge generation invalid")
	var staleFault *BridgeFault
	assert.False(t, errors.As(stale, &staleFault),
		"a stale generation must not receive the live generation's exit fault")
	assert.EqualValues(t, 0, bp.pendingCount())
}

func TestBridgeProcess_WriteFailureHonorsTheCallersDeadline(t *testing.T) {
	// A bridge that is still alive and refuses the frame is a failure its caller is
	// still waiting for, so the observation that decides what to report cannot
	// outlive that caller's own deadline. The deadline is the caller's, expressed as
	// an explicit context on the call rather than as a connector-wide timeout: a
	// cancel deadline is the shortest one this connector accepts, and a write that
	// failed a moment before it expires must not be held open for the observation
	// grace on top of it.
	proc := newFakeProc(970008)
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: &recordingStarter{next: func([]string, string, []string) (Process, error) {
			go serveFakeBridgeRPC(t, proc, nil)
			return proc, nil
		}},
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	// The child stays alive and now refuses every frame, which is a write failure
	// the owner will never stamp a fault for.
	require.NoError(t, proc.stdinR.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = bp.Call(ctx, protocol.MethodModelsList, json.RawMessage(`{}`))
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, bridgeObservationGrace,
		"a write failure must not be reported after the caller's deadline plus the observation grace")
	assert.EqualValues(t, 0, bp.pendingCount(),
		"a call whose write failed must not stay registered")
}

func TestBridgeProcess_LiveGenerationWriteFailureKeepsWriteError(t *testing.T) {
	// A bridge that is still alive and refuses the frame is a different failure:
	// reporting it as an exit would name a lifecycle event that never happened.
	proc := newFakeProc(970002)
	require.NoError(t, proc.stdinR.Close())
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: &recordingStarter{next: func([]string, string, []string) (Process, error) {
			return proc, nil
		}},
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed pipe")
	var fault *BridgeFault
	assert.False(t, errors.As(err, &fault), "a live generation must not be reported as an exited bridge")
}

// ignoringTerminationProc is an installed bridge that neither exits nor releases
// its wait owner when it is killed, which is how a child that ignores termination
// looks to the reap paths.
type ignoringTerminationProc struct {
	*fakeProc
	waitN   *atomic.Int32
	waitEnd chan struct{}
}

func (p *ignoringTerminationProc) Wait() error {
	p.waitN.Add(1)
	<-p.waitEnd
	return errors.New("killed")
}

func (p *ignoringTerminationProc) Kill() error { return nil }

func TestBridgeProcess_InstalledGenerationKeepsExactlyOneWaitOwner(t *testing.T) {
	// waitProc owns the handle of the generation it installed, so no reap path may
	// wait on that handle a second time. A reap that outruns its owner reports the
	// bound it exceeded and leaves the wait to the owner already inside it.
	var waitN atomic.Int32
	var proc *ignoringTerminationProc
	starter := &recordingStarter{next: func([]string, string, []string) (Process, error) {
		inner := newFakeProc(970007)
		proc = &ignoringTerminationProc{
			fakeProc: inner, waitN: &waitN, waitEnd: make(chan struct{}),
		}
		t.Cleanup(func() {
			inner.exit(errors.New("test cleanup"))
			close(proc.waitEnd)
		})
		go serveFakeBridgeRPC(t, inner, nil)
		return proc, nil
	}}
	cfg := testConfig("/bridge/exe")
	// The supported minimum reap bound, so the reap path is exercised against a
	// contract-valid configuration instead of a deadline no operator can configure.
	cfg.ShutdownTimeout = time.Second
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.NotNil(t, proc)
	require.Eventually(t, func() bool { return waitN.Load() == 1 }, time.Second, 5*time.Millisecond,
		"the generation's wait owner must be the one waiting on the handle")

	// Terminating it releases nothing, so the reap bound expires while the owner
	// is still inside Wait. That is a reported timeout, not a second wait.
	reapErr := bp.killAndReapGeneration(bp.Generation(), bp.currentIdentity())
	require.Error(t, reapErr)
	assert.Contains(t, reapErr.Error(), "reap timed out")
	assert.EqualValues(t, 1, waitN.Load(), "the reap must not wait on a handle its owner already owns")
}

func TestBridgeProcess_TerminalFaultIsNilSafeAndGenerationScoped(t *testing.T) {
	// The helper's own contract, in isolation: only the process owner records an
	// exit fault, and a generation it failed without one - a rejected handshake, or
	// a reap that outran its wait owner - has nothing to report. Absence must stay
	// absence, because a missing fault pointer wrapped in an error is still an
	// error and the caller would report a fault it was never given. That state is
	// not reachable through a settled generation, which is why it is pinned here;
	// the recording itself is pinned end to end by the owner-settlement test above.
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter:   &recordingStarter{},
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})

	bp.mu.Lock()
	bp.gen = 7
	bp.exitFault = nil
	assert.True(t, bp.terminalFaultLocked(7) == nil,
		"a generation with no recorded exit fault must report none")
	bp.exitFault = BridgeExited(nil, "stderr=bridge: prerequisite not provisioned")
	fault := bp.terminalFaultLocked(7)
	bp.mu.Unlock()

	require.Error(t, fault)
	assert.Contains(t, fault.Error(), "prerequisite not provisioned")

	bp.mu.Lock()
	defer bp.mu.Unlock()
	assert.True(t, bp.terminalFaultLocked(6) == nil,
		"a stale generation must not read the live generation's exit fault")
}
