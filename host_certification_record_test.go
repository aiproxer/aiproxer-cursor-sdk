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
// whose path no longer matches the archive layout, or a blocker that has quietly stopped
// being described.

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

// TestHostCertificationRecord_PostureStatesTheMeasuredResultNotTheOldAbsence keeps the
// release metadata's reason true.
//
// The reason used to say no downloadable host release existed. That is no longer the state
// of the world: v0.1.0 is published and was measured. A reason that still claimed the
// deferral would be stale in the direction that hides work that was actually done, and it
// would leave a reader with no way to tell "we could not test this" from "we tested it and
// found something".
func TestHostCertificationRecord_PostureStatesTheMeasuredResultNotTheOldAbsence(t *testing.T) {
	t.Parallel()

	meta := readHostReleaseMetadata(t)
	require.Equal(t, "uncertified", meta.HostCertification,
		"the released host does not hand a discovered plugin its install tree on the platform that was "+
			"measured, so this release cannot be certified against it yet")
	require.NotEmpty(t, meta.HostCertificationReason,
		"an uncertified posture has to carry the measured reason, not an absent key")

	reason := strings.ToLower(strings.Join(strings.Fields(meta.HostCertificationReason), " "))
	assert.NotContains(t, reason, "no downloadable",
		"a published host release exists now; the reason has to state what the measurement found")
	assert.NotContains(t, reason, "deferred pending separate maintainer authorization",
		"host publication happened; the reason has to name the measured finding instead")
	assert.Contains(t, reason, hostCertifiedHostVersion,
		"the reason has to name the host release the measurement ran against")
	assert.Contains(t, reason, "staging",
		"the measured finding is that the host stages the verified executable; the reason has to say so")
	for _, platform := range packagelayout.SupportedPlatforms() {
		assert.Contains(t, reason, platform,
			"the measurement covers %s, so the reason has to name what was measured there", platform)
	}
	assert.Contains(t, reason, "unreachable on windows",
		"the reason has to say which platform the packaged default fails on, not just that something failed")
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

	// The packaging decision and the record must agree on the posture.
	packaging := readFileText(t, filepath.Join(repoRoot(t), "docs", "packaging.md"))
	assert.Contains(t, packaging, hostCertifiedHostVersion,
		"the packaging decision has to name the host release the certification ran against")
	assert.Contains(t, packaging, "uncertified",
		"the packaging decision has to state the same posture release.yaml declares")
}

// TestHostCertificationRecord_BlockingFindingIsRecordedWithItsRemedy keeps the measured
// finding usable.
//
// A blocker nobody can act on is not a blocker, it is a dead end. So the record has to name
// what the host does, what the plugin then reports, the operator remedy the plugin itself
// prints, and the fact that the remedy was exercised successfully - otherwise a reader would
// have to re-derive the workaround from the test.
//
// It also has to keep the finding scoped to the platform it was measured on. The default
// resolution works on one declared platform and not the other, so a record that described it
// as broken everywhere would send a Linux operator to set a field they do not need, and one
// that described it as working everywhere would send a Windows operator to a bootstrap
// failure.
func TestHostCertificationRecord_BlockingFindingIsRecordedWithItsRemedy(t *testing.T) {
	t.Parallel()

	doc := hostCertificationDocument(t)
	for _, subject := range []string{
		"bridge_executable",
		"private/bridge",
	} {
		assert.Contains(t, doc, subject,
			"the record has to name %s as part of the measured finding", subject)
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
	assert.Contains(t, strings.ToLower(doc), "not certified against a host",
		"the record has to keep saying plainly that this release is not certified against a host")

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
// The gate measured one host release, on one platform, with a deterministic bridge in
// place of the Cursor SDK. A record that read as host certification of every platform, of
// live provider behaviour, or of a published plugin release would be claiming more than any
// run established, so each of those three over-claims is named explicitly as absent.
func TestHostCertificationRecord_NoClaimOutrunsTheGate(t *testing.T) {
	t.Parallel()

	doc := strings.ToLower(strings.Join(strings.Fields(hostCertificationDocument(t)), " "))
	for _, nonClaim := range []string{
		"no linux host evidence is missing",
		"not live provider runs",
		"no plugin artifact has been released",
		"provider quota",
	} {
		assert.Contains(t, doc, nonClaim,
			"the record has to state the %s position explicitly, so a green gate is not read as more than it is",
			nonClaim)
	}
}
