package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// TestMain_LayoutReportIsTheSinglePackagingContract keeps the packaging scripts
// honest: they read archive names from this report instead of restating them, so
// the report has to name every fixed file, the required file set, and the
// declared platforms in one stable, machine-readable shape.
func TestMain_LayoutReportIsTheSinglePackagingContract(t *testing.T) {
	t.Parallel()

	report := runLayoutReport(t, runtime.GOOS, runtime.GOARCH)
	require.Equal(t, "golip.cursorsdk.package.layout/v1", report["schema"])
	require.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, report["platform"])
	require.Equal(t, packagelayout.ExeSuffixFor(runtime.GOOS), report["exe_suffix"])
	require.Equal(t, "plugin.backendplugin.json", report["manifest"])
	require.Equal(t, "checksums.sha256", report["checksums"])
	require.Equal(t, "compatibility.json", report["compatibility"])
	require.Equal(t, "LICENSES", report["licenses_dir"])
	require.Equal(t, "private/bridge/bin/lip-cursor-sdk-bridge.js", report["bridge_entry"])
	require.Equal(t, "private/node/node[.exe]", report["private_runtime_doc"])

	for _, key := range []string{"os", "arch", "manifest", "outer_executable",
		"launcher", "launcher_name", "bridge_package_dir", "bridge_entry", "bridge_dist",
		"bridge_modules", "bridge_package_json", "bridge_package_lock",
		"provisioned_prefix", "sdk_package_json", "sdk_package_name", "sdk_provision_command",
		"private_runtime", "private_runtime_doc", "private_npm_root", "private_npm_cli",
		"private_npm_license", "private_npm_modules",
		"private_prefix", "compatibility", "checksums", "licenses_dir",
		"checksum_separator", "declared_platforms"} {
		require.NotEmpty(t, report[key], "layout report key %q", key)
	}
	require.Equal(t, []any{"shipped", "installed"}, report["tree_states"])

	// The staged npm tree carries its own license texts, and the notice points at them
	// rather than at the Node distribution license, which does not cover npm. Both paths
	// are reported so a packager names the real staged files instead of hardcoding a
	// spelling that the layout could move out from under it.
	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	require.Equal(t, archive.PrivateRuntimeNPMLicensePath(), report["private_npm_license"])
	require.Equal(t, archive.PrivateRuntimeNPMModulesPath(), report["private_npm_modules"])

	// exe_suffix is the one report field that is legitimately empty: it is empty
	// exactly on the platforms whose executables carry no suffix. An empty value
	// there is the contract, not a missing field, so it is compared instead.
	require.Equal(t, packagelayout.ExeSuffixFor(runtime.GOOS), report["exe_suffix"])

	required := stringSlice(t, report["required_entries"])
	require.Contains(t, required, "plugin.backendplugin.json")
	require.Contains(t, required, "checksums.sha256")
	require.Contains(t, required, report["bridge_entry"])
	require.Contains(t, required, report["private_runtime"])
	// The provisioned dependency tree is not archive content, so it is reported and
	// required to be absent rather than required to be present.
	require.NotContains(t, required, report["bridge_modules"])
	require.Contains(t, required, report["bridge_package_lock"])
	require.Contains(t, required, report["private_npm_cli"])

	priv := stringSlice(t, report["private_entries"])
	require.NotEmpty(t, priv)
	for _, rel := range priv {
		require.True(t, strings.HasPrefix(rel, "private/"), rel)
		require.Contains(t, required, rel)
	}
	require.Contains(t, stringSlice(t, report["declared_platforms"]), runtime.GOOS+"/"+runtime.GOARCH)
}

// TestMain_LayoutReportRejectsUndeclaredPlatform keeps a cross-compiled platform
// out of the packaging contract: the report is the packager's only source of
// layout names, so an undeclared platform has to fail here instead of producing
// an archive whose names came from nowhere.
func TestMain_LayoutReportRejectsUndeclaredPlatform(t *testing.T) {
	t.Parallel()

	cmd := exec.Command(goToolPath(t), "run", ".", "layout", "-platform", "linux/386")
	cmd.Dir = filepath.Dir(thisFile(t))
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "linux/386")
	require.NotContains(t, string(out), `"schema"`)
}

// runLayoutReport runs the layout command and decodes its JSON report.
func runLayoutReport(tb testing.TB, goos, goarch string) map[string]any {
	tb.Helper()

	cmd := exec.Command(goToolPath(tb), "run", ".", "layout", "-platform", goos+"/"+goarch)
	cmd.Dir = filepath.Dir(thisFile(tb))
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	require.NoError(tb, err, "layout report: %v", err)

	var report map[string]any
	require.NoError(tb, json.Unmarshal(out, &report), "layout report was not JSON: %s", out)
	return report
}

func stringSlice(tb testing.TB, v any) []string {
	tb.Helper()

	raw, ok := v.([]any)
	require.True(tb, ok, "expected a JSON array, got %T", v)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		require.True(tb, ok, "expected a JSON string, got %T", item)
		out = append(out, s)
	}
	return out
}

func goToolPath(tb testing.TB) string {
	tb.Helper()

	exe, err := exec.LookPath("go")
	require.NoError(tb, err)
	return exe
}

func thisFile(tb testing.TB) string {
	tb.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(tb, ok)
	return file
}
