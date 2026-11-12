package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectInstallTreeRejectsWritableDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("POSIX ownership and mode checks are implemented for Linux")
	}
	for _, tc := range []struct {
		name string
		dir  bool
	}{
		{name: "file with newline\nin name"},
		{name: "nested directory", dir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0o700))
			path := filepath.Join(root, tc.name)
			if tc.dir {
				require.NoError(t, os.Mkdir(path, 0o700))
			} else {
				require.NoError(t, os.WriteFile(path, []byte("safe"), 0o600))
			}
			_, err := inspectInstallTree(root)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(path, 0o775))
			_, err = inspectInstallTree(root)
			require.ErrorContains(t, err, "writable beyond its owner")
		})
	}
}

func TestInspectInstallTreeChecksLinksWithoutLeavingRoot(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(root, 0o700))
	}
	safe := filepath.Join(root, "safe")
	require.NoError(t, os.WriteFile(safe, []byte("safe"), 0o600))
	link := filepath.Join(root, "link with spaces")
	if err := os.Symlink("safe", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := inspectInstallTree(root)
	require.NoError(t, err, "a contained npm-style link is safe")

	require.NoError(t, os.Remove(link))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, link))
	_, err = inspectInstallTree(root)
	require.ErrorContains(t, err, "escapes the protected plugin root")

	// A link that leaves the root and later points back inside is still unsafe:
	// its outside hop could be changed after the verifier checks it.
	require.NoError(t, os.Remove(link))
	outsideLink := filepath.Join(filepath.Dir(outside), "back-inside")
	require.NoError(t, os.Symlink(safe, outsideLink))
	require.NoError(t, os.Symlink(outsideLink, link))
	_, err = inspectInstallTree(root)
	require.ErrorContains(t, err, "escapes the protected plugin root")

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink("missing", link))
	_, err = inspectInstallTree(root)
	require.ErrorContains(t, err, "dangling or cannot be resolved")

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(filepath.Base(link), link))
	_, err = inspectInstallTree(root)
	require.ErrorContains(t, err, "dangling or cannot be resolved")
}

func TestInspectInstallTreeTrustedOwners(t *testing.T) {
	require.True(t, trustedTreeOwner(1000, 1000))
	require.True(t, trustedTreeOwner(0, 1000))
	require.False(t, trustedTreeOwner(1001, 1000))
}

func TestInspectInstallTreeRejectsUnreadableWalk(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("requires an unprivileged Linux user")
	}
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	blocked := filepath.Join(root, "blocked")
	require.NoError(t, os.Mkdir(blocked, 0o700))
	require.NoError(t, os.Chmod(blocked, 0))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	_, err := inspectInstallTree(root)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "cannot enumerate"), err.Error())
}
