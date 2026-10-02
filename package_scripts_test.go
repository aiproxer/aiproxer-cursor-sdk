package cursorsdk_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/stretchr/testify/require"
)

// packagingScripts is every packaging script that has to satisfy the same contract
// as its counterpart on the other platform. A trust check that exists in only one of
// them is a trust check the other platform silently lacks.
var packagingScripts = []string{
	filepath.Join("scripts", "package-plugin.sh"),
	filepath.Join("scripts", "package-plugin.ps1"),
	filepath.Join("scripts", "verify-package.sh"),
	filepath.Join("scripts", "verify-package.ps1"),
}

// TestPackageArchive_ScriptsReportUsageErrorsWithTheSameExitCode keeps the two
// verifiers interchangeable for the automation that runs them: a caller has to be
// able to treat "you did not tell me which install root" the same way whichever
// script it invoked. The shell script answers a usage error with exit 2, so the
// PowerShell script has to answer with the same code instead of the 1 a thrown
// error produces.
//
// An argument PowerShell itself rejects while binding parameters is outside this:
// the host parses the command line before the script runs, and reports its own exit
// code. What both scripts inspect themselves has to agree.
func TestPackageArchive_ScriptsReportUsageErrorsWithTheSameExitCode(t *testing.T) {
	t.Parallel()

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			out, code := runPackagingScriptEnvImpl(t, "verify-package", impl, nil, nil)
			require.Equal(t, 2, code, "verify-package (%s) usage error exit code:\n%s", impl, out)
			// The two scripts spell the option in their own syntax, so the check
			// compares the name without the punctuation that introduces it.
			named := strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(out))
			require.Contains(t, named, "packageroot",
				"the usage error has to name the missing option:\n%s", out)
		})
	}
}

// TestPackageArchive_ScriptsSetGoWorkspaceOffBeforeAnyGoInvocation keeps the
// scripts independent of a Go workspace. This repository is a standalone module: a go
// invocation that runs with a developer's GOWORK pointing at a sibling checkout
// proves something about that workspace rather than about the plugin, and the README
// states that the scripts set GOWORK=off themselves. Setting it after the first
// invocation, or only in the verifier, satisfies neither.
func TestPackageArchive_ScriptsSetGoWorkspaceOffBeforeAnyGoInvocation(t *testing.T) {
	t.Parallel()

	goInvocation := regexp.MustCompile(`(?:^|[\s"'(])go\s+(?:env|run|build|test|list|vet|mod)\b`)

	for _, script := range packagingScripts {
		body := readFileText(t, filepath.Join(repoRoot(t), script))
		set := false
		for i, raw := range strings.Split(body, "\n") {
			line := strings.TrimRight(raw, "\r")
			if strings.Contains(line, "GOWORK") {
				set = true
			}
			if !goInvocation.MatchString(line) {
				continue
			}
			require.True(t, set || strings.Contains(line, "GOWORK"),
				"%s:%d invokes go before GOWORK is turned off: %s", script, i+1, strings.TrimSpace(line))
		}
	}
}

// TestPackageArchive_NodeFreeEnvironmentHasNoReachableNode keeps the private-runtime
// evidence real: the environment the harness hands the verifier has to have no
// working node behind the runtime name.
//
// The POSIX host cannot simply drop the directories that hold a node, because they
// are the system directories the shell verifier also needs sha256sum, find, and sort
// from. Shadowing the name with a directory is not enough either: PATH resolution
// stops at the first directory that holds something runnable, and it keeps looking
// through the rest of PATH, so a real node further down was still reachable and the
// "no global Node" proof proved nothing. What has to hold is that the first runnable
// node on the scrubbed PATH is the harness's own failing shim.
func TestPackageArchive_NodeFreeEnvironmentHasNoReachableNode(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	nodeDir := filepath.Join(base, "node dir")
	toolsDir := filepath.Join(base, "tools dir")
	require.NoError(t, os.MkdirAll(nodeDir, 0o755))
	require.NoError(t, os.MkdirAll(toolsDir, 0o755))
	installBinary(t, filepath.Join(nodeDir, "node"+exeSuffix()), fakebridge.BuildNodeExe(t))
	poison := writeNodeShims(t)

	hostPath := strings.Join([]string{nodeDir, toolsDir}, string(filepath.ListSeparator))

	// POSIX: the directories stay because the scripts need them, so the failing shim
	// has to be what the runtime name resolves to.
	posix := scrubbedPath(hostPath, poison, false)
	require.Equal(t, poison, firstRunnableNodeDir(t, posix),
		"a working node is still reachable through the scrubbed PATH:\n%s", posix)
	require.Contains(t, posix, toolsDir,
		"the POSIX scrub has to keep the directories the shell verifier needs")
	require.Contains(t, posix, nodeDir,
		"the POSIX scrub keeps the system directory; the shim is what stops the name")

	// Windows: the toolchain lives in node-free directories, so a node directory can be
	// dropped outright rather than only shadowed.
	windows := scrubbedPath(hostPath, poison, true)
	require.Equal(t, []string{poison}, runnableNodeDirs(t, windows),
		"a node directory survived the Windows scrub:\n%s", windows)
	require.Contains(t, windows, toolsDir)

	// The shims themselves have to fail, or shadowing a name with them would hide a
	// working runtime rather than remove one.
	for _, shim := range nodeShimPaths(poison) {
		require.False(t, nodeRuns(t, shim), "%s has to fail: a working shim would hide the real question", shim)
	}

	// And the environment this machine actually builds has the same property, checked
	// against the real toolchain rather than a synthetic PATH: the runtime the name
	// resolves to has to be one that fails.
	env := nodeFreeEnvironment(t)
	for _, path := range pathValues(t, env) {
		dir := firstRunnableNodeDir(t, path)
		if dir == "" {
			continue
		}
		reached, ok := runnableNode(dir)
		if !ok {
			continue
		}
		require.False(t, nodeRuns(t, reached),
			"a working node is reachable on the scrubbed PATH as %s:\n%s", reached, path)
	}
}

// scrubbedPath rewrites one PATH value so the runtime name cannot resolve to a
// working node: node directories are dropped when they can be, and the failing shim
// directory always comes first, because resolution stops at the first match.
func scrubbedPath(value, poison string, dropNodeDirs bool) string {
	dirs := []string{poison}
	for _, dir := range filepath.SplitList(value) {
		if dropNodeDirs && hasNodeExecutable(filepath.Clean(dir)) {
			continue
		}
		dirs = append(dirs, dir)
	}
	return strings.Join(dirs, string(filepath.ListSeparator))
}

// runnableNodeDirs lists every directory on one PATH value that holds a runnable
// node, so a scrub can be checked for what it removed and not only for what it
// shadowed.
func runnableNodeDirs(tb testing.TB, path string) []string {
	tb.Helper()

	var out []string
	for _, dir := range filepath.SplitList(path) {
		if hasNodeExecutable(filepath.Clean(dir)) {
			out = append(out, filepath.Clean(dir))
		}
	}
	return out
}

// pathValues returns the PATH values of an environment, requiring at least one.
func pathValues(tb testing.TB, env []string) []string {
	tb.Helper()

	var out []string
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			out = append(out, value)
		}
	}
	require.NotEmpty(tb, out, "the scrubbed environment has no PATH")
	return out
}

// writeNodeShims creates the directory of runtimes that fail on purpose and returns
// it. A shim that cannot run is enough: nothing resolves to a working node.
func writeNodeShims(tb testing.TB) string {
	tb.Helper()

	poison := tb.TempDir()
	for _, shim := range nodeShimPaths(poison) {
		if runtime.GOOS == "windows" {
			require.NoError(tb, os.WriteFile(shim, []byte("@exit /b 1\r\n"), 0o700))
			continue
		}
		require.NoError(tb, os.WriteFile(shim, []byte("#!/bin/sh\nexit 1\n"), 0o700))
	}
	return poison
}

// nodeShimPaths are the runtime names a resolver may pick in a directory that holds
// only the failing shims.
func nodeShimPaths(dir string) []string {
	names := []string{"node", "node.exe"}
	if runtime.GOOS == "windows" {
		names = []string{"node.cmd", "node.ps1"}
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// nodeRuns reports whether a node shim or executable actually runs. A shim that
// cannot run is enough to satisfy the harness: nothing resolves to a working runtime.
func nodeRuns(tb testing.TB, shim string) bool {
	tb.Helper()

	name := filepath.Base(shim)
	shell, args := "sh", []string{shim, "--version"}
	switch {
	case strings.HasSuffix(name, ".cmd"):
		// A batch file needs the command interpreter: CreateProcess cannot start one.
		shell, args = windowsCommandInterpreter(), []string{"/c", shim, "--version"}
	case strings.HasSuffix(name, ".ps1"):
		pwsh, err := exec.LookPath("pwsh")
		require.NoError(tb, err)
		shell, args = pwsh, []string{"-NoProfile", "-NonInteractive", "-File", shim, "--version"}
	}
	cmd := exec.Command(shell, args...)
	cmd.Dir = filepath.Dir(shim)
	return cmd.Run() == nil
}

// windowsCommandInterpreter is cmd.exe by absolute path: a bare cmd on PATH can be
// anything, and the answer to "does this shim fail" cannot depend on that.
func windowsCommandInterpreter() string {
	system := os.Getenv("SystemRoot")
	if system == "" {
		return "cmd"
	}
	return filepath.Join(system, "System32", "cmd.exe")
}

// TestPackageArchive_ScriptsMakeTheStagedRuntimeExecutable keeps the staged private
// runtime startable as a direct process. The launcher starts it without a shell, so
// a staged runtime that is not executable is an archive that cannot serve a request:
// both packagers have to grant the execute bit, each through its own shell. Windows
// has no permission bit to grant, which is why the packagers grant it where one
// exists rather than where the archive is assembled.
func TestPackageArchive_ScriptsMakeTheStagedRuntimeExecutable(t *testing.T) {
	t.Parallel()

	for script, marker := range map[string]string{
		filepath.Join("scripts", "package-plugin.sh"):  "chmod 0755",
		filepath.Join("scripts", "package-plugin.ps1"): "SetUnixFileMode",
	} {
		body := readFileText(t, filepath.Join(repoRoot(t), script))
		require.Contains(t, body, marker,
			"%s does not make the staged private runtime executable", script)
	}
}
