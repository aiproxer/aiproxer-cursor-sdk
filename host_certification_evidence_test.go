package cursorsdk_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This file audits the certified release metadata this repository ships.
//
// The measurement that produced it is real and recorded: the real-host gate ran against
// Go-LIP v0.1.0 on both declared platforms and passed on both, with a measured difference
// between them that the maintainer adopted as the per-platform installation contract. What
// was missing was the record: an archive could say "certified" and name two host digests,
// and a reader would have no way to tell which digest belonged to which platform or what a
// supported configuration on that platform looks like. That is the ambiguity this file
// closes - not by pinning prose, but by requiring the declared evidence to be complete,
// per platform, and consistent with the gate and the documents that describe it.

// certifiedHostArtifact is one platform's certified host artifact as release.yaml declares
// it. It is the release side of the same record the renderer copies into compatibility.json.
type certifiedHostArtifact struct {
	Platform           string `yaml:"platform"`
	HostProject        string `yaml:"host_project"`
	HostVersion        string `yaml:"host_version"`
	HostReleaseAsset   string `yaml:"host_release_asset"`
	HostChecksumsAsset string `yaml:"host_checksums_asset"`
	HostBinary         string `yaml:"host_binary"`
	HostArtifactSHA    string `yaml:"host_artifact_sha256"`
	CompanionContract  string `yaml:"companion_contract"`
}

// certifiedReleaseMetadata is the release-metadata subset the certification audit reads.
type certifiedReleaseMetadata struct {
	Schema            string                  `yaml:"schema"`
	Version           string                  `yaml:"version"`
	Tag               string                  `yaml:"tag"`
	HostCertification string                  `yaml:"host_certification"`
	HostReason        string                  `yaml:"host_certification_reason"`
	Artifacts         []certifiedHostArtifact `yaml:"certified_host_artifacts"`
}

func readCertifiedReleaseMetadata(tb testing.TB) certifiedReleaseMetadata {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(repoRoot(tb), "release.yaml"))
	var meta certifiedReleaseMetadata
	require.NoError(tb, yaml.Unmarshal([]byte(raw), &meta))
	return meta
}

// TestHostCertificationRecord_CertifiedPostureCarriesOneHostArtifactPerDeclaredPlatform is
// the shape a certified release has to have.
//
// A certification is a statement about artifacts, so a flat list of digests is not enough:
// two digests and two declared platforms, with no statement of which is which, cannot be
// checked against anything. Each platform therefore has to name its own host release, the
// asset that release published, the binary inside it, and the digest that binary was
// measured at - and the set has to be exactly the declared platforms, because a certified
// platform nobody declared would be a claim outside the manifest and an undeclared
// platform in the manifest would be a claim with no artifact behind it.
func TestHostCertificationRecord_CertifiedPostureCarriesOneHostArtifactPerDeclaredPlatform(t *testing.T) {
	t.Parallel()

	meta := readCertifiedReleaseMetadata(t)
	require.Equal(t, "certified", meta.HostCertification,
		"the real-host gate ran against the released host on both declared platforms and the maintainer adopted "+
			"the per-platform contract it measured, so this release is certified; an uncertified posture here "+
			"would describe a state that no longer exists")
	assert.Empty(t, meta.HostReason,
		"a certified record carries host artifact digests instead of a reason; a reason here would state why the "+
			"artifacts below are not evidence")

	declared := packagelayout.SupportedPlatforms()
	platforms := make([]string, 0, len(meta.Artifacts))
	for _, artifact := range meta.Artifacts {
		platforms = append(platforms, artifact.Platform)
	}
	assert.ElementsMatch(t, declared, platforms,
		"the certified host artifacts have to cover exactly the declared platforms: each one needs an artifact "+
			"it was measured against, and no artifact may be certified for a platform the manifest does not declare")

	require.NotEmpty(t, meta.Artifacts[0].HostArtifactSHA)
	for _, artifact := range meta.Artifacts {
		assert.NotEmpty(t, artifact.HostProject,
			"%s has to name the host release the measurement ran against", artifact.Platform)
		assert.NotEmpty(t, artifact.HostVersion,
			"%s has to name the host version, or the artifact digest is not tied to a release", artifact.Platform)
		assert.NotEmpty(t, artifact.HostReleaseAsset,
			"%s has to name the release asset that was downloaded, so the measurement can be repeated from the "+
				"same bytes rather than from whatever the host publishes next", artifact.Platform)
		assert.NotEmpty(t, artifact.HostBinary,
			"%s has to name the binary inside that asset", artifact.Platform)
		assert.Len(t, artifact.HostArtifactSHA, 64,
			"%s has to carry the full measured sha256 of its host binary", artifact.Platform)
		assert.Equal(t, strings.ToLower(artifact.HostArtifactSHA), artifact.HostArtifactSHA,
			"%s records its host digest in the lowercase hex form the renderer and both verifiers compare", artifact.Platform)
		assert.Contains(t, []string{
			string(packagelayout.CompanionPackagedDefault),
			string(packagelayout.CompanionExplicitBridgeExecutable),
		}, artifact.CompanionContract,
			"%s has to adopt one of the two measured companion contracts; a third spelling would be a contract "+
				"no operator is documented to run", artifact.Platform)
	}

	digests := make([]string, 0, len(meta.Artifacts))
	for _, artifact := range meta.Artifacts {
		digests = append(digests, artifact.HostArtifactSHA)
	}
	require.Len(t, slices.Compact(slices.Clone(digests)), len(digests),
		"two platforms recorded with one host digest would be a claim that one measured artifact certified two "+
			"platforms; the gate ran each platform against its own host binary")

	// The adoption is a measured difference, not a preference, so the two platforms cannot
	// agree about the contract. That equality is what made the measurement worth recording.
	contracts := map[string]bool{}
	for _, artifact := range meta.Artifacts {
		contracts[artifact.CompanionContract] = true
	}
	assert.Len(t, contracts, 2,
		"the host launches a staging copy of the verified executable on windows/amd64 and execs the installed one "+
			"on linux/amd64, so the two platforms have to record different companion contracts")
}

// TestHostCertificationRecord_DocumentsNameTheCertifiedEvidenceForEveryPlatform keeps the
// prose from describing a different certification than the one that was performed.
//
// A digest and a per-platform contract are only auditable if the record an operator reads
// names them. The assertions are formatted from the declared metadata rather than written
// out here, so a corrected digest or an adopted contract has to be described once - in the
// release metadata - and every document that mentions it follows, rather than a test that
// pins one spelling and breaks when a second platform is added.
func TestHostCertificationRecord_DocumentsNameTheCertifiedEvidenceForEveryPlatform(t *testing.T) {
	t.Parallel()

	meta := readCertifiedReleaseMetadata(t)
	for _, artifact := range meta.Artifacts {
		archive, err := packagelayout.ForPlatform(splitPlatformFor(artifact.Platform))
		require.NoError(t, err)

		for _, doc := range []string{
			filepath.Join("docs", "certification.md"),
			filepath.Join("docs", "packaging.md"),
		} {
			body := readFileText(t, filepath.Join(repoRoot(t), doc))
			assert.Contains(t, body, artifact.HostArtifactSHA,
				"%s has to name the %s host artifact digest the release metadata records; an operator auditing the "+
					"certification has to be able to find the digest that was measured", doc, artifact.Platform)
			assert.Contains(t, body, artifact.HostVersion,
				"%s has to name the host release version the %s measurement ran against", doc, artifact.Platform)
		}

		// The contract an operator has to follow is spelled with the packaged path the
		// layout contract produces, not a hand-written one: an operator on the platform
		// that needs the field cannot be told to point at a file that does not exist.
		guide := readFileText(t, filepath.Join(repoRoot(t), "docs", "installation.md"))
		assert.Contains(t, guide, archive.LauncherPath(),
			"the installation guide has to print the packaged launcher path an operator points bridge_executable "+
				"at on %s", artifact.Platform)
		assert.Contains(t, guide, artifact.CompanionContract,
			"the installation guide has to state the %s companion contract in the spelling the release metadata "+
				"records, so the guide and the archive cannot describe different installations", artifact.Platform)
	}

	// The release this metadata describes is a specific one, so the documents have to name
	// its tag: a certification record that did not say which release it belonged to could
	// be read as evidence about any of them.
	//
	// Nothing here asserts that a document mentions a workflow or a job name. How the
	// release is produced is proved by a release run, not by the text of a file, and a test
	// that pinned those names would only prove that the prose had not moved.
	for _, doc := range []string{
		filepath.Join("docs", "certification.md"),
		filepath.Join("docs", "packaging.md"),
		"README.md",
	} {
		assert.Contains(t, readFileText(t, filepath.Join(repoRoot(t), doc)), meta.Tag,
			"%s has to name the tag this release carries", doc)
	}
}

// TestHostCertificationRecord_TheCertifiedRecordIsWhatTheGateEnforces keeps the declared
// contract and the enforced one from becoming two different contracts.
//
// The gate used to hold its own table of what each platform supports, which meant the
// shipped record and the enforcement could disagree and nothing would fail. Both now read
// the release metadata, so a platform whose contract changed is enforced under its new
// contract rather than the old one. This asserts the two agree on every declared platform,
// and that they still disagree with each other where the measurement said they should.
func TestHostCertificationRecord_TheCertifiedRecordIsWhatTheGateEnforces(t *testing.T) {
	t.Parallel()

	meta := readCertifiedReleaseMetadata(t)
	require.Len(t, meta.Artifacts, 2, "the gate enforces a per-platform contract, so the record declares one per platform")

	contracts := hostCertCompanionContracts(t)
	for _, artifact := range meta.Artifacts {
		declared, err := packagelayout.ParseCompanionContract(
			packagelayout.CompanionContract(artifact.CompanionContract))
		require.NoError(t, err)
		enforced, ok := contracts[artifact.Platform]
		require.True(t, ok,
			"the gate has no companion contract for %s, so it cannot certify a platform the release metadata "+
				"declares", artifact.Platform)

		assert.Equal(t, declared.BridgeExecutableRequired(), enforced.explicitCompanion == hostCertExplicitRequired,
			"%s records companion_contract %q, so the gate has to enforce that same requirement",
			artifact.Platform, artifact.CompanionContract)
		assert.Equal(t, declared.PackagedDefaultSupported(), enforced.packagedDefaultSupported,
			"%s records companion_contract %q, so the gate has to enforce whether the packaged default stays "+
				"supported there", artifact.Platform, artifact.CompanionContract)
	}

	windows, ok := contracts["windows/amd64"]
	require.True(t, ok)
	linux, ok := contracts["linux/amd64"]
	require.True(t, ok)
	assert.NotEqual(t, windows.explicitCompanion, linux.explicitCompanion,
		"the measured difference between the platforms is the whole reason the contract is per platform, so the "+
			"two cannot end up enforcing the same requirement")
}
