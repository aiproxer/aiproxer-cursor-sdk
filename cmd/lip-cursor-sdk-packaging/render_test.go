package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"

	"github.com/stretchr/testify/require"
)

// stagedRelease is a minimal staged install root the renderer can read: the
// outer executable, the plugin-private launcher, the private runtime with its
// bundled npm, the bridge entry, the bridge manifest and lockfile, and the
// release template. The operator-provisioned dependency tree is deliberately
// absent: the renderer refuses to describe a tree that carries one.
type stagedRelease struct {
	root           string
	release        string
	template       string
	exe            string
	runtime        string
	entry          string
	npmCLI         string
	bridgeJSON     string
	bridgeLock     string
	buildID        string
	platform       string
	exeSHA256      string
	nodeSource     string
	nodeSourceKind string
	nodeVersion    string
}

func TestRender_PreservesManifestIdentityAndNativePlatformClaim(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)

	manifest, compatibility := renderAndRead(t, lay)

	require.Equal(t, "golip.backendplugin.manifest/v1", manifest["schema"])
	require.Equal(t, "io.golip.backend.cursorsdk", manifest["plugin_id"])
	require.Equal(t, "0.1.0", manifest["version"])
	require.Equal(t, lay.buildID, manifest["build_id"])
	require.Equal(t, lay.exeSHA256, manifest["sha256"])
	require.Equal(t, "bin/lip-backend-cursorsdk"+lay.suffix(), manifest["executable"])

	// The exported posture is the plugin's declared trust boundary: static
	// credentials, local-only access, per-instance processes, agent runtime.
	// Rendering must carry it through unchanged rather than restating it.
	exports := decodeList(t, manifest["exports"])
	require.Len(t, exports, 1)
	require.Equal(t, map[string]any{
		"kind":            "cursorsdk",
		"credential_mode": "static",
		"access_scope":    "local_only",
		"process_sharing": "per_instance",
		"execution_class": "agent_runtime",
	}, exports[0])

	// A natively assembled archive may claim only the platform it was assembled
	// and validated on. The template's cross-platform claims must not survive
	// into a single-platform artifact.
	plats := decodeList(t, manifest["platforms"])
	require.Equal(t, []any{map[string]any{"os": lay.goos(), "arch": lay.goarch()}}, plats)

	// Release metadata is plugin-release metadata, not host manifest content: the
	// host manifest stays closed and keeps no packaging fields.
	for _, key := range []string{"private", "runtime", "checksums", "compatibility", "licenses", "platform", "goos"} {
		require.NotContains(t, manifest, key)
	}

	body := mustJSON(t, manifest)
	require.NotContains(t, body, "REPLACE_")

	// The compatibility record reports the same identity plus the facts a
	// maintainer or operator needs to audit the private runtime.
	require.Equal(t, "golip.cursorsdk.compatibility/v1", compatibility["schema"])
	require.Equal(t, "io.golip.backend.cursorsdk", compatibility["plugin_id"])
	require.Equal(t, "0.1.0", compatibility["plugin_version"])
	require.Equal(t, lay.buildID, compatibility["build_id"])
	require.Equal(t, "cursorsdk-v0.1.0", compatibility["release_tag_declared"])
	require.Equal(t, "github.com/matdev83/go-llm-interactive-proxy", compatibility["published_root_module"])
	require.Equal(t, lay.platform, compatibility["platform"])
	require.Equal(t, "private-runtime", compatibility["packaging_variant"])
	require.Equal(t, false, compatibility["external_node_required"])
	require.Equal(t, float64(1), compatibility["protocol_major"])
	require.Equal(t, float64(0), compatibility["protocol_min_minor"])
	require.Equal(t, float64(0), compatibility["protocol_max_minor"])
	require.Equal(t, lay.exeSHA256, compatibility["outer_executable_sha256"])
	require.Equal(t, "bin/lip-backend-cursorsdk"+lay.suffix(), compatibility["outer_executable"])
}

// TestRender_RecordsPrivateRuntimeAndSDKMetadataFromTheStagedTree keeps the
// recorded runtime facts derived from the staged archive itself: the SDK version
// has to resolve from the staged production tree, and the private runtime version
// has to come from the shipped executable rather than from a caller supplied
// string.
//
// The recorded provenance has to name where the runtime actually came from. The
// packager falls back to the node on PATH when it is given neither a distribution
// archive nor a runtime, and a record that prefixed every source with
// "nodejs-official-distribution" would state, about a copy of a build machine's
// working installation, that it is a copy of an official distribution.
func TestRender_RecordsPrivateRuntimeAndSDKMetadataFromTheStagedTree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind  string
		label string
		wants string
	}{
		{kind: "official-distribution", label: "node-v22.22.3-dist", wants: "nodejs-official-distribution:node-v22.22.3-dist"},
		{kind: "supplied-runtime", label: "node", wants: "nodejs-supplied-runtime:node"},
		{kind: "path-fallback", label: "node.exe", wants: "nodejs-path-fallback:node.exe"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			lay.nodeSourceKind = tc.kind
			lay.nodeSource = tc.label
			_, compatibility := renderAndRead(t, lay)

			require.Equal(t, "1.0.23", compatibility["cursor_sdk_required_version"])
			require.Equal(t, false, compatibility["cursor_sdk_bundled"])
			require.Equal(t, "0.1.0", compatibility["bridge_version"])
			require.Equal(t, ">=22.13", compatibility["bridge_node_engine"])
			require.Equal(t, lay.nodeVersion, compatibility["private_runtime_version"])
			require.Equal(t, tc.wants, compatibility["private_runtime_source"])
			require.NotEmpty(t, compatibility["private_runtime_sha256"])
			require.Equal(t, lay.platform, compatibility["native_platform_assembled"])
			require.Equal(t, "", compatibility["tested_host_artifact_sha256"])
			require.NotEmpty(t, compatibility["licensing_status"])
		})
	}
}

// TestRender_RefusesAnUnnamedRuntimeSourceKind keeps the recorded provenance from
// drifting back into a single prefix for every source. An unknown or missing kind is
// a packaging failure: the alternative is a runtime whose provenance the record
// cannot state honestly.
func TestRender_RefusesAnUnnamedRuntimeSourceKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", "official distribution", "path", "nodejs-official-distribution"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			lay.nodeSourceKind = kind

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), "node-source-kind")
			require.Contains(t, err.Error(), "path-fallback")
		})
	}
}

// TestRender_DoesNotClaimAReleaseExists keeps the record from reading as a
// publication. release.yaml declares the tag a future publication would carry, and
// there is no such tag: a field named release_tag would be read as one that exists.
func TestRender_DoesNotClaimAReleaseExists(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	require.Equal(t, "cursorsdk-v0.1.0", compatibility["release_tag_declared"])
	require.NotContains(t, compatibility, "release_tag")
}

// TestRender_RecordsTheCursorSDKAsRequiredAndNotBundled keeps the non-redistribution
// decision legible in the record every archive carries.
//
// The SDK is proprietary and no redistribution right for it or for the native binaries
// its platform package bundles has been verified, so the archive ships the manifest and
// the lockfile that pin the SDK and neither the SDK nor its dependency closure. A record
// that only named a version would read as "this is what the archive ships"; the record
// has to say the SDK is required at run time, not bundled, give the one command that
// provisions it, and state the non-redistribution position explicitly.
func TestRender_RecordsTheCursorSDKAsRequiredAndNotBundled(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	archive, err := packagelayout.ForPlatform(lay.goos(), lay.goarch())
	require.NoError(t, err)

	require.Equal(t, "1.0.23", compatibility["cursor_sdk_required_version"],
		"the required version is the pin the bridge verifies at run time")
	require.Equal(t, false, compatibility["cursor_sdk_bundled"],
		"the archive ships no Cursor SDK and none of its dependency closure")
	require.Equal(t, archive.ProvisionCommand(""), compatibility["cursor_sdk_provisioning_command"],
		"the record has to carry the exact provisioning command an operator runs")
	require.NotContains(t, compatibility, "cursor_sdk_version",
		"a recorded installed version would read as a claim about what the archive ships")

	redistribution := stringField(compatibility, "cursor_sdk_redistribution")
	require.Contains(t, redistribution, "not redistributed")
	require.Contains(t, redistribution, "operator")

	// The licensing statement has to agree: it may not keep claiming the SDK's
	// redistribution rights are unresolved inside an archive that ships no SDK.
	licensing := stringField(compatibility, "licensing_status")
	require.NotContains(t, licensing, "confirm redistribution rights")
	require.Contains(t, licensing, "not redistributed")

	// The record also may not hand the shipped npm tree the Node.js MIT grant. npm
	// licenses its own application under the Artistic License 2.0 in a LICENSE inside
	// the staged npm tree and states that the packages it bundles are licensed on their
	// respective terms, so a record calling that tree MIT is false of the artifact and
	// would leave an auditor with the wrong license for most of an archive.
	require.NotContains(t, licensing,
		"including its bundled npm and the third-party dependencies npm bundles with it, is MIT",
		"the shipped npm tree is not covered by the Node.js MIT grant")
	require.NotContains(t, licensing, "whose notices cover those bundled components",
		"the Node distribution license does not carry the bundled packages' own license texts")
	require.Contains(t, licensing, "Artistic-2.0",
		"the record has to name the license npm's own staged text declares")
	require.Contains(t, licensing, "its own license text in its own package directory",
		"each package npm bundles is attributed to the license text it ships itself")
	require.Contains(t, licensing, "THIRD-PARTY-NOTICES.md",
		"the record has to point at the staged notice that names any bundled package shipping none")

	// The record describes an archive that provisions itself, so the staged tree has
	// to carry the two files that make the recorded command runnable.
	require.FileExists(t, filepath.Join(lay.root, filepath.FromSlash(archive.BridgePackageLockPath())))
	require.FileExists(t, filepath.Join(lay.root, filepath.FromSlash(archive.PrivateRuntimeNPMCLIPath())))
}

// TestRender_RefusesAStagedOperatorProvisionedTree keeps a staged dependency tree an
// explicit packaging failure.
//
// The archive must not ship the Cursor SDK's dependency closure: @cursor/sdk is
// proprietary and its platform package bundles native binaries whose license texts it
// does not redistribute, so staging the closure would assert a redistribution right
// nobody has verified. A renderer that described such a tree anyway would produce
// release metadata that reads as a redistributable bundle, which is the exact thing the
// archive must not be.
func TestRender_RefusesAStagedOperatorProvisionedTree(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	archive, err := packagelayout.ForPlatform(lay.goos(), lay.goarch())
	require.NoError(t, err)

	staged := filepath.Join(lay.root, filepath.FromSlash(archive.ProvisionedSDKPackageJSONPath()))
	writeFile(t, staged, `{"name":"@cursor/sdk","version":"1.0.23"}`)

	err = runRenderCmd(t, lay)
	require.Error(t, err)
	require.Contains(t, err.Error(), archive.BridgeModulesPath())
	require.Contains(t, err.Error(), "not redistributed")
}

// TestRender_RefusesStagedTreeWithoutTheProvisioningPrerequisites keeps a tree an
// operator could not provision an explicit packaging failure: without the shipped
// lockfile the SDK version is not pinned, and without the shipped runtime's own npm
// the recorded provisioning command cannot run without a global package manager.
func TestRender_RefusesStagedTreeWithoutTheProvisioningPrerequisites(t *testing.T) {
	t.Parallel()

	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)

	for _, tc := range []struct {
		name  string
		drop  func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name:  "shipped lockfile",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.bridgeLock)) },
			wants: archive.BridgePackageLockPath(),
		},
		{
			name:  "shipped bundled npm",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.npmCLI)) },
			wants: archive.PrivateRuntimeNPMCLIPath(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.drop(t, lay)

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
			require.Contains(t, err.Error(), "reinstall")
		})
	}
}

// TestRender_RefusesMissingPrivateRuntimeOrBridgeEntry keeps an incomplete staged
// archive an explicit prerequisite failure rather than metadata over a tree that
// cannot run.
func TestRender_RefusesMissingPrivateRuntimeOrBridgeEntry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		drop  func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name:  "private runtime",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.runtime)) },
			wants: "private/node/node",
		},
		{
			name:  "bridge entry",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.entry)) },
			wants: "private/bridge/bin/lip-cursor-sdk-bridge.js",
		},
		{
			name: "staged bridge manifest",
			drop: func(t *testing.T, lay *stagedRelease) {
				require.NoError(t, os.Remove(lay.bridgeJSON))
			},
			wants: "private/bridge/package.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.drop(t, lay)

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
			require.Contains(t, err.Error(), "reinstall")
		})
	}
}

// TestRender_RefusesWeakenedExportPosture keeps the closed manifest's declared
// trust boundary load-bearing: a template that quietly dropped local-only access,
// per-instance process sharing, static credentials, or the agent runtime
// execution class must not be rendered into a release.
func TestRender_RefusesWeakenedExportPosture(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ field, value, wants string }{
		{field: "access_scope", value: "shared", wants: "access_scope"},
		{field: "process_sharing", value: "shared", wants: "process_sharing"},
		{field: "credential_mode", value: "passthrough", wants: "credential_mode"},
		{field: "execution_class", value: "in_process", wants: "execution_class"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			body := readFile(t, lay.template)
			field := regexp.MustCompile(`"` + tc.field + `":\s*"[^"]*"`)
			require.True(t, field.MatchString(body), "template has no %s field", tc.field)
			writeFile(t, lay.template, field.ReplaceAllString(body, `"`+tc.field+`": "`+tc.value+`"`))

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.field)
			require.Contains(t, err.Error(), tc.value)
		})
	}
}

// TestRender_RefusesUnknownPlaceholdersAndBadDigests keeps a template that no
// longer matches the closed manifest contract, or a caller that hands over an
// implausible executable digest, a failure instead of a manifest the host would
// reject at install time.
func TestRender_RefusesUnknownPlaceholdersAndBadDigests(t *testing.T) {
	t.Parallel()

	t.Run("leftover placeholder", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		body := readFile(t, lay.template)
		writeFile(t, lay.template, strings.Replace(body, `"protocol_major": 1,`, `"protocol_major": REPLACE_PROTOCOL_MAJOR,`, 1))

		err := runRenderCmd(t, lay)
		require.Error(t, err)
		require.Contains(t, err.Error(), "REPLACE_PROTOCOL_MAJOR")
	})

	t.Run("short executable digest", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		lay.exeSHA256 = "not-a-digest"

		err := runRenderCmd(t, lay)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sha256")
	})
}

// TestRender_ReportsUndeclaredPlatformAsExplicitFailure keeps a cross-platform
// claim an explicit refusal.
func TestRender_ReportsUndeclaredPlatformAsExplicitFailure(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	lay.platform = "linux/386"

	err := runRenderCmd(t, lay)
	require.Error(t, err)
	require.Contains(t, err.Error(), "linux/386")
}

func (lay *stagedRelease) goos() string {
	goos, _, _ := strings.Cut(lay.platform, "/")
	return goos
}

func (lay *stagedRelease) goarch() string {
	_, goarch, _ := strings.Cut(lay.platform, "/")
	return goarch
}

func (lay *stagedRelease) suffix() string {
	if lay.goos() == "windows" {
		return ".exe"
	}
	return ""
}

// npmRelPath is where the platform's own Node distribution keeps its bundled npm, read
// from the archive contract so the staged tree mirrors the real distribution layout.
func npmRelPath(goos string) string {
	a, err := packagelayout.ForPlatform(goos, runtime.GOARCH)
	if err != nil {
		panic("npmRelPath: " + err.Error())
	}
	return filepath.FromSlash(a.PrivateRuntimeNPMRel())
}

// newStagedRelease builds a staged install root whose private runtime is a
// compiled stand-in reporting one fixed Node version. The renderer records the
// version the shipped executable reports, so the stand-in has to be a real
// executable rather than a file with plausible bytes.
func newStagedRelease(tb testing.TB) *stagedRelease {
	tb.Helper()

	goos := runtime.GOOS
	exeSuffix := ""
	if goos == "windows" {
		exeSuffix = ".exe"
	}

	root := filepath.Join(tb.TempDir(), "staged install root with spaces")
	lay := &stagedRelease{
		root:           root,
		platform:       goos + "/" + runtime.GOARCH,
		buildID:        "localdev",
		nodeSource:     "node-v22.22.3-dist",
		nodeSourceKind: "official-distribution",
		nodeVersion:    "v22.22.3",
	}
	lay.exeSHA256 = strings.Repeat("ab", 32)
	lay.exe = writeFile(tb, filepath.Join(root, "bin", "lip-backend-cursorsdk"+exeSuffix), "outer executable")
	lay.runtime = installBinaryFile(tb, filepath.Join(root, "private", "node", "node"+exeSuffix), buildRuntimeStub(tb, lay.nodeVersion))
	lay.npmCLI = writeFile(tb, filepath.Join(root, "private", "node", npmRelPath(goos), "bin", "npm-cli.js"),
		"// shipped bundled npm entry point\n")
	lay.entry = writeFile(tb, filepath.Join(root, "private", "bridge", "bin", "lip-cursor-sdk-bridge.js"), "// bridge entry\n")
	writeFile(tb, filepath.Join(root, "private", "bridge", "lip-cursor-sdk-bridge"+exeSuffix), "launcher")
	writeFile(tb, filepath.Join(root, "private", "bridge", "dist", "main.js"), "// built\n")
	lay.bridgeJSON = writeFile(tb, filepath.Join(root, "private", "bridge", "package.json"),
		`{"name":"lip-cursor-sdk-bridge","version":"0.1.0","engines":{"node":">=22.13"},"dependencies":{"@cursor/sdk":"1.0.23"}}`)
	lay.bridgeLock = writeFile(tb, filepath.Join(root, "private", "bridge", "package-lock.json"),
		`{"name":"lip-cursor-sdk-bridge","lockfileVersion":3,"packages":{}}`)

	repo := filepath.Join(tb.TempDir(), "repo")
	lay.release = writeFile(tb, filepath.Join(repo, "release.yaml"), strings.Join([]string{
		"# Connector release metadata.",
		"schema: golip.connector.release/v1",
		"plugin_id: io.golip.backend.cursorsdk",
		"factory_kind: cursorsdk",
		"module: github.com/aiproxer/aiproxer-cursor-sdk",
		"command: ./cmd/lip-backend-cursorsdk",
		"manifest_template: manifest/template.backendplugin.json",
		"version: 0.1.0",
		"build_id: localdev",
		"tag: cursorsdk-v0.1.0",
		"profiles:",
		"  - full",
		"published_root_module: github.com/matdev83/go-llm-interactive-proxy",
		"replace_policy: released-dependency-pins-no-replace",
		"private_companions:",
		"  - bridge-node",
		"",
	}, "\n"))
	lay.template = writeFile(tb, filepath.Join(repo, "manifest", "template.backendplugin.json"), strings.Join([]string{
		"{",
		`  "schema": "golip.backendplugin.manifest/v1",`,
		`  "plugin_id": "io.golip.backend.cursorsdk",`,
		`  "version": "0.1.0",`,
		`  "build_id": "REPLACE_BUILD_ID",`,
		`  "executable": "bin/lip-backend-cursorsdk",`,
		`  "sha256": "REPLACE_SHA256",`,
		`  "protocol_major": 1,`,
		`  "protocol_min_minor": 0,`,
		`  "protocol_max_minor": 0,`,
		`  "platforms": [`,
		`    {"os": "windows", "arch": "amd64"},`,
		`    {"os": "linux", "arch": "amd64"}`,
		`  ],`,
		`  "exports": [`,
		`    {`,
		`      "kind": "cursorsdk",`,
		`      "credential_mode": "static",`,
		`      "access_scope": "local_only",`,
		`      "process_sharing": "per_instance",`,
		`      "execution_class": "agent_runtime"`,
		`    }`,
		`  ]`,
		"}",
		"",
	}, "\n"))
	return lay
}

func renderAndRead(tb testing.TB, lay *stagedRelease) (map[string]any, map[string]any) {
	tb.Helper()

	require.NoError(tb, runRenderCmd(tb, lay))
	return decodeMap(tb, readFile(tb, filepath.Join(lay.root, "plugin.backendplugin.json"))),
		decodeMap(tb, readFile(tb, filepath.Join(lay.root, "compatibility.json")))
}

func runRenderCmd(tb testing.TB, lay *stagedRelease) error {
	tb.Helper()

	cmd := exec.Command(goToolPath(tb), "run", ".",
		"render",
		"-repo", filepath.Dir(lay.release),
		"-staging", lay.root,
		"-platform", lay.platform,
		"-exe-sha256", lay.exeSHA256,
		"-node-source", lay.nodeSource,
		"-node-source-kind", lay.nodeSourceKind,
	)
	cmd.Dir = thisFileDir(tb)
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &renderError{output: string(out), err: err}
	}
	return nil
}

// renderError carries the renderer's diagnostics so assertions can read them.
type renderError struct {
	output string
	err    error
}

func (e *renderError) Error() string { return e.output }
func (e *renderError) Unwrap() error { return e.err }

func writeFile(tb testing.TB, path, body string) string {
	tb.Helper()

	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(tb, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// installBinaryFile copies a built executable into a staged private slot.
func installBinaryFile(tb testing.TB, dst, src string) string {
	tb.Helper()

	raw, err := os.ReadFile(src)
	require.NoError(tb, err)
	require.NoError(tb, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(tb, os.WriteFile(dst, raw, 0o700))
	return dst
}

// buildRuntimeStub compiles a private-runtime stand-in that reports one fixed
// version, mirroring a staged Node executable closely enough for the renderer to
// ask it for its own version.
func buildRuntimeStub(tb testing.TB, version string) string {
	tb.Helper()

	dir := tb.TempDir()
	writeFile(tb, filepath.Join(dir, "go.mod"), "module runtimestub\n\ngo 1.26\n")
	writeFile(tb, filepath.Join(dir, "main.go"), fmt.Sprintf(
		"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(%q) }\n", version))

	out := filepath.Join(dir, "node"+packagelayout.ExeSuffixFor(runtime.GOOS))
	cmd := exec.Command(goToolPath(tb), "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	raw, err := cmd.CombinedOutput()
	require.NoError(tb, err, "build runtime stub: %s", raw)
	return out
}

func readFile(tb testing.TB, path string) string {
	tb.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(tb, err)
	return string(raw)
}

func decodeMap(tb testing.TB, body string) map[string]any {
	tb.Helper()

	var out map[string]any
	require.NoError(tb, json.Unmarshal([]byte(body), &out))
	return out
}

func decodeList(tb testing.TB, v any) []any {
	tb.Helper()

	out, ok := v.([]any)
	require.True(tb, ok, "expected a JSON array, got %T", v)
	return out
}

func mustJSON(tb testing.TB, v any) string {
	tb.Helper()

	raw, err := json.MarshalIndent(v, "", "  ")
	require.NoError(tb, err)
	return string(raw)
}

func thisFileDir(tb testing.TB) string {
	tb.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(tb, ok)
	return filepath.Dir(file)
}
