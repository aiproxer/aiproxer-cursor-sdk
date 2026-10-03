package packagelayout_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// TestArchive_MatchesDesignLayoutBlock pins the release archive layout to the
// design's layout block. The packager, the verifier, and the private launcher all
// resolve their paths from this one contract, so a layout change has to be made
// here and nowhere else.
func TestArchive_MatchesDesignLayoutBlock(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		os, arch string
		exe      string
	}{
		{os: "windows", arch: "amd64", exe: ".exe"},
		{os: "windows", arch: "arm64", exe: ".exe"},
		{os: "linux", arch: "amd64", exe: ""},
		{os: "linux", arch: "arm64", exe: ""},
	} {
		t.Run(tc.os+"/"+tc.arch, func(t *testing.T) {
			t.Parallel()

			a, err := packagelayout.ForPlatform(tc.os, tc.arch)
			require.NoError(t, err)
			require.Equal(t, tc.exe, a.ExeSuffix())
			require.Equal(t, tc.os+"/"+tc.arch, a.Platform())

			require.Equal(t, "plugin.backendplugin.json", a.ManifestPath())
			require.Equal(t, "bin/lip-backend-cursorsdk"+tc.exe, a.OuterExecutablePath())
			require.Equal(t, "private/bridge/lip-cursor-sdk-bridge"+tc.exe, a.LauncherPath())
			require.Equal(t, "private/bridge/bin/lip-cursor-sdk-bridge.js", a.BridgeEntryPath())
			require.Equal(t, "private/bridge/dist", a.BridgeDistPath())
			require.Equal(t, "private/bridge/package.json", a.BridgePackageJSONPath())
			require.Equal(t, "private/bridge/package-lock.json", a.BridgePackageLockPath())
			require.Equal(t, "private/node/node"+tc.exe, a.PrivateRuntimePath())
			require.Equal(t, "compatibility.json", a.CompatibilityPath())
			require.Equal(t, "checksums.sha256", a.ChecksumsPath())
			require.Equal(t, "LICENSES", a.LicensesDir())
		})
	}
}

// TestArchive_ResolvesTheOperatorProvisionedSDKTree pins where the SDK the plugin
// does not redistribute lands. The archive stages no third-party package code, so
// this tree exists only after the operator provisions it, and every consumer that
// has to talk about it - the verifier, the launcher preflight, the metadata, and
// the checksum scope - resolves it here rather than spelling it out.
func TestArchive_ResolvesTheOperatorProvisionedSDKTree(t *testing.T) {
	t.Parallel()

	a, err := packagelayout.ForPlatform("linux", "amd64")
	require.NoError(t, err)

	require.Equal(t, "private/bridge/node_modules", a.BridgeModulesPath())
	require.Equal(t, a.BridgeModulesPath()+"/", packagelayout.ProvisionedPrefix)
	require.Equal(t, "private/bridge/node_modules/@cursor/sdk", a.ProvisionedSDKDirPath())
	require.Equal(t, "private/bridge/node_modules/@cursor/sdk/package.json", a.ProvisionedSDKPackageJSONPath())
	require.Equal(t, "@cursor/sdk", packagelayout.SDKPackageName)
}

// TestArchive_ShipsNoThirdPartyPackageCode is the archive content rule: the
// Cursor SDK is proprietary and is not redistributed, so the dependency closure
// is not in the archive either. What the archive does ship is the pair the
// operator provisions against - the bridge manifest and the lockfile that pins the
// SDK - plus the private runtime's own bundled npm, without which the documented
// provisioning command could not run without a global package manager.
func TestArchive_ShipsNoThirdPartyPackageCode(t *testing.T) {
	t.Parallel()

	for _, platform := range packagelayout.SupportedPlatforms() {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()

			goos, goarch, ok := strings.Cut(platform, "/")
			require.True(t, ok, platform)
			a, err := packagelayout.ForPlatform(goos, goarch)
			require.NoError(t, err)

			entries := a.RequiredEntries()
			require.NotContains(t, entries, a.BridgeModulesPath(),
				"the provisioned dependency tree is not archive content")
			for _, rel := range entries {
				require.False(t, strings.HasPrefix(rel, packagelayout.ProvisionedPrefix),
					"required archive entry %s is inside the operator-provisioned tree", rel)
			}

			require.Contains(t, entries, a.BridgePackageJSONPath())
			require.Contains(t, entries, a.BridgePackageLockPath(),
				"the lockfile that pins the SDK has to ship: it is what the operator provisions against")
			require.Contains(t, entries, a.PrivateRuntimeNPMCLIPath(),
				"provisioning has to run on the shipped runtime's own npm, not a global one")
		})
	}
}

// TestPrivateRuntimeNPMCLIPath_MatchesTheOfficialDistribution pins the npm entry
// point the provisioning command names to where npm actually lives inside a Node
// distribution: the POSIX distribution keeps the runtime in bin/ and npm under
// lib/, and the Windows distribution keeps both at the root. A documented path
// that does not exist is not documentation, and the packager stages the tree this
// path names.
func TestPrivateRuntimeNPMCLIPath_MatchesTheOfficialDistribution(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		goos, runtime, npmRoot, npmCLI string
	}{
		{
			goos:    "linux",
			runtime: "private/node/node",
			// node-v22.22.3-linux-x64/bin/node and
			// node-v22.22.3-linux-x64/lib/node_modules/npm/bin/npm-cli.js.
			npmRoot: "private/node/lib/node_modules/npm",
			npmCLI:  "private/node/lib/node_modules/npm/bin/npm-cli.js",
		},
		{
			goos:    "windows",
			runtime: "private/node/node.exe",
			// node-v22.22.3-win-x64/node.exe and
			// node-v22.22.3-win-x64/node_modules/npm/bin/npm-cli.js.
			npmRoot: "private/node/node_modules/npm",
			npmCLI:  "private/node/node_modules/npm/bin/npm-cli.js",
		},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()

			a, err := packagelayout.ForPlatform(tc.goos, "amd64")
			require.NoError(t, err)
			require.Equal(t, tc.runtime, a.PrivateRuntimePath())
			require.Equal(t, tc.npmRoot, a.PrivateRuntimeNPMRootPath())
			require.Equal(t, tc.npmCLI, a.PrivateRuntimeNPMCLIPath())

			// The staging target is the distribution's own layout, so a packager
			// copies the npm tree without renaming anything inside it.
			require.Equal(t, strings.TrimPrefix(tc.npmRoot, a.PrivateRuntimeDirPath()+"/"), a.PrivateRuntimeNPMRel())
		})
	}
}

// TestProvisionCommand_IsTheOneOperatorCommand pins the single command an operator
// runs. It has to use the shipped runtime and the shipped runtime's own npm: a
// command that needed a globally installed Node or npm would put back exactly the
// prerequisite the private runtime exists to remove.
func TestProvisionCommand_IsTheOneOperatorCommand(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ goos, wants string }{
		{
			goos:  "linux",
			wants: "cd <plugin-root>/private/bridge && ../node/node ../node/lib/node_modules/npm/bin/npm-cli.js ci --omit=dev",
		},
		{
			goos:  "windows",
			wants: "cd <plugin-root>/private/bridge && ../node/node.exe ../node/node_modules/npm/bin/npm-cli.js ci --omit=dev",
		},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()

			a, err := packagelayout.ForPlatform(tc.goos, "amd64")
			require.NoError(t, err)
			require.Equal(t, tc.wants, a.ProvisionCommand(""))
			require.Equal(t,
				strings.Replace(tc.wants, "<plugin-root>", "/opt/lip/plugins/cursorsdk", 1),
				a.ProvisionCommand("/opt/lip/plugins/cursorsdk"))

			// Both operands stay inside the archive, relative to the directory the
			// command changes into, so the command cannot reach a global toolchain.
			require.Contains(t, a.ProvisionCommand(""), "../node/")
			require.Contains(t, a.ProvisionCommand(""), " ci --omit=dev")
			require.NotContains(t, a.ProvisionCommand(""), "PATH")
		})
	}
}

// TestArchive_ExposesThePathsAPackagerMustStage pins the layout rows a packaging
// script cannot restate. The scripts have to copy the bridge entry out of the source
// tree, stage the lockfile that pins the SDK, and stage the private runtime's own
// npm, and all three paths exist here for exactly that reason: a script that spelled
// them out would be a second copy of the layout.
func TestArchive_ExposesThePathsAPackagerMustStage(t *testing.T) {
	t.Parallel()

	a, err := packagelayout.ForPlatform("linux", "amd64")
	require.NoError(t, err)

	require.Equal(t, "bin/lip-cursor-sdk-bridge.js", packagelayout.BridgeEntrySourceRel())
	require.Equal(t, a.BridgePackageDirPath()+"/"+packagelayout.BridgeEntrySourceRel(), a.BridgeEntryPath())
	require.Equal(t, "node_modules", packagelayout.ModulesDirName)
	require.Equal(t, a.BridgePackageDirPath()+"/"+packagelayout.ModulesDirName, a.BridgeModulesPath())
	require.Equal(t, a.BridgePackageDirPath()+"/"+packagelayout.BridgeLockFileName, a.BridgePackageLockPath())

	// The source-relative rows the packager derives have to stay inside the bridge
	// package directory: a script joins them onto the source tree, and a row that
	// escaped it would stage from somewhere the layout does not describe.
	for _, rel := range []string{packagelayout.BridgeEntrySourceRel(), packagelayout.ModulesDirName} {
		require.False(t, strings.HasPrefix(rel, "/"), "%s is not relative", rel)
		require.False(t, strings.Contains(rel, ".."), "%s escapes the bridge package directory", rel)
	}
}

// TestArchive_RequiredEntries_AreInstallRootRelativeCleanSlashPaths keeps the
// contract usable by both packaging scripts: every archive-relative path is
// slash-separated, clean, relative to the install root, and free of any
// traversal or drive prefix.
func TestArchive_RequiredEntries_AreInstallRootRelativeCleanSlashPaths(t *testing.T) {
	t.Parallel()

	a, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	entries := a.RequiredEntries()
	require.NotEmpty(t, entries)

	seen := make(map[string]struct{}, len(entries))
	for _, rel := range entries {
		require.NotContains(t, rel, `\`, "archive paths are slash-separated")
		require.False(t, filepath.IsAbs(rel), rel)
		require.NotContains(t, rel, ":", rel)
		require.Equal(t, filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), rel)
		require.False(t, rel == "." || strings.HasPrefix(rel, "../"), rel)
		require.NotEmpty(t, strings.TrimSpace(rel))
		_, dup := seen[rel]
		require.False(t, dup, "duplicate required entry %q", rel)
		seen[rel] = struct{}{}
	}

	// The required entries are the fixed private-runtime contract, not a
	// summary: an archive missing any of them is not an installable package.
	for _, rel := range []string{
		a.ManifestPath(),
		a.OuterExecutablePath(),
		a.LauncherPath(),
		a.BridgeEntryPath(),
		a.PrivateRuntimePath(),
		a.BridgeDistPath(),
		a.BridgePackageJSONPath(),
		a.BridgePackageLockPath(),
		a.PrivateRuntimeNPMCLIPath(),
		a.CompatibilityPath(),
		a.ChecksumsPath(),
		a.LicensesDir(),
	} {
		require.Contains(t, entries, rel)
	}
}

// TestForPlatform_RejectsUnvalidatedPlatforms keeps an unvalidated platform out
// of the packaging contract instead of letting a script invent a cross-compiled
// archive for it.
func TestForPlatform_RejectsUnvalidatedPlatforms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ os, arch, want string }{
		{os: "darwin", arch: "arm64", want: "darwin/arm64"},
		{os: "linux", arch: "386", want: "linux/386"},
		{os: "windows", arch: "riscv64", want: "windows/riscv64"},
	} {
		t.Run(tc.os+"/"+tc.arch, func(t *testing.T) {
			t.Parallel()

			got, err := packagelayout.ForPlatform(tc.os, tc.arch)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.Empty(t, got.OS())
			require.Empty(t, got.Arch())
		})
	}
}

// TestPrivateFor_CorrespondsToArchiveLayout is the correspondence test between
// the two consumers of the layout: the private launcher resolves the packaged
// runtime from its own location, and the archive contract names the same files.
// The launcher cannot import the archive paths directly, so an assertion that
// merely restated the shared expression would pass for any layout; asserting the
// correspondence proves that a launcher installed at the archive location finds
// exactly the runtime and entry the archive stages.
func TestPrivateFor_CorrespondsToArchiveLayout(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"windows", "linux"} {
		a, err := packagelayout.ForPlatform(goos, "amd64")
		require.NoError(t, err)
		root := filepath.Join(t.TempDir(), "install root with spaces")
		launcher := filepath.Join(root, filepath.FromSlash(a.LauncherPath()))

		priv, err := packagelayout.PrivateFor(launcher, goos)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(root, filepath.FromSlash(a.PrivateRuntimePath())), priv.Runtime)
		require.Equal(t, filepath.Join(root, filepath.FromSlash(a.BridgeEntryPath())), priv.Entry)

		rel, err := filepath.Rel(root, priv.Runtime)
		require.NoError(t, err)
		require.Equal(t, a.PrivateRuntimePath(), filepath.ToSlash(rel))
		rel, err = filepath.Rel(root, priv.Entry)
		require.NoError(t, err)
		require.Equal(t, a.BridgeEntryPath(), filepath.ToSlash(rel))

		require.Equal(t, a.PrivateRuntimePath(), priv.RuntimeRel)
		require.Equal(t, a.BridgeEntryPath(), priv.EntryRel)
	}
}

// TestPrivateFor_ResolvesTheProvisioningSlots keeps the launcher's run-time
// preflight reading the same files the archive contract names.
//
// The launcher has to tell an operator that the SDK is not provisioned, and to
// print the command that provisions it. Both answers come from resolved slots, so
// this is the correspondence between the two consumers of the layout: a launcher
// installed at the archive location has to find the bridge manifest, the
// operator-provisioned SDK metadata, and the shipped npm entry point exactly where
// the archive stages them.
func TestPrivateFor_ResolvesTheProvisioningSlots(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()

			a, err := packagelayout.ForPlatform(goos, "amd64")
			require.NoError(t, err)
			root := filepath.Join(t.TempDir(), "install root with spaces")
			launcher := filepath.Join(root, filepath.FromSlash(a.LauncherPath()))

			priv, err := packagelayout.PrivateFor(launcher, goos)
			require.NoError(t, err)

			rel := func(path string) string {
				out, relErr := filepath.Rel(root, path)
				require.NoError(t, relErr)
				return filepath.ToSlash(out)
			}
			require.Equal(t, a.BridgePackageDirPath(), rel(priv.PackageDir))
			require.Equal(t, a.ProvisionedSDKPackageJSONPath(), rel(priv.SDKPackageJSON))
			require.Equal(t, a.PrivateRuntimeNPMCLIPath(), rel(priv.NPMCLI))
			require.Equal(t, a.BridgeModulesPath()+"/", priv.ProvisionedPrefix)
			require.Contains(t, priv.ProvisionCommand, "cd ")
			require.Contains(t, priv.ProvisionCommand, " ci --omit=dev")
			require.Contains(t, priv.ProvisionCommand, filepath.ToSlash(priv.PackageDir),
				"the command changes into the directory it provisions")
		})
	}
}

// TestPrivateFor_RejectsUnusableLauncherPath keeps an unusable launcher path a
// prerequisite failure instead of resolving private slots against a guessed root.
func TestPrivateFor_RejectsUnusableLauncherPath(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "   "} {
		priv, err := packagelayout.PrivateFor(in, runtime.GOOS)
		require.Error(t, err)
		require.Contains(t, err.Error(), "launcher executable")
		require.Empty(t, priv.Runtime)
		require.Empty(t, priv.Entry)
	}
}

// TestCheckSlot_ClassifiesEveryPrivateRuntimeFailure keeps a missing, unusable,
// non-regular, or directory private slot an explicit prerequisite failure that
// names the packaged location. The launcher renders these classifications into
// its operator diagnostics, so the classification itself is the contract.
func TestCheckSlot_ClassifiesEveryPrivateRuntimeFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	require.NoError(t, os.WriteFile(regular, []byte("x"), 0o600))
	dir := filepath.Join(root, "directory")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	for _, tc := range []struct {
		name string
		path string
		want packagelayout.SlotErrorKind
		rel  string
	}{
		{name: "missing", path: filepath.Join(root, "absent"), want: packagelayout.SlotMissing, rel: "private/node/node[.exe]"},
		{name: "directory", path: dir, want: packagelayout.SlotDirectory, rel: "private/node/node[.exe]"},
		{name: "regular", path: regular, want: packagelayout.SlotOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := packagelayout.CheckSlot(tc.path, tc.rel)
			if tc.want == packagelayout.SlotOK {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)

			var slot *packagelayout.SlotError
			require.True(t, errors.As(err, &slot))
			require.Equal(t, tc.want, slot.Kind)
			require.Equal(t, tc.path, slot.Path)
			require.Equal(t, tc.rel, slot.Rel)
			require.Contains(t, err.Error(), tc.rel)
		})
	}
}

// TestCheckSlot_DistinguishesDirectoryFromOtherNonRegularFiles keeps the
// directory case distinct: an operator whose private runtime slot was replaced
// by a directory needs a different remedy than an operator whose slot is some
// other non-regular file, and the launcher renders both messages.
func TestCheckSlot_DistinguishesDirectoryFromOtherNonRegularFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	err := packagelayout.CheckSlot(root, "private/node/node[.exe]")
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a directory")

	var slot *packagelayout.SlotError
	require.True(t, errors.As(err, &slot))
	require.Equal(t, packagelayout.SlotDirectory, slot.Kind)
	require.False(t, errors.Is(err, os.ErrNotExist))
}

// TestPrivateFor_RelPathsAreSlashSeparated keeps the slash-separated archive
// locations that operator diagnostics quote usable as literal archive paths.
func TestPrivateFor_RelPathsAreSlashSeparated(t *testing.T) {
	t.Parallel()

	a, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	priv, err := packagelayout.PrivateFor(filepath.Join(t.TempDir(), filepath.FromSlash(a.LauncherPath())), runtime.GOOS)
	require.NoError(t, err)
	require.NotContains(t, priv.RuntimeRel, `\`)
	require.NotContains(t, priv.EntryRel, `\`)
	require.Equal(t, "private/node/node"+a.ExeSuffix(), priv.RuntimeRel)
	require.Equal(t, "private/bridge/bin/lip-cursor-sdk-bridge.js", priv.EntryRel)
}
