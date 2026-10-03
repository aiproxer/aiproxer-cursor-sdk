package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// TestPreflightSDK_ProvisionedTreePassesAndThePinIsReadFromTheShippedManifest keeps
// the run-time SDK check honest in both directions.
//
// The SDK is not redistributed, so the launcher cannot tell what is installed by
// looking at what it shipped. It reads the pinned version from the bridge manifest the
// archive does ship and compares it against the operator-provisioned package metadata.
// A tree that matches passes; a tree that does not is a prerequisite failure rather
// than a bridge that fails later with a resolution stack.
func TestPreflightSDK_ProvisionedTreePassesAndThePinIsReadFromTheShippedManifest(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	writeBridgeManifest(t, lay, "1.0.23")
	writeProvisionedSDK(t, lay, "1.0.23")

	require.NoError(t, preflightSDK((mustLayout(t, lay)).private))

	// The pin comes from the shipped manifest, not from a constant in this binary: a
	// launcher that carried its own copy of the pin could disagree with the bridge
	// manifest the operator provisioned against.
	writeBridgeManifest(t, lay, "1.0.24")
	writeProvisionedSDK(t, lay, "1.0.24")
	require.NoError(t, preflightSDK((mustLayout(t, lay)).private),
		"the pin is whatever the shipped bridge manifest names")
}

// TestPreflightSDK_UnprovisionedTreeIsAnActionablePrerequisiteFailure keeps an
// installed-but-unprovisioned tree an explicit prerequisite failure that names the
// missing SDK and prints the exact provisioning command.
//
// This is the check that replaces a JavaScript module-resolution stack with one line
// an operator can act on. The command has to be the one the archive is built for: the
// shipped runtime running the shipped runtime's own npm, so an operator needs neither a
// global Node nor a global npm.
func TestPreflightSDK_UnprovisionedTreeIsAnActionablePrerequisiteFailure(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	writeBridgeManifest(t, lay, "1.0.23")

	err := preflightSDK((mustLayout(t, lay)).private)
	require.Error(t, err)
	require.Contains(t, err.Error(), packagelayout.SDKPackageName)
	require.Contains(t, err.Error(), "not provisioned")
	require.Contains(t, err.Error(), "1.0.23")
	require.Contains(t, err.Error(), filepath.ToSlash(lay.root))
	require.Contains(t, err.Error(), "ci --omit=dev")
	// The remedy is provisioning, not reinstalling: the archive is complete and the
	// operator is the one who fetches the SDK.
	require.NotContains(t, err.Error(), "reinstall the Cursor plugin package")
}

// TestPreflightSDK_MismatchedProvisionedVersionIsAFailure keeps a provisioned tree at
// the wrong SDK version a prerequisite failure rather than a warning. The bridge
// refuses such a tree anyway; catching it here means the operator sees why instead of
// an unhandled rejection inside the runtime.
func TestPreflightSDK_MismatchedProvisionedVersionIsAFailure(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	writeBridgeManifest(t, lay, "1.0.23")
	writeProvisionedSDK(t, lay, "1.0.22")

	err := preflightSDK((mustLayout(t, lay)).private)
	require.Error(t, err)
	require.Contains(t, err.Error(), "1.0.22")
	require.Contains(t, err.Error(), "1.0.23")
	require.Contains(t, err.Error(), "ci --omit=dev")
}

// TestPreflightSDK_UnreadableProvisionedMetadataIsAFailure keeps a provisioned tree the
// launcher cannot read an explicit failure. A tree with an unparsable package.json is
// not a working install, and reporting "unreadable" is a different remedy from
// reporting "missing".
func TestPreflightSDK_UnreadableProvisionedMetadataIsAFailure(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	writeBridgeManifest(t, lay, "1.0.23")
	path := writeProvisionedSDK(t, lay, "1.0.23")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	err := preflightSDK((mustLayout(t, lay)).private)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unreadable")
	require.Contains(t, err.Error(), packagelayout.SDKPackageName)
}

// TestPreflightSDK_VersionlessProvisionedMetadataIsAFailure keeps a package manifest
// with no version a failure instead of an empty string that happens to match nothing:
// inventing a version would let any tree through.
func TestPreflightSDK_VersionlessProvisionedMetadataIsAFailure(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	writeBridgeManifest(t, lay, "1.0.23")
	path := writeProvisionedSDK(t, lay, "1.0.23")
	require.NoError(t, os.WriteFile(path, []byte(`{"name":"@cursor/sdk"}`), 0o644))

	err := preflightSDK((mustLayout(t, lay)).private)
	require.Error(t, err)
	require.Contains(t, err.Error(), "version")
}

// TestPreflightSDK_MissingShippedBridgeManifestIsAFailure keeps an archive whose bridge
// manifest is gone an explicit prerequisite failure. Without it there is nothing to
// check the provisioned tree against, so the launcher cannot decide and says so
// instead of starting the runtime to find out.
func TestPreflightSDK_MissingShippedBridgeManifestIsAFailure(t *testing.T) {
	t.Parallel()

	lay := installPrivateLayout(t, true, true)
	writeProvisionedSDK(t, lay, "1.0.23")

	err := preflightSDK((mustLayout(t, lay)).private)
	require.Error(t, err)
	require.Contains(t, err.Error(), "private/bridge/package.json")
	require.Contains(t, err.Error(), "reinstall")
}

// writeBridgeManifest stages the shipped bridge manifest with one SDK pin.
func writeBridgeManifest(tb testing.TB, lay installedPrivateLayout, pin string) {
	tb.Helper()

	path := filepath.Join(lay.packageDir, "package.json")
	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	body := `{"name":"lip-cursor-sdk-bridge","version":"0.1.0","dependencies":{"@cursor/sdk":"` + pin + `"}}`
	require.NoError(tb, os.WriteFile(path, []byte(body), 0o644))
}

// writeProvisionedSDK stages the operator-provisioned SDK package metadata and returns
// its path.
func writeProvisionedSDK(tb testing.TB, lay installedPrivateLayout, version string) string {
	tb.Helper()

	path := filepath.Join(lay.packageDir, packagelayout.ModulesDirName,
		filepath.FromSlash(packagelayout.SDKPackageName), "package.json")
	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(tb, os.WriteFile(path,
		[]byte(`{"name":"`+packagelayout.SDKPackageName+`","version":"`+version+`"}`), 0o644))
	return path
}
