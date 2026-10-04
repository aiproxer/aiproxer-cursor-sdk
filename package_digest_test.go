package cursorsdk_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// digestCountEnv names the file a digest-process counter appends to. It belongs to the
// counter this file installs, not to the verifier: nothing here reaches a verdict.
const digestCountEnv = "LIP_TEST_DIGEST_COUNT"

// TestPackageArchive_ChecksumRecordMatchingIsByExactPath keeps the recorded side of the
// checksum comparison keyed by the path itself.
//
// A shipped tree's names are arbitrary bytes, and a record scan that compares one listed path
// against the whole joined record instead of looking it up confuses names that merely resemble
// one another: a path containing another as a space-delimited substring, a path holding a
// wildcard, a path that would read as an option to the digest tool. Every one of those is a
// real file a real archive can carry, and treating it as a duplicate record line would fail a
// tree nobody tampered with. Duplicate detection is therefore exact-path, and it still fails
// closed on a path the record really lists twice.
func TestPackageArchive_ChecksumRecordMatchingIsByExactPath(t *testing.T) {
	t.Parallel()

	archive := packagelayoutArchive(t)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			t.Run("names that resemble one another are not duplicates", func(t *testing.T) {
				t.Parallel()

				root := probeTree(t, archive)
				appendRecordEntries(t, root, archive, resemblanceQuirkPaths())

				report, code := runVerifierBounded(t, impl, repoRoot(t), root, nil, nil, probeTestRunBound)
				require.Equal(t, 0, code,
					"a tree whose shipped names merely resemble one another has to verify:\n%s", report)
				require.Empty(t, verifyFindingsContaining(report, "duplicate checksum line"),
					"two different paths are not a duplicate:\n%s", report)
				require.Empty(t, verifyFindingsContaining(report, "checksum mismatch"),
					"every listed name has to be digested as itself:\n%s", report)
				require.Empty(t, verifyFindingsContaining(report, "present but not listed"),
					"every listed name has to be found on disk:\n%s", report)

				// The report enumerates what the record holds, so a name that was digested
				// under some other spelling would show up here as a missing entry.
				listed := readChecksums(t, filepath.Join(root, filepath.FromSlash(archive.ChecksumsPath())))
				require.Equal(t, len(listed), len(reportFileSection(t, report)))
			})

			t.Run("a path listed twice is still a finding", func(t *testing.T) {
				t.Parallel()

				root := probeTree(t, archive)
				recordPath := filepath.Join(root, filepath.FromSlash(archive.ChecksumsPath()))
				body := readFileText(t, recordPath)
				first := strings.SplitN(strings.TrimRight(body, "\n"), "\n", 2)[0]
				_, rel, ok := strings.Cut(first, packagelayout.ChecksumSeparator)
				require.True(t, ok, "malformed checksum line %q", first)
				require.NoError(t, os.WriteFile(recordPath, []byte(body+first+"\n"), 0o644))

				report, code := runVerifierBounded(t, impl, repoRoot(t), root, nil, nil, probeTestRunBound)
				require.NotEqual(t, 0, code,
					"a record that lists the same path twice passed verification:\n%s", report)
				require.Contains(t, report, "duplicate checksum line for "+rel,
					"the finding has to name the path the record lists twice:\n%s", report)
				// One duplicate is one finding, not one per read of the record.
				require.Len(t, verifyFindingsContaining(report, "duplicate checksum line"), 1)
			})

			t.Run("a name holding a newline is one unaccounted file", func(t *testing.T) {
				t.Parallel()

				// The shipped record is line based, so such a path cannot be listed in it at
				// all and has to be reported as content the archive does not account for.
				// What it must not become is two files or a malformed line: the walk has to
				// read it as the single name it is.
				if runtime.GOOS == "windows" {
					t.Skip("Windows refuses a newline in a file name")
				}
				root := probeTree(t, archive)
				extra := filepath.Join(root, "private", "bridge", "dist", "h\ni")
				require.NoError(t, os.WriteFile(extra, []byte("// newline in a name\n"), 0o644))

				report, code := runVerifierBounded(t, impl, repoRoot(t), root, nil, nil, probeTestRunBound)
				require.NotEqual(t, 0, code,
					"a tree carrying content the record cannot name passed verification:\n%s", report)
				require.Contains(t, report,
					"file is present but not listed in checksums.sha256: private/bridge/dist/h\ni;",
					"the finding has to name the whole path, newline included:\n%s", report)
				require.Len(t, verifyFindingsContaining(report, "present but not listed"), 1,
					"one name is one unaccounted file, not one per line of it:\n%s", report)
				require.Empty(t, verifyFindingsContaining(report, "malformed checksum"),
					"a name the record cannot hold is not a malformed record line:\n%s", report)
			})
		})
	}
}

// resemblanceQuirkPaths are shipped names a record scan that compares one listed path against
// the whole joined record gets wrong: one that contains another as a space-delimited
// substring, and one that would read as an option to the digest tool. A wildcard and a
// backslash are added where the filesystem allows them, because a wildcard is what a joined
// record would expand and a backslash is what a digest tool's own name escaping would rewrite.
//
// A name holding a newline is deliberately not here: the shipped record is line based, so such
// a path cannot be listed in it at all and is covered by its own case.
func resemblanceQuirkPaths() []string {
	paths := []string{
		"private/bridge/dist/a",
		"private/bridge/dist/b a",
		"private/bridge/dist/-leading",
	}
	if runtime.GOOS != "windows" {
		// Windows refuses every character a record scan would read as a pattern, so the
		// cases that need one can only be staged where the platform allows it.
		paths = append(paths,
			"private/bridge/dist/c*",
			"private/bridge/dist/d?e",
			"private/bridge/dist/f\\g")
	}
	return paths
}

// TestPackageArchive_ShellVerifierDigestsAnArchiveWithABoundedNumberOfProcesses keeps the
// digest pass bounded by the command line rather than by the size of the archive.
//
// A per-file digest costs one process per shipped file, and a real archive carries about two
// thousand of them, so the work grew with the tree while the tree was already fixed. What has
// to hold is not a duration but a count: two archives that differ in size must cost the same
// number of digest processes, because the count is what decides whether the gate scales.
//
// The static half of the same claim holds on every platform, so a checkout that only ever runs
// the PowerShell verifier still guards the shell script against a per-file call creeping back
// into the walk.
func TestPackageArchive_ShellVerifierDigestsAnArchiveWithABoundedNumberOfProcesses(t *testing.T) {
	t.Parallel()

	shell := readFileText(t, filepath.Join(repoRoot(t), "scripts", "verify-package.sh"))
	require.Contains(t, shell, "xargs -0 sha256sum -b -z",
		"the shell verifier has to digest a batch of names in one invocation")
	require.Contains(t, shell, "declare -A recorded_digest",
		"the recorded digests have to be indexed rather than searched for once per file")
	require.NotContains(t, shell, `sha256_of "$package_root/$rel"`,
		"the file walk must not digest a file at a time; that is the per-file cost this replaced")
	require.Equal(t, 0, strings.Count(shell, "probe_runs"),
		"a probe is bounded now, so the unbounded form must not survive beside it")

	if runtime.GOOS == "windows" {
		// The PowerShell verifier digests in process and this count is about the shell
		// script's process-per-file shape, which a Windows host cannot exercise: running the
		// shell script here would need a POSIX tree, and the ubuntu package lane is where
		// that happens.
		t.Skip("the shell verifier's POSIX digest path needs a POSIX host")
	}
	require.Contains(t, shell, "sha256_batches_available",
		"the batched path has to be chosen by what the platform digest tool can do")

	counts := make(map[string]int)
	for _, tc := range []struct {
		name  string
		extra int
	}{
		{name: "small", extra: 8},
		{name: "large", extra: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := packagelayoutArchive(t)
			root := probeTree(t, archive)
			appendRecordEntries(t, root, archive, manyDistPaths(tc.extra))

			counter := t.TempDir() + string(filepath.Separator) + "digests"
			report, code := runVerifierBounded(t, "sh", repoRoot(t), root,
				countingDigestEnvironment(t, counter), nil, probeTestRunBound)

			require.Equal(t, 0, code, "the tree has to verify the same way it does without a counter:\n%s", report)
			counts[tc.name] = countLines(t, counter)
		})
	}

	require.Positive(t, counts["small"], "the counter has to have observed digest processes at all")
	require.Equal(t, counts["small"], counts["large"],
		"an archive ten times the size must not cost ten times the digest processes")
}

// countingDigestEnvironment puts a digest tool on PATH that counts its own invocations before
// doing what the real one does, so the verdict is decided by the same digests the shipped path
// would have produced.
func countingDigestEnvironment(tb testing.TB, counter string) []string {
	tb.Helper()

	real, err := exec.LookPath("sha256sum")
	require.NoError(tb, err)

	shimDir := tb.TempDir()
	shim := fmt.Sprintf("#!/bin/sh\nprintf 'x\\n' >> \"$%s\"\nexec %s \"$@\"\n",
		digestCountEnv, shellQuote(real))
	require.NoError(tb, os.WriteFile(filepath.Join(shimDir, "sha256sum"), []byte(shim), 0o700))

	var env []string
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == digestCountEnv || strings.EqualFold(key, "PATH") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"PATH="+shimDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		digestCountEnv+"="+counter,
		"GOWORK=off")
}

// shellQuote renders one path as a single shell word.
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// countLines returns how many times the digest counter was appended to.
func countLines(tb testing.TB, path string) int {
	tb.Helper()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(tb, err)
	return len(strings.Fields(string(raw)))
}

// TestPackageArchive_ShellVerifierDigestsEscapedNamesOnTheFallbackPath keeps the per-file
// fallback reading a digest rather than an escaped name.
//
// Batching is preferred, but it is only used where a round trip through the digest tool hands
// back the exact name it was given, and a digest tool that escapes names instead takes the
// fallback. That fallback reads a name holding a backslash or a newline as two different
// things: the tool escapes the name and prefixes the whole line with a backslash, so a
// space-delimited field split returns a digest with a stray leading character. A shipped file
// with a backslash in its name is ordinary - it ships in the lockfile's own tree - so a tree
// carrying one has to verify on the fallback as well as on the batch.
//
// The digest tool is shadowed by one that has no batched form at all, which is exactly the
// platform that reaches the fallback.
func TestPackageArchive_ShellVerifierDigestsEscapedNamesOnTheFallbackPath(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		// The shell verifier's POSIX digest path needs a POSIX host; the windows package
		// lane is where that happens, and this case is not worth a synthetic tree there.
		t.Skip("the shell verifier's POSIX digest path needs a POSIX host")
	}
	archive := packagelayoutArchive(t)
	escaped := []string{"private/bridge/dist/back\\slash", "private/bridge/dist/leading\\"}

	for _, tc := range []struct {
		name string
		tool string
		// args are the options the shadow is given on the command line.
		args []string
		// names names the shadowed tool.
		names string
	}{
		{name: "no batched form", tool: "sha256sum", args: []string{"-b", "-z"}, names: "sha256sum"},
		{name: "no NUL output form", tool: "sha256sum", args: []string{"-z"}, names: "sha256sum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := probeTree(t, archive)
			appendRecordEntries(t, root, archive, escaped)

			report, code := runVerifierBounded(t, "sh", repoRoot(t), root,
				unbatchedDigestEnvironment(t, tc.names, tc.tool, tc.args), nil, probeTestRunBound)

			require.Equal(t, 0, code,
				"a shipped name holding a backslash has to digest on the fallback path too:\n%s", report)
			require.Empty(t, verifyFindingsContaining(report, "checksum mismatch"),
				"the fallback read an escaped name rather than a digest:\n%s", report)
			require.Contains(t, report, "verify-package: ok",
				"the fallback has to reach the same clean verdict the batched path does:\n%s", report)

			// The report has to carry the digests the fallback actually computed, which is
			// only checkable by reading them back out of the record and comparing.
			listed := readChecksums(t, filepath.Join(root, filepath.FromSlash(archive.ChecksumsPath())))
			for _, rel := range escaped {
				require.Contains(t, listed, rel)
			}
		})
	}
}

// unbatchedDigestEnvironment puts a digest tool on PATH that has no batched form, so the
// verifier has to take the per-file fallback. The shadow refuses the batched options and
// otherwise delegates to the real tool, so the digests it reports are the real ones and the
// verdict is the one the shipped path would reach.
func unbatchedDigestEnvironment(tb testing.TB, name, tool string, refused []string) []string {
	tb.Helper()

	real, err := exec.LookPath(tool)
	require.NoError(tb, err)

	shimDir := tb.TempDir()
	shim := "#!/bin/sh\n" +
		"# A digest tool with no batched form, standing in for one that cannot hand names back.\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		strings.Join(refused, " | ") + ") exit 1 ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + shellQuote(real) + " \"$@\"\n"
	require.NoError(tb, os.WriteFile(filepath.Join(shimDir, name), []byte(shim), 0o700))

	var env []string
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, "PATH") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PATH="+shimDir+string(filepath.ListSeparator)+os.Getenv("PATH"), "GOWORK=off")
}

// manyDistPaths are n distinct shipped paths under the built JavaScript directory.
func manyDistPaths(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("private/bridge/dist/chunk-%04d.js", i))
	}
	return out
}

// appendRecordEntries stages extra shipped files and records each one, which is how a case
// grows a tree without restating the whole fixture.
func appendRecordEntries(tb testing.TB, root string, archive packagelayout.Archive, rels []string) {
	tb.Helper()

	var lines strings.Builder
	for i, rel := range rels {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
		body := fmt.Sprintf("// shipped file %d for %s\n", i, rel)
		require.NoError(tb, os.WriteFile(path, []byte(body), 0o644))
		lines.WriteString(fileSHA256(tb, path) + packagelayout.ChecksumSeparator + rel + "\n")
	}
	recordPath := filepath.Join(root, filepath.FromSlash(archive.ChecksumsPath()))
	record, err := os.OpenFile(recordPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(tb, err)
	defer record.Close()
	_, err = record.WriteString(lines.String())
	require.NoError(tb, err)
}
