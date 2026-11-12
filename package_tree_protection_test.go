package cursorsdk_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// Both verifier entry points must reject unsafe descendants before reading archive
// metadata or running a companion. These synthetic trees need no network or SDK.
func TestPackageArchive_VerifiersRejectUnsafeDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("POSIX modes are checked on Linux; Windows ACLs remain unverified")
	}
	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				make func(t *testing.T, root string)
				want string
			}{
				{"writable-file", func(t *testing.T, root string) {
					p := filepath.Join(root, "nested", "file")
					require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
					require.NoError(t, os.Chmod(p, 0o664))
				}, "writable beyond its owner"},
				{"writable-directory", func(t *testing.T, root string) {
					require.NoError(t, os.Chmod(filepath.Join(root, "nested"), 0o775))
				}, "writable beyond its owner"},
				{"external-link", func(t *testing.T, root string) {
					require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "nested", "link")))
				}, "escapes the protected plugin root"},
				{"dangling-link", func(t *testing.T, root string) {
					require.NoError(t, os.Symlink("missing", filepath.Join(root, "nested", "link")))
				}, "dangling or cannot be resolved"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "install root")
					require.NoError(t, os.Mkdir(root, 0o700))
					require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o700))
					tc.make(t, root)
					report, code := runVerifyScriptImpl(t, impl, root, nil, []string{"--tree-state", "shipped"})
					require.NotZero(t, code, report)
					require.Contains(t, report, tc.want)
					require.Contains(t, report, "verify-package: FAIL:")
				})
			}
			t.Run("safe-internal-link", func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "install root")
				require.NoError(t, os.Mkdir(root, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(root, "target"), []byte("x"), 0o600))
				require.NoError(t, os.Mkdir(filepath.Join(root, "LICENSES"), 0o700))
				require.NoError(t, os.Symlink("target", filepath.Join(root, "link")))
				report, _ := runVerifyScriptImpl(t, impl, root, nil, []string{"--tree-state", "shipped"})
				require.Contains(t, report, "install tree protection: POSIX modes, ownership and link containment checked")
			})
		})
	}
}
