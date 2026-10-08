package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// This file covers the release report, which is what a release process reads instead of
// parsing release metadata itself.
//
// The report exists because the tag, the version, the declared platforms, and the host
// artifact each platform was certified against are five facts that have to agree, and every
// consumer that wanted them separately would read release.yaml with its own parser and its own
// idea of what a missing key means. The disagreement would surface in a published artifact,
// which is the last place anyone can still fix it cheaply.
//
// So what is tested here is behaviour: what the report says for a declaration that is
// complete, and what it refuses to say for one that is not. Nothing here reads the shape of a
// workflow or a script; how the report is consumed is proved by running the release.

// TestRelease_ReportsTheDeclaredReleaseAndItsCertifiedHostArtifacts keeps the report a
// report of the declaration.
//
// Every value is one a release process acts on, so every value has to come from the release
// metadata rather than be composed by the reader: the tag and version decide what is
// published, and the host release, asset, binary, and digest decide which host artifact is
// certified against on this platform. The two URLs are derived from those recorded parts, so
// a reader cannot be pointed at a host release this repository does not name - and a wrong
// composition is a wrong download, which is the one thing this report must not be able to
// produce.
func TestRelease_ReportsTheDeclaredReleaseAndItsCertifiedHostArtifacts(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	certifyRelease(t, lay)
	report := releaseReportFor(t, lay)

	require.Equal(t, releaseReportSchema, report["schema"])
	require.Equal(t, "io.golip.backend.cursorsdk", report["plugin_id"])
	require.Equal(t, "0.1.0", report["plugin_version"])
	require.Equal(t, "cursorsdk-v0.1.0", report["tag"])
	require.Equal(t, "certified", report["host_certification"])
	require.Equal(t, []any{"linux/amd64", "windows/amd64"}, report["declared_platforms"])

	entries := decodeList(t, report["platforms"])
	require.Len(t, entries, 2, "one entry per declared platform")

	contracts := map[string]string{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		require.True(t, ok, "a platform entry has to be an object")
		platform, _ := entry["platform"].(string)
		archive, err := packagelayout.ForPlatform(splitTestPlatform(platform))
		require.NoError(t, err)

		require.Equal(t, rootModule, entry["host_project"])
		require.Equal(t, "v0.1.0", entry["host_version"])
		require.NotEmpty(t, entry["host_release_asset"])
		require.NotEmpty(t, entry["host_checksums_asset"])
		require.NotEmpty(t, entry["host_binary"])
		require.Len(t, entry["host_artifact_sha256"], 64,
			"the report has to carry the measured digest whole, or nothing can be checked against it")
		require.Equal(t, archive.LauncherPath(), entry["bridge_executable_rel"],
			"the report has to name the packaged launcher path this platform's contract points at, spelled as the "+
				"archive layout spells it")

		// Both URLs are composed from the recorded parts, so they cannot name a different
		// project, a different tag, or a different asset than the ones the certification ran
		// against. A doubled host name or a v-less version here is a download of nothing.
		project := entry["host_project"].(string)
		version := entry["host_version"].(string)
		require.Equal(t, "https://"+project+"/releases/download/"+version+"/"+entry["host_release_asset"].(string),
			entry["host_release_url"])
		require.Equal(t, "https://"+project+"/releases/download/"+version+"/"+entry["host_checksums_asset"].(string),
			entry["host_checksums_url"])

		contracts[platform] = entry["companion_contract"].(string)
	}

	// The two platforms' contracts differ, because the host binds the verified executable
	// differently on each. A report that flattened them would tell an operator on one of
	// them to do the wrong thing.
	require.Equal(t, string(packagelayout.CompanionPackagedDefault), contracts["linux/amd64"])
	require.Equal(t, string(packagelayout.CompanionExplicitBridgeExecutable), contracts["windows/amd64"])
}

// TestRelease_RefusesADeclarationItCannotReport keeps the report from being a summary of a
// release nobody certified.
//
// The report is what a release process acts on, so a declaration it cannot state - a certified
// posture with no artifacts, an artifact for a platform the manifest does not declare, a
// declared platform with no artifact, a malformed digest, or a contract outside the declared
// vocabulary - has to fail here rather than be reported with a gap in it. A release process
// reading a partial report is a release process acting on a certification nobody performed.
func TestRelease_RefusesADeclarationItCannotReport(t *testing.T) {
	t.Parallel()

	t.Run("an uncertified release reports no platforms", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		report := releaseReportFor(t, lay)

		require.Equal(t, "uncertified", report["host_certification"],
			"the default fixture declares the posture release.yaml ships with")
		require.Empty(t, report["platforms"],
			"an uncertified release has no certified platform to report, and reporting one would invent evidence")
		require.NotEmpty(t, report["tag"], "the tag and the version are reported whatever the posture is")
	})

	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name:  "a certified release with no artifact",
			alter: func(t *testing.T, lay *stagedRelease) { certifyPlatforms(t, lay) },
			wants: "certified_host_artifacts",
		},
		{
			name: "an artifact for an undeclared platform",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, "  - platform: linux/amd64", "  - platform: darwin/amd64")
			},
			wants: "platform",
		},
		{
			name: "a declared platform with no artifact",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyPlatforms(t, lay, "windows/amd64")
			},
			wants: "certified_host_artifacts for linux/amd64",
		},
		{
			name: "an artifact digest that is not a digest",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, windowsHostDigest, "v0.1.0")
			},
			wants: "sha256",
		},
		{
			name: "an artifact with no host release named",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, "    host_version: v0.1.0", "    host_version:")
			},
			wants: "host_version",
		},
		{
			name: "a companion contract nobody documented",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay,
					"    companion_contract: "+string(packagelayout.CompanionExplicitBridgeExecutable),
					"    companion_contract: whatever-felt-right")
			},
			wants: "companion_contract",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.alter(t, lay)

			err := runReleaseCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
		})
	}
}

// TestRelease_RefusesARepositoryWithNoDeclaredRelease keeps the report from being asked for
// out of a directory that is not this project.
//
// The reader resolves release.yaml and the manifest template relative to the repository it is
// pointed at, and refuses rather than reporting an empty release: a report with no tag and no
// platforms reads as a declaration, and a release process that acted on it would publish
// nothing under a name nobody chose.
func TestRelease_RefusesARepositoryWithNoDeclaredRelease(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		stage func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name:  "no release metadata",
			stage: func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.release)) },
			wants: "release.yaml",
		},
		{
			name:  "no manifest template",
			stage: func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.template)) },
			wants: "manifest template",
		},
		{
			name: "a manifest template that declares no platform",
			stage: func(t *testing.T, lay *stagedRelease) {
				patchRelease(t, lay, "manifest_template: manifest/template.backendplugin.json",
					"manifest_template: manifest/absent.json")
			},
			wants: "manifest template",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.stage(t, lay)

			err := runReleaseCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
		})
	}
}

// releaseReportFor runs the release reader over the fixture repository and returns what it
// reported.
func releaseReportFor(tb testing.TB, lay *stagedRelease) map[string]any {
	tb.Helper()

	require.NoError(tb, runReleaseCmd(tb, lay))
	return decodeMap(tb, releaseOutput(tb, lay))
}

// runReleaseCmd runs the release reader over the fixture repository.
func runReleaseCmd(tb testing.TB, lay *stagedRelease) error {
	tb.Helper()

	_, err := runReleaseProcess(tb, lay)
	return err
}

// releaseOutput returns what the release reader printed.
func releaseOutput(tb testing.TB, lay *stagedRelease) string {
	tb.Helper()

	out, err := runReleaseProcess(tb, lay)
	require.NoError(tb, err)
	return out
}

// runReleaseProcess runs the release reader through the toolchain, the same way the packaging
// scripts and the release workflow run it, and hands back what it printed.
func runReleaseProcess(tb testing.TB, lay *stagedRelease) (string, error) {
	tb.Helper()

	cmd := exec.Command(goToolPath(tb), "run", ".", "release", "-repo", filepath.Dir(lay.release))
	cmd.Dir = thisFileDir(tb)
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &renderError{output: string(out), err: err}
	}
	return strings.TrimSpace(string(out)), nil
}
