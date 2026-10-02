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
	"regexp"
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

		// The record may not read as a publication: release.yaml declares the tag a
		// future release would carry, and there is no such tag.
		require.Equal(t, "cursorsdk-v0.1.0", record["release_tag_declared"])
		require.NotContains(t, record, "release_tag")

		// The recorded runtime provenance has to name the source the packager
		// actually used. Both scripts fall back to the node on PATH when they are
		// given neither a distribution nor a runtime, and a gate run supplies
		// neither, so an archive assembled here records a PATH fallback. Recording
		// that as a copy of an official distribution would be a false statement
		// about the artifact.
		require.Equal(t, "nodejs-"+built.nodeSourceKind+":"+built.nodeSource, record["private_runtime_source"])
		require.NotContains(t, record["private_runtime_source"], "official-distribution")
	})

	t.Run("third_party_notices_state_the_runtime_source_honestly", func(t *testing.T) {
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		notices := readFileText(t, filepath.Join(built.installRoot,
			filepath.FromSlash(archive.LicensesDir()), "THIRD-PARTY-NOTICES.md"))
		// The notice text is hard-wrapped, so the statements it makes are compared as
		// one sequence of words.
		words := strings.Join(strings.Fields(notices), " ")

		// A runtime staged from the build machine's toolchain has to say so in the
		// archive's own notices, not only in compatibility.json. The remedy is named
		// in the packager's own option spelling, so the check is spelling-agnostic.
		require.Contains(t, words, "source kind: "+built.nodeSourceKind)
		if built.nodeSourceKind != "official-distribution" {
			require.Contains(t, words, "not from an official Node distribution")
			remedy := strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(words))
			require.Contains(t, remedy, "nodedist",
				"the notice has to name the option that re-stages an official distribution")
		}
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

	t.Run("checksum_record_is_ordered_ordinally_by_path", func(t *testing.T) {
		// Both packagers have to write the record in the same order for the same
		// tree, or a checksum record is not a reproducible artifact: the shell
		// packager sorts with LC_ALL=C and the PowerShell one has to agree with it
		// byte for byte. A culture-aware sort puts bin/ before LICENSES/ while an
		// ordinal one puts LICENSES/ first, because 'b' < 'L' is false in code
		// points and true in a case-insensitive collation.
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)

		paths := make([]string, 0, built.fileCount)
		for _, line := range strings.Split(readFileText(t,
			filepath.Join(built.installRoot, filepath.FromSlash(archive.ChecksumsPath()))), "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			digest, rel, ok := strings.Cut(line, packagelayout.ChecksumSeparator)
			require.True(t, ok, "malformed checksum line %q", line)
			require.Len(t, digest, 64)
			paths = append(paths, rel)
		}
		require.Len(t, paths, built.fileCount-1)
		require.True(t, slices.IsSortedFunc(paths, strings.Compare),
			"checksums.sha256 is not sorted by ordinal path comparison:\n%s", strings.Join(paths, "\n"))
	})

	t.Run("staged_private_runtime_is_startable_as_a_direct_process", func(t *testing.T) {
		// The launcher starts the staged runtime by absolute path, without a shell.
		// A runtime staged without its execute bit is an archive that cannot serve a
		// request, so the packager has to grant the bit on the platform that has one.
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		staged := filepath.Join(built.installRoot, filepath.FromSlash(archive.PrivateRuntimePath()))

		info, err := os.Stat(staged)
		require.NoError(t, err)
		if runtime.GOOS != "windows" {
			require.NotZero(t, info.Mode().Perm()&0o111,
				"the staged private runtime is not executable: %v", info.Mode().Perm())
		}

		out, runErr := runPackagedExecutable(t, staged, "--version")
		require.NoError(t, runErr, "the staged private runtime does not run: %s", out)
		require.Equal(t, built.nodeVersion, strings.TrimSpace(out))
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
		// Every verifier implementation this machine can run has to reach the same
		// verdict: an install root any local user can write is not a protected root.
		for _, impl := range verifierImplementations(t) {
			t.Run(impl, func(t *testing.T) {
				root := copyInstallRoot(t, built.installRoot)
				report, errCode := runVerifyScriptImpl(t, impl, root, nil, nil)
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
				report, errCode = runVerifyScriptImpl(t, impl, root, nil, nil)
				require.NotEqual(t, 0, errCode, "a world-writable plugin root passed the %s verifier:\n%s", impl, report)
				require.Contains(t, report, "writable beyond its owner")
				require.Contains(t, report, "protected plugin root")
			})
		}
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

	t.Run("recorded_release_digests_are_cross_checked", func(t *testing.T) {
		// compatibility.json records the digest of the manifest and of the private
		// runtime, both taken from the staged tree. A recorded digest is only worth
		// reading if something compares it, so each of them has to fail verification
		// when it disagrees with the file it names.
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		for _, tc := range []struct{ field, rel string }{
			{field: "manifest_sha256", rel: archive.ManifestPath()},
			{field: "private_runtime_sha256", rel: archive.PrivateRuntimePath()},
		} {
			t.Run(tc.field, func(t *testing.T) {
				root := copyInstallRoot(t, built.installRoot)
				recordPath := filepath.Join(root, filepath.FromSlash(archive.CompatibilityPath()))
				record := decodeJSONObject(t, recordPath)
				record[tc.field] = strings.Repeat("0", 64)
				writeJSONFile(t, recordPath, record)
				// The record is itself checksummed, so its record entry is recomputed:
				// the case has to isolate the recorded-digest check instead of failing
				// earlier on the checksum.
				rewriteChecksum(t, root, archive.CompatibilityPath())

				report, errCode := runVerifyScript(t, root, nil, nil)
				require.NotEqual(t, 0, errCode,
					"a recorded digest that matches nothing passed verification:\n%s", report)
				require.Contains(t, report, "records "+tc.field,
					"the finding has to name the recorded digest that disagreed:\n%s", report)
				require.Contains(t, report, tc.rel,
					"the finding has to name the file it disagrees with:\n%s", report)
			})
		}
	})

	t.Run("a_caller_chosen_layout_contract_does_not_reach_a_real_archive", func(t *testing.T) {
		// The always-on half of this is
		// TestPackageArchive_VerifierReadsTheLayoutContractFromTheVerifiedRepository.
		// This is the same attack against an assembled archive rather than a synthetic
		// tree: a payload appended to the plugin-private build output, the shipped
		// checksum record removed, and a working directory carrying a contract that
		// describes exactly what is left.
		archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
		require.NoError(t, err)
		for _, impl := range verifierImplementations(t) {
			t.Run(impl, func(t *testing.T) {
				root := copyInstallRoot(t, built.installRoot)
				payload := filepath.Join(root, filepath.FromSlash(archive.BridgeDistPath()), "main.js")
				require.NoError(t, os.WriteFile(payload,
					[]byte(readFileText(t, payload)+"\n// appended payload\n"), 0o644))
				require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(archive.ChecksumsPath()))))

				decoy := foreignLayoutDir(t, callerLayoutContract(t, root, archive, true))
				report, errCode := runVerifyScriptImplFrom(t, impl, "", decoy, root, nil, nil)
				require.NotEqual(t, 0, errCode,
					"the %s verifier accepted a tampered archive from a directory carrying its own layout contract:\n%s",
					impl, report)
				require.NotContains(t, report, "verify-package: ok")
				require.Contains(t, report, "checksum record is missing: "+archive.ChecksumsPath(),
					"the verifier has to decide against the shipped contract:\n%s", report)
			})
		}
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
//
// The staging directories count as layout rows as much as the file names do: a script
// that reads private/bridge/bin/lip-cursor-sdk-bridge.js from the report and then
// re-derives the source path by hand has restated the layout just as much as one that
// spelled out the archive name.
//
// What this guard enforces is exactly this literal set, on these four scripts. It is not
// a proof that no script could restate anything: it cannot see a name the contract does
// not carry, and it says nothing about a script it does not read. A layout row added to
// the list below has to be spelled there by hand.
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
			// The rows a packager would have to spell out to find the bridge entry
			// and the staged dependency tree in the source tree.
			packagelayout.BridgeEntrySourceRel(),
			packagelayout.ModulesDirName,
		} {
			require.NotContains(t, body, literal, "%s restates the archive layout", script)
		}
		require.Contains(t, body, "lip-cursor-sdk-packaging", "%s must read the layout contract", script)

		// The built JavaScript directory gets a path-element check rather than the
		// bare-word one above. "dist" is an ordinary word in these scripts: it is the
		// --node-dist option and the -NodeDist parameter, it appears throughout the
		// distribution provenance wording, and it is this repository's own default
		// output directory, so forbidding it outright would forbid all three. What a
		// packager that restated this row would have to write is a path element, and
		// the word boundary keeps the derived variables ($bridge_dist, bridge_dist/)
		// from reading as a restatement of it.
		require.NotRegexp(t, distPathElement, body,
			"%s restates the built-JavaScript staging directory", script)
	}

	// Both packagers have to read the entry, the built JavaScript, and the dependency
	// tree out of the report, and both have to derive the source-tree path of each from
	// the bridge package directory rather than from a name of their own. The verifiers
	// do not stage them and are not expected to.
	for _, script := range []string{
		filepath.Join("scripts", "package-plugin.sh"),
		filepath.Join("scripts", "package-plugin.ps1"),
	} {
		body := readFileText(t, filepath.Join(repo, script))
		for _, field := range []string{"bridge_entry", "bridge_dist", "bridge_modules", "bridge_package_dir"} {
			require.Contains(t, body, field, "%s must derive its staging paths from the layout report", script)
		}
	}
}

// TestPackageArchive_VerifierReadsTheLayoutContractFromTheVerifiedRepository keeps the
// archive layout contract out of the caller's hands.
//
// One report decides what a verified tree is: the required-entry set, the
// checksum-record path and separator, the manifest digest authority, the plugin-private
// prefix count, and the runtime and launcher probes all come out of it. A verifier that
// resolves that report through its own working directory lets whoever chose the working
// directory decide the same, and a tree with a payload appended to a plugin-private
// file and the shipped checksum record removed then verifies clean.
//
// So the contract has to come from the repository being verified, and every
// implementation this machine can run has to agree. The decoy below is that: a
// directory holding a cmd/lip-cursor-sdk-packaging that reports a contract describing
// exactly the tampered tree, so a verifier that adopts it accepts the tree and one that
// does not rejects it.
func TestPackageArchive_VerifierReadsTheLayoutContractFromTheVerifiedRepository(t *testing.T) {
	t.Parallel()

	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)

	for _, impl := range verifierImplementations(t) {
		t.Run(impl, func(t *testing.T) {
			root := callerTamperedTree(t, archive)
			decoy := foreignLayoutDir(t, callerLayoutContract(t, root, archive, false))

			report, errCode := runVerifyScriptImplFrom(t, impl, "", decoy, root, nil, nil)
			require.NotEqual(t, 0, errCode,
				"the %s verifier accepted a tampered tree while running from a directory that carried its own layout contract:\n%s",
				impl, report)
			require.NotContains(t, report, "verify-package: ok")
			// The contract that has to be read is the shipped one, and it is the
			// shipped one that names the checksum record this tree no longer carries.
			require.Contains(t, report, "checksum record is missing: "+archive.ChecksumsPath(),
				"the verifier has to decide against the layout contract of the repository it verifies:\n%s", report)
		})
	}
}

// callerTamperedTree is the tree the defect let through: a payload appended to the
// plugin-private build output, with the shipped checksum record removed so that nothing
// in the tree has to account for the payload.
func callerTamperedTree(tb testing.TB, archive packagelayout.Archive) string {
	tb.Helper()

	root := filepath.Join(tb.TempDir(), "caller install root with spaces")
	staged := filepath.Join(root, filepath.FromSlash(archive.BridgeDistPath()))
	require.NoError(tb, os.MkdirAll(staged, 0o755))
	require.NoError(tb, os.WriteFile(filepath.Join(staged, "main.js"),
		[]byte("// staged bridge build output\nexport const run = () => 'ok'\n// appended payload\n"), 0o644))
	// The license directory has to exist: the report counts the notices in it on every
	// run, including the run that fails, and the shipped contract requires the directory.
	notices := filepath.Join(root, filepath.FromSlash(archive.LicensesDir()))
	require.NoError(tb, os.MkdirAll(notices, 0o755))
	require.NoError(tb, os.WriteFile(filepath.Join(notices, "THIRD-PARTY-NOTICES.md"),
		[]byte("This file records what the plugin archive redistributes.\n"), 0o644))
	return root
}

// callerLayoutContract writes the caller's own checksum record into a tree and returns
// the layout report that describes exactly that tree: every entry it names is present,
// and every file in it is checksummed.
//
// keepReleaseRecord decides whether the contract names the release metadata the tree
// really carries or a file it does not. A synthetic tree names a file it does not carry,
// because the release-metadata block is also where the verifier probes the private
// runtime and the launcher, and a synthetic tree has no runtime to probe; an assembled
// archive keeps its real record, which is the fidelity the package lane needs.
func callerLayoutContract(tb testing.TB, root string, archive packagelayout.Archive, keepReleaseRecord bool) map[string]any {
	tb.Helper()

	const callerRecord = "private/checksums.sha256"
	recordPath := filepath.Join(root, filepath.FromSlash(callerRecord))
	require.NoError(tb, os.MkdirAll(filepath.Dir(recordPath), 0o755))

	var lines []string
	entries := []string{callerRecord}
	for rel := range walkRelativeFiles(tb, root) {
		if rel == callerRecord {
			continue
		}
		entries = append(entries, rel)
		lines = append(lines, fileSHA256(tb, filepath.Join(root, filepath.FromSlash(rel)))+
			packagelayout.ChecksumSeparator+rel)
	}
	slices.Sort(lines)
	require.NoError(tb, os.WriteFile(recordPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	slices.Sort(entries)

	compatibility := "caller-compatibility.json"
	if keepReleaseRecord {
		compatibility = archive.CompatibilityPath()
	}
	return map[string]any{
		"platform":           archive.Platform(),
		"compatibility":      compatibility,
		"manifest":           "caller-manifest.json",
		"checksums":          callerRecord,
		"checksum_separator": packagelayout.ChecksumSeparator,
		"private_prefix":     packagelayout.PrivatePrefix,
		"licenses_dir":       archive.LicensesDir(),
		"outer_executable":   archive.OuterExecutablePath(),
		"launcher":           archive.LauncherPath(),
		"private_runtime":    archive.PrivateRuntimePath(),
		"required_entries":   entries,
	}
}

// foreignLayoutDir writes a directory outside this repository that answers the layout
// report a verifier asks for: a cmd/lip-cursor-sdk-packaging that prints the contract the
// caller chose and ignores the platform it is asked about. A verifier that resolves the
// layout from its working directory adopts that answer.
func foreignLayoutDir(tb testing.TB, layout map[string]any) string {
	tb.Helper()

	report, err := json.Marshal(layout)
	require.NoError(tb, err)
	dir := tb.TempDir()

	// A standalone module with no dependencies, so `go run` answers for this directory
	// alone. Its go directive is the repository's own, shortened to major.minor: a lower
	// directive than the toolchain in use can never ask for a download.
	require.NoError(tb, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module caller/layout\n\ngo "+repoGoMinorVersion(tb)+"\n"), 0o644))
	source := "// Command lip-cursor-sdk-packaging is a decoy layout report. It answers with\n" +
		"// a contract the caller chose, so a verifier that asks its working directory\n" +
		"// instead of the repository it verifies adopts the caller's answer.\n" +
		"package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n" +
		"func main() {\n\tif _, err := fmt.Print(" + strconv.Quote(string(report)) +
		"); err != nil {\n\t\tfmt.Fprintln(os.Stderr, err)\n\t\tos.Exit(1)\n\t}\n}\n"
	command := filepath.Join(dir, "cmd", "lip-cursor-sdk-packaging")
	require.NoError(tb, os.MkdirAll(command, 0o755))
	require.NoError(tb, os.WriteFile(filepath.Join(command, "main.go"), []byte(source), 0o644))
	return dir
}

// repoGoMinorVersion is this repository's go directive as major.minor.
func repoGoMinorVersion(tb testing.TB) string {
	tb.Helper()

	for _, line := range strings.Split(readFileText(tb, filepath.Join(repoRoot(tb), "go.mod")), "\n") {
		version, ok := strings.CutPrefix(strings.TrimSpace(line), "go ")
		if !ok {
			continue
		}
		if major, minor, split := strings.Cut(version, "."); split {
			return major + "." + minor
		}
		return version
	}
	tb.Fatalf("go.mod declares no go directive")
	return ""
}

// TestPackageArchive_BothVerifiersCrossCheckTheRecordedReleaseDigests keeps one trust
// check from existing in only one of the two verifiers.
//
// compatibility.json records the digest of the manifest and of the private runtime, and
// a recorded digest that nothing compares is provenance decoration. The package lane
// proves the behaviour on whichever platform it runs, which means the other platform's
// script is only covered there; this static half holds everywhere, so the POSIX script
// cannot silently lose a check the Windows one keeps.
func TestPackageArchive_BothVerifiersCrossCheckTheRecordedReleaseDigests(t *testing.T) {
	t.Parallel()

	for _, script := range []string{
		filepath.Join("scripts", "verify-package.sh"),
		filepath.Join("scripts", "verify-package.ps1"),
	} {
		body := readFileText(t, filepath.Join(repoRoot(t), script))
		for _, field := range []string{"manifest_sha256", "private_runtime_sha256"} {
			require.Contains(t, body, field, "%s does not cross-check the recorded %s", script, field)
		}
		// Both implementations have to say the same thing about a disagreement, so an
		// operator reading either report is reading the same verdict.
		require.Contains(t, body, "the record does not describe this archive",
			"%s has to report a recorded-digest disagreement the way its counterpart does", script)
	}
}

// distPathElement is the layout's built-JavaScript directory name used as a path
// element: the one shape a script that restates that row has to write.
var distPathElement = regexp.MustCompile(`\b` + regexp.QuoteMeta(packagelayout.BridgeDistDirName) + `[/\\]`)

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
	installRoot    string
	archive        string
	archiveSHA256  string
	platform       string
	pluginVersion  string
	buildID        string
	nodeVersion    string
	nodeSource     string
	nodeSourceKind string
	fileCount      int
	outDir         string
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
			outDir:         outDir,
			installRoot:    fields["install_root"],
			archive:        fields["archive"],
			archiveSHA256:  fields["archive_sha256"],
			platform:       fields["platform"],
			pluginVersion:  fields["plugin_version"],
			buildID:        fields["build_id"],
			nodeVersion:    fields["node_version"],
			nodeSource:     fields["node_source"],
			nodeSourceKind: fields["node_source_kind"],
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
	// The packager has to say which of its three runtime sources it used, because the
	// recorded provenance and the staged notices are both derived from that answer.
	require.NotEmpty(tb, packageResult.nodeSourceKind)
	require.NotEmpty(tb, packageResult.nodeSource)
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
// is how the no-global-Node case runs, using this platform's own verifier.
func runVerifyScript(tb testing.TB, installRoot string, env, extra []string) (string, int) {
	tb.Helper()

	return runVerifyScriptImpl(tb, nativeScriptImpl(), installRoot, env, extra)
}

// runVerifyScriptImpl verifies one install root with one named implementation, so
// the shell verifier and the PowerShell verifier are each exercised instead of only
// the one this platform happens to use.
func runVerifyScriptImpl(tb testing.TB, impl, installRoot string, env, extra []string) (string, int) {
	tb.Helper()

	return runVerifyScriptImplFrom(tb, impl, "", "", installRoot, env, extra)
}

// runVerifyScriptImplFrom verifies one install root with one named implementation
// from a chosen working directory, and against a chosen copy of the script.
//
// workDir is the directory the script starts in. It is a parameter because a
// verifier that asks its working directory for the layout contract answers for
// whatever repository the caller happened to leave there instead of for the archive
// it was asked to verify. scriptDir is the directory holding scripts/, so a case can
// hand the verifier a substituted helper without touching the shipped one; the empty
// string means this repository.
func runVerifyScriptImplFrom(tb testing.TB, impl, scriptDir, workDir, installRoot string, env, extra []string) (string, int) {
	tb.Helper()

	if scriptDir == "" {
		scriptDir = repoRoot(tb)
	}
	if workDir == "" {
		workDir = repoRoot(tb)
	}
	script, shell, shellArgs := packagingScriptArgs(tb, scriptDir, "verify-package", impl)
	argv := append(shellArgs, append([]string{script},
		scriptArgsFor(impl, map[string]string{"package-root": installRoot}, extra)...)...)
	return runScriptProcess(tb, shell, argv, workDir, env)
}

// verifierImplementations lists the verify-package implementations this machine can
// actually run, native first. Each implementation is a trust check in its own right,
// so a verdict only counts as proven when every implementation that can run here
// reaches it.
//
// The foreign implementation is only added on a POSIX host, where PowerShell can run
// the same POSIX tree: a Windows host would have to hand the shell script a Windows
// path, which is not a check of anything. The POSIX branch of the PowerShell
// ownership check is therefore covered by the ubuntu packaging lane and, on every
// platform, by the direct predicate test in TestPackageArchive_PowerShellOwnership.
func verifierImplementations(tb testing.TB) []string {
	tb.Helper()

	out := []string{nativeScriptImpl()}
	if runtime.GOOS == "windows" {
		return out
	}
	if _, err := exec.LookPath("pwsh"); err == nil {
		return append(out, "ps1")
	}
	return out
}

// nativeScriptImpl is the packaging-script implementation this platform uses.
func nativeScriptImpl() string {
	if runtime.GOOS == "windows" {
		return "ps1"
	}
	return "sh"
}

// scriptArgs renders options for one implementation's packaging script. The canonical
// spelling is the POSIX long option; the PowerShell script takes the same options
// in its own parameter spelling, so the harness is what knows the difference.
func scriptArgs(options map[string]string, extra []string) []string {
	return scriptArgsFor(nativeScriptImpl(), options, extra)
}

func scriptArgsFor(impl string, options map[string]string, extra []string) []string {
	args := make([]string, 0, 2*len(options)+len(extra))
	for _, key := range slices.Sorted(maps.Keys(options)) {
		args = append(args, scriptOption(impl, key), options[key])
	}
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		if name, ok := strings.CutPrefix(arg, "--"); ok {
			arg = scriptOption(impl, name)
		}
		args = append(args, arg)
	}
	return args
}

// scriptOption renders one canonical option name for one implementation's script.
func scriptOption(impl, name string) string {
	if impl != "ps1" {
		return "--" + name
	}
	spelled := map[string]string{
		"out-dir":         "OutDir",
		"package-root":    "PackageRoot",
		"platform":        "Platform",
		"report":          "ReportPath",
		"expect-platform": "ExpectPlatform",
		"repo-root":       "RepoRoot",
	}[name]
	if spelled == "" {
		return "-" + name
	}
	return "-" + spelled
}

// runPackagingScript runs one packaging script for this platform.
func runPackagingScript(tb testing.TB, name string, args []string) (string, int) {
	tb.Helper()

	return runPackagingScriptEnvImpl(tb, name, nativeScriptImpl(), args, nil)
}

func runPackagingScriptEnvImpl(tb testing.TB, name, impl string, args, env []string) (string, int) {
	tb.Helper()

	script, shell, shellArgs := packagingScriptArgs(tb, repoRoot(tb), name, impl)
	return runScriptProcess(tb, shell, append(shellArgs, append([]string{script}, args...)...), repoRoot(tb), env)
}

// runScriptProcess runs one script interpreter with its arguments and returns the
// combined output and the exit status. workDir is the directory the process starts
// in, which is what a verifier that resolves its layout contract relative to the
// working directory answers for instead of for the archive it was asked to verify.
func runScriptProcess(tb testing.TB, shell string, argv []string, workDir string, env []string) (string, int) {
	tb.Helper()

	cmd := exec.Command(shell, argv...)
	cmd.Dir = workDir
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
			tb.Fatalf("run %s: %v", shell, err)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// packagingScriptArgs picks the script and the interpreter that runs it. The
// PowerShell script is the Windows implementation and the shell script is the
// POSIX one; each platform validates its own. dir is the directory holding scripts/,
// so a case can run a substituted copy of a script instead of the shipped one.
func packagingScriptArgs(tb testing.TB, dir, name, impl string) (script string, shell string, shellArgs []string) {
	tb.Helper()

	if impl == "ps1" {
		pwsh, err := exec.LookPath("pwsh")
		require.NoError(tb, err)
		return filepath.Join(dir, "scripts", name+".ps1"), pwsh, []string{"-NoProfile", "-NonInteractive", "-File"}
	}
	bash, err := exec.LookPath("bash")
	require.NoError(tb, err)
	return filepath.Join(dir, "scripts", name+".sh"), bash, nil
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

// nodeFreeEnvironment is the process environment in which no runnable node is
// reachable through PATH, which is what the private-runtime cases need: the packaged
// runtime has to be the one that serves the request, and a verification that quietly
// fell back to a developer Node would prove nothing.
//
// On Windows the node directories are dropped, because the Go toolchain and the
// PowerShell host live in their own node-free directories. On POSIX the system
// directories hold both the POSIX tools the scripts need and the distribution's node,
// so they stay and a shim that fails on purpose shadows the runtime name: PATH
// resolution stops at the first directory holding something runnable, so the shim has
// to be the first such directory, not merely a directory named after the runtime.
func nodeFreeEnvironment(tb testing.TB) []string {
	tb.Helper()

	poison := writeNodeShims(tb)
	// Windows keeps the toolchain in node-free directories, so the node directories
	// are dropped. On POSIX they are the system directories the scripts themselves
	// need, so the failing shim does the work instead.
	dropNodeDirs := runtime.GOOS == "windows"

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
		env = append(env, key+"="+scrubbedPath(value, poison, dropNodeDirs))
	}

	// The assertion the harness owes its readers: the first runnable node on this
	// PATH is the harness's own failing shim, or there is none at all.
	for _, path := range pathValues(tb, env) {
		reached := firstRunnableNodeDir(tb, path)
		if reached == "" {
			continue
		}
		require.Equal(tb, poison, reached,
			"a working node is reachable on the scrubbed PATH:\n%s", path)
	}
	return append(env, "GOWORK=off")
}

// firstRunnableNodeDir resolves the runtime name through one PATH value the way a
// process start does: the first directory holding a runnable node wins, and every
// later directory is only reached when the earlier ones hold nothing runnable.
func firstRunnableNodeDir(tb testing.TB, path string) string {
	tb.Helper()

	for _, dir := range filepath.SplitList(path) {
		if hasNodeExecutable(filepath.Clean(dir)) {
			return filepath.Clean(dir)
		}
	}
	return ""
}

// nodeRuntimeNames are the spellings a process start may pick up as the runtime,
// ordered the way Windows resolves them within one directory.
var nodeRuntimeNames = []string{"node", "node.exe", "node.cmd", "node.ps1", "node.bat"}

// hasNodeExecutable reports whether a directory holds a runnable node or node shim.
func hasNodeExecutable(dir string) bool {
	_, ok := runnableNode(dir)
	return ok
}

// runnableNode returns the runtime a process start would pick up in one directory.
func runnableNode(dir string) (string, bool) {
	for _, name := range nodeRuntimeNames {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
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
