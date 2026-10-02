package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"gopkg.in/yaml.v3"
)

// compatibilitySchema versions the plugin release metadata record. It is plugin
// release metadata, not a host manifest field: the host manifest stays closed and
// learns nothing about packaging.
const compatibilitySchema = "golip.cursorsdk.compatibility/v1"

// packagingVariant names the private-runtime archive shape: the plugin ships its
// own Node runtime, so no system-wide Node installation is required.
const packagingVariant = "private-runtime"

// requiredExports is the declared trust boundary of the closed host manifest. A
// template that dropped or weakened any of it is not releasable: the render fails
// instead of shipping a manifest with a wider blast radius.
var requiredExports = map[string]string{
	"kind":            "cursorsdk",
	"credential_mode": "static",
	"access_scope":    "local_only",
	"process_sharing": "per_instance",
	"execution_class": "agent_runtime",
}

// releaseMeta is the flat release metadata the packaging scripts render from.
type releaseMeta struct {
	Schema              string `yaml:"schema"`
	PluginID            string `yaml:"plugin_id"`
	FactoryKind         string `yaml:"factory_kind"`
	Module              string `yaml:"module"`
	Command             string `yaml:"command"`
	ManifestTemplate    string `yaml:"manifest_template"`
	Version             string `yaml:"version"`
	BuildID             string `yaml:"build_id"`
	Tag                 string `yaml:"tag"`
	Profiles            []string
	PublishedRootModule string   `yaml:"published_root_module"`
	ReplacePolicy       string   `yaml:"replace_policy"`
	PrivateCompanions   []string `yaml:"private_companions"`
}

// compatibility is the flat plugin release metadata written into the archive.
type compatibility struct {
	Schema        string `json:"schema"`
	PluginID      string `json:"plugin_id"`
	PluginVersion string `json:"plugin_version"`
	BuildID       string `json:"build_id"`
	// ReleaseTagDeclared is the tag release.yaml declares for a future publication.
	// No such tag exists: the field is named for the declaration so that nothing in an
	// archive can be read as a claim that a release was published.
	ReleaseTagDeclared      string `json:"release_tag_declared"`
	Module                  string `json:"module"`
	PublishedRootModule     string `json:"published_root_module"`
	Platform                string `json:"platform"`
	NativePlatformAssembled string `json:"native_platform_assembled"`
	PackagingVariant        string `json:"packaging_variant"`
	ExternalNodeRequired    bool   `json:"external_node_required"`
	ProtocolMajor           uint32 `json:"protocol_major"`
	ProtocolMinMinor        uint32 `json:"protocol_min_minor"`
	ProtocolMaxMinor        uint32 `json:"protocol_max_minor"`
	OuterExecutable         string `json:"outer_executable"`
	OuterExecutableSHA256   string `json:"outer_executable_sha256"`
	Manifest                string `json:"manifest"`
	// ManifestSHA256 ties the record to the exact manifest bytes the archive
	// carries, so the checksum file and this record describe the same artifact.
	ManifestSHA256         string `json:"manifest_sha256"`
	CursorSDKVersion       string `json:"cursor_sdk_version"`
	CursorSDKPinnedVersion string `json:"cursor_sdk_pinned_version"`
	BridgeVersion          string `json:"bridge_version"`
	BridgeNodeEngine       string `json:"bridge_node_engine"`
	PrivateRuntimeVersion  string `json:"private_runtime_version"`
	PrivateRuntimeSource   string `json:"private_runtime_source"`
	PrivateRuntimeSHA256   string `json:"private_runtime_sha256"`
	// TestedHostArtifactSHA256 stays empty until a release is certified against a
	// versioned host artifact. An unverified claim is recorded as absent rather than
	// invented.
	TestedHostArtifactSHA256 string `json:"tested_host_artifact_sha256"`
	// PackageVerification records how the archive was verified. The verification
	// report itself is produced by scripts/verify-package and attached to release
	// evidence; the archive only names the check that has to pass.
	PackageVerification string `json:"package_verification"`
	LicensingStatus     string `json:"licensing_status"`
	GeneratedBy         string `json:"generated_by"`
}

// runRender writes the host manifest and the compatibility metadata for a staged
// install root. Every value is derived from the release metadata, the closed
// manifest template, and the staged tree itself, so a script cannot stage a tree
// and describe a different one.
func runRender(args []string) error {
	fs := newFlagSet("render")
	repo := fs.String("repo", ".", "plugin repository root holding release.yaml and the manifest template")
	staging := fs.String("staging", "", "staged plugin install root to describe")
	platform := fs.String("platform", "", "os/arch of the staged archive (defaults to the host platform)")
	exeSHA := fs.String("exe-sha256", "", "sha256 of the outer executable, lowercase hex")
	nodeSource := fs.String("node-source", "", "provenance label of the private Node runtime source")
	nodeSourceKind := fs.String("node-source-kind", "",
		"where the private Node runtime came from: "+
			strings.Join(slices.Sorted(maps.Keys(runtimeSourcePrefixes)), ", "))
	if err := fs.Parse(args); err != nil {
		return reportError("render", err)
	}
	if strings.TrimSpace(*staging) == "" {
		return reportError("render", errors.New("-staging is required"))
	}

	archive, err := packagelayout.ForPlatform(hostOr(*platform))
	if err != nil {
		return reportError("render", err)
	}
	return reportError("render", renderStaged(*repo, *staging, *exeSHA, *nodeSourceKind, *nodeSource, archive))
}

// hostOr returns the requested platform or the host platform when none was given.
func hostOr(platform string) (goos, goarch string) {
	if platform != "" {
		return splitPlatform(platform)
	}
	return hostPlatform()
}

// hostPlatform is the platform the packaging scripts run on. The scripts refuse to
// assemble an archive for any other platform: cross-compilation is not native
// validation, and an unvalidated platform claim is not a truthful one.
func hostPlatform() (goos, goarch string) { return runtime.GOOS, runtime.GOARCH }

// renderStaged writes both metadata files into the staged install root.
func renderStaged(repo, staging, exeSHA, nodeSourceKind, nodeSource string, archive packagelayout.Archive) error {
	meta, err := loadRelease(filepath.Join(repo, "release.yaml"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.ManifestTemplate) == "" {
		return errors.New("release.yaml has no manifest_template")
	}
	templatePath := filepath.Join(repo, filepath.FromSlash(meta.ManifestTemplate))
	if meta.BuildID == "" {
		return errors.New("release.yaml has no build_id")
	}
	if meta.Version == "" {
		return errors.New("release.yaml has no version")
	}
	digest, err := checkSHA256(exeSHA)
	if err != nil {
		return err
	}

	manifestBody, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("manifest template %s: %w", templatePath, err)
	}
	manifest, err := renderManifest(manifestBody, meta, archive, digest)
	if err != nil {
		return fmt.Errorf("manifest template %s: %w", templatePath, err)
	}
	comp, err := renderCompatibility(staging, meta, archive, digest, nodeSourceKind, nodeSource, manifest)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(staging, filepath.FromSlash(archive.ManifestPath()))
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(staging, filepath.FromSlash(archive.CompatibilityPath())), comp)
}

// placeholderPattern finds every template placeholder token.
var placeholderPattern = regexp.MustCompile(`REPLACE_[A-Z0-9_]+`)

// knownPlaceholders are the only substitutions the renderer performs. A template
// that carries any other placeholder is a packaging failure: an unresolved token
// would otherwise reach an installed manifest and be rejected by the host's
// strict parser at install time, long after the release was assembled.
var knownPlaceholders = map[string]struct{}{
	"REPLACE_SHA256":   {},
	"REPLACE_BUILD_ID": {},
}

// checkPlaceholders rejects a template placeholder the renderer does not resolve.
func checkPlaceholders(template []byte) error {
	unknown := map[string]struct{}{}
	for _, token := range placeholderPattern.FindAllString(string(template), -1) {
		if _, ok := knownPlaceholders[token]; !ok {
			unknown[token] = struct{}{}
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	tokens := slices.Sorted(maps.Keys(unknown))
	return fmt.Errorf("template carries unknown placeholder(s) %s; the renderer resolves only %s",
		strings.Join(tokens, ", "), strings.Join(slices.Sorted(maps.Keys(knownPlaceholders)), ", "))
}

// renderManifest renders the closed host manifest for one platform: it substitutes
// the placeholders, points at the platform executable, keeps every other identity
// field from the template, and narrows the platform claim to the platform the
// archive was assembled on.
func renderManifest(template []byte, meta releaseMeta, archive packagelayout.Archive, digest string) (map[string]any, error) {
	if err := checkPlaceholders(template); err != nil {
		return nil, err
	}

	var manifest map[string]any
	if err := json.Unmarshal(template, &manifest); err != nil {
		return nil, err
	}

	if got, ok := manifest["plugin_id"].(string); !ok || got != meta.PluginID {
		return nil, fmt.Errorf("plugin_id %q does not match release.yaml plugin_id %q", got, meta.PluginID)
	}
	if got, ok := manifest["version"].(string); !ok || got != meta.Version {
		return nil, fmt.Errorf("version %q does not match release.yaml version %q", got, meta.Version)
	}

	manifest["build_id"] = meta.BuildID
	manifest["sha256"] = digest
	manifest["executable"] = archive.OuterExecutablePath()

	platforms, err := nativePlatforms(manifest["platforms"], archive)
	if err != nil {
		return nil, err
	}
	manifest["platforms"] = platforms

	if err := checkExports(manifest["exports"]); err != nil {
		return nil, err
	}
	if err := checkProtocolRange(manifest); err != nil {
		return nil, err
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(body), "REPLACE_") {
		return nil, fmt.Errorf("rendered manifest still contains an unresolved placeholder: %s", firstPlaceholder(string(body)))
	}
	return manifest, nil
}

// nativePlatforms narrows the declared platform list to the platform the archive
// was assembled on. A single-platform artifact that still claimed the other
// declared platforms would either be rejected by the host's strict manifest
// parser or, worse, advertise support that was never validated.
func nativePlatforms(raw any, archive packagelayout.Archive) ([]any, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("template has no platforms array")
	}
	declared := false
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if entry["os"] == archive.OS() && entry["arch"] == archive.Arch() {
			declared = true
		}
	}
	if !declared {
		return nil, fmt.Errorf("template does not declare %s", archive.Platform())
	}
	return []any{map[string]any{"os": archive.OS(), "arch": archive.Arch()}}, nil
}

// checkExports keeps the declared trust boundary load-bearing.
func checkExports(raw any) error {
	list, ok := raw.([]any)
	if !ok || len(list) != 1 {
		return fmt.Errorf("template must declare exactly one export, got %d", len(list))
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		return errors.New("template export is not an object")
	}
	for field, want := range requiredExports {
		got, ok := entry[field].(string)
		if !ok {
			return fmt.Errorf("export is missing %s", field)
		}
		if got != want {
			return fmt.Errorf("export %s is %q but the plugin declares %q", field, got, want)
		}
	}
	return nil
}

// checkProtocolRange keeps the negotiated protocol range well formed.
func checkProtocolRange(manifest map[string]any) error {
	if _, ok := numberField(manifest, "protocol_major"); !ok {
		return errors.New("template has no protocol_major")
	}
	min, okMin := numberField(manifest, "protocol_min_minor")
	max, okMax := numberField(manifest, "protocol_max_minor")
	if !okMin || !okMax {
		return errors.New("template has no protocol_min_minor/protocol_max_minor")
	}
	if min > max {
		return fmt.Errorf("protocol_min_minor %d is above protocol_max_minor %d", uint32(min), uint32(max))
	}
	return nil
}

// runtimeSourcePrefixes maps the runtime source kind the packaging script resolved to
// the provenance prefix the release metadata records.
//
// The packager has three sources, and only one of them is an official distribution: it
// can stage a distribution archive or directory, a runtime the caller supplied, or
// the node on the build machine's PATH. Recording all three under the distribution
// prefix would state, about a copy of somebody's working installation, that it is a
// copy of an official distribution, so each source kind names itself.
var runtimeSourcePrefixes = map[string]string{
	"official-distribution": "nodejs-official-distribution",
	"supplied-runtime":      "nodejs-supplied-runtime",
	"path-fallback":         "nodejs-path-fallback",
}

// runtimeSource records where the staged private runtime actually came from. An
// unknown kind is a packaging failure rather than a default prefix: guessing here
// would put a false provenance statement inside a release artifact.
func runtimeSource(kind, label string) (string, error) {
	prefix, ok := runtimeSourcePrefixes[strings.TrimSpace(kind)]
	if !ok {
		return "", fmt.Errorf("-node-source-kind %q is not one of %s; the recorded provenance has to say where the runtime came from",
			kind, strings.Join(slices.Sorted(maps.Keys(runtimeSourcePrefixes)), ", "))
	}
	if strings.TrimSpace(label) == "" {
		return "", errors.New("-node-source is required: the private runtime provenance must be recorded")
	}
	return prefix + ":" + strings.TrimSpace(label), nil
}

// renderCompatibility derives the release metadata from the staged tree, so the
// recorded SDK and private runtime versions are the ones the archive actually
// carries.
func renderCompatibility(staging string, meta releaseMeta, archive packagelayout.Archive, digest, nodeSourceKind, nodeSource string, manifest map[string]any) (*compatibility, error) {
	outer := filepath.Join(staging, filepath.FromSlash(archive.OuterExecutablePath()))
	if err := packagelayout.CheckSlot(outer, archive.OuterExecutablePath()); err != nil {
		return nil, prerequisite(err)
	}
	priv, err := packagelayout.PrivateFor(filepath.Join(staging, filepath.FromSlash(archive.LauncherPath())), archive.OS())
	if err != nil {
		return nil, err
	}
	runtimePath := filepath.Join(staging, filepath.FromSlash(archive.PrivateRuntimePath()))
	if err := packagelayout.CheckSlot(runtimePath, archive.PrivateRuntimePath()); err != nil {
		return nil, prerequisite(err)
	}
	if err := packagelayout.CheckSlot(priv.Entry, archive.BridgeEntryPath()); err != nil {
		return nil, prerequisite(err)
	}
	for _, dir := range []string{archive.BridgeDistPath(), archive.BridgeModulesPath()} {
		info, err := os.Stat(filepath.Join(staging, filepath.FromSlash(dir)))
		if err != nil {
			return nil, prerequisite(fmt.Errorf("staged %s: %w", dir, err))
		}
		if !info.IsDir() {
			return nil, prerequisite(fmt.Errorf("staged %s is not a directory", dir))
		}
	}

	bridge, err := readStagedJSON(filepath.Join(staging, filepath.FromSlash(archive.BridgePackageJSONPath())), archive.BridgePackageJSONPath())
	if err != nil {
		return nil, prerequisite(err)
	}
	pinned, err := pinnedSDKVersion(bridge, archive.BridgePackageJSONPath())
	if err != nil {
		return nil, prerequisite(err)
	}
	sdkRel := archive.BridgeModulesPath() + "/@cursor/sdk/package.json"
	sdk, err := readStagedJSON(filepath.Join(staging, filepath.FromSlash(sdkRel)), sdkRel)
	if err != nil {
		return nil, prerequisite(fmt.Errorf("staged production dependency tree has no %s: %w", sdkRel, err))
	}
	sdkVersion, _ := sdk["version"].(string)
	if sdkVersion != pinned {
		return nil, fmt.Errorf("staged @cursor/sdk is %s but the bridge pins %s; the released archive must match the pin the bridge verifies at run time",
			orUnknown(sdkVersion), orUnknown(pinned))
	}

	runtimeSHA, err := fileSHA256(runtimePath)
	if err != nil {
		return nil, err
	}
	version, err := privateRuntimeVersion(runtimePath)
	if err != nil {
		return nil, err
	}
	provenance, err := runtimeSource(nodeSourceKind, nodeSource)
	if err != nil {
		return nil, err
	}

	manifestSHA, err := manifestSHA256(manifest)
	if err != nil {
		return nil, err
	}
	return &compatibility{
		Schema:                   compatibilitySchema,
		PluginID:                 meta.PluginID,
		PluginVersion:            meta.Version,
		BuildID:                  meta.BuildID,
		ReleaseTagDeclared:       meta.Tag,
		Module:                   meta.Module,
		PublishedRootModule:      meta.PublishedRootModule,
		Platform:                 archive.Platform(),
		NativePlatformAssembled:  archive.Platform(),
		PackagingVariant:         packagingVariant,
		ExternalNodeRequired:     false,
		ProtocolMajor:            uintField(manifest, "protocol_major"),
		ProtocolMinMinor:         uintField(manifest, "protocol_min_minor"),
		ProtocolMaxMinor:         uintField(manifest, "protocol_max_minor"),
		OuterExecutable:          archive.OuterExecutablePath(),
		OuterExecutableSHA256:    digest,
		Manifest:                 archive.ManifestPath(),
		ManifestSHA256:           manifestSHA,
		CursorSDKVersion:         sdkVersion,
		CursorSDKPinnedVersion:   pinned,
		BridgeVersion:            stringField(bridge, "version"),
		BridgeNodeEngine:         engineConstraint(bridge),
		PrivateRuntimeVersion:    version,
		PrivateRuntimeSource:     provenance,
		PrivateRuntimeSHA256:     runtimeSHA,
		TestedHostArtifactSHA256: "",
		PackageVerification:      "scripts/verify-package",
		LicensingStatus:          licensingStatus,
		GeneratedBy:              "scripts/package-plugin",
	}, nil
}

// licensingStatus is the standing redistribution statement. The private Node
// runtime is MIT and its notices ship with it; @cursor/sdk is proprietary under
// Cursor's terms and bundles platform binaries. No redistribution right is
// asserted here: a maintainer has to confirm it before anything is published.
const licensingStatus = "private Node runtime is MIT with its bundled third-party notices in " +
	"LICENSES/; @cursor/sdk is proprietary under Cursor's terms and ships bundled platform " +
	"binaries, so a maintainer must confirm redistribution rights before publishing this archive"

// loadRelease reads the flat release metadata. An unknown key or an unparsable
// file is a failure: the packager must not describe a release it did not read.
func loadRelease(path string) (releaseMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return releaseMeta{}, fmt.Errorf("%s: %w", path, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	var meta releaseMeta
	if err := dec.Decode(&meta); err != nil {
		return releaseMeta{}, fmt.Errorf("%s: %w", path, err)
	}
	if meta.Schema != "golip.connector.release/v1" {
		return releaseMeta{}, fmt.Errorf("%s: unsupported schema %q", path, meta.Schema)
	}
	return meta, nil
}

// checkSHA256 rejects an implausible executable digest instead of writing a
// manifest the host would reject at install time.
func checkSHA256(digest string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(digest))
	if len(trimmed) != sha256.Size*2 {
		return "", fmt.Errorf("outer executable sha256 must be %d lowercase hex characters, got %q", sha256.Size*2, digest)
	}
	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("outer executable sha256 is not hex: %w", err)
	}
	return trimmed, nil
}

// pinnedSDKVersion reads the exact SDK pin the bridge verifies at run time.
func pinnedSDKVersion(bridge map[string]any, rel string) (string, error) {
	deps, ok := bridge["dependencies"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("staged %s has no dependencies", rel)
	}
	pinned, ok := deps["@cursor/sdk"].(string)
	if !ok || strings.TrimSpace(pinned) == "" {
		return "", fmt.Errorf("staged %s does not pin @cursor/sdk", rel)
	}
	return pinned, nil
}

// engineConstraint reads the bridge's Node engine requirement.
func engineConstraint(bridge map[string]any) string {
	engines, ok := bridge["engines"].(map[string]any)
	if !ok {
		return ""
	}
	constraint, _ := engines["node"].(string)
	return constraint
}

// privateRuntimeVersion asks the shipped runtime for its own version, so the
// recorded version is the one the archive actually runs rather than a value the
// caller supplied.
func privateRuntimeVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("staged private runtime %s does not run: %w", path, err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("staged private runtime %s reported no version", path)
	}
	return version, nil
}

// readStagedJSON reads one staged JSON metadata file.
func readStagedJSON(path, rel string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("staged %s: %w", rel, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("staged %s: %w", rel, err)
	}
	return out, nil
}

// prerequisite turns an incomplete staged tree into an explicit prerequisite
// failure that names the operator remedy.
func prerequisite(err error) error {
	return fmt.Errorf("%w; reinstall the Cursor plugin package or re-run scripts/package-plugin", err)
}

// firstPlaceholder names the first unresolved placeholder in a rendered body.
func firstPlaceholder(body string) string {
	i := strings.Index(body, "REPLACE_")
	if i < 0 {
		return ""
	}
	end := i
	for end < len(body) && !strings.ContainsRune("\" ,}", rune(body[end])) {
		end++
	}
	return body[i:end]
}

// marshalJSON renders one metadata document the way the packager writes it:
// indented, unescaped, and newline terminated. Disabling HTML escaping keeps
// values such as a Node engine constraint readable ("&gt;=22.13" would be a
// correct but unreadable record), and it makes the rendered bytes byte-stable
// across runs so a digest of the rendered manifest is meaningful.
func marshalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// manifestSHA256 digests the rendered manifest so the record and the checksum
// file describe the same bytes.
func manifestSHA256(manifest map[string]any) (string, error) {
	body, err := marshalJSON(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// writeJSONFile writes the staged metadata with a trailing newline so it is
// diffable and byte-stable across packaging runs.
func writeJSONFile(path string, value any) error {
	body, err := marshalJSON(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// fileSHA256 digests one file.
func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// numberField reads a JSON number field.
func numberField(manifest map[string]any, key string) (float64, bool) {
	value, ok := manifest[key].(float64)
	return value, ok
}

// uintField reads a JSON number field as the unsigned protocol field it is.
func uintField(manifest map[string]any, key string) uint32 {
	value, _ := numberField(manifest, key)
	return uint32(value)
}

// stringField reads a JSON string field.
func stringField(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

// orUnknown renders an empty version for diagnostics without hiding it.
func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}
