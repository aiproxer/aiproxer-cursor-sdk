package fakebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

var (
	buildNodeOnce sync.Once
	buildNodePath string
	buildNodeErr  error
)

// BuildNodeExe compiles cmd/fake-cursor-sdk-node into a temp directory for
// tests. It stands in for the packaged private Node runtime at
// private/node/node[.exe], so launcher lifecycle tests need no Node
// installation, no npm, and no network.
func BuildNodeExe(tb testing.TB) string {
	tb.Helper()
	buildNodeOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			buildNodeErr = errors.New("runtime.Caller failed")
			return
		}
		name := "fake-cursor-sdk-node"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		// A per-process directory keeps concurrent test binaries from building
		// over one executable the other is still running.
		dir, err := os.MkdirTemp("", "fake-cursor-sdk-node-")
		if err != nil {
			buildNodeErr = err
			return
		}
		exe := filepath.Join(dir, name)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/fake-cursor-sdk-node")
		cmd.Dir = filepath.Dir(thisFile)
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildNodeErr = fmt.Errorf("go build fake-cursor-sdk-node: %w\n%s", err, out)
			return
		}
		buildNodePath = exe
	})
	if buildNodeErr != nil {
		tb.Fatal(buildNodeErr)
	}
	return buildNodePath
}
