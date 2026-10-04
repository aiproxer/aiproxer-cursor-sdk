package cursorsdk_test

import (
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// adversarialCase is one way a package can lie about its contents, and the verdict that has to
// come out of it.
type adversarialCase struct {
	name string
	// mutate changes one tree so that it no longer says what the release claims.
	mutate func(tb testing.TB, root string, archive packagelayout.Archive)
	// want is a fragment of the finding the mutation has to produce, chosen from the
	// verifier's own wording so a case cannot pass on a paraphrase.
	want string
}

// TestPackageArchive_AdversarialVerdictsSurviveTheIndexedGate keeps the performance and
// robustness work out of the verdicts.
//
// Indexing the checksum record and bounding the probes changes how the verifier reads a tree,
// never what it concludes. Every way a package can lie about itself therefore has to reach the
// same verdict it reached before, and - because each implementation is its own trust check - it
// has to reach the same verdict as its counterpart on the other platform. A finding that
// appeared in one implementation and not the other would be the shape of that drift, so the
// comparison is over whole finding sets rather than over the fragment each case asserts.
func TestPackageArchive_AdversarialVerdictsSurviveTheIndexedGate(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)
	cases := []adversarialCase{
		{
			name: "tampered shipped file",
			mutate: func(tb testing.TB, root string, _ packagelayout.Archive) {
				tampered := filepath.Join(root, "private", "bridge", "dist", "main.js")
				require.NoError(tb, os.WriteFile(tampered,
					[]byte(readFileText(tb, tampered)+"\n// tampered\n"), 0o644))
			},
			want: "checksum mismatch for private/bridge/dist/main.js",
		},
		{
			name: "shipped file nobody listed",
			mutate: func(tb testing.TB, root string, _ packagelayout.Archive) {
				extra := filepath.Join(root, "private", "bridge", "notes.txt")
				require.NoError(tb, os.WriteFile(extra, []byte("not part of the archive\n"), 0o644))
			},
			want: "file is present but not listed in checksums.sha256: private/bridge/notes.txt",
		},
		{
			name: "listed shipped file that is gone",
			mutate: func(tb testing.TB, root string, a packagelayout.Archive) {
				require.NoError(tb, os.Remove(filepath.Join(root, filepath.FromSlash(a.BridgePackageLockPath()))))
			},
			want: "file listed in checksums.sha256 is missing: private/bridge/package-lock.json",
		},
		{
			name: "record covering the operator's own tree",
			mutate: func(tb testing.TB, root string, a packagelayout.Archive) {
				appendRecordEntries(tb, root, a, []string{a.ProvisionedSDKPackageJSONPath()})
			},
			want: "checksum record lists the operator-provisioned private/bridge/node_modules/@cursor/sdk/package.json",
		},
		{
			name: "provisioned SDK at the wrong version",
			mutate: func(tb testing.TB, root string, a packagelayout.Archive) {
				metadata := filepath.Join(root, filepath.FromSlash(a.ProvisionedSDKPackageJSONPath()))
				require.NoError(tb, os.WriteFile(metadata,
					[]byte(strings.Replace(readFileText(tb, metadata), "1.0.23", "1.0.22", 1)), 0o644))
			},
			want: "the provisioned Cursor SDK is 1.0.22",
		},
		{
			name: "recorded runtime digest that matches nothing",
			mutate: func(tb testing.TB, root string, a packagelayout.Archive) {
				recordPath := filepath.Join(root, filepath.FromSlash(a.CompatibilityPath()))
				record := decodeJSONObject(tb, recordPath)
				record["private_runtime_sha256"] = strings.Repeat("0", 64)
				writeJSONFile(tb, recordPath, record)
				// The record is itself checksummed, so its entry is recomputed: the case
				// has to isolate the recorded-digest check instead of failing earlier.
				rewriteChecksum(tb, root, a.CompatibilityPath())
			},
			want: "release metadata records private_runtime_sha256",
		},
	}

	if runtime.GOOS != "windows" {
		// Windows exposes the install root's protection through its ACL rather than
		// permission bits, and the verifier states the requirement there instead of
		// measuring it, so the group-writable root is a POSIX case.
		cases = append(cases, adversarialCase{
			name: "install root any local user can write",
			mutate: func(tb testing.TB, root string, _ packagelayout.Archive) {
				require.NoError(tb, os.Chmod(root, 0o777))
				tb.Cleanup(func() { _ = os.Chmod(root, 0o700) })
			},
			want: "is writable beyond its owner",
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			perImplementation := make(map[string][]string)
			for _, impl := range verifierImplementations(t) {
				root := probeTree(t, archive)
				tc.mutate(t, root, archive)

				report, code := runVerifierBounded(t, impl, repoRoot(t), root, nil, nil, probeTestRunBound)
				require.NotEqual(t, 0, code,
					"the %s verifier accepted a tree it should have rejected:\n%s", impl, report)
				require.NotContains(t, report, "verify-package: ok")
				require.Contains(t, report, tc.want,
					"the %s verifier has to report this mutation the way it always did:\n%s", impl, report)

				perImplementation[impl] = normalizedFindings(report, root)
			}
			requireCrossImplementationAgreement(t, perImplementation)
		})
	}
}

// normalizedFindings are the verifier's FAIL lines with the install root folded away, so two
// implementations that verified two copies of the same tree can be compared. Order is not
// evidence: the verifier reports its checks in a fixed order, not in verdict order.
func normalizedFindings(report, root string) []string {
	var out []string
	for _, finding := range verifyFindings(report) {
		out = append(out, strings.ReplaceAll(finding, root, "<install-root>"))
	}
	slices.Sort(out)
	return out
}

// requireCrossImplementationAgreement requires every implementation that ran to reach exactly
// the same findings. A verdict one platform reaches and another does not is the drift this
// catches, and it is invisible to any check that runs a single implementation.
func requireCrossImplementationAgreement(tb testing.TB, perImplementation map[string][]string) {
	tb.Helper()

	implementations := slices.Sorted(maps.Keys(perImplementation))
	require.NotEmpty(tb, implementations)
	reference := perImplementation[implementations[0]]
	for _, impl := range implementations[1:] {
		require.Equal(tb, reference, perImplementation[impl],
			"the %s and %s verifiers have to reach the same verdict; "+
				"a finding one of them reaches and the other does not is the drift this catches",
			implementations[0], impl)
	}
}
