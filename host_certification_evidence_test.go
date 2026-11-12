package cursorsdk_test

import (
	"path/filepath"
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
