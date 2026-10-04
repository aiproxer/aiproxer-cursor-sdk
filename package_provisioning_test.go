package cursorsdk_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// provisioningTreeOptions describes one synthetic install tree for the provisioning
// trust checks.
type provisioningTreeOptions struct {
	// provisionedVersion is the SDK version in the operator-provisioned tree. An empty
	// string stages no dependency tree at all, which is the installed-but-unprovisioned
	// state the plugin has to report as a prerequisite failure.
	provisionedVersion string
	// recordProvisioned puts the operator-provisioned files into the shipped checksum
	// record, which is the mistake a packaging change could make.
	recordProvisioned bool
	// tamperShippedFile appends to a shipped file so its digest no longer matches the
	// record.
	tamperShippedFile bool
	// omitCertificationState drops host_certification_state from the recorded release
	// metadata, which is how a tree that never declared its certification posture looks.
	// A verifier has to name that absence exactly once, without echoing the record.
	omitCertificationState bool
	// sourceState is the recorded source cleanliness: "true", "false", or "" for a record
	// that states none at all. The last one is the case a report must not read as clean.
	sourceState string
}

// The synthetic record's own statements, named so a report assertion can quote the exact
// line a verifier has to print, and can notice the record being echoed back instead.
const (
	fixtureCertificationReason = "no downloadable host binary release exists to certify this tree against"
	fixtureSourceRevision      = "0123456789abcdef0123456789abcdef01234567"
	fixtureRedistribution      = "@cursor/sdk is not redistributed by this archive; the operator provisions it themselves"
)

// TestPackageArchive_VerifierRejectsAShippedArchiveCarryingTheSDK keeps the archive
// content rule checkable rather than assumed.
//
// The Cursor SDK is proprietary and is not redistributed, so a released archive must
// contain no Cursor SDK and none of its dependency closure. A packager that staged
// that closure would produce an archive asserting a redistribution right nobody has
// verified, and nothing downstream would notice unless verification could check for it.
// So the shipped-state audit has to reject the tree the operator gets before anyone
// unpacks it.
func TestPackageArchive_VerifierRejectsAShippedArchiveCarryingTheSDK(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			// A shipped archive with no Cursor SDK code raises nothing about the
			// redistribution posture. The synthetic tree is deliberately incomplete -
			// it has no runtime and no launcher to probe - so the evidence is the
			// absence of the finding, not a clean exit status.
			shipped := provisioningTree(t, archive, provisioningTreeOptions{})
			report, _ := runVerifyScriptImpl(t, impl, shipped, nil, []string{"--tree-state", "shipped"})
			for _, finding := range verifyFindings(report) {
				require.NotContains(t, strings.ToLower(finding), "not redistributed",
					"a shipped archive with no Cursor SDK code must not raise the redistribution finding:\n%s", report)
				require.NotContains(t, finding, archive.BridgeModulesPath()+"/",
					"a shipped archive with no Cursor SDK code must not raise a provisioned-tree finding:\n%s", report)
			}
			require.Contains(t, report, "sdk: not redistributed, required 1.0.23 at run time")
			require.Contains(t, report, archive.PrivateRuntimeNPMCLIPath(),
				"the shipped-state report has to say where the provisioning command gets its npm:\n%s", report)

			// The same tree once the operator has provisioned it is a different claim,
			// so the shipped-state audit has to be the thing that decides, not the tree
			// alone.
			provisioned := provisioningTree(t, archive, provisioningTreeOptions{provisionedVersion: "1.0.23"})
			report, code := runVerifyScriptImpl(t, impl, provisioned, nil, []string{"--tree-state", "shipped"})
			require.NotEqual(t, 0, code,
				"a shipped archive carrying the SDK passed the shipped-state audit:\n%s", report)
			require.Contains(t, report, archive.BridgeModulesPath(),
				"the finding has to name the directory an archive must not ship:\n%s", report)
			require.Contains(t, report, "not redistributed")
		})
	}
}

// TestPackageArchive_VerifierFailsClosedOnAnUnprovisionedTree keeps an installed tree
// without its SDK an explicit, actionable prerequisite failure.
//
// Nothing installs the SDK for an operator: the archive ships the manifest and the
// lockfile that pin it, and the operator runs one command against the runtime the
// archive ships. So an installed tree that has not been provisioned cannot serve a
// request, and the verifier has to say exactly what is missing and print the command
// rather than reporting a tree that looks complete.
func TestPackageArchive_VerifierFailsClosedOnAnUnprovisionedTree(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := provisioningTree(t, archive, provisioningTreeOptions{})
			report, code := runVerifyScriptImpl(t, impl, root, nil, nil)
			require.NotEqual(t, 0, code,
				"an installed tree with no provisioned SDK passed verification:\n%s", report)
			require.Contains(t, report, packagelayout.SDKPackageName,
				"the finding has to name the missing SDK:\n%s", report)
			require.Contains(t, report, "not provisioned",
				"the finding has to say the tree is unprovisioned rather than broken:\n%s", report)
			require.Contains(t, report, "1.0.23",
				"the finding has to name the version the operator has to provision:\n%s", report)
			require.Contains(t, report, "ci --omit=dev",
				"the finding has to print the provisioning command:\n%s", report)
			require.Contains(t, report, "npm-cli.js",
				"the provisioning command has to run the shipped runtime's own npm:\n%s", report)
			require.NotContains(t, report, "verify-package: ok")
		})
	}
}

// TestPackageArchive_VerifierRejectsAWrongProvisionedSDKVersion keeps a provisioned
// tree at the wrong SDK version a finding rather than a warning.
//
// The bridge refuses such a tree anyway, and refusing it there surfaces as a provider
// error an operator reads as a Cursor problem. Verification is where the requirement
// belongs: the operator learns that the tree they provisioned does not match the pin
// the archive ships.
func TestPackageArchive_VerifierRejectsAWrongProvisionedSDKVersion(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := provisioningTree(t, archive, provisioningTreeOptions{provisionedVersion: "1.0.22"})
			report, code := runVerifyScriptImpl(t, impl, root, nil, nil)
			require.NotEqual(t, 0, code,
				"a provisioned tree at the wrong SDK version passed verification:\n%s", report)
			require.Contains(t, report, "1.0.22")
			require.Contains(t, report, "1.0.23")
			require.Contains(t, report, "ci --omit=dev")
		})
	}
}

// TestPackageArchive_VerifierAcceptsAProvisionedTreeAndScopesTheChecksumRecord keeps
// the trust split load-bearing in both directions.
//
// The shipped checksum record covers shipped files only: the plugin authenticates what
// it ships and the operator authenticates what they provisioned. Reporting every
// operator-provisioned file as "present but not listed" would fail every provisioned
// install, and dropping the shipped side of the split would let a tampered companion
// pass. Both halves are checked against the same tree.
func TestPackageArchive_VerifierAcceptsAProvisionedTreeAndScopesTheChecksumRecord(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := provisioningTree(t, archive, provisioningTreeOptions{provisionedVersion: "1.0.23"})
			report, code := runVerifyScriptImpl(t, impl, root, nil, nil)
			require.NotContains(t, report, "present but not listed",
				"operator-provisioned files are outside the shipped record, not unaccounted for:\n%s", report)
			require.NotEmpty(t, verifyFindings(report),
				"the tree is still missing its runtime and launcher, so the run must not be reported as clean")
			require.Contains(t, report, "sdk: required 1.0.23, provisioned 1.0.23")

			// The same tree with a shipped file modified: the provisioned half of the
			// split says nothing about this, so the shipped half has to catch it.
			tampered := provisioningTree(t, archive, provisioningTreeOptions{
				provisionedVersion: "1.0.23",
				tamperShippedFile:  true,
			})
			report, code = runVerifyScriptImpl(t, impl, tampered, nil, nil)
			require.NotEqual(t, 0, code)
			require.Contains(t, report, "checksum mismatch")
			require.Contains(t, report, archive.BridgePackageLockPath())
		})
	}
}

// TestPackageArchive_VerifierRejectsAChecksumRecordOverProvisionedFiles keeps the
// shipped record from covering operator content.
//
// checksums.sha256 is the plugin's own artifact, produced before anyone provisions
// anything. A record that listed the provisioned tree would claim the plugin
// authenticated the operator's npm resolution, which is exactly the claim the split
// exists to prevent.
func TestPackageArchive_VerifierRejectsAChecksumRecordOverProvisionedFiles(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := provisioningTree(t, archive, provisioningTreeOptions{
				provisionedVersion: "1.0.23",
				recordProvisioned:  true,
			})
			report, code := runVerifyScriptImpl(t, impl, root, nil, nil)
			require.NotEqual(t, 0, code,
				"a checksum record covering operator-provisioned files passed verification:\n%s", report)
			require.Contains(t, report, archive.ProvisionedSDKPackageJSONPath())
			require.Contains(t, report, "operator-provisioned")
		})
	}
}

// TestPackageArchive_VerifierReportsTheRecordedReleaseEvidence keeps the verifier's
// report an account of the release record rather than of the tree it happened to look at.
//
// The record is the audit surface: it says whether the release is certified against a host
// artifact and why not, which platforms the project claims against the ones this archive is
// evidence for, what has been verified about the package, and what state the source tree was
// in. A report that only prints some of that leaves the rest to be taken on trust, and the
// source cleanliness is the field where a missing measurement is most likely to be read as
// a clean build - so all three states are asserted here, per implementation.
func TestPackageArchive_VerifierReportsTheRecordedReleaseEvidence(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			for _, tc := range []struct {
				name        string
				sourceState string
				wants       string
			}{
				{name: "dirty tree", sourceState: "true", wants: "source modified: yes (built from a work tree with uncommitted changes)"},
				{name: "clean tree", sourceState: "false", wants: "source modified: no (the build tree had no uncommitted changes)"},
				{
					name:        "unestablished state",
					sourceState: "",
					wants: "source modified: unknown (not established: no source identity in this record, " +
						"or the build tree state could not be read)",
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					root := provisioningTree(t, archive, provisioningTreeOptions{
						provisionedVersion: "1.0.23",
						sourceState:        tc.sourceState,
					})
					report, _ := runVerifyScriptImpl(t, impl, root, nil, nil)

					require.Contains(t, report, "host certification: uncertified (no host artifact was certified against)")
					require.Contains(t, report, "host certification reason: "+fixtureCertificationReason,
						"an uncertified posture is only auditable with its reason:\n%s", report)
					require.Contains(t, report, "tested host artifacts: none recorded")

					for _, platform := range packagelayout.SupportedPlatforms() {
						require.Contains(t, report, "declared platform: "+platform,
							"the report has to name every platform the project declares:\n%s", report)
					}
					for _, platform := range otherPlatforms(archive) {
						require.Contains(t, report, "declared platform this archive is not evidence for: "+platform,
							"the report has to separate the claims from this archive's evidence:\n%s", report)
					}
					require.Contains(t, report,
						"package verification recorded in this archive: not-performed (performed: no)",
						"the record is written before verification runs, and the report has to say so:\n%s", report)

					require.Contains(t, report, "source revision: "+fixtureSourceRevision)
					require.Contains(t, report, tc.wants,
						"the three source states are three different answers:\n%s", report)

					// Nothing in the report may read as a verification that did not happen.
					require.NotContains(t, report, "verify-package: ok",
						"this synthetic tree has no runtime or launcher, so a clean verdict would be false:\n%s", report)
					require.NotContains(t, strings.ToLower(report), "verified against")
				})
			}
		})
	}
}

// TestPackageArchive_VerifierNamesAMissingCertificationPostureWithoutEchoingTheRecord
// keeps an absent field an absence.
//
// The record is read with a text reader rather than a parser, so a field it does not carry
// used to answer with the whole document: the report printed the record, and a comparison
// compared the record with itself. The finding has to name the missing field exactly once
// and the report must never carry the record's own text, because an operator reading a
// verdict wants the reason, not the file.
func TestPackageArchive_VerifierNamesAMissingCertificationPostureWithoutEchoingTheRecord(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)
	const want = "verify-package: FAIL: release metadata records no host_certification_state; " +
		"an archive has to declare whether it was certified against a host artifact"

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := provisioningTree(t, archive, provisioningTreeOptions{
				provisionedVersion:     "1.0.23",
				omitCertificationState: true,
			})
			report, code := runVerifyScriptImpl(t, impl, root, nil, nil)

			require.NotEqual(t, 0, code,
				"a record that declares no certification posture passed verification:\n%s", report)
			require.Contains(t, report, want,
				"the finding has to name the missing field and what it has to say:\n%s", report)

			postureFindings := 0
			for _, finding := range verifyFindings(report) {
				if strings.Contains(finding, "host_certification") {
					postureFindings++
				}
			}
			require.Equal(t, 1, postureFindings,
				"the missing posture is one finding, not one per field read:\n%s", report)

			// The record itself must not appear anywhere in the report.
			require.NotContains(t, report, fixtureRedistribution,
				"the report echoed the record instead of naming the absent field:\n%s", report)
			require.NotContains(t, report, "cursor_sdk_redistribution")
			require.NotContains(t, report, "package_verification_shipped_command")
		})
	}
}

// provisioningTree stages a minimal install tree carrying exactly what the
// provisioning checks read: the shipped bridge manifest that pins the SDK, the shipped
// lockfile, the release metadata that records the requirement, the license notices, and
// a checksum record over the staged files.
//
// The tree deliberately has no private runtime and no launcher. Those are probed
// through native processes and belong to the package gate; the provisioning checks are
// about files, so this fixture keeps them hermetic and runs in the default unit lane on
// every platform instead of only where an archive is assembled.
func provisioningTree(tb testing.TB, archive packagelayout.Archive, opts provisioningTreeOptions) string {
	tb.Helper()

	root := filepath.Join(tb.TempDir(), "plugin install root with spaces")
	staged := map[string]string{
		archive.BridgePackageJSONPath(): `{"name":"lip-cursor-sdk-bridge","version":"0.1.0",` +
			`"engines":{"node":">=22.13"},"dependencies":{"@cursor/sdk":"1.0.23"}}`,
		archive.BridgePackageLockPath(): `{"name":"lip-cursor-sdk-bridge","lockfileVersion":3,` +
			`"packages":{"":{"dependencies":{"@cursor/sdk":"1.0.23"}}}}`,
		archive.LicensesDir() + "/THIRD-PARTY-NOTICES.md": "# Third-party notices\n",
		archive.CompatibilityPath():                       provisioningRecord(tb, archive, root, opts),
	}
	if opts.provisionedVersion != "" {
		staged[archive.ProvisionedSDKPackageJSONPath()] = `{"name":"@cursor/sdk","version":"` +
			opts.provisionedVersion + `"}`
	}
	if opts.tamperShippedFile {
		staged[archive.BridgePackageLockPath()] += "\n// tampered after packaging\n"
	}

	for rel, body := range staged {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(tb, os.WriteFile(path, []byte(body), 0o644))
	}
	writeProvisioningChecksums(tb, root, archive, staged, opts.recordProvisioned)
	return root
}

// provisioningRecord renders the synthetic release record the provisioning checks read.
//
// It mirrors the shape the renderer writes, including the tri-state source cleanliness and
// the declared platform set, so a report assertion here is an assertion about the record
// an operator actually receives rather than about a trimmed copy of it.
func provisioningRecord(tb testing.TB, archive packagelayout.Archive, root string, opts provisioningTreeOptions) string {
	tb.Helper()

	declared := packagelayout.SupportedPlatforms()
	slices.Sort(declared)
	fields := []string{
		"{",
		`  "schema": "golip.cursorsdk.compatibility/v2",`,
		`  "platform": "` + archive.Platform() + `",`,
		`  "native_platform_assembled": "` + archive.Platform() + `",`,
		`  "packaging_variant": "private-runtime",`,
		`  "external_node_required": false,`,
		`  "declared_platforms": [` + strings.Join(quoteAll(declared), ", ") + `],`,
		`  "declared_platforms_not_assembled": [` + strings.Join(quoteAll(otherPlatforms(archive)), ", ") + `],`,
		`  "cursor_sdk_required_version": "1.0.23",`,
		`  "cursor_sdk_bundled": false,`,
		`  "cursor_sdk_provisioning_command": "` + strings.ReplaceAll(archive.ProvisionCommand(root), `\`, `\\`) + `",`,
		`  "cursor_sdk_redistribution": "` + fixtureRedistribution + `",`,
		`  "source_revision": "` + fixtureSourceRevision + `",`,
		`  "source_stamp_evidence": "resolved from the version-control state of the tree this record was written for",`,
	}
	if opts.sourceState != "" {
		fields = append(fields, `  "source_modified": `+opts.sourceState+`,`)
	}
	if !opts.omitCertificationState {
		fields = append(fields,
			`  "host_certification_state": "uncertified",`,
			`  "host_certification_reason": "`+fixtureCertificationReason+`",`,
		)
	}
	fields = append(fields,
		`  "tested_host_artifacts": [],`,
		`  "package_verification_state": "not-performed",`,
		`  "package_verification_performed": false,`,
		`  "package_verification_shipped_command": "scripts/verify-package.sh `+
			`--package-root <plugin-root> --tree-state shipped",`,
		`  "package_verification_installed_command": "scripts/verify-package.sh `+
			`--package-root <plugin-root> --tree-state installed"`,
		"}",
		"",
	)
	return strings.Join(fields, "\n")
}

// writeProvisioningChecksums records every staged file, so the "present but not
// listed" finding can only appear for the tree this fixture did not stage. It can also
// record the provisioned files, which is the mistake the record scope check exists to
// catch.
func writeProvisioningChecksums(tb testing.TB, root string, archive packagelayout.Archive, staged map[string]string, withProvisioned bool) {
	tb.Helper()

	rels := make([]string, 0, len(staged))
	for rel := range staged {
		if !withProvisioned && strings.HasPrefix(rel, packagelayout.ProvisionedPrefix) {
			continue
		}
		rels = append(rels, rel)
	}
	slices.Sort(rels)

	lines := make([]string, 0, len(rels))
	for _, rel := range rels {
		lines = append(lines, fileSHA256(tb, filepath.Join(root, filepath.FromSlash(rel)))+
			packagelayout.ChecksumSeparator+rel)
	}
	path := filepath.Join(root, filepath.FromSlash(archive.ChecksumsPath()))
	require.NoError(tb, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// packagelayoutArchive is the host platform's archive contract.
func packagelayoutArchive(tb testing.TB) packagelayout.Archive {
	tb.Helper()

	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(tb, err)
	return archive
}

// otherPlatforms lists the declared platforms besides this host's, which is what the
// fixture records as declared-but-not-assembled for a tree staged on this platform.
func otherPlatforms(archive packagelayout.Archive) []string {
	var out []string
	for _, platform := range packagelayout.SupportedPlatforms() {
		if platform != archive.Platform() {
			out = append(out, platform)
		}
	}
	return out
}

// quoteAll renders platform names as JSON string elements.
func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, `"`+value+`"`)
	}
	return out
}
