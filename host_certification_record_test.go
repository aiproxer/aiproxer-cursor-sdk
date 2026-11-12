package cursorsdk_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This file keeps the recorded real-host certification result honest.
//
// The gate in host_release_certification_test.go is opt-in because it needs an artifact
// this repository does not build. That has a cost: it does not run on a push, so nothing
// mechanically stops the prose around it from claiming a certification that was never
// performed, or drifting away from what the gate asserts.
//
// So the prose is bound to the gate instead. Each assertion here checks one way a release
// claim could become false while the suite stays green: a posture that no longer matches
// the measured result, a documented command that does not exist, an operator instruction
// whose path no longer matches the archive layout, or a measured platform difference that
// has quietly stopped being described.

// hostCertifiedRelease declares the host release this repository's certification ran
// against. It is recorded here rather than only in prose so the documents, the release
// metadata, and the gate all name the same artifact.
const (
	// hostCertifiedHostProject is the host distribution whose release the gate ran against.
	hostCertifiedHostProject = "github.com/matdev83/go-llm-interactive-proxy"
	// hostCertifiedHostVersion is the released host version that was measured.
	hostCertifiedHostVersion = "v0.1.0"
)

// hostReleaseMetadata is the release-metadata subset the host-posture audit reads.
type hostReleaseMetadata struct {
	HostCertification       string `yaml:"host_certification"`
	HostCertificationReason string `yaml:"host_certification_reason"`
}

func readHostReleaseMetadata(tb testing.TB) hostReleaseMetadata {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(repoRoot(tb), "release.yaml"))
	var meta hostReleaseMetadata
	require.NoError(tb, yaml.Unmarshal([]byte(raw), &meta))
	return meta
}

// hostCertificationDocument is docs/certification.md, which carries both the plugin-side
// and the real-host records.
func hostCertificationDocument(tb testing.TB) string {
	tb.Helper()
	return readFileText(tb, filepath.Join(repoRoot(tb), "docs", "certification.md"))
}

// TestHostCertificationRecord_NamesTheHostReleaseTheMeasurementRanAgainst keeps the
// declared evidence tied to a release anybody can download.
//
// A digest is only evidence about a version if the record says which version, and it is only
// checkable if it says which artifact of that release was measured. Both are declared per
// platform, so this asserts they are declared rather than asserting any particular digest:
// the digests themselves are the measurement, and this repository records them in one place
// rather than restating them here.
func TestHostCertificationRecord_NamesTheHostReleaseTheMeasurementRanAgainst(t *testing.T) {
	t.Parallel()

	meta := readCertifiedReleaseMetadata(t)
	// A release's asset names carry its version without the leading v, so the relation the
	// assertion can hold is between the recorded version and the recorded asset, not between
	// the recorded version and one spelling of it.
	version := strings.TrimPrefix(hostCertifiedHostVersion, "v")
	for _, artifact := range meta.Artifacts {
		assert.Equal(t, hostCertifiedHostProject, artifact.HostProject,
			"%s has to name the host project the measurement ran against", artifact.Platform)
		assert.Equal(t, hostCertifiedHostVersion, artifact.HostVersion,
			"%s has to name the host release version the measurement ran against", artifact.Platform)
		assert.Contains(t, artifact.HostReleaseAsset, version,
			"%s has to name an asset of that same release; a digest measured against some other artifact is not "+
				"evidence for this host version", artifact.Platform)
		assert.NotEmpty(t, artifact.HostChecksumsAsset,
			"%s has to name the checksum file the host published its artifacts in", artifact.Platform)
		assert.NotEmpty(t, artifact.HostBinary,
			"%s has to name the binary inside the asset the measurement ran", artifact.Platform)
	}
}

// TestHostCertificationRecord_DocumentsNameTheGateAndItsOptIn keeps the real-host record
// auditable.
//
// A record a reader cannot re-derive is a claim. So the certification record has to name
// the gate that produced the result and the environment it needs, and the packaging
// decision document has to carry the same posture rather than leaving the two to disagree.
func TestHostCertificationRecord_DocumentsNameTheGateAndItsOptIn(t *testing.T) {
	t.Parallel()

	doc := hostCertificationDocument(t)
	for _, evidence := range []string{
		"TestHostReleaseCertification_RealHostInstallTrustAndOptionalActivation",
		hostCertifiedHostProject,
		hostCertifiedHostVersion,
		hostCertGateEnv,
		hostCertBinaryEnv,
		hostCertSHA256Env,
		hostCertPluginRootEnv,
		"Requirement 3.1, 3.2, 3.3, 3.4, 4.5, 6.1",
	} {
		assert.Contains(t, doc, evidence,
			"the certification record has to name %s so a reader can re-derive the result", evidence)
	}

	// The requirements in scope are each named, so a reader can tell which acceptance
	// criteria the real-host run covers and which it does not.
	for _, requirement := range []string{"3.1", "3.2", "3.3", "3.4", "4.5", "6.1"} {
		assert.Contains(t, doc, "| "+requirement+" |",
			"the real-host record has to map requirement %s to the case that covered it", requirement)
	}

	// The gate must not be mistakable for part of the default verification.
	lowered := strings.ToLower(doc)
	assert.Contains(t, lowered, "opt-in",
		"the record has to say the real-host gate is opt-in rather than default verification")

	// The packaging decision and the record must agree on the posture, and the posture is
	// read from release.yaml rather than written here: a test that pinned one of the two
	// words would have to be edited at every flip, and would keep passing in the direction
	// that hid the change.
	packaging := readFileText(t, filepath.Join(repoRoot(t), "docs", "packaging.md"))
	assert.Contains(t, packaging, hostCertifiedHostVersion,
		"the packaging decision has to name the host release the certification ran against")
	assert.Contains(t, packaging, readHostReleaseMetadata(t).HostCertification,
		"the packaging decision has to state the same posture release.yaml declares")
}

// TestHostCertificationRecord_MeasuredPlatformDifferenceIsRecordedWithItsAdoptedRemedy
// keeps the measured difference usable.
//
// It is a measured difference between the two platforms, not a defect, and it now has an
// adopted remedy. So the record has to name what the host does, what the plugin then
// reports, the operator remedy the plugin itself prints, and the fact that the remedy was
// exercised successfully - otherwise a reader would have to re-derive the remedy from the
// test.
//
// It also has to keep the difference scoped to the platform it was measured on. The default
// resolution works on one declared platform and not the other, so a record that described it
// as broken everywhere would send a Linux operator to set a field they do not need, and one
// that described it as working everywhere would send a Windows operator to a bootstrap
// failure.
func TestHostCertificationRecord_MeasuredPlatformDifferenceIsRecordedWithItsAdoptedRemedy(t *testing.T) {
	t.Parallel()

	doc := hostCertificationDocument(t)
	for _, subject := range []string{
		"bridge_executable",
		"private/bridge",
	} {
		assert.Contains(t, doc, subject,
			"the record has to name %s as part of the measured platform difference", subject)
	}
	for _, platform := range packagelayout.SupportedPlatforms() {
		assert.Contains(t, doc, platform,
			"the record has to state the measured outcome for %s", platform)
	}
	for _, measured := range []string{"REACHABLE", "UNREACHABLE"} {
		assert.Contains(t, doc, measured,
			"the record has to quote the gate's own %s verdict so the prose cannot drift from the measurement", measured)
	}
	assert.Contains(t, doc, "explicit prerequisite",
		"the record has to state that the packaged default fails as an explicit prerequisite rather than silently")
	assert.Contains(t, strings.ToLower(doc), "adopted",
		"the record has to say the per-platform difference is an adopted contract, not an open question")

	// The installation guide has to answer the operator's question per platform rather
	// than once, because the answer differs.
	guide := readFileText(t, filepath.Join(repoRoot(t), "docs", "installation.md"))
	assert.Contains(t, guide, "measured working",
		"the installation guide has to say the default was measured working where it was")
	assert.Contains(t, guide, "not** working",
		"the installation guide has to say where the default was measured not working")
}

// TestHostCertificationRecord_InstallationGuidePrintsTheOperatorRemedy keeps the operator
// procedure true.
//
// The remedy for the measured finding is an explicit bridge_executable pointing at the
// packaged launcher, and that path differs per platform because the executable suffix and
// the runtime layout do. The guide therefore has to print the value derived from the layout
// contract rather than one hand-written path, or an operator on the other platform is told
// to point at a file that does not exist.
func TestHostCertificationRecord_InstallationGuidePrintsTheOperatorRemedy(t *testing.T) {
	t.Parallel()

	guide := readFileText(t, filepath.Join(repoRoot(t), "docs", "installation.md"))
	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)

	assert.Contains(t, guide, archive.LauncherPath(),
		"the installation guide has to print the packaged launcher path an operator points bridge_executable at")
	assert.Contains(t, guide, "bridge_executable",
		"the installation guide has to name the field that carries the operator remedy")
	for _, platform := range packagelayout.SupportedPlatforms() {
		assert.Contains(t, guide, platform,
			"the installation guide has to state the remedy per platform; it differs for %s", platform)
	}
}

// TestHostCertificationRecord_NoClaimOutrunsTheGate keeps the record inside what was run.
//
// The gate measured two host binaries of one host release, one per platform, with a
// deterministic bridge in place of the Cursor SDK and no race detector. A record that read as
// certification of every host, of live provider behaviour, of every platform, or of a race
// that was never run would be claiming more than any run established, so each of those is
// named explicitly as absent.
//
// Each phrase is one the record has to state in that sense; a broader phrase like "no claim"
// would be satisfied by any sentence in the document that disclaims anything at all, which is
// why none is used here.
func TestHostCertificationRecord_NoClaimOutrunsTheGate(t *testing.T) {
	t.Parallel()

	doc := strings.ToLower(strings.Join(strings.Fields(hostCertificationDocument(t)), " "))
	for _, nonClaim := range []string{
		"no linux host evidence is missing",
		"not live provider runs",
		"did **not** run with `-race`",
		"a certification is a statement about specific host artifacts",
		"not a certification about every host",
		"not macos support",
	} {
		assert.Contains(t, doc, nonClaim,
			"the record has to state the %s position explicitly, so a green gate is not read as more than it is",
			nonClaim)
	}
}
