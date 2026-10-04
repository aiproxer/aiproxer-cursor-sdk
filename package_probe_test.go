package cursorsdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// The bounds below exist so the bounded-probe contract is checkable in a unit lane.
//
// The shipped bound is the packaging tool's own constant, and it is deliberately generous:
// a verdict that depended on a tight bound would be a verdict about this machine's speed. A
// case that waited the shipped bound out would prove nothing about a hang, so a case that
// needs the bound to be reached runs a copy of this repository whose bound is rewritten to
// probeTestTreeDeadline and then requires the verifier to finish inside probeTestRunBound.
// The bound is reached through a copied source tree and never through an option, so nothing
// a caller of the shipped verifier can supply changes how long a staged executable is given
// to answer.
const (
	probeTestTreeDeadline = 3 * time.Second
	probeTestRunBound     = 5 * time.Minute
	// probeTestDescendantGrace is how long a case waits before concluding that a descendant
	// of a hung probe survived it. It is well past the point at which a survivor records
	// itself, so the absence of that record is evidence rather than a race.
	probeTestDescendantGrace = 12 * time.Second
	// probeTestDescendantSettle is how long a case waits for a descendant it deliberately
	// left behind to exit on its own. A staged executable that is still running when the
	// case's files are cleaned up cannot be deleted on Windows, so leaving one behind would
	// turn a passing case into a failing one for a reason unrelated to the verdict.
	probeTestDescendantSettle = 8 * time.Second
	// fakePackageRuntimeVersion is the private runtime version the stand-in reports.
	fakePackageRuntimeVersion = "22.17.0"
)

// probeDeadlineConstant matches the packaging tool's shipped probe bound, so a case can
// rewrite it in a copied repository without restating where it lives or how it is spelled.
var probeDeadlineConstant = regexp.MustCompile(`(probeDeadline\s*=\s*)\d+(\s*\*\s*time\.Second)`)

// TestPackageArchive_BoundedProbesFailInsteadOfHanging keeps a replaced interactive binary a
// finding rather than a hang.
//
// The verifier probes the staged private runtime and the staged bridge launcher because an
// archive that carries them but cannot run them is not a working plugin. Both are executables
// this project ships but cannot vouch for at verification time, so a stub, a wrong build, or
// anything else that never answers has to end the run with a finding that names it. Without
// the bound, one such install turns the gate into a run that reports nothing at all, which is
// indistinguishable from a gate that was never executed.
//
// Every probe the verifier makes is named here, because each one is a way in: the runtime
// answers three questions and the launcher one, and a bound that covered only some of them
// would leave the rest waiting forever.
//
// The descendant is the second half of the same property: giving up on the launcher has to
// take the runtime it owns with it, or the run ends while the process it was waiting for is
// still consuming the machine.
func TestPackageArchive_BoundedProbesFailInsteadOfHanging(t *testing.T) {
	t.Parallel()

	// The bound under test is shorter than any real probe would use, so a hung probe has
	// to be decided in seconds. The finding quotes the bound that was applied, which is
	// how a reader of the report learns how long the verifier waited.
	want := strconv.Itoa(int(probeTestTreeDeadline.Seconds())) + "s"

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			for _, tc := range []struct {
				name string
				// hang is the staged failure the verifier's environment asks the
				// stand-in for, named by the probe it replaces.
				hang string
				// names names the executable the finding has to identify.
				names string
			}{
				{name: "private_runtime", hang: "runtime:hang", names: "private runtime"},
				{name: "bridge_launcher", hang: "doctor:hang", names: "bridge launcher"},
				{name: "runtime_version", hang: "version:hang", names: "private runtime"},
				{name: "runtime_components", hang: "components:hang", names: "private runtime"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					marker := filepath.Join(t.TempDir(), "descendant survived.txt")
					root := probeTree(t, packagelayoutArchive(t))

					report, code := runVerifierBounded(t, impl, shortProbeDeadlineRepo(t),
						root, probeStagedEnvironment(t, tc.hang, marker, ""), nil, probeTestRunBound)

					require.NotEqual(t, 0, code,
						"a staged %s that never answers passed verification:\n%s", tc.names, report)
					timedOut := verifyFindingsContaining(report, "probe timed out")
					require.NotEmpty(t, timedOut,
						"the run has to end with a finding that names the timed-out probe:\n%s", report)
					require.Contains(t, timedOut[0], want,
						"the finding has to name the bound that was applied:\n%s", report)
					require.NotContains(t, report, "verify-package: ok")

					time.Sleep(probeTestDescendantGrace)
					_, err := os.Stat(marker)
					require.ErrorIs(t, err, os.ErrNotExist,
						"a descendant of the timed-out %s survived the probe", tc.names)
				})
			}
		})
	}
}

// TestPackageArchive_ProbeResultsAreReportedAsTheyHappen keeps a probe that answered
// answering.
//
// The bound only decides what happens to an executable that does not answer. Everything a
// working runtime and launcher do still has to reach the report: the path the runtime resolved
// to, the version it reported, the components it carries, and what the launcher's doctor said.
// A bounded probe that answered "bounded" instead of the answer would satisfy a timeout case
// and lose the evidence a reader of the report depends on.
func TestPackageArchive_ProbeResultsAreReportedAsTheyHappen(t *testing.T) {
	t.Parallel()

	// The shipped bound, not the rewritten one: this case is about the answers, and the
	// stand-in answers immediately.
	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := probeTree(t, packagelayoutArchive(t))

			report, code := runVerifierBounded(t, impl, repoRoot(t), root, nil, nil, probeTestRunBound)

			require.Equal(t, 0, code,
				"a tree whose staged runtime and launcher answer has to verify:\n%s", report)
			require.Contains(t, report, "private runtime resolves to itself: yes",
				"the report has to say which executable answered:\n%s", report)
			require.Contains(t, report, "private runtime version: v"+fakePackageRuntimeVersion+
				" (recorded "+fakePackageRuntimeVersion+")",
				"the report has to carry the version the runtime itself printed:\n%s", report)
			for _, component := range []string{
				"node=" + fakePackageRuntimeVersion,
				"icu=75.1",
				"openssl=3.0.15+quic",
				"uv=1.51.0",
				"zlib=1.3.0.1-motley",
			} {
				require.Contains(t, report, component,
					"the report has to carry the component the runtime reported: %s\n%s", component, report)
			}
			require.Contains(t, report,
				"bridge doctor (launcher -> private runtime -> bridge entry): doctor: ok",
				"the report has to carry what the launcher's doctor said:\n%s", report)
			require.Contains(t, report, "private runtime sha256 (recorded, matches): ",
				"the recorded runtime digest is still compared against the staged bytes:\n%s", report)

			require.Empty(t, verifyFindingsContaining(report, "probe timed out"),
				"nothing here timed out, so nothing may be reported as having:\n%s", report)
			require.Empty(t, verifyFindingsContaining(report, "left its output open"),
				"the staged executables left nothing behind, so nothing may be reported as having:\n%s", report)
			require.Empty(t, verifyFindingsContaining(report, "did not run"),
				"a runtime that answered is not a runtime that failed to run:\n%s", report)
		})
	}
}

// TestPackageArchive_AProbeThatLeftOutputOpenIsReportedRatherThanWaitedFor keeps the bound
// meaningful for a staged executable that answers and then leaves something behind.
//
// A launcher that starts a runtime and exits without waiting for it is a working-looking
// executable: it answered, its exit status is success, and its answer is in the report. What it
// also did is leave a process holding the stream the verifier was reading its answer from. The
// run has to finish anyway, and it has to say that it finished by giving up on a stream rather
// than by reaching the end of one - because what is left behind is not something the verifier
// can promise to have terminated.
func TestPackageArchive_AProbeThatLeftOutputOpenIsReportedRatherThanWaitedFor(t *testing.T) {
	t.Parallel()

	want := strconv.Itoa(int(probeTestTreeDeadline.Seconds())) + "s"

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			held := filepath.Join(t.TempDir(), "stream held.txt")
			root := probeTree(t, packagelayoutArchive(t))

			report, _ := runVerifierBounded(t, impl, shortProbeDeadlineRepo(t), root,
				probeStagedEnvironment(t, "", "", held), nil, probeTestRunBound)

			// Nothing here hung, and nothing timed out: the staged executables answered.
			require.Empty(t, verifyFindingsContaining(report, "probe timed out"),
				"a staged executable that answered did not time out:\n%s", report)
			require.Contains(t, report, "private runtime resolves to itself: yes",
				"the runtime answered, so the run has to say so:\n%s", report)
			require.Contains(t, report, "bridge doctor (launcher -> private runtime -> bridge entry): doctor: ok",
				"the launcher answered, so the run has to say so:\n%s", report)
			require.NotContains(t, report, "verify-package: ok",
				"a stream that could not be finished is a finding, so the run is not clean:\n%s", report)

			undrained := verifyFindingsContaining(report, "left its output open")
			require.NotEmpty(t, undrained,
				"the run has to end with a finding that names the stream it could not finish:\n%s", report)
			require.Contains(t, undrained[0], want,
				"the finding has to name the bound that was applied:\n%s", report)
			require.Contains(t, undrained[0], "cannot be terminated",
				"the finding has to state the limit rather than imply the leftover is gone:\n%s", report)

			// The descendant really is still there: the limit the finding states is the
			// true one, so the case checks it rather than trusting the wording.
			_, err := os.Stat(held)
			require.NoError(t, err,
				"the staged descendant is expected to outlive an executable that exited on its own")

			time.Sleep(probeTestDescendantSettle)
		})
	}
}

// TestPackageArchive_AProbeCarriesAFailingRuntimeExitStatusAndOutput keeps a runtime that
// refuses to start a reportable failure with the reason it gave.
//
// A runtime replaced by something that exits immediately is the common shape of a broken
// install, and the reason it printed is the only thing that tells an operator whether the tree
// is damaged, provisioned wrongly, or running the wrong build. So the exit status has to decide
// the verdict and the output has to reach the report, which is the ordinary case the bounded
// probe must not turn into a special one.
func TestPackageArchive_AProbeCarriesAFailingRuntimeExitStatusAndOutput(t *testing.T) {
	t.Parallel()

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			t.Parallel()

			root := probeTree(t, packagelayoutArchive(t))

			report, code := runVerifierBounded(t, impl, repoRoot(t), root,
				probeStagedEnvironment(t, "version:fail:7", "", ""), nil, probeTestRunBound)

			require.NotEqual(t, 0, code,
				"a staged runtime that exits non-zero passed verification:\n%s", report)
			require.Contains(t, report, "the staged private runtime did not run",
				"the finding has to say the staged runtime did not run:\n%s", report)
			require.Contains(t, report, "fake-runtime: staged failure",
				"the finding has to carry what the runtime printed:\n%s", report)
			require.Empty(t, verifyFindingsContaining(report, "probe timed out"),
				"a runtime that exited is not a runtime that timed out:\n%s", report)
			require.NotContains(t, report, "verify-package: ok")
		})
	}
}

// shortProbeDeadlineRepo mirrors this repository with the packaging tool's probe bound
// rewritten, so a case can reach a hung probe in seconds without the verifier offering a way
// to choose that bound.
//
// This is the same seam the install-ownership cases use: the verifier resolves its layout
// contract from the repository it ships in, so giving it a temporary repository is how a case
// varies something about that repository. Nothing here is an input a caller of the shipped
// verifier can supply.
func shortProbeDeadlineRepo(tb testing.TB) string {
	tb.Helper()

	root := tb.TempDir()
	mirrorRepo(tb, repoRoot(tb), root)

	seconds := strconv.Itoa(int(probeTestTreeDeadline.Seconds()))
	path := filepath.Join(root, "cmd", "lip-cursor-sdk-packaging", "probe.go")
	body := readFileText(tb, path)
	rewritten := probeDeadlineConstant.ReplaceAllString(body, "${1}"+seconds+"${2}")
	require.NotEqual(tb, body, rewritten, "%s no longer declares the probe bound this case rewrites", path)
	require.NoError(tb, os.WriteFile(path, []byte(rewritten), 0o644))
	return root
}

// runVerifierBounded verifies one install root under a wall-clock bound.
//
// The bound is what a hang fails: a verifier that waits on a staged executable which never
// answers produces no report, so a case has to notice that rather than wait for it. A run that
// overruns is failed with the output produced so far, because that output is what says which
// probe it was stuck on.
func runVerifierBounded(tb testing.TB, impl, scriptDir, installRoot string, env, extra []string,
	bound time.Duration,
) (string, int) {
	tb.Helper()

	if scriptDir == "" {
		scriptDir = repoRoot(tb)
	}
	script, shell, shellArgs := packagingScriptArgs(tb, scriptDir, "verify-package", impl)
	argv := append(shellArgs, append([]string{script},
		scriptArgsFor(impl, map[string]string{"package-root": installRoot}, extra)...)...)

	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, argv...)
	cmd.Dir = scriptDir
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = append(os.Environ(), "GOWORK=off")
	}
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		terminateProbeHarnessTree(cmd)
		tb.Fatalf("the %s verifier did not finish within %s, so a probe of a staged executable is unbounded:\n%s",
			impl, bound, out)
	}
	code := 0
	var exitErr *exec.ExitError
	if err != nil {
		if !errors.As(err, &exitErr) {
			tb.Fatalf("run %s: %v", shell, err)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// verifyFindingsContaining returns the verifier's FAIL lines that mention text, so a case can
// assert on the findings one branch produces without restating the report around them.
func verifyFindingsContaining(report, text string) []string {
	var out []string
	for _, finding := range verifyFindings(report) {
		if strings.Contains(finding, text) {
			out = append(out, finding)
		}
	}
	return out
}

// probeStagedEnvironment is the environment a staged-failure case runs the verifier with.
//
// hang replaces the stand-in's answer with a staged failure for the named probes.
// treeMarker leaves a descendant that records itself much later, so a case can see whether it
// was terminated with the executable that started it.
// streamMarker leaves a descendant that keeps the staged executable's output stream open after
// it exits, which is the case the verifier has to finish anyway and report.
//
// The verifier hands its own environment to the executables it starts, so the stand-in reads
// this from the same environment the verifier does. It is a property of the stand-in rather
// than an input to the verifier: nothing here reaches a verdict.
func probeStagedEnvironment(tb testing.TB, hang, treeMarker, streamMarker string) []string {
	tb.Helper()

	var env []string
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.HasPrefix(key, "LIP_FAKE_PACKAGE_RUNTIME_") {
			continue
		}
		env = append(env, entry)
	}
	if hang != "" {
		env = append(env, "LIP_FAKE_PACKAGE_RUNTIME_HANG="+hang)
	}
	if treeMarker != "" {
		env = append(env, "LIP_FAKE_PACKAGE_RUNTIME_TREE="+treeMarker)
	}
	if streamMarker != "" {
		env = append(env, "LIP_FAKE_PACKAGE_RUNTIME_STREAM="+streamMarker)
	}
	return append(env, "GOWORK=off")
}

// probeTree stages an install tree whose private runtime and bridge launcher are
// deterministic stand-ins, so what the verifier does with a probed executable is checkable
// without a JavaScript toolchain, a network, or an assembled archive.
//
// Everything the verifier reads is staged, including every required archive entry and a host
// manifest that agrees with the staged bytes, so a case that expects a clean verdict gets one
// and a case that expects a finding reads findings it set up. The release record's runtime
// digest is the staged executable's real digest, so the recorded-digest cross-check passes.
func probeTree(tb testing.TB, archive packagelayout.Archive) string {
	tb.Helper()

	root := filepath.Join(tb.TempDir(), "probe install root with spaces")

	// The two probed executables are the same stand-in staged under the two names the
	// layout contract gives them, because the verifier distinguishes them only by the
	// arguments it passes.
	fake := fakePackageRuntimeExe(tb)
	for _, rel := range []string{archive.PrivateRuntimePath(), archive.LauncherPath()} {
		staged := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(tb, os.MkdirAll(filepath.Dir(staged), 0o755))
		require.NoError(tb, copyFileMode(fake, staged))
	}

	text := map[string]string{
		archive.BridgePackageJSONPath(): `{"name":"lip-cursor-sdk-bridge","version":"0.1.0",` +
			`"engines":{"node":">=22.13"},"dependencies":{"@cursor/sdk":"1.0.23"}}`,
		archive.BridgePackageLockPath(): `{"name":"lip-cursor-sdk-bridge","lockfileVersion":3,` +
			`"packages":{"":{"dependencies":{"@cursor/sdk":"1.0.23"}}}}`,
		archive.BridgeEntryPath():                         "// bridge entry shim\n",
		archive.BridgeDistPath() + "/main.js":             "// staged bridge build output\n",
		archive.LicensesDir() + "/THIRD-PARTY-NOTICES.md": "# Third-party notices\n",
		archive.PrivateRuntimeNPMCLIPath():                "// npm ships with the runtime\n",
		archive.PrivateRuntimeNPMLicensePath():            "npm is licensed on its own terms.\n",
		archive.OuterExecutablePath():                     "// staged outer executable\n",
	}
	writeStagedFiles(tb, root, text)

	// The release record and the host manifest each name a digest of a staged file, so they
	// are composed once those bytes exist rather than staged with everything else.
	text[archive.CompatibilityPath()] = probeReleaseRecord(tb, archive, root)
	text[archive.ManifestPath()] = probeHostManifest(tb, archive, root)
	writeStagedFiles(tb, root, map[string]string{
		archive.CompatibilityPath(): text[archive.CompatibilityPath()],
		archive.ManifestPath():      text[archive.ManifestPath()],
	})

	// The provisioned tree is written after the shipped record is composed, because it is
	// operator-owned content that the shipped record must not cover.
	sdkMetadata := filepath.Join(root, filepath.FromSlash(archive.ProvisionedSDKPackageJSONPath()))
	require.NoError(tb, os.MkdirAll(filepath.Dir(sdkMetadata), 0o755))
	require.NoError(tb, os.WriteFile(sdkMetadata, []byte(`{"name":"@cursor/sdk","version":"1.0.23"}`), 0o644))

	// The checksum record covers the shipped files the tree stages, plus the two probed
	// executables, which are binaries rather than staged text.
	shipped := make(map[string]string, len(text)+2)
	for rel, body := range text {
		shipped[rel] = body
	}
	shipped[archive.PrivateRuntimePath()] = ""
	shipped[archive.LauncherPath()] = ""
	writeProvisioningChecksums(tb, root, archive, shipped, false)
	return root
}

// writeStagedFiles writes install-root-relative files into a tree, creating their
// directories. A verifier reads them all by relative path, so a fixture stages them the same
// way an archive would.
func writeStagedFiles(tb testing.TB, root string, files map[string]string) {
	tb.Helper()

	for rel, body := range files {
		staged := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(tb, os.MkdirAll(filepath.Dir(staged), 0o755))
		require.NoError(tb, os.WriteFile(staged, []byte(body), 0o644))
	}
}

// probeReleaseRecord renders the release record this tree carries.
//
// It reuses the shape the provisioning fixtures render, because the probe block reads the same
// record they do, and adds the two fields only a staged runtime answers: the version it reports
// and the digest of its bytes.
func probeReleaseRecord(tb testing.TB, archive packagelayout.Archive, root string) string {
	tb.Helper()

	fields := map[string]any{}
	require.NoError(tb, json.Unmarshal([]byte(provisioningRecord(tb, archive, root,
		provisioningTreeOptions{provisionedVersion: "1.0.23"})), &fields))
	fields["private_runtime_version"] = fakePackageRuntimeVersion
	fields["private_runtime_sha256"] = fileSHA256(tb,
		filepath.Join(root, filepath.FromSlash(archive.PrivateRuntimePath())))
	fields["private_runtime_source"] = "staged by a probe fixture"

	body, err := json.MarshalIndent(fields, "", "  ")
	require.NoError(tb, err)
	return string(body) + "\n"
}

// probeHostManifest renders the closed host manifest the tree carries, agreeing with the staged
// bytes it names. The verifier checks the digest, the platform claim, the identity it shares
// with release.yaml, and the export posture, so a tree that wants a clean verdict has to carry
// all four rather than only the entries the probe block reads.
func probeHostManifest(tb testing.TB, archive packagelayout.Archive, root string) string {
	tb.Helper()

	manifest := map[string]any{
		"schema":     "golip.backendplugin.manifest/v1",
		"plugin_id":  "io.golip.backend.cursorsdk",
		"version":    "0.1.0",
		"build_id":   "localdev",
		"executable": archive.OuterExecutablePath(),
		"sha256": fileSHA256(tb,
			filepath.Join(root, filepath.FromSlash(archive.OuterExecutablePath()))),
		"platforms": []any{map[string]any{"os": archive.OS(), "arch": archive.Arch()}},
		"exports": []any{map[string]any{
			"kind":            "cursorsdk",
			"credential_mode": "static",
			"access_scope":    "local_only",
			"process_sharing": "per_instance",
			"execution_class": "agent_runtime",
		}},
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(tb, err)
	return string(body) + "\n"
}

// fakePackageRuntimeExe compiles the stand-in for the staged private runtime and the staged
// bridge launcher, once for the whole package. TestMain removes what it builds.
var (
	fakeRuntimeOnce sync.Once
	fakeRuntimeDir  string
	fakeRuntimePath string
	fakeRuntimeErr  error
)

func fakePackageRuntimeExe(tb testing.TB) string {
	tb.Helper()

	fakeRuntimeOnce.Do(func() {
		name := "fake-cursor-sdk-runtime"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		dir, err := os.MkdirTemp("", "fake-cursor-sdk-runtime-")
		if err != nil {
			fakeRuntimeErr = err
			return
		}
		exe := filepath.Join(dir, name)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./internal/packagetest/cmd/fake-cursor-sdk-runtime")
		cmd.Dir = repoRoot(tb)
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			fakeRuntimeErr = fmt.Errorf("go build fake-cursor-sdk-runtime: %w\n%s", buildErr, out)
			return
		}
		fakeRuntimeDir = dir
		fakeRuntimePath = exe
	})
	if fakeRuntimeErr != nil {
		tb.Fatal(fakeRuntimeErr)
	}
	return fakeRuntimePath
}

// discardFakePackageRuntime removes the compiled stand-in this run built.
func discardFakePackageRuntime() {
	if fakeRuntimeDir != "" {
		_ = os.RemoveAll(fakeRuntimeDir)
	}
}

// TestMain keeps the stand-in the packaging probe cases use from outliving the run.
//
// The stand-in is compiled once per test binary into a directory of its own rather than into a
// temporary directory the testing package owns, because concurrent test binaries would
// otherwise build over one executable another is still running. That makes it this package's to
// remove, and leaving a compiled executable behind on every run is how a machine's temporary
// directory fills up.
func TestMain(m *testing.M) {
	code := m.Run()
	discardFakePackageRuntime()
	os.Exit(code)
}
