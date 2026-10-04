package cursorsdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This file audits the release metadata inputs and the operator documentation against
// each other, so a release claim cannot drift away from what the build and the declared
// inputs actually say.
//
// The renderer proves the record inside a staged archive is derived honestly (see
// cmd/lip-cursor-sdk-packaging). What no single component can see is the seam between
// that record and the inputs it reads: a release input that names a module the plugin no
// longer pins puts a false statement in every archive, and a documentation file that
// keeps describing an evaluation nobody performed, or a platform nobody assembles, is
// the same kind of claim written in prose.

// releaseMetadata is the subset of release.yaml this audit reads. An input renamed in
// release.yaml reads as empty here, which fails the audit below rather than silently
// dropping the check that referenced it.
type releaseMetadata struct {
	PublishedRootModule     string `yaml:"published_root_module"`
	PublishedACPModule      string `yaml:"published_acp_module"`
	ReplacePolicy           string `yaml:"replace_policy"`
	HostCertification       string `yaml:"host_certification"`
	HostCertificationReason string `yaml:"host_certification_reason"`
}

func readReleaseMetadata(tb testing.TB) releaseMetadata {
	tb.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(tb), "release.yaml"))
	require.NoError(tb, err)
	var meta releaseMetadata
	require.NoError(tb, yaml.Unmarshal(raw, &meta))
	return meta
}

// TestReleaseMetadata_AgreesWithThePinnedModuleBaseline keeps the declared host
// contracts and the module manifest telling the same story.
//
// release.yaml names the two published modules the plugin claims to be built against,
// go.mod is what `go build` actually resolved, and README documents the pins for a
// maintainer. All three have to agree: a name in release.yaml that the manifest does not
// require would put a module path in every archive's compatibility record that the bytes
// in it were never built from, and a README table that drifts from go.mod would send the
// next person to a version that is not the pinned one.
func TestReleaseMetadata_AgreesWithThePinnedModuleBaseline(t *testing.T) {
	t.Parallel()

	meta := readReleaseMetadata(t)
	require.NotEmpty(t, meta.PublishedRootModule)
	require.NotEmpty(t, meta.PublishedACPModule)
	require.Equal(t, "released-dependency-pins-no-replace", meta.ReplacePolicy)

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	require.NoError(t, err)
	body := string(raw)
	for _, module := range []string{meta.PublishedRootModule, meta.PublishedACPModule} {
		require.NotContains(t, body, "replace "+module,
			"the record names published module versions, so %s cannot be replaced: %s", module, body)
		version := pinnedModuleVersion(t, body, module)
		require.NotEmpty(t, version, "go.mod does not require %s", module)
	}

	readme := readFileText(t, filepath.Join(repoRoot(t), "README.md"))
	for _, module := range []string{meta.PublishedRootModule, meta.PublishedACPModule} {
		require.Contains(t, readme, "| `"+module+"` | `"+pinnedModuleVersion(t, body, module)+"` |",
			"README's pinned host contract table has to state the version go.mod pins")
	}
}

// pinnedModuleVersion reads the version one module is required at.
func pinnedModuleVersion(tb testing.TB, goMod, module string) string {
	tb.Helper()

	for _, line := range strings.Split(goMod, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1]
		}
	}
	return ""
}

// TestDocs_DescribeTheSourceIdentityBasisAccurately keeps the documents that describe the
// release record honest about where the source identity in it comes from.
//
// The record carries a source revision from one of two bases: the Go build VCS stamp the
// toolchain writes into the executable, or the revision the packager resolved from the tree
// it built in when the toolchain wrote none - which is the linked-work-tree case this
// project always builds in. A document that mentions only the stamp describes a basis that
// is absent from the archives this project actually produces, and the cleanliness of the
// build is a third state rather than a yes or a no. Both facts have to be stated wherever
// the record is described.
func TestDocs_DescribeTheSourceIdentityBasisAccurately(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		"README.md",
		"PROVENANCE.md",
		filepath.Join("docs", "packaging.md"),
		filepath.Join("docs", "installation.md"),
	} {
		// Prose is hard-wrapped, so the statements are matched as one sequence of words:
		// what has to hold is that the document says it, not where the lines break.
		body := strings.Join(strings.Fields(readFileText(t, filepath.Join(repoRoot(t), doc))), " ")
		lowered := strings.ToLower(body)

		require.Contains(t, lowered, "vcs stamp",
			"%s has to name the build-stamp basis for the source revision", doc)
		require.Contains(t, lowered, "resolved from the tree",
			"%s has to name the resolved basis the packager falls back to", doc)
		require.Contains(t, lowered, "unknown",
			"%s has to say that an unestablished source state is unknown, not clean", doc)
		require.NotContains(t, lowered, "the source revision stamped into the outer executable",
			"%s describes only the build-stamp basis, which is absent from a linked-work-tree build", doc)
	}
}

// TestVerifierScriptsReportTheSameRecordedEvidence keeps the two verifiers reporting the
// same record.
//
// An operator reads whichever verifier their platform runs, so a field one of them prints
// and the other ignores is a difference in the evidence rather than in the tree. The list
// is the release evidence this project decided to record: source identity, the exact
// pinned host contracts, the platform evidence, the host certification posture with its
// evidence, and the package verification state. Both implementations have to name all of
// them, and the POSIX package lane is what actually runs both over the same tree.
func TestVerifierScriptsReportTheSameRecordedEvidence(t *testing.T) {
	t.Parallel()

	fields := []string{
		"source_revision",
		"source_modified",
		"source_stamp_evidence",
		"published_root_module",
		"host_contract_root_version",
		"published_acp_module",
		"host_contract_acp_version",
		"declared_platforms",
		"declared_platforms_not_assembled",
		"host_certification_state",
		"host_certification_reason",
		"tested_host_artifacts",
		"package_verification_state",
		"package_verification_performed",
		"package_verification_shipped_command",
		"package_verification_installed_command",
	}

	for _, script := range []string{
		filepath.Join("scripts", "verify-package.sh"),
		filepath.Join("scripts", "verify-package.ps1"),
	} {
		body := readFileText(t, filepath.Join(repoRoot(t), script))
		for _, field := range fields {
			require.Contains(t, body, field,
				"%s does not report the recorded %s, so the two verifiers describe the record differently",
				script, field)
		}
	}
}

// TestReleaseMetadata_DoesNotClaimACertificationItCannotBack keeps the host
// certification posture a fact rather than a default.
//
// The posture is declared in release.yaml, it is what compatibility.json records, and the
// operator documentation restates it. An uncertified release has to carry a reason and
// the prose has to agree with it; a certified one has no reason to carry, because its
// evidence is the host artifact digests. The failure this catches is a release that reads
// as certified - or as deliberately uncertified - in one place and not the other.
func TestReleaseMetadata_DoesNotClaimACertificationItCannotBack(t *testing.T) {
	t.Parallel()

	meta := readReleaseMetadata(t)
	switch meta.HostCertification {
	case "uncertified":
		require.NotEmpty(t, meta.HostCertificationReason,
			"an uncertified release has to say why; an absent reason reads as an oversight")
		for _, doc := range []string{"README.md", filepath.Join("docs", "packaging.md")} {
			require.Contains(t, readFileText(t, filepath.Join(repoRoot(t), doc)), "uncertified",
				"%s has to state the same certification posture release.yaml declares", doc)
		}
	case "certified":
		require.Empty(t, meta.HostCertificationReason,
			"a certified release carries host artifact digests instead of a reason")
	default:
		t.Fatalf("release.yaml declares host_certification %q; the postures are certified and uncertified",
			meta.HostCertification)
	}
}

// TestPackagingDocs_RecordThePackagingEvaluationAndItsEvidence keeps the packaging
// decision auditable on its own.
//
// The evaluation compares two packaging shapes, and the failure mode of such a document
// is a decision that reads as tested when it was reasoned about. So every subject the
// decision turns on has to be present, the shipped decision has to name the shape it
// ships, the tested and the untested have to be separated explicitly, and the evidence
// has to be named rather than gestured at.
func TestPackagingDocs_RecordThePackagingEvaluationAndItsEvidence(t *testing.T) {
	t.Parallel()

	doc := readFileText(t, filepath.Join(repoRoot(t), "docs", "packaging.md"))

	for _, subject := range []string{
		"SDK loading",
		"Dynamic imports",
		"Package metadata lookup",
		"Native assets",
		"Sandbox",
		"Signatures",
		"Platform limits",
	} {
		require.Contains(t, doc, subject,
			"the evaluation has to cover %s; that is one of the things it decides on", subject)
	}

	// The two shapes the decision compares, named as such.
	require.Contains(t, doc, "Single Executable Application")
	require.Contains(t, doc, "private-runtime")

	// Tested and reasoned-about are separate claims, and the document has to keep them
	// separate rather than presenting a reasoned conclusion as a measurement.
	require.Contains(t, doc, "Tested")
	require.Contains(t, doc, "Not tested")

	// The evidence has to be nameable: an auditor should be able to open the runner
	// matrix and the gate and see for themselves.
	require.Contains(t, doc, ".github/workflows/verify.yml")
	require.Contains(t, doc, "TestPackageArchive_NativeArchiveIsInstallableAndVerifiable")
	require.Contains(t, doc, "scripts/package-plugin")
	require.Contains(t, doc, "scripts/verify-package")

	// Every platform this project claims is a platform somebody assembles and verifies
	// natively, and the document has to list exactly those.
	for _, platform := range packagelayout.SupportedPlatforms() {
		require.Contains(t, doc, platform,
			"the evaluation has to state the evidence for every declared platform")
	}

	require.Contains(t, readFileText(t, filepath.Join(repoRoot(t), "README.md")), "docs/packaging.md",
		"the packaging decision has to be reachable from the README")
}

// TestInstallationDocs_CoverEveryNativelyAssembledPlatform keeps the operator
// instructions true per platform.
//
// The archive is assembled per platform and the runtime keeps its bundled npm somewhere
// different on each, so "run the provisioning command" is only an instruction if the
// command is the one that platform actually ships. The test reads those commands out of
// the layout contract rather than out of the document, so a layout change cannot leave
// the instructions describing a tree that does not exist - and it checks the answer to
// the question every operator asks first, which is whether a system Node is needed.
func TestInstallationDocs_CoverEveryNativelyAssembledPlatform(t *testing.T) {
	t.Parallel()

	doc := readFileText(t, filepath.Join(repoRoot(t), "docs", "installation.md"))
	lowered := strings.ToLower(doc)

	require.Contains(t, lowered, "a system node is not required",
		"the installation guide has to answer whether a system Node is needed, in those words")
	require.Contains(t, lowered, "@cursor/sdk")
	require.Contains(t, doc, "cursor_sdk_provisioning_command",
		"the guide names the field the record carries the provisioning command in")
	require.Contains(t, doc, "scripts/verify-package")
	require.Contains(t, doc, "TestPackageArchive_NativeArchiveIsInstallableAndVerifiable",
		"the guide has to point at the evidence for the platform it describes")
	require.Contains(t, doc, ".github/workflows/verify.yml")

	for _, platform := range packagelayout.SupportedPlatforms() {
		archive, err := packagelayout.ForPlatform(splitPlatformFor(platform))
		require.NoError(t, err)

		// The command the layout contract produces, without the leading `cd`, is the
		// per-platform part an operator has to be able to copy.
		_, invocation, found := strings.Cut(archive.ProvisionCommand(""), " && ")
		require.True(t, found)
		require.Contains(t, doc, invocation,
			"the guide has to print the provisioning command %s ships", platform)
		require.Contains(t, doc, archive.OS(),
			"the guide has to cover %s, whose runtime keeps npm somewhere else", platform)
	}
}

// splitPlatformFor splits one "os/arch" platform claim.
func splitPlatformFor(platform string) (goos, goarch string) {
	goos, goarch, _ = strings.Cut(platform, "/")
	return goos, goarch
}
