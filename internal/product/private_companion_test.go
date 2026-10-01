package product

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalize_defaultResolvesPluginLocalCompanion pins the packaged default:
// with no bridge_executable the connector resolves the plugin-private companion
// next to the running outer plugin executable. The test binary is not an
// installed plugin package, so the expected outcome is either that exact
// resolution or an explicit prerequisite failure naming the expected location.
func TestNormalize_defaultResolvesPluginLocalCompanion(t *testing.T) {
	t.Parallel()
	ResetLookPathCache()
	t.Cleanup(ResetLookPathCache)

	const apiKey = "plugin-local-companion-key-value"
	outer, err := os.Executable()
	require.NoError(t, err)
	expected := filepath.Clean(filepath.Join(filepath.Dir(outer), "..", "private", "bridge", DefaultBridgeExecutable+platformExeSuffix()))

	if info, statErr := os.Stat(expected); statErr == nil && !info.IsDir() {
		cfg, err := Normalize(Input{APIKey: apiKey}, "")
		require.NoError(t, err)
		require.Equal(t, expected, cfg.BridgeExecutable)
		require.Equal(t, []string{expected}, cfg.BridgeArgv())
		return
	}

	cfg, err := Normalize(Input{APIKey: apiKey}, "")
	require.Error(t, err)
	require.Empty(t, cfg.BridgeExecutable)
	require.Contains(t, err.Error(), "private/bridge/"+DefaultBridgeExecutable)
	require.NotContains(t, err.Error(), "direct PATH/absolute lookup only")
	require.NotContains(t, err.Error(), "npm install")
	require.NotContains(t, err.Error(), apiKey)
}

// TestNormalize_defaultIgnoresPathLookup proves the packaged default is not a
// PATH lookup: with a startable decoy discoverable through the existing direct
// PATH lookup, the packaged default still fails explicitly instead of adopting
// the decoy. Working-directory independence is proven end to end by
// TestInstalledLayout_PrivateCompanionResolution.
func TestNormalize_defaultIgnoresPathLookup(t *testing.T) {
	ResetLookPathCache()
	t.Cleanup(ResetLookPathCache)

	decoyDir := t.TempDir()
	decoy := filepath.Join(decoyDir, DefaultBridgeExecutable+platformExeSuffix())
	require.NoError(t, os.WriteFile(decoy, []byte("decoy"), 0o700))
	t.Setenv("PATH", decoyDir)
	ResetLookPathCache()

	// Sanity: the decoy is discoverable through the existing direct PATH lookup
	// used by explicit overrides.
	resolved, ok := checkBridgeExecutable(DefaultBridgeExecutable)
	require.True(t, ok, "decoy must be startable through PATH for this proof to mean anything")
	require.Equal(t, decoy, resolved)

	cfg, err := Normalize(Input{APIKey: "path-decoy-key-value"}, "")
	require.Error(t, err)
	require.Empty(t, cfg.BridgeExecutable)
	require.Contains(t, err.Error(), "private/bridge/"+DefaultBridgeExecutable)
}

// TestNormalize_explicitOverrideBypassesPluginLocalCompanion keeps explicit
// bridge_executable overrides authoritative, including inside a path that
// contains spaces.
func TestNormalize_explicitOverrideBypassesPluginLocalCompanion(t *testing.T) {
	t.Parallel()
	ResetLookPathCache()
	t.Cleanup(ResetLookPathCache)

	override := filepath.Join(t.TempDir(), "override dir with spaces", "custom-bridge"+platformExeSuffix())
	require.NoError(t, os.MkdirAll(filepath.Dir(override), 0o755))
	require.NoError(t, os.WriteFile(override, []byte("override"), 0o700))

	cfg, err := Normalize(Input{APIKey: "override-key-value", BridgeExecutable: override}, "")
	require.NoError(t, err)
	require.Equal(t, override, cfg.BridgeExecutable)
	require.Equal(t, []string{override}, cfg.BridgeArgv())
}

// TestNormalize_explicitOverrideKeepsDirectExecutableValidation pins the
// unchanged failure contract for explicit overrides: the direct-lookup message
// stays byte-for-byte identical and shell/npm launchers stay rejected.
func TestNormalize_explicitOverrideKeepsDirectExecutableValidation(t *testing.T) {
	t.Parallel()
	ResetLookPathCache()
	t.Cleanup(ResetLookPathCache)

	const apiKey = "override-validation-key-value"
	missing := filepath.Join(t.TempDir(), "absent-bridge"+platformExeSuffix())
	_, err := Normalize(Input{APIKey: apiKey, BridgeExecutable: missing}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), strconv.Quote(missing))
	require.Contains(t, err.Error(), "not found (direct PATH/absolute lookup only; Go-LIP never runs npm install)")
	require.NotContains(t, err.Error(), apiKey)

	for _, name := range []string{"npm", "npx", "sh", "bash", "cmd.exe", "powershell"} {
		_, err := Normalize(Input{APIKey: apiKey, BridgeExecutable: name}, "")
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "bridge_executable must be a direct bridge binary, not shell or npm launcher")
	}

	_, err = Normalize(Input{APIKey: apiKey, BridgeExecutable: "bridge && curl evil"}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not contain shell metacharacters")
}

// TestPrivateCompanionRelPath_MatchesPackagedLayout pins the release archive
// location of the private companion relative to the outer plugin executable.
func TestPrivateCompanionRelPath_MatchesPackagedLayout(t *testing.T) {
	t.Parallel()

	rel := privateCompanionRelPath()
	require.Equal(t, "../private/bridge/"+DefaultBridgeExecutable+platformExeSuffix(), rel)
	require.NotContains(t, rel, `\`)
	require.Equal(t, DefaultBridgeExecutable, strings.TrimSuffix(path.Base(rel), platformExeSuffix()))
}

// TestPrivateCompanionPathFor_ResolvesNextToOuterExecutableInPathWithSpaces
// covers the packaged layout inside a directory that contains spaces.
func TestPrivateCompanionPathFor_ResolvesNextToOuterExecutableInPathWithSpaces(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "plugin root with spaces")
	outer := filepath.Join(root, "bin", "lip-backend-cursorsdk"+platformExeSuffix())
	companion := installTestCompanion(t, root)

	got, err := privateCompanionPathFor(outer)
	require.NoError(t, err)
	require.Equal(t, companion, got)
	require.True(t, filepath.IsAbs(got))
	again, err := privateCompanionPathFor(outer)
	require.NoError(t, err)
	require.Equal(t, companion, again)
}

// TestPrivateCompanionPathFor_MissingCompanionNamesExpectedLocation keeps the
// prerequisite failure explicit and free of any fallback suggestion.
func TestPrivateCompanionPathFor_MissingCompanionNamesExpectedLocation(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "plugin root with spaces")
	outer := filepath.Join(root, "bin", "lip-backend-cursorsdk"+platformExeSuffix())

	got, err := privateCompanionPathFor(outer)
	require.Error(t, err)
	require.Empty(t, got)
	require.Contains(t, err.Error(), "../private/bridge/"+DefaultBridgeExecutable)
	require.Contains(t, err.Error(), "bridge_executable")
	require.NotContains(t, err.Error(), "npm install")
	// The packaged default is not the explicit-override lookup path.
	require.NotContains(t, err.Error(), "direct PATH/absolute lookup only")
}

// TestPrivateCompanionPathFor_RejectsCompanionDirectoryAndEmptyOuter covers the
// non-executable and unknown-outer-path edges.
func TestPrivateCompanionPathFor_RejectsCompanionDirectoryAndEmptyOuter(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "plugin root")
	outer := filepath.Join(root, "bin", "lip-backend-cursorsdk"+platformExeSuffix())
	companionDir := filepath.Join(root, "private", "bridge")
	require.NoError(t, os.MkdirAll(filepath.Join(companionDir, DefaultBridgeExecutable+platformExeSuffix()), 0o755))

	got, err := privateCompanionPathFor(outer)
	require.Error(t, err)
	require.Empty(t, got)
	require.Contains(t, err.Error(), "private/bridge/"+DefaultBridgeExecutable)

	got, err = privateCompanionPathFor("   ")
	require.Error(t, err)
	require.Empty(t, got)
}

func installTestCompanion(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "private", "bridge")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	companion := filepath.Join(dir, DefaultBridgeExecutable+platformExeSuffix())
	require.NoError(t, os.WriteFile(companion, []byte("test companion"), 0o700))
	return companion
}

func platformExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
