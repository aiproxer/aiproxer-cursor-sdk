package cursorsdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This file keeps the certification record and the pipeline that produces it from
// drifting apart, and keeps both from claiming more than they do.
//
// A certification document is only evidence if somebody re-derives it. A document that
// lists requirements without naming the test and the lane that satisfy them is a claim;
// a lane that exists but is not named is invisible evidence. So each requirement in
// scope is required to appear next to a named test and a named lane, and the platform
// scopes are required to agree with the packaging matrix that is allowed to declare
// them.

// certificationRequirements is the set task 4.1 certifies: the provider and stream
// semantics, the lifecycle and secret-safe diagnostics, and the certification record
// itself.
var certificationRequirements = []string{
	"4.1", "4.2", "4.3", "4.4", "4.5",
	"5.1", "5.2", "5.3", "5.4", "5.5",
	"6.2",
}

// TestCertificationEvidence_DocumentNamesEveryCertifiedRequirementAndItsProof keeps
// docs/certification.md a record rather than an assertion.
func TestCertificationEvidence_DocumentNamesEveryCertifiedRequirementAndItsProof(t *testing.T) {
	t.Parallel()

	doc := certificationDocument(t)
	lowered := strings.ToLower(strings.Join(strings.Fields(doc), " "))

	for _, requirement := range certificationRequirements {
		assert.Contains(t, doc, "Requirement "+requirement,
			"the certification record has to state requirement %s, not paraphrase it", requirement)
	}

	// Each entry has to name the evidence rather than gesture at it. These are the
	// proofs a reader would otherwise have to take on trust.
	for _, evidence := range []string{
		"TestPublicConformance_SharedContractSuiteRunsOverTheReleasedHostAdapter",
		"TestPublicConformance_ExecuteDeliversOrderedEventsAndOneTerminal",
		"TestPublicConformance_CancellationEndsTheRunWithoutLaterContent",
		"TestPublicConformance_CancelThenCloseLeavesNoGoroutines",
		"TestPublicConformance_ShutdownClosesTheInstanceIdempotentlyAndLeavesNoGoroutines",
		"TestPublicConformance_BoundedShutdownEndsAnInFlightAttemptWithoutHanging",
		"TestPublicConformance_RequiredCapabilityIsRefusedExplicitly",
		"TestPublicConformance_SecretBearingBridgeDiagnosticsAreRedactedOnTheWire",
		"TestInstalledLayout_ExecuteAndCancelThroughThePackagedPlugin",
		"TestStandaloneModule_HasNoReplaceDirective",
		"TestStandaloneModule_HasNoWorkspaceAboveIt",
		"TestPlatformSmoke_FakeBridgeLane",
		"go",
		"bridge-node",
		"package",
	} {
		assert.Contains(t, doc, evidence,
			"the certification record has to name %s as the evidence for a claim it makes", evidence)
	}

	// The shared corpus partition is published as one measured sentence. The corpus test
	// asserts the same three numbers from a real run, so the record and the measurement
	// fail together rather than one quietly going stale.
	assert.Contains(t, doc, "26 scenarios: 10 executed, 16 hard negatives",
		"the record has to state the corpus partition exactly as the corpus test measures it")

	// Requirement 4.1 is the one whose public-ABI proof is narrower than the
	// requirement. The record has to keep naming the relocated proofs that cover the
	// rest, or a reader would take the public wire test as evidence for reasoning, usage,
	// and tool activity that it never observes.
	for _, relocated := range []string{
		"internal/product/event_mapper_test.go",
		"internal/product/stream_test.go",
		"internal/product/run_sub_test.go",
		"internal/product/reasoning_profile_test.go",
		"internal/product/inventory_test.go",
	} {
		assert.Contains(t, doc, relocated,
			"requirement 4.1 spans semantics the public wire proofs do not exercise; the record has to "+
				"name the relocated proof that covers %s", relocated)
	}
	assert.Contains(t, doc, "**This is the text path only.**",
		"the record has to say which part of requirement 4.1 the public Execute proof actually covers")

	// The fixture table has to name the single-language fixtures as such. Two of the
	// eight are read by one language only, and "consumed by both languages" applied to
	// all of them would be wrong twice.
	for _, singleLanguage := range []string{
		"**Go only** (`internal/product/config_test.go`)",
		"**TypeScript only** (`bridge-node/src/models.test.ts`)",
	} {
		assert.Contains(t, doc, singleLanguage,
			"the fixture table has to mark %s rather than implying both languages read it", singleLanguage)
	}

	// The absences are the claims most worth writing down, because each is the kind of
	// thing a reader would otherwise assume. Each phrase here is one the record has to state
	// in that sense and nothing else in the document could satisfy by accident.
	for _, nonClaim := range []string{
		"never",
		"credential",
		"host artifact",
		"darwin",
	} {
		assert.Contains(t, lowered, nonClaim,
			"the certification record has to state the %s position explicitly", nonClaim)
	}
}

// TestCertificationEvidence_LiveScenariosStayOutOfDefaultVerification keeps the live
// provider lanes separate from the default ones.
//
// A live scenario spends a real Cursor quota against a real credential. Folding it into
// a default lane would make every push bill someone, so the opt-in has to be visible in
// the record and in the scripts the record points at.
func TestCertificationEvidence_LiveScenariosStayOutOfDefaultVerification(t *testing.T) {
	t.Parallel()

	lowered := strings.ToLower(strings.Join(strings.Fields(certificationDocument(t)), " "))
	for _, optIn := range []string{"cursor_sdk_live", "cursor_api_key"} {
		assert.Contains(t, lowered, optIn,
			"the record has to name the %s opt-in a live scenario needs", optIn)
	}
	assert.Contains(t, lowered, "opt-in",
		"the record has to say that live provider scenarios are opt-in rather than default")

	for _, script := range []string{
		filepath.Join("scripts", "test-cursor-sdk-live.sh"),
		filepath.Join("scripts", "test-cursor-sdk-live-bridge.sh"),
	} {
		body := readFileText(t, filepath.Join(repoRoot(t), script))
		assert.Contains(t, body, "CURSOR_SDK_LIVE",
			"%s has to gate on the opt-in flag so a default run cannot spend quota", script)
		assert.Contains(t, body, "BLOCKED",
			"%s has to report a blocked run rather than a green one when it is not opted in", script)
	}
}

// TestCertificationEvidence_NativeWindowsLaneRunsTheFullGoAndPlatformSuites is the
// fix for the gap that started this: the default Go lane was Linux-only, so the one
// platform the plugin also declares and packages had no full Go evidence of its own.
//
// The `package` matrix already runs on Windows, but it assembles and audits an archive
// and never runs the whole suite. A Windows-only regression outside packaging - a
// process-tree bug, a path assumption, a race - had nowhere to fail.
func TestCertificationEvidence_NativeWindowsLaneRunsTheFullGoAndPlatformSuites(t *testing.T) {
	t.Parallel()

	windows := verificationLane(t, "go-windows")
	assert.Equal(t, "windows-latest", windows.runsOn, "the Windows Go lane has to run natively on Windows")
	assert.True(t, windows.runsGOWORKOff,
		"the Windows lane has to hold the same standalone-module contract as the default lane")
	assert.True(t, windows.runsFullGoSuite,
		"the Windows lane has to run the complete `go test ./...`, not a package subset")
	assert.True(t, windows.runsPlatformSmoke,
		"the Windows lane has to run the fake-bridge platform smoke, which is the lifecycle evidence "+
			"that only a real subprocess tree can give")
}

// TestCertificationEvidence_MacOSLaneIsDevelopmentEvidenceOnly keeps macOS honest.
//
// The design allows macOS fake-bridge and lifecycle coverage as development
// certification and forbids advertising production Darwin support from it. So the lane
// has to exist, run the fake bridge, and stay out of the packaging matrix and the
// manifest - three statements that have to agree or one of them is a claim.
func TestCertificationEvidence_MacOSLaneIsDevelopmentEvidenceOnly(t *testing.T) {
	t.Parallel()

	darwin := verificationLane(t, "go-macos-dev")
	assert.Equal(t, "macos-latest", darwin.runsOn, "the macOS development lane has to run natively on macOS")
	assert.True(t, darwin.runsGOWORKOff,
		"the macOS lane has to hold the same standalone-module contract as the default lane")
	assert.True(t, darwin.runsPlatformSmoke,
		"the macOS lane has to run the fake-bridge platform smoke; that is the development evidence it exists for")

	// The packaging matrix and the manifest are the two places a platform claim is
	// published. Neither may name Darwin while the only macOS evidence is a fake bridge.
	require.ElementsMatch(t, []string{"ubuntu-latest", "windows-latest"}, packageRunnerMatrix(t),
		"the packaging matrix declares which platforms are natively assembled; macOS is not one of them")
	for _, platform := range manifestTemplatePlatforms(t) {
		assert.NotContains(t, platform, "darwin",
			"the manifest declares production platform support, and macOS has only fake-bridge evidence")
	}
}

func certificationDocument(tb testing.TB) string {
	tb.Helper()

	path := filepath.Join(repoRoot(tb), "docs", "certification.md")
	_, err := os.Stat(path)
	require.NoError(tb, err,
		"docs/certification.md is the certification record task 4.1 produces; without it the suites "+
			"prove behaviour but nobody can tell which requirement each one covers")
	return readFileText(tb, path)
}

// verificationLane is the parsed shape of one job in the verify workflow.
type verificationLaneShape struct {
	runsOn            string
	runsGOWORKOff     bool
	runsFullGoSuite   bool
	runsPlatformSmoke bool
}

func verificationLane(tb testing.TB, job string) verificationLaneShape {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(repoRoot(tb), ".github", "workflows", "verify.yml"))
	var workflow struct {
		Jobs map[string]struct {
			RunsOn string            `yaml:"runs-on"`
			Env    map[string]string `yaml:"env"`
			Steps  []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(tb, yaml.Unmarshal([]byte(raw), &workflow))

	definition, ok := workflow.Jobs[job]
	require.True(tb, ok, "the verify workflow declares no %q job", job)

	shape := verificationLaneShape{runsOn: definition.RunsOn, runsGOWORKOff: definition.Env["GOWORK"] == "off"}
	for _, step := range definition.Steps {
		shape.runsFullGoSuite = shape.runsFullGoSuite ||
			strings.Contains(step.Run, "go test ./...")
		shape.runsPlatformSmoke = shape.runsPlatformSmoke ||
			strings.Contains(step.Run, "TestPlatformSmoke_")
	}
	return shape
}

func packageRunnerMatrix(tb testing.TB) []string {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(repoRoot(tb), ".github", "workflows", "verify.yml"))
	var workflow struct {
		Jobs struct {
			Package struct {
				Strategy struct {
					Matrix struct {
						OS []string `yaml:"os"`
					} `yaml:"matrix"`
				} `yaml:"strategy"`
			} `yaml:"package"`
		} `yaml:"jobs"`
	}
	require.NoError(tb, yaml.Unmarshal([]byte(raw), &workflow))
	require.NotEmpty(tb, workflow.Jobs.Package.Strategy.Matrix.OS, "the packaging matrix declares no runner")
	return workflow.Jobs.Package.Strategy.Matrix.OS
}
