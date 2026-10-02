package cursorsdk_test

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
)

// packageGateEnv opts a checkout into the native package gate.
//
// The gate assembles a real archive: it builds production JavaScript, resolves the
// production dependency tree over the network, and copies a private Node runtime,
// which takes minutes and needs the build-time toolchain. That is deliberate work,
// not a unit test, so it runs only where it is asked for - the packaging lane, or a
// maintainer running scripts/package-plugin by hand. The static half of this file
// (the layout-parity check) always runs.
const packageGateEnv = "LIP_PACKAGE_GATE"

// TestPackageArchive_NativeArchiveIsInstallableAndVerifiable is the native package
// platform validation gate: an archive assembled on this machine has to install,
// verify, and run its private runtime, and every way a package can lie about its
// contents has to fail verification rather than pass silently.
//
// Nothing here is hermetic: packaging builds production JavaScript and stages the
// production dependency tree with npm, and the gate validates exactly one platform:
// the one it runs on. Cross-compiling another platform's archive and claiming it
// would be the failure mode this gate exists to prevent.
func TestPackageArchive_NativeArchiveIsInstallableAndVerifiable(t *testing.T) {
	if testing.Short() {
		t.Skip("native package platform validation builds production JavaScript and stages the dependency tree")
	}
	if !packageGateRequested() {
		t.Skipf("set %s=1, or run scripts/package-plugin and scripts/verify-package, to assemble and validate a native archive",
			packageGateEnv)
	}
	for _, tool := range []string{"go", "npm", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("native package platform validation needs the build-time %s toolchain", tool)
		}
	}

	built := builtPackage(t)

	t.Run("staged_tree_is_the_design_layout_block", func(t *testing.T) {
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		for _, rel := range archive.RequiredEntries() {
			full := filepath.Join(built.installRoot, filepath.FromSlash(rel))
			info, statErr := os.Stat(full)
			require.NoError(t, statErr, "required archive entry %s is missing", rel)
			require.True(t, info.IsDir() || info.Mode().IsRegular(), "%s is neither a file nor a directory", rel)
		}

		// The bridge entry is the package's own CLI shim, not dist/main.js: the
		// connector's tool contract reaches --version and doctor through it.
		entry := readFileText(t, filepath.Join(built.installRoot, filepath.FromSlash(archive.BridgeEntryPath())))
		require.Contains(t, entry, "PINNED_SDK_VERSION")
		require.Contains(t, entry, "runDoctor")
		require.FileExists(t, filepath.Join(built.installRoot, filepath.FromSlash(archive.BridgeDistPath()), "main.js"))

		// Only production dependencies are staged. A dev dependency in the archive
		// is a larger, more attackable run-time surface than the bridge needs.
		for _, dev := range []string{"typescript", "tsx", "esbuild", "@types", ".bin/tsc", ".bin/tsx"} {
			require.NoFileExists(t, filepath.Join(built.installRoot,
				filepath.FromSlash(archive.BridgeModulesPath()), filepath.FromSlash(dev)), dev)
		}
		// No npm is required to run the archive, so its lockfile has no place in it.
		require.NoFileExists(t, filepath.Join(built.installRoot,
			filepath.FromSlash(archive.BridgePackageDirPath()), "package-lock.json"))

		info, err := os.Stat(filepath.Join(built.installRoot, filepath.FromSlash(archive.PrivateRuntimePath())))
		require.NoError(t, err)
		require.Greater(t, info.Size(), int64(1<<20), "the staged private runtime is a stub, not a runtime")
	})

	t.Run("manifest_preserves_identity_and_native_platform_claim", func(t *testing.T) {
		manifest := decodeJSONObject(t, filepath.Join(built.installRoot, "plugin.backendplugin.json"))
		exeRel := "bin/lip-backend-cursorsdk" + exeSuffix()
		exePath := filepath.Join(built.installRoot, filepath.FromSlash(exeRel))

		require.Equal(t, "golip.backendplugin.manifest/v1", manifest["schema"])
		require.Equal(t, "io.golip.backend.cursorsdk", manifest["plugin_id"])
		require.Equal(t, built.pluginVersion, manifest["version"])
		require.Equal(t, built.buildID, manifest["build_id"])
		require.Equal(t, exeRel, manifest["executable"])
		// The host's digest stays the authority for the outer process, so the
		// manifest carries exactly the outer executable's digest.
		require.Equal(t, fileSHA256(t, exePath), manifest["sha256"])
		require.Equal(t, []any{map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH}}, manifest["platforms"])
		require.Equal(t, []any{map[string]any{
			"kind":            "cursorsdk",
			"credential_mode": "static",
			"access_scope":    "local_only",
			"process_sharing": "per_instance",
			"execution_class": "agent_runtime",
		}}, manifest["exports"])
		require.NotContains(t, manifest, "private_runtime")
		require.NotContains(t, manifest, "compatibility")
	})

	t.Run("compatibility_records_runtime_and_sdk_metadata", func(t *testing.T) {
		record := decodeJSONObject(t, filepath.Join(built.installRoot, "compatibility.json"))

		require.Equal(t, "golip.cursorsdk.compatibility/v1", record["schema"])
		require.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, record["platform"])
		require.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, record["native_platform_assembled"])
		require.Equal(t, "private-runtime", record["packaging_variant"])
		require.Equal(t, false, record["external_node_required"])
		require.Equal(t, "1.0.23", record["cursor_sdk_version"])
		require.Equal(t, "1.0.23", record["cursor_sdk_pinned_version"])
		require.Equal(t, built.pluginVersion, record["bridge_version"])
		require.Equal(t, ">=22.13", record["bridge_node_engine"])
		require.Equal(t, float64(1), record["protocol_major"])
		require.NotEmpty(t, record["private_runtime_version"])
		require.NotEmpty(t, record["private_runtime_sha256"])
		require.NotEmpty(t, record["licensing_status"])

		// Nothing in this task certified a host artifact, so the record says so
		// instead of naming a host it never ran against.
		require.Equal(t, "", record["tested_host_artifact_sha256"])
	})

	t.Run("checksums_cover_every_file_including_plugin_private_ones", func(t *testing.T) {
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)

		listed := readChecksums(t, filepath.Join(built.installRoot, filepath.FromSlash(archive.ChecksumsPath())))
		onDisk := walkRelativeFiles(t, built.installRoot)

		covered := mapsWithout(onDisk, archive.ChecksumsPath())
		require.Len(t, listed, len(covered), "every staged file except the checksum record itself is listed")
		for rel := range covered {
			require.Contains(t, listed, rel)
			require.Equal(t, fileSHA256(t, filepath.Join(built.installRoot, filepath.FromSlash(rel))), listed[rel])
		}

		// The coverage that matters most: the plugin-private runtime, the launcher,
		// the bridge entry, and the staged dependency tree are all in the record,
		// not just the manifest and the outer executable. A required entry that is
		// a directory has to be covered by the files inside it.
		for _, rel := range archive.PrivateEntries() {
			if info, statErr := os.Stat(filepath.Join(built.installRoot, filepath.FromSlash(rel))); statErr == nil && info.IsDir() {
				require.NotEmpty(t, filesUnderPrefix(t, listed, rel+"/"), "no checksummed file under %s", rel)
				continue
			}
			require.Contains(t, listed, rel, "private entry %s is not checksummed", rel)
		}
		require.Contains(t, listed, archive.ManifestPath())
		require.Contains(t, listed, archive.OuterExecutablePath())
		require.NotEmpty(t, filesUnderPrefix(t, listed, packagelayout.PrivatePrefix))
	})

	t.Run("verification_reports_files_checksums_and_runtime_metadata", func(t *testing.T) {
		report, errCode := runVerifyScript(t, built.installRoot, nil, nil)
		require.Equal(t, 0, errCode, "verify-package failed:\n%s", report)

		require.Contains(t, report, "platform: "+runtime.GOOS+"/"+runtime.GOARCH)
		require.Contains(t, report, "packaging variant: private-runtime")
		require.Contains(t, report, "external node required: no")
		require.Contains(t, report, "node on PATH: not required")
		require.Contains(t, report, "plugin-private files checksummed:")
		require.Contains(t, report, "host digest authority:")
		require.Contains(t, report, "sdk version (staged production tree): 1.0.23")
		require.Contains(t, report, "node engine required: >=22.13")
		require.Contains(t, report, "private runtime resolves to itself: yes")

		// Exact files and checksums: the report enumerates the archive, not a
		// summary of it.
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		listed := readChecksums(t, filepath.Join(built.installRoot, filepath.FromSlash(archive.ChecksumsPath())))
		entries := reportFileSection(t, report)
		require.Len(t, entries, len(listed))
		for _, line := range entries {
			digest, rel, ok := strings.Cut(line, packagelayout.ChecksumSeparator)
			require.True(t, ok, "malformed checksum line %q", line)
			require.Equal(t, listed[filepath.ToSlash(rel)], digest)
		}
	})

	t.Run("unprotected_install_root_is_rejected", func(t *testing.T) {
		root := copyInstallRoot(t, built.installRoot)
		report, errCode := runVerifyScript(t, root, nil, nil)
		require.Equal(t, 0, errCode, "a protected install root failed verification:\n%s", report)
		require.Contains(t, report, "install ownership:")

		if runtime.GOOS == "windows" {
			// Windows exposes ownership through the directory ACL, which the host
			// owns; the verifier states the requirement instead of guessing.
			require.Contains(t, report, "not machine-checkable here (Windows ACL)")
			return
		}
		// A group- or world-writable plugin root lets any local user replace a
		// checksummed companion after verification.
		require.NoError(t, os.Chmod(root, 0o777))
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		report, errCode = runVerifyScript(t, root, nil, nil)
		require.NotEqual(t, 0, errCode, "a world-writable plugin root passed verification")
		require.Contains(t, report, "writable beyond its owner")
		require.Contains(t, report, "protected plugin root")
	})

	t.Run("extracted_archive_installs_and_verifies", func(t *testing.T) {
		// The archive, not the staging tree, is what an operator receives: unpack
		// it into a fresh install root and verify the extracted bytes.
		installed := extractArchive(t, built.archive)
		report, errCode := runVerifyScript(t, installed, nil, nil)
		require.Equal(t, 0, errCode, "extracted archive failed verification:\n%s", report)
		require.Contains(t, report, "sdk version (staged production tree): 1.0.23")
		require.Contains(t, report, "private runtime resolves to itself: yes")
		require.Contains(t, report, fmt.Sprintf("install root: %s", installed))
	})

	t.Run("tampered_plugin_private_file_fails_verification", func(t *testing.T) {
		root := copyInstallRoot(t, built.installRoot)
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		tampered := filepath.Join(root, filepath.FromSlash(archive.BridgeDistPath()), "models.js")
		require.NoError(t, os.WriteFile(tampered, []byte(readFileText(t, tampered)+"\n// tampered\n"), 0o644))

		report, errCode := runVerifyScript(t, root, nil, nil)
		require.NotEqual(t, 0, errCode, "a tampered private file passed verification:\n%s", report)
		require.Contains(t, report, "private/bridge/dist/models.js")
		require.Contains(t, report, "checksum mismatch")
	})

	t.Run("missing_private_companion_fails_explicitly", func(t *testing.T) {
		root := copyInstallRoot(t, built.installRoot)
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		launcher := filepath.Join(root, filepath.FromSlash(archive.LauncherPath()))
		require.NoError(t, os.Remove(launcher))

		report, errCode := runVerifyScript(t, root, nil, nil)
		require.NotEqual(t, 0, errCode)
		require.Contains(t, report, archive.LauncherPath())
		require.Contains(t, report, "reinstall")
		require.NotContains(t, strings.ToLower(report), "npm install")
	})

	t.Run("missing_private_runtime_fails_explicitly_in_verify_and_in_the_launcher", func(t *testing.T) {
		root := copyInstallRoot(t, built.installRoot)
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(archive.PrivateRuntimePath()))))

		report, errCode := runVerifyScript(t, root, nil, nil)
		require.NotEqual(t, 0, errCode)
		require.Contains(t, report, archive.PrivateRuntimePath())
		require.Contains(t, report, "reinstall")

		// The run-time side is an explicit prerequisite failure too: the launcher
		// exits with EX_CONFIG and starts nothing, rather than falling back to a
		// node on PATH or any other provider integration.
		launcher := filepath.Join(root, filepath.FromSlash(archive.LauncherPath()))
		out, launchErr := runPackagedExecutable(t, launcher, "doctor")
		require.Error(t, launchErr)
		require.Contains(t, strings.ToLower(out), "private runtime file")
		require.Contains(t, out, "private/node/node")
		require.Contains(t, out, "reinstall")
		require.NotContains(t, out, "npm")
	})

	t.Run("unlisted_and_missing_files_are_detected", func(t *testing.T) {
		t.Run("unlisted", func(t *testing.T) {
			root := copyInstallRoot(t, built.installRoot)
			extra := filepath.Join(root, "private", "bridge", "notes.txt")
			require.NoError(t, os.WriteFile(extra, []byte("not part of the archive\n"), 0o644))

			report, errCode := runVerifyScript(t, root, nil, nil)
			require.NotEqual(t, 0, errCode)
			require.Contains(t, report, "private/bridge/notes.txt")
			require.Contains(t, report, "checksums.sha256")
		})

		t.Run("missing", func(t *testing.T) {
			root := copyInstallRoot(t, built.installRoot)
			archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(filepath.Join(root,
				filepath.FromSlash(archive.BridgeModulesPath()), ".package-lock.json")))

			report, errCode := runVerifyScript(t, root, nil, nil)
			require.NotEqual(t, 0, errCode)
			require.Contains(t, report, ".package-lock.json")
			require.Contains(t, report, "missing")
		})
	})

	t.Run("platform_overclaim_is_rejected", func(t *testing.T) {
		root := copyInstallRoot(t, built.installRoot)
		manifestPath := filepath.Join(root, "plugin.backendplugin.json")
		manifest := decodeJSONObject(t, manifestPath)
		manifest["platforms"] = []any{
			map[string]any{"os": "windows", "arch": "amd64"},
			map[string]any{"os": "windows", "arch": "arm64"},
			map[string]any{"os": "linux", "arch": "amd64"},
			map[string]any{"os": "linux", "arch": "arm64"},
		}
		writeJSONFile(t, manifestPath, manifest)
		rewriteChecksum(t, root, "plugin.backendplugin.json")

		report, errCode := runVerifyScript(t, root, nil, nil)
		require.NotEqual(t, 0, errCode, "an archive claiming unvalidated platforms passed verification")
		require.Contains(t, report, "platform")
		require.Contains(t, report, runtime.GOOS+"/"+runtime.GOARCH)
	})

	t.Run("private_runtime_variant_needs_no_global_node", func(t *testing.T) {
		// PATH keeps the Go toolchain the layout contract needs and drops every
		// directory that holds a node executable, so a runtime lookup through PATH
		// would fail instead of silently succeeding with the developer's Node.
		env := nodeFreeEnvironment(t)
		require.NotEmpty(t, env)

		report, errCode := runVerifyScript(t, built.installRoot, env, nil)
		require.Equal(t, 0, errCode, "verification needed a global Node:\n%s", report)
		require.Contains(t, report, "node on PATH: not required")
		require.Contains(t, report, "private runtime resolves to itself: yes")
		require.Contains(t, report, "sdk version (staged production tree): 1.0.23")
	})

	t.Run("cross_platform_archive_is_refused", func(t *testing.T) {
		other := "linux/amd64"
		if runtime.GOOS == "linux" {
			other = "windows/amd64"
		}
		out, errCode := runPackageScript(t, filepath.Join(t.TempDir(), "refused"), []string{"--platform", other})
		require.NotEqual(t, 0, errCode)
		require.Contains(t, out, other)
		require.Contains(t, out, "native")
	})
}

// TestPackageArchive_ScriptsDoNotRestateTheArchiveLayout keeps one layout
// contract. The shell and PowerShell scripts must read archive names from the
// layout report rather than carrying literals, otherwise a layout change could be
// applied to the Go contract while a script kept staging the previous one. The
// check is static so it holds for the platform it does not run on too.
func TestPackageArchive_ScriptsDoNotRestateTheArchiveLayout(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	for _, script := range []string{
		filepath.Join("scripts", "package-plugin.sh"),
		filepath.Join("scripts", "package-plugin.ps1"),
		filepath.Join("scripts", "verify-package.sh"),
		filepath.Join("scripts", "verify-package.ps1"),
	} {
		body := readFileText(t, filepath.Join(repo, script))
		for _, literal := range []string{
			packagelayout.OuterExecutableName,
			packagelayout.LauncherName,
			packagelayout.BridgeEntryName,
			packagelayout.ManifestFileName,
			packagelayout.CompatibilityFileName,
			packagelayout.ChecksumsFileName,
			packagelayout.LicensesDirName,
			// The plugin-private prefix catches every hardcoded private path,
			// including the private runtime, without forbidding the bare runtime
			// file name, which is an ordinary word on POSIX.
			packagelayout.PrivatePrefix,
		} {
			require.NotContains(t, body, literal, "%s restates the archive layout", script)
		}
		require.Contains(t, body, "lip-cursor-sdk-packaging", "%s must read the layout contract", script)
	}
}

// packageGateRequested reports whether this run opted into the native package gate.
func packageGateRequested() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(packageGateEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// packagedArchive is one natively assembled archive plus the report its packager
// produced.
type packagedArchive struct {
	installRoot   string
	archive       string
	archiveSHA256 string
	platform      string
	pluginVersion string
	buildID       string
	nodeVersion   string
	nodeSource    string
	fileCount     int
	outDir        string
}

var (
	packageOnce   sync.Once
	packageResult *packagedArchive
	packageErr    error
)

// builtPackage assembles one archive for the whole package: the platform gate is
// a single native assembly plus a set of cheap verifications of copies of it.
func builtPackage(tb testing.TB) *packagedArchive {
	tb.Helper()

	packageOnce.Do(func() {
		outDir := filepath.Join(tb.TempDir(), "package out dir")
		out, errCode := runPackageScriptWithRoot(tb, outDir, nil)
		if errCode != 0 {
			packageErr = fmt.Errorf("package-plugin exited %d:\n%s", errCode, out)
			return
		}
		fields := parseReportFields(out)
		result := &packagedArchive{
			outDir:        outDir,
			installRoot:   fields["install_root"],
			archive:       fields["archive"],
			archiveSHA256: fields["archive_sha256"],
			platform:      fields["platform"],
			pluginVersion: fields["plugin_version"],
			buildID:       fields["build_id"],
			nodeVersion:   fields["node_version"],
			nodeSource:    fields["node_source"],
		}
		count, err := strconv.Atoi(fields["file_count"])
		if err != nil {
			packageErr = fmt.Errorf("package-plugin reported no file count: %v\n%s", err, out)
			return
		}
		result.fileCount = count
		packageResult = result
	})
	if packageErr != nil {
		tb.Fatal(packageErr)
	}
	require.NotNil(tb, packageResult)
	require.DirExists(tb, packageResult.installRoot)
	require.FileExists(tb, packageResult.archive)
	require.Contains(tb, packageResult.installRoot, " ", "the install root must carry a space")
	require.Equal(tb, runtime.GOOS+"/"+runtime.GOARCH, packageResult.platform)
	return packageResult
}

// runPackageScript runs the platform packaging script with an explicit output dir.
func runPackageScript(tb testing.TB, outDir string, extra []string) (string, int) {
	tb.Helper()

	return runPackageScriptWithRoot(tb, outDir, extra)
}

func runPackageScriptWithRoot(tb testing.TB, outDir string, extra []string) (string, int) {
	tb.Helper()

	return runPackagingScript(tb, "package-plugin", scriptArgs(map[string]string{"out-dir": outDir}, extra))
}

// runVerifyScript verifies one install root with an overridden environment, which
// is how the no-global-Node case runs.
func runVerifyScript(tb testing.TB, installRoot string, env, extra []string) (string, int) {
	tb.Helper()

	return runPackagingScriptEnv(tb, "verify-package",
		scriptArgs(map[string]string{"package-root": installRoot}, extra), env)
}

// scriptArgs renders options for the platform's packaging script. The canonical
// spelling is the POSIX long option; the PowerShell script takes the same options
// in its own parameter spelling, so the harness is what knows the difference.
func scriptArgs(options map[string]string, extra []string) []string {
	args := make([]string, 0, 2*len(options)+len(extra))
	for _, key := range slices.Sorted(maps.Keys(options)) {
		args = append(args, scriptOption(key), options[key])
	}
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		if name, ok := strings.CutPrefix(arg, "--"); ok {
			arg = scriptOption(name)
		}
		args = append(args, arg)
	}
	return args
}

// scriptOption renders one canonical option name for this platform's script.
func scriptOption(name string) string {
	if runtime.GOOS != "windows" {
		return "--" + name
	}
	spelled := map[string]string{
		"out-dir":         "OutDir",
		"package-root":    "PackageRoot",
		"platform":        "Platform",
		"report":          "ReportPath",
		"expect-platform": "ExpectPlatform",
	}[name]
	if spelled == "" {
		return "-" + name
	}
	return "-" + spelled
}

// runPackagingScript runs one packaging script for this platform.
func runPackagingScript(tb testing.TB, name string, args []string) (string, int) {
	tb.Helper()

	return runPackagingScriptEnv(tb, name, args, nil)
}

func runPackagingScriptEnv(tb testing.TB, name string, args, env []string) (string, int) {
	tb.Helper()

	script, shell, shellArgs := packagingScriptArgs(tb, name)
	argv := append(shellArgs, append([]string{script}, args...)...)
	cmd := exec.Command(shell, argv...)
	cmd.Dir = repoRoot(tb)
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = append(os.Environ(), "GOWORK=off")
	}
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if err != nil {
		if !errors.As(err, &exitErr) {
			tb.Fatalf("run %s: %v", script, err)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// packagingScriptArgs picks the script and the interpreter that runs it. The
// PowerShell script is the Windows implementation and the shell script is the
// POSIX one; each platform validates its own.
func packagingScriptArgs(tb testing.TB, name string) (script string, shell string, shellArgs []string) {
	tb.Helper()

	repo := repoRoot(tb)
	if runtime.GOOS == "windows" {
		pwsh, err := exec.LookPath("pwsh")
		require.NoError(tb, err)
		return filepath.Join(repo, "scripts", name+".ps1"), pwsh, []string{"-NoProfile", "-NonInteractive", "-File"}
	}
	bash, err := exec.LookPath("bash")
	require.NoError(tb, err)
	return filepath.Join(repo, "scripts", name+".sh"), bash, nil
}

// runPackagedExecutable runs one executable from an assembled install root.
func runPackagedExecutable(tb testing.TB, exe string, args ...string) (string, error) {
	tb.Helper()

	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// nodeFreeEnvironment is the process environment in which no executable node is
// reachable through PATH, which is what the private-runtime cases need: the
// packaged runtime has to be the one that serves the request, and a verification
// that quietly fell back to a developer Node would prove nothing.
//
// On Windows the node directories are simply dropped, because the Go toolchain and
// the PowerShell host live in their own node-free directories. On POSIX the system
// directories hold both the POSIX tools the scripts need and the distribution's
// node, so a directory named after the runtime is placed first on PATH instead:
// PATH resolution stops there, and it holds no executable to run.
func nodeFreeEnvironment(tb testing.TB) []string {
	tb.Helper()

	poison := tb.TempDir()
	names := []string{"node", "node.exe", "node.cmd", "node.ps1"}
	for _, name := range names {
		require.NoError(tb, os.Mkdir(filepath.Join(poison, name), 0o755))
	}

	var env []string
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(key, "NODE_PATH") || strings.EqualFold(key, "NPM_CONFIG_PREFIX") {
			continue
		}
		if !strings.EqualFold(key, "PATH") {
			env = append(env, entry)
			continue
		}
		// Windows keeps the toolchain in node-free directories, so the node
		// directories are dropped. On POSIX the system directories hold both the
		// POSIX tools the scripts need and the distribution's node, so they stay
		// and the shadowing directory does the work.
		dirs := []string{poison}
		for _, dir := range filepath.SplitList(value) {
			if runtime.GOOS == "windows" && hasNodeExecutable(filepath.Clean(dir)) {
				continue
			}
			dirs = append(dirs, dir)
		}
		env = append(env, key+"="+strings.Join(dirs, string(filepath.ListSeparator)))
	}

	// The assertion the harness owes its readers: nothing executable named after a
	// node runtime is reachable through this PATH.
	for _, entry := range env {
		if !strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			continue
		}
		seen := false
		for _, dir := range filepath.SplitList(entry[len("PATH="):]) {
			if !hasNodeName(filepath.Clean(dir)) {
				continue
			}
			require.False(tb, hasNodeExecutable(filepath.Clean(dir)),
				"%s is reachable on the scrubbed PATH as a node executable", dir)
			seen = true
			break
		}
		require.True(tb, seen, "the scrubbed PATH never shadows the runtime name")
	}
	return append(env, "GOWORK=off")
}

// hasNodeName reports whether a directory holds any entry named after a node
// runtime, executable or not.
func hasNodeName(dir string) bool {
	for _, name := range []string{"node", "node.exe", "node.cmd", "node.ps1", "node.bat"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// hasNodeExecutable reports whether a directory holds a runnable node or node shim.
func hasNodeExecutable(dir string) bool {
	for _, name := range []string{"node", "node.exe", "node.cmd", "node.ps1", "node.bat"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// copyInstallRoot installs one assembled archive into a fresh install root whose
// path carries spaces, the way an operator's plugin directory does.
func copyInstallRoot(tb testing.TB, installRoot string) string {
	tb.Helper()

	dst := filepath.Join(tb.TempDir(), "plugin install root with spaces")
	require.NoError(tb, copyTree(installRoot, dst))
	require.Contains(tb, dst, " ")
	return dst
}

// copyTree copies a directory tree, creating parents as needed.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFileMode(path, target)
	})
}

// copyFileMode copies one file, preserving its executable bit.
func copyFileMode(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, info.Mode().Perm())
}

// extractArchive unpacks one assembled archive into a temp directory and returns
// the install root inside it, so verification runs against extracted bytes rather
// than the staging tree.
func extractArchive(tb testing.TB, archive string) string {
	tb.Helper()

	dst := filepath.Join(tb.TempDir(), "extracted install root with spaces")
	require.NoError(tb, os.MkdirAll(dst, 0o755))
	if strings.HasSuffix(archive, ".zip") {
		reader, err := zip.OpenReader(archive)
		require.NoError(tb, err)
		defer reader.Close()
		for _, file := range reader.File {
			require.NoError(tb, writeZipEntry(dst, file))
		}
	} else {
		raw, err := os.Open(archive)
		require.NoError(tb, err)
		defer raw.Close()
		gz, err := gzip.NewReader(raw)
		require.NoError(tb, err)
		defer gz.Close()
		tarReader := tar.NewReader(gz)
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(tb, err)
			require.NoError(tb, writeTarEntry(dst, header, tarReader))
		}
	}

	// An archive carries a single install root at its top level; an operator
	// unpacks exactly that directory into the host plugin root.
	require.NoFileExists(tb, filepath.Join(dst, "plugin.backendplugin.json"),
		"the archive must not have the install root contents at its top level")
	entries, err := os.ReadDir(dst)
	require.NoError(tb, err)
	require.Len(tb, entries, 1, "the archive must carry exactly one install root")
	root := filepath.Join(dst, entries[0].Name())
	require.FileExists(tb, filepath.Join(root, "plugin.backendplugin.json"))
	require.Contains(tb, root, " ")
	return root
}

// writeZipEntry writes one zip member under root.
func writeZipEntry(root string, file *zip.File) error {
	target, err := containedTarget(root, file.Name)
	if err != nil {
		return err
	}
	if file.FileInfo().IsDir() {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := file.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// writeTarEntry writes one tar member under root.
func writeTarEntry(root string, header *tar.Header, reader io.Reader) error {
	target, err := containedTarget(root, header.Name)
	if err != nil {
		return err
	}
	if header.Typeflag == tar.TypeDir {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode).Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, reader)
	return err
}

// containedTarget keeps an archive member inside the extraction root: an archive
// that tried to escape would otherwise write outside the temp directory.
func containedTarget(root, name string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive member %q escapes the extraction root", name)
	}
	return target, nil
}

// readChecksums parses a sha256sum-style checksum record into digest per path.
func readChecksums(tb testing.TB, path string) map[string]string {
	tb.Helper()

	raw, err := os.Open(path)
	require.NoError(tb, err)
	defer raw.Close()

	out := make(map[string]string)
	scanner := bufio.NewScanner(raw)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		digest, rel, ok := strings.Cut(line, packagelayout.ChecksumSeparator)
		require.True(tb, ok, "malformed checksum line %q", line)
		require.Len(tb, digest, 64, "malformed digest in %q", line)
		_, dup := out[filepath.ToSlash(rel)]
		require.False(tb, dup, "duplicate checksum line for %q", rel)
		out[filepath.ToSlash(rel)] = digest
	}
	require.NoError(tb, scanner.Err())
	return out
}

// rewriteChecksum recomputes one record entry so a negative case can isolate the
// check it targets instead of failing earlier on the digest.
func rewriteChecksum(tb testing.TB, root, rel string) {
	tb.Helper()

	path := filepath.Join(root, packagelayout.ChecksumsFileName)
	listed := readChecksums(tb, path)
	listed[rel] = fileSHA256(tb, filepath.Join(root, filepath.FromSlash(rel)))

	var lines []string
	for _, key := range slices.Sorted(maps.Keys(listed)) {
		lines = append(lines, listed[key]+packagelayout.ChecksumSeparator+key)
	}
	require.NoError(tb, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// reportFileSection returns the checksum lines of a verification report.
func reportFileSection(tb testing.TB, report string) []string {
	tb.Helper()

	const begin, end = "--- files ---", "--- end of files ---"
	start := strings.Index(report, begin)
	require.GreaterOrEqual(tb, start, 0, "report has no file section:\n%s", report)
	stop := strings.Index(report, end)
	require.Greater(tb, stop, start, "report has an unterminated file section:\n%s", report)
	body := report[start+len(begin) : stop]

	var out []string
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseReportFields reads the key: value lines a packaging script prints.
func parseReportFields(out string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		key, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok || strings.ContainsAny(key, " \\") {
			continue
		}
		fields[key] = value
	}
	return fields
}

// walkRelativeFiles returns every file under root as a slash-separated relative
// path.
func walkRelativeFiles(tb testing.TB, root string) map[string]struct{} {
	tb.Helper()

	out := make(map[string]struct{})
	require.NoError(tb, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = struct{}{}
		return nil
	}))
	return out
}

// filesUnderPrefix counts the record entries under a path prefix.
func filesUnderPrefix(tb testing.TB, listed map[string]string, prefix string) []string {
	tb.Helper()

	var out []string
	for rel := range listed {
		if strings.HasPrefix(rel, prefix) {
			out = append(out, rel)
		}
	}
	slices.Sort(out)
	return out
}

// mapsWithout returns the keys of a set with one key removed.
func mapsWithout(set map[string]struct{}, drop string) map[string]struct{} {
	out := make(map[string]struct{}, len(set))
	for key := range set {
		if key != drop {
			out[key] = struct{}{}
		}
	}
	return out
}

// decodeJSONObject reads one staged JSON object.
func decodeJSONObject(tb testing.TB, path string) map[string]any {
	tb.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(tb, err)
	var out map[string]any
	require.NoError(tb, json.Unmarshal(raw, &out))
	return out
}

// writeJSONFile rewrites one staged JSON object the way the packager writes it.
func writeJSONFile(tb testing.TB, path string, value any) {
	tb.Helper()

	raw, err := json.MarshalIndent(value, "", "  ")
	require.NoError(tb, err)
	require.NoError(tb, os.WriteFile(path, append(raw, '\n'), 0o644))
}

// readFileText reads one file as text.
func readFileText(tb testing.TB, path string) string {
	tb.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(tb, err)
	return string(raw)
}

// fileSHA256 digests one file.
func fileSHA256(tb testing.TB, path string) string {
	tb.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(tb, err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// repoRoot is the plugin repository root, derived from this file's location.
func repoRoot(tb testing.TB) string {
	tb.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(tb, ok)
	return filepath.Dir(thisFile)
}
