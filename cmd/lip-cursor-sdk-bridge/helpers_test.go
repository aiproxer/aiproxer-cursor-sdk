package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/stretchr/testify/require"
)

// mustLayout resolves the installed private layout the way production does.
func mustLayout(tb testing.TB, lay installedPrivateLayout) privateLayout {
	tb.Helper()
	resolved, err := privateLayoutFor(lay.launcher)
	require.NoError(tb, err)
	return resolved
}

// unresolvedLayoutFor is [mustLayout] for layouts that intentionally do not
// resolve, so a caller can prove the failure happens before any start.
func unresolvedLayoutFor(lay installedPrivateLayout) privateLayout {
	return privateLayout{
		Runtime: lay.runtime,
		Entry:   lay.entry,
	}
}

func writeBridgeScript(tb testing.TB, lay installedPrivateLayout, script string) {
	tb.Helper()
	require.NoError(tb, os.WriteFile(lay.entry, []byte(script), 0o644))
}

// installRunnablePrivateLayout installs a packaged private layout whose private
// runtime is the deterministic fake Node binary, so launcher lifecycle tests
// exercise a real descendant without a JavaScript toolchain.
//
// The layout is a provisioned one: the shipped bridge manifest pins the SDK and the
// operator-provisioned package metadata resolves to that pin, which is what the
// launcher's SDK preflight requires before it starts anything. A case that wants the
// unprovisioned answer stages its own tree.
func installRunnablePrivateLayout(tb testing.TB, script string) installedPrivateLayout {
	tb.Helper()
	lay := installPrivateLayout(tb, true, true)
	require.NoError(tb, copyFile(fakebridge.BuildNodeExe(tb), lay.runtime))
	writeBridgeScript(tb, lay, script)
	writeBridgeManifest(tb, lay, pinnedTestSDKVersion)
	writeProvisionedSDK(tb, lay, pinnedTestSDKVersion)
	return lay
}

// pinnedTestSDKVersion is the SDK pin the launcher fixtures ship, matching the bridge
// manifest the plugin stages.
const pinnedTestSDKVersion = "1.0.23"

func strconvQuote(value string) string {
	return strconv.Quote(value)
}

func filepathJoin(parts ...string) string {
	return filepath.Join(parts...)
}

func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o700)
}

func readFileString(tb testing.TB, path string) string {
	tb.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(tb, err)
	return string(raw)
}

func readLines(tb testing.TB, path string) []string {
	tb.Helper()
	text := strings.TrimRight(readFileString(tb, path), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// waitForFile blocks on a deterministic file barrier instead of a sleep.
func waitForFile(tb testing.TB, path string) {
	tb.Helper()
	require.Eventually(tb, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Size() > 0
	}, 30*time.Second, 2*time.Millisecond, "timed out waiting for %s", path)
}

func readPID(tb testing.TB, path string) int {
	tb.Helper()
	lines := readLines(tb, path)
	require.NotEmpty(tb, lines)
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	require.NoError(tb, err)
	return pid
}

// requireEventuallyDead polls process termination instead of assuming an
// instantaneous kill.
func requireEventuallyDead(tb testing.TB, pid int) {
	tb.Helper()
	require.Eventually(tb, func() bool { return !processAlive(tb, pid) },
		30*time.Second, 20*time.Millisecond, "process %d is still alive", pid)
}
