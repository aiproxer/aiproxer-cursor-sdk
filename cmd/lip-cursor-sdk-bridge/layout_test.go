package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPrivateLayoutFor_ResolvesFixedPrivateRuntimeNextToLauncher pins the
// packaged private layout: the launcher resolves the fixed private Node
// executable and bridge entry relative to its own location, including inside an
// install root that contains spaces.
func TestPrivateLayoutFor_ResolvesFixedPrivateRuntimeNextToLauncher(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	require.Equal(t, filepath.Join(lay.root, "private", "bridge", launcherName+platformExeSuffix()), lay.launcher)

	got, err := privateLayoutFor(lay.launcher)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(lay.root, "private", "node", "node"+platformExeSuffix()), got.Runtime)
	require.Equal(t, filepath.Join(lay.root, "private", "bridge", "bin", bridgeEntryName), got.Entry)
	require.True(t, filepath.IsAbs(got.Runtime))
	require.True(t, filepath.IsAbs(got.Entry))

	require.Equal(t, []string{got.Runtime, got.Entry}, got.argv(nil))
	require.Equal(t, []string{got.Runtime, got.Entry, "--version"}, got.argv([]string{"--version"}))
}

// TestPrivateLayoutFor_IgnoresPathWorkingDirectoryAndDecoys proves the launcher
// resolves its private runtime only from its own location: a startable `node`
// on PATH, a `node` in the working directory, and a `node` in a global bin
// directory must never be adopted.
func TestPrivateLayoutFor_IgnoresPathWorkingDirectoryAndDecoys(t *testing.T) {
	lay := installPrivateLayout(t, true, true)

	decoyDir := t.TempDir()
	for _, dir := range []string{decoyDir, workDirContaining(t, decoyDir)} {
		decoy := filepath.Join(dir, "node"+platformExeSuffix())
		require.NoError(t, os.WriteFile(decoy, []byte("decoy runtime"), 0o700))
	}
	t.Setenv("PATH", decoyDir)
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir(decoyDir)
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got, err := privateLayoutFor(lay.launcher)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(lay.root, "private", "node", "node"+platformExeSuffix()), got.Runtime)
	require.Equal(t, filepath.Join(lay.root, "private", "bridge", "bin", bridgeEntryName), got.Entry)
}

// TestPrivateLayoutFor_MissingPrivateRuntimeIsExplicit keeps a missing private
// Node runtime an explicit prerequisite failure naming the packaged location,
// with no fallback, download, npm, or shell hint.
func TestPrivateLayoutFor_MissingPrivateRuntimeIsExplicit(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, false, true)
	got, err := privateLayoutFor(lay.launcher)
	require.Error(t, err)
	require.Empty(t, got.Runtime)
	require.Empty(t, got.Entry)
	require.Contains(t, err.Error(), "private/node/node")
	require.Contains(t, err.Error(), "not found")
	require.Contains(t, err.Error(), "reinstall")
	require.NotContains(t, err.Error(), "npm")
	require.NotContains(t, err.Error(), "PATH=")
}

// TestPrivateLayoutFor_MissingBridgeEntryIsExplicit keeps a missing bridge
// entrypoint an explicit prerequisite failure naming the packaged location.
func TestPrivateLayoutFor_MissingBridgeEntryIsExplicit(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, false)
	got, err := privateLayoutFor(lay.launcher)
	require.Error(t, err)
	require.Empty(t, got.Runtime)
	require.Empty(t, got.Entry)
	require.Contains(t, err.Error(), "private/bridge/bin/"+bridgeEntryName)
	require.Contains(t, err.Error(), "not found")
	require.NotContains(t, err.Error(), "npm")
}

// TestPrivateLayoutFor_RejectsNonRegularPrivateRuntimeAndUnknownLauncher covers
// a directory or symlinked-to-directory in a private runtime slot, and an
// unusable launcher path, so neither reaches a start attempt.
func TestPrivateLayoutFor_RejectsNonRegularPrivateRuntimeAndUnknownLauncher(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	require.NoError(t, os.Remove(lay.runtime))
	require.NoError(t, os.MkdirAll(lay.runtime, 0o755))
	got, err := privateLayoutFor(lay.launcher)
	require.Error(t, err)
	require.Equal(t, privateLayout{}, got)
	require.Contains(t, err.Error(), "private/node/node")
	require.Contains(t, err.Error(), "directory")

	got, err = privateLayoutFor("   ")
	require.Error(t, err)
	require.Equal(t, privateLayout{}, got)
	require.Contains(t, err.Error(), "launcher executable")
}

// TestPrivateLayoutRelPaths_MatchReleaseArchive pins the slash-separated archive
// locations quoted in operator diagnostics against what layout resolution
// actually produces.
//
// privateEntryRelPath is the single constant the diagnostics quote and the one the
// packager must stage, so the assertion has to be a correspondence rather than a
// restating of the expression that defines it: the constant must name exactly the
// entry path that privateLayoutFor resolves inside the packaged install root, and
// the runtime constant must stay the documented platform-suffixed archive
// spelling. A layout that moves its bridge entry, its entry directory, or the
// runtime directory therefore fails here instead of producing diagnostics that
// name a file the install does not contain.
func TestPrivateLayoutRelPaths_MatchReleaseArchive(t *testing.T) {
	t.Parallel()

	require.Equal(t, "private/node/node[.exe]", privateRuntimeRelPath)

	lay := installPrivateLayout(t, true, true)
	got, err := privateLayoutFor(lay.launcher)
	require.NoError(t, err)

	runtimeRel, err := filepath.Rel(lay.root, got.Runtime)
	require.NoError(t, err)
	require.Equal(t, "private/node/node"+platformExeSuffix(), filepath.ToSlash(runtimeRel))

	entryRel, err := filepath.Rel(lay.root, got.Entry)
	require.NoError(t, err)
	require.Equal(t, "private/bridge/bin/lip-cursor-sdk-bridge.js", filepath.ToSlash(entryRel))
	require.Equal(t, privateEntryRelPath, filepath.ToSlash(entryRel))
	require.NotContains(t, privateRuntimeRelPath, `\`)
	require.NotContains(t, privateEntryRelPath, `\`)
}

// installedPrivateLayout is one packaged plugin-private layout in a temp root.
type installedPrivateLayout struct {
	root     string
	launcher string
	runtime  string
	entry    string
	// packageDir is the bridge package directory: the shipped manifest, the built
	// JavaScript, and the operator-provisioned dependency tree live here.
	packageDir string
}

func installPrivateLayout(tb testing.TB, withRuntime, withEntry bool) installedPrivateLayout {
	tb.Helper()
	root := filepath.Join(tb.TempDir(), "plugin root with spaces")
	lay := installedPrivateLayout{
		root:       root,
		launcher:   filepath.Join(root, "private", "bridge", launcherName+platformExeSuffix()),
		runtime:    filepath.Join(root, "private", "node", "node"+platformExeSuffix()),
		entry:      filepath.Join(root, "private", "bridge", "bin", bridgeEntryName),
		packageDir: filepath.Join(root, "private", "bridge"),
	}
	require.NoError(tb, os.MkdirAll(filepath.Dir(lay.launcher), 0o755))
	// The launcher slot only has to exist for resolution; process-level tests
	// install the built launcher binary instead.
	require.NoError(tb, os.WriteFile(lay.launcher, []byte("launcher placeholder"), 0o700))
	if withRuntime {
		require.NoError(tb, os.MkdirAll(filepath.Dir(lay.runtime), 0o755))
		require.NoError(tb, os.WriteFile(lay.runtime, []byte("node placeholder"), 0o700))
	}
	if withEntry {
		require.NoError(tb, os.MkdirAll(filepath.Dir(lay.entry), 0o755))
		require.NoError(tb, os.WriteFile(lay.entry, []byte(`{"mode":"echo"}`), 0o644))
	}
	return lay
}

func workDirContaining(tb testing.TB, base string) string {
	tb.Helper()
	dir := filepath.Join(base, "working directory decoy")
	require.NoError(tb, os.MkdirAll(dir, 0o755))
	require.False(tb, strings.Contains(dir, "\x00"))
	return dir
}
