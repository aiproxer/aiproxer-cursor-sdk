package cursorsdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// These checks protect the release build's pinned runtime and the explicit
// advisory gates. Lockfile scanners do not inspect code bundled inside the
// Cursor SDK; the separately shipped npm has its own inventory and release gate.
type dependencySecurityWorkflow struct {
	Jobs map[string]struct {
		If              string            `yaml:"if"`
		ContinueOnError bool              `yaml:"continue-on-error"`
		Needs           []string          `yaml:"needs"`
		Env             map[string]string `yaml:"env"`
		Steps           []struct {
			Name             string            `yaml:"name"`
			Uses             string            `yaml:"uses"`
			Run              string            `yaml:"run"`
			WorkingDirectory string            `yaml:"working-directory"`
			If               string            `yaml:"if"`
			ContinueOnError  bool              `yaml:"continue-on-error"`
			With             map[string]string `yaml:"with"`
		} `yaml:"steps"`
		Strategy struct {
			Matrix struct {
				Include []struct {
					Platform string `yaml:"platform"`
					NodeDist string `yaml:"node_dist"`
				} `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
	} `yaml:"jobs"`
}

func readDependencySecurityWorkflow(t *testing.T, name string) dependencySecurityWorkflow {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	require.NoError(t, err)
	var workflow dependencySecurityWorkflow
	require.NoError(t, yaml.Unmarshal(content, &workflow))
	return workflow
}

func TestDependencySecurity_CIAdvisoryGates(t *testing.T) {
	verify := readDependencySecurityWorkflow(t, "verify.yml")
	release := readDependencySecurityWorkflow(t, "release.yml")
	for _, tc := range []struct {
		name, job, step, command, directory string
		workflow                            dependencySecurityWorkflow
	}{
		{"verify-go", "go", "Check Go vulnerabilities", "go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -test ./...", "", verify},
		{"verify-npm", "bridge-node", "Check npm vulnerabilities", "npm audit --audit-level=high", "bridge-node", verify},
		{"release-go", "security", "Check Go vulnerabilities", "go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -test ./...", "", release},
		{"release-npm", "security", "Check npm vulnerabilities", "npm audit --audit-level=high", "bridge-node", release},
		{"verify-packaged-test", "bridge-node", "Test packaged npm security checks", "node --test scripts/audit-bundled-npm.test.mjs scripts/check-packaged-npm.test.cjs", "", verify},
		{"verify-packaged-scan", "bridge-node", "Check packaged npm vulnerabilities", "node scripts/audit-bundled-npm.mjs bridge-node/node_modules/npm --fail-on-high", "", verify},
		{"release-packaged-test", "security", "Test packaged npm security checks", "node --test scripts/audit-bundled-npm.test.mjs scripts/check-packaged-npm.test.cjs", "", release},
		{"release-packaged-scan", "security", "Check packaged npm vulnerabilities", "node scripts/audit-bundled-npm.mjs bridge-node/node_modules/npm --fail-on-high", "", release},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, ok := tc.workflow.Jobs[tc.job]
			require.True(t, ok)
			require.Empty(t, job.If, "the advisory job must not be conditional")
			require.False(t, job.ContinueOnError)
			found := false
			for _, step := range job.Steps {
				if step.Name != tc.step {
					continue
				}
				require.False(t, found, "duplicate advisory gate")
				found = true
				require.Equal(t, tc.command, step.Run)
				require.Equal(t, tc.directory, step.WorkingDirectory)
				require.Empty(t, step.If, "the advisory step must not be conditional")
				require.False(t, step.ContinueOnError)
			}
			require.True(t, found, "the advisory gate must exist")
		})
	}
}

func TestDependencySecurity_ReleaseScansTaggedRevisionBeforePackaging(t *testing.T) {
	workflow := readDependencySecurityWorkflow(t, "release.yml")
	security, ok := workflow.Jobs["security"]
	require.True(t, ok)
	require.Contains(t, security.Needs, "guard")
	require.Equal(t, "off", security.Env["GOWORK"])
	require.Contains(t, workflow.Jobs["package"].Needs, "security")
	require.Contains(t, workflow.Jobs["publish"].Needs, "package")

	checkoutIndex, goSetupIndex, goScanIndex, npmScanIndex := -1, -1, -1, -1
	for i, step := range security.Steps {
		switch {
		case strings.HasPrefix(step.Uses, "actions/checkout@"):
			require.Equal(t, -1, checkoutIndex, "duplicate checkout")
			checkoutIndex = i
			require.Equal(t, "${{ needs.guard.outputs.commit }}", step.With["ref"])
		case strings.HasPrefix(step.Uses, "actions/setup-go@"):
			goSetupIndex = i
			require.Equal(t, "go.mod", step.With["go-version-file"])
		case step.Name == "Check Go vulnerabilities":
			goScanIndex = i
		case step.Name == "Check npm vulnerabilities":
			npmScanIndex = i
		}
	}
	require.GreaterOrEqual(t, checkoutIndex, 0)
	require.Greater(t, goSetupIndex, checkoutIndex)
	require.Greater(t, goScanIndex, goSetupIndex)
	require.Greater(t, npmScanIndex, checkoutIndex)
}

func TestDependencySecurity_ReleaseScansActualUnpackedNPM(t *testing.T) {
	workflow := readDependencySecurityWorkflow(t, "release.yml")
	job, ok := workflow.Jobs["package"]
	require.True(t, ok)
	unpack, scan := -1, -1
	for i, step := range job.Steps {
		switch step.Name {
		case "Unpack the archive into a fresh tree":
			unpack = i
		case "Check npm in the unpacked release archive":
			require.Equal(t, -1, scan)
			scan = i
			require.Empty(t, step.If)
			require.False(t, step.ContinueOnError)
			for _, want := range []string{"$env:INSTALL_ROOT", "$layout.private_npm_root", "audit-bundled-npm.mjs", "--fail-on-high", "$LASTEXITCODE"} {
				require.Contains(t, step.Run, want)
			}
		}
	}
	require.GreaterOrEqual(t, unpack, 0)
	require.Greater(t, scan, unpack, "the release must scan the unpacked archive")
}

func TestDependencySecurity_ReleaseNodeMatchesVerification(t *testing.T) {
	const nodeVersion = "22.23.5"
	verify := readDependencySecurityWorkflow(t, "verify.yml")
	release := readDependencySecurityWorkflow(t, "release.yml")

	for _, tc := range []struct {
		name     string
		workflow dependencySecurityWorkflow
		job      string
	}{
		{"bridge tests", verify, "bridge-node"},
		{"package tests", verify, "package"},
		{"release security", release, "security"},
		{"release package", release, "package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, ok := tc.workflow.Jobs[tc.job]
			require.True(t, ok)
			found := false
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, "actions/setup-node@") {
					continue
				}
				require.False(t, found, "duplicate Node setup")
				found = true
				require.Equal(t, nodeVersion, step.With["node-version"])
			}
			require.True(t, found, "Node setup must be present")
		})
	}

	job := release.Jobs["package"]
	distributions := map[string]string{}
	for _, item := range job.Strategy.Matrix.Include {
		distributions[item.Platform] = item.NodeDist
	}
	require.Equal(t, map[string]string{
		"linux/amd64":   "node-v" + nodeVersion + "-linux-x64.tar.gz",
		"windows/amd64": "node-v" + nodeVersion + "-win-x64.zip",
	}, distributions)
}
