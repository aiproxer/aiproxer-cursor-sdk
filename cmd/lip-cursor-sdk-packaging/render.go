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
const compatibilitySchema = "golip.cursorsdk.compatibility/v2"

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
	PublishedACPModule  string   `yaml:"published_acp_module"`
	ReplacePolicy       string   `yaml:"replace_policy"`
	PrivateCompanions   []string `yaml:"private_companions"`
	// HostCertification declares whether this release has been certified against a
	// host binary artifact. It is declared rather than inferred, because an absent
	// value would read as an unremarked absence of evidence instead of as a decision.
	HostCertification string `yaml:"host_certification"`
	// HostCertificationReason is what an uncertified record carries in place of a
	// host artifact digest. A certified release has none: it has the digests.
	HostCertificationReason string `yaml:"host_certification_reason"`
	// CertifiedHostArtifacts are the host artifacts a certified release was measured
	// against, one per declared platform. They are read from here rather than from a
	// caller so that the evidence an archive carries is the evidence this repository
	// records, and a packaging run can only assert what was measured rather than
	// substitute a digest of its own.
	CertifiedHostArtifacts []certifiedHostArtifact `yaml:"certified_host_artifacts"`
}

// certifiedHostArtifact is one declared platform's certified host artifact.
//
// The whole record is per platform rather than a flat list of digests because the
// measurement was: the gate downloaded each platform's own host archive, extracted its
// own host binary from it, and checked that binary against the digest recorded here. Two
// digests with no statement of which belongs to which platform cannot be checked against
// anything, and the companion contract below is the per-platform installation procedure
// that measurement produced.
type certifiedHostArtifact struct {
	Platform           string                          `yaml:"platform"`
	HostProject        string                          `yaml:"host_project"`
	HostVersion        string                          `yaml:"host_version"`
	HostReleaseAsset   string                          `yaml:"host_release_asset"`
	HostChecksumsAsset string                          `yaml:"host_checksums_asset"`
	HostBinary         string                          `yaml:"host_binary"`
	HostArtifactSHA256 string                          `yaml:"host_artifact_sha256"`
	CompanionContract  packagelayout.CompanionContract `yaml:"companion_contract"`
}

// renderInputs are the caller-supplied build inputs of one render. Everything else the
// record says is derived from the repository and the staged tree.
type renderInputs struct {
	// Repo is the plugin repository root holding release.yaml, go.mod, and the
	// manifest template.
	Repo string
	// Staging is the staged plugin install root being described.
	Staging string
	// ExeSHA256 is the digest of the staged outer executable.
	ExeSHA256 string
	// NodeSourceKind and NodeSource say where the staged private runtime came from.
	NodeSourceKind string
	NodeSource     string
	// SourceRevision and SourceModified are the source state the packager resolved from
	// the tree it built in. They are a cross-check on, and a fallback for, the stamp
	// inside the staged executable; see resolveSource.
	SourceRevision string
	// SourceModified is the caller's claim about that tree: "true", "false", or
	// "unknown". Anything else, including an empty value, leaves the recorded
	// cleanliness unestablished rather than clean.
	SourceModified string
	// TestedHosts are the host artifact digests supplied as certification evidence.
	TestedHosts []string
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
	ReleaseTagDeclared  string `json:"release_tag_declared"`
	Module              string `json:"module"`
	PublishedRootModule string `json:"published_root_module"`
	// PublishedACPModule and the two versions below name the exact published host
	// contracts this archive was built against. They are read from the plugin's own
	// module manifest, which is what `go build` resolved, so the record cannot name a
	// contract version the bytes in the archive were not compiled from.
	PublishedACPModule      string `json:"published_acp_module"`
	HostContractRootVersion string `json:"host_contract_root_version"`
	HostContractACPVersion  string `json:"host_contract_acp_version"`
	// SourceRevision is the revision the archive's bytes were built from. It comes from
	// the Go build stamp inside the staged outer executable where the toolchain wrote
	// one, and from the tree the packager built in where it did not; SourceEvidence says
	// which of the two it is, and an archive with neither names no revision rather than
	// papering over the absence with a placeholder.
	SourceRevision string `json:"source_revision"`
	// SourceModified is absent from the record when nothing established the state of the
	// tree the build ran in. That absence is the honest answer: a recorded false would
	// be a claim that an unestablished build was a clean one, which is the one claim
	// nobody downstream would think to check.
	SourceModified *bool  `json:"source_modified,omitempty"`
	SourceEvidence string `json:"source_stamp_evidence"`
	Platform       string `json:"platform"`
	// NativePlatformAssembled is the platform this archive was assembled and can be
	// verified on; DeclaredPlatforms and DeclaredPlatformsNotAssembled say which
	// claims the project makes and which of them this single artifact is evidence for.
	NativePlatformAssembled       string   `json:"native_platform_assembled"`
	DeclaredPlatforms             []string `json:"declared_platforms"`
	DeclaredPlatformsNotAssembled []string `json:"declared_platforms_not_assembled"`
	PackagingVariant              string   `json:"packaging_variant"`
	ExternalNodeRequired          bool     `json:"external_node_required"`
	ProtocolMajor                 uint32   `json:"protocol_major"`
	ProtocolMinMinor              uint32   `json:"protocol_min_minor"`
	ProtocolMaxMinor              uint32   `json:"protocol_max_minor"`
	OuterExecutable               string   `json:"outer_executable"`
	OuterExecutableSHA256         string   `json:"outer_executable_sha256"`
	Manifest                      string   `json:"manifest"`
	// ManifestSHA256 ties the record to the exact manifest bytes the archive
	// carries, so the checksum file and this record describe the same artifact.
	ManifestSHA256 string `json:"manifest_sha256"`
	// CursorSDKRequiredVersion is the SDK version an install tree has to resolve
	// after the operator provisions it. It is the pin the bridge manifest carries and
	// the bridge verifies at run time, so the record and the run-time check cannot
	// disagree.
	CursorSDKRequiredVersion string `json:"cursor_sdk_required_version"`
	// CursorSDKBundled is false in every archive this tool writes. The Cursor SDK is
	// proprietary and is not redistributed: the archive ships the manifest and the
	// lockfile that pin it and neither the SDK nor its dependency closure. The third-party
	// package code this archive does ship is the private runtime's own bundled npm.
	CursorSDKBundled bool `json:"cursor_sdk_bundled"`
	// CursorSDKProvisioningCommand is the exact command that provisions the pinned SDK
	// with the runtime the archive ships.
	CursorSDKProvisioningCommand string `json:"cursor_sdk_provisioning_command"`
	// CursorSDKRedistribution states the non-redistribution position inside every
	// archive, so nothing the plugin ships can be read as asserting a redistribution
	// right it does not hold.
	CursorSDKRedistribution string `json:"cursor_sdk_redistribution"`
	BridgeVersion           string `json:"bridge_version"`
	BridgeNodeEngine        string `json:"bridge_node_engine"`
	PrivateRuntimeVersion   string `json:"private_runtime_version"`
	PrivateRuntimeSource    string `json:"private_runtime_source"`
	PrivateRuntimeSHA256    string `json:"private_runtime_sha256"`
	// HostCertificationState is the declared host certification posture, and
	// HostCertificationReason is what an uncertified record carries in its place of
	// evidence. TestedHostArtifacts holds only host artifact digests a release
	// operator actually supplied, so an uncertified archive carries none and no
	// archive can name a host it was never certified against.
	HostCertificationState  string   `json:"host_certification_state"`
	HostCertificationReason string   `json:"host_certification_reason"`
	TestedHostArtifacts     []string `json:"tested_host_artifacts"`
	// HostCertificationPlatforms is the certified evidence with the platform it belongs
	// to: which host release, which artifact of it, which binary, at which measured
	// digest, and which companion spelling is the supported configuration there. It is
	// what makes the flat digest list above auditable, and it is what tells an operator on
	// one of the two platforms whether they have to configure anything.
	HostCertificationPlatforms []hostCertificationPlatform `json:"host_certification_platforms"`
	// The package verification fields state what has and has not been verified about
	// this archive. The packager writes them before verification can run, so they
	// record no outcome and name the runs that have to be performed instead.
	PackageVerificationState            string `json:"package_verification_state"`
	PackageVerificationPerformed        bool   `json:"package_verification_performed"`
	PackageVerificationReason           string `json:"package_verification_reason"`
	PackageVerificationShippedCommand   string `json:"package_verification_shipped_command"`
	PackageVerificationInstalledCommand string `json:"package_verification_installed_command"`
	LicensingStatus                     string `json:"licensing_status"`
	GeneratedBy                         string `json:"generated_by"`
}

// runRender writes the host manifest and the compatibility metadata for a staged
// install root. Every value is derived from the release metadata, the closed
// manifest template, and the staged tree itself, so a script cannot stage a tree
// and describe a different one.
func runRender(args []string) error {
	fs := newFlagSet("render")
	repo := fs.String("repo", ".", "plugin repository root holding release.yaml, go.mod, and the manifest template")
	staging := fs.String("staging", "", "staged plugin install root to describe")
	platform := fs.String("platform", "", "os/arch of the staged archive (defaults to the host platform)")
	exeSHA := fs.String("exe-sha256", "", "sha256 of the outer executable, lowercase hex")
	nodeSource := fs.String("node-source", "", "provenance label of the private Node runtime source")
	nodeSourceKind := fs.String("node-source-kind", "",
		"where the private Node runtime came from: "+
			strings.Join(slices.Sorted(maps.Keys(runtimeSourcePrefixes)), ", "))
	testedHosts := &digestList{}
	fs.Var(testedHosts, "tested-host",
		"sha256 of a host artifact this archive is certified against; repeatable, and only with "+
			"host_certification: certified in release.yaml")
	sourceRevision := fs.String("source-revision", "",
		"version-control revision the build tree was at, for when the toolchain stamped none into the "+
			"executable; cross-checked against the stamp when there is one")
	sourceModified := fs.String("source-modified", "",
		"whether the build tree had uncommitted changes: true, false, or unknown; cross-checked against "+
			"the stamp when there is one, and left out of the record when unknown")
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
	inputs := renderInputs{
		Repo:           *repo,
		Staging:        *staging,
		ExeSHA256:      *exeSHA,
		NodeSourceKind: *nodeSourceKind,
		NodeSource:     *nodeSource,
		SourceRevision: *sourceRevision,
		SourceModified: *sourceModified,
		TestedHosts:    *testedHosts,
	}
	return reportError("render", renderStaged(inputs, archive))
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
func renderStaged(in renderInputs, archive packagelayout.Archive) error {
	meta, err := loadRelease(filepath.Join(in.Repo, "release.yaml"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.ManifestTemplate) == "" {
		return errors.New("release.yaml has no manifest_template")
	}
	templatePath := filepath.Join(in.Repo, filepath.FromSlash(meta.ManifestTemplate))
	if meta.BuildID == "" {
		return errors.New("release.yaml has no build_id")
	}
	if meta.Version == "" {
		return errors.New("release.yaml has no version")
	}
	digest, err := checkSHA256(in.ExeSHA256)
	if err != nil {
		return err
	}
	pins, err := hostContractPins(in.Repo, meta)
	if err != nil {
		return err
	}

	manifestBody, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("manifest template %s: %w", templatePath, err)
	}
	manifest, declared, err := renderManifest(manifestBody, meta, archive, digest)
	if err != nil {
		return fmt.Errorf("manifest template %s: %w", templatePath, err)
	}
	comp, err := renderCompatibility(in, meta, archive, digest, pins, declared, manifest)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(in.Staging, filepath.FromSlash(archive.ManifestPath()))
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(in.Staging, filepath.FromSlash(archive.CompatibilityPath())), comp)
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
//
// It also returns the platform set the template declares, because narrowing the
// manifest is exactly what removes the record of every other claim the project makes
// and the release metadata needs that set back.
func renderManifest(template []byte, meta releaseMeta, archive packagelayout.Archive, digest string) (map[string]any, []string, error) {
	if err := checkPlaceholders(template); err != nil {
		return nil, nil, err
	}

	var manifest map[string]any
	if err := json.Unmarshal(template, &manifest); err != nil {
		return nil, nil, err
	}

	if got, ok := manifest["plugin_id"].(string); !ok || got != meta.PluginID {
		return nil, nil, fmt.Errorf("plugin_id %q does not match release.yaml plugin_id %q", got, meta.PluginID)
	}
	if got, ok := manifest["version"].(string); !ok || got != meta.Version {
		return nil, nil, fmt.Errorf("version %q does not match release.yaml version %q", got, meta.Version)
	}

	manifest["build_id"] = meta.BuildID
	manifest["sha256"] = digest
	manifest["executable"] = archive.OuterExecutablePath()

	declared, platforms, err := nativePlatforms(manifest["platforms"], archive)
	if err != nil {
		return nil, nil, err
	}
	manifest["platforms"] = platforms

	if err := checkExports(manifest["exports"]); err != nil {
		return nil, nil, err
	}
	if err := checkProtocolRange(manifest); err != nil {
		return nil, nil, err
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if strings.Contains(string(body), "REPLACE_") {
		return nil, nil, fmt.Errorf("rendered manifest still contains an unresolved placeholder: %s", firstPlaceholder(string(body)))
	}
	return manifest, declared, nil
}

// nativePlatforms returns the declared platform set and the single-entry claim of a
// natively assembled archive.
//
// A single-platform artifact that still claimed the other declared platforms would
// either be rejected by the host's strict manifest parser or, worse, advertise
// support that was never validated.
func nativePlatforms(raw any, archive packagelayout.Archive) (declared []string, narrowed []any, err error) {
	declared, err = declaredPlatforms(raw)
	if err != nil {
		return nil, nil, err
	}
	if !slices.Contains(declared, archive.Platform()) {
		return nil, nil, fmt.Errorf("template does not declare %s; it declares %s",
			archive.Platform(), strings.Join(declared, ", "))
	}
	return declared, []any{map[string]any{"os": archive.OS(), "arch": archive.Arch()}}, nil
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
// recorded versions and provenance are the ones the archive actually carries.
func renderCompatibility(in renderInputs, meta releaseMeta, archive packagelayout.Archive, digest string,
	pins hostContractVersions, declared []string, manifest map[string]any) (*compatibility, error) {
	outerRel := archive.OuterExecutablePath()
	outer := filepath.Join(in.Staging, filepath.FromSlash(outerRel))
	if err := packagelayout.CheckSlot(outer, outerRel); err != nil {
		return nil, prerequisite(err)
	}
	priv, err := packagelayout.PrivateFor(filepath.Join(in.Staging, filepath.FromSlash(archive.LauncherPath())), archive.OS())
	if err != nil {
		return nil, err
	}
	runtimePath := filepath.Join(in.Staging, filepath.FromSlash(archive.PrivateRuntimePath()))
	if err := packagelayout.CheckSlot(runtimePath, archive.PrivateRuntimePath()); err != nil {
		return nil, prerequisite(err)
	}
	if err := packagelayout.CheckSlot(priv.Entry, archive.BridgeEntryPath()); err != nil {
		return nil, prerequisite(err)
	}
	// The shipped runtime's own npm is what the recorded provisioning command runs,
	// so an archive that does not carry it cannot be provisioned without a global
	// package manager and is not the archive this tool describes.
	if err := packagelayout.CheckSlot(priv.NPMCLI, archive.PrivateRuntimeNPMCLIPath()); err != nil {
		return nil, prerequisite(err)
	}
	if err := checkStagedDir(in.Staging, archive.BridgeDistPath()); err != nil {
		return nil, err
	}
	// The bridge manifest and the lockfile that pins the SDK are archive content;
	// the dependency closure they describe is not.
	if err := packagelayout.CheckSlot(
		filepath.Join(in.Staging, filepath.FromSlash(archive.BridgePackageLockPath())),
		archive.BridgePackageLockPath()); err != nil {
		return nil, prerequisite(err)
	}
	if err := rejectProvisionedTree(in.Staging, archive); err != nil {
		return nil, err
	}

	bridge, err := readStagedJSON(filepath.Join(in.Staging, filepath.FromSlash(archive.BridgePackageJSONPath())), archive.BridgePackageJSONPath())
	if err != nil {
		return nil, prerequisite(err)
	}
	required, err := pinnedSDKVersion(bridge, archive.BridgePackageJSONPath())
	if err != nil {
		return nil, prerequisite(err)
	}

	runtimeSHA, err := fileSHA256(runtimePath)
	if err != nil {
		return nil, err
	}
	version, err := privateRuntimeVersion(runtimePath)
	if err != nil {
		return nil, err
	}
	provenance, err := runtimeSource(in.NodeSourceKind, in.NodeSource)
	if err != nil {
		return nil, err
	}
	// The source identity and the host certification posture are resolved before they
	// are recorded, so a record that cannot state either one is never written.
	source, err := resolveSource(stagedSource(outer, outerRel), in.SourceRevision, in.SourceModified)
	if err != nil {
		return nil, err
	}
	certification, err := hostCertification(meta, in.TestedHosts, declared)
	if err != nil {
		return nil, err
	}

	manifestSHA, err := manifestSHA256(manifest)
	if err != nil {
		return nil, err
	}
	verification := packageVerification(archive)
	return &compatibility{
		Schema:                              compatibilitySchema,
		PluginID:                            meta.PluginID,
		PluginVersion:                       meta.Version,
		BuildID:                             meta.BuildID,
		ReleaseTagDeclared:                  meta.Tag,
		Module:                              meta.Module,
		PublishedRootModule:                 meta.PublishedRootModule,
		PublishedACPModule:                  meta.PublishedACPModule,
		HostContractRootVersion:             pins.Root,
		HostContractACPVersion:              pins.ACP,
		SourceRevision:                      source.Revision,
		SourceModified:                      source.Modified,
		SourceEvidence:                      source.Evidence,
		Platform:                            archive.Platform(),
		NativePlatformAssembled:             archive.Platform(),
		DeclaredPlatforms:                   declared,
		DeclaredPlatformsNotAssembled:       platformsNotAssembled(declared, archive.Platform()),
		PackagingVariant:                    packagingVariant,
		ExternalNodeRequired:                false,
		ProtocolMajor:                       uintField(manifest, "protocol_major"),
		ProtocolMinMinor:                    uintField(manifest, "protocol_min_minor"),
		ProtocolMaxMinor:                    uintField(manifest, "protocol_max_minor"),
		OuterExecutable:                     outerRel,
		OuterExecutableSHA256:               digest,
		Manifest:                            archive.ManifestPath(),
		ManifestSHA256:                      manifestSHA,
		CursorSDKRequiredVersion:            required,
		CursorSDKBundled:                    false,
		CursorSDKProvisioningCommand:        archive.ProvisionCommand(""),
		CursorSDKRedistribution:             cursorSDKRedistribution,
		BridgeVersion:                       stringField(bridge, "version"),
		BridgeNodeEngine:                    engineConstraint(bridge),
		PrivateRuntimeVersion:               version,
		PrivateRuntimeSource:                provenance,
		PrivateRuntimeSHA256:                runtimeSHA,
		HostCertificationState:              certification.State,
		HostCertificationReason:             certification.Reason,
		TestedHostArtifacts:                 certification.TestedArtifacts,
		HostCertificationPlatforms:          certification.Platforms,
		PackageVerificationState:            verification.State,
		PackageVerificationPerformed:        verification.Performed,
		PackageVerificationReason:           verification.Reason,
		PackageVerificationShippedCommand:   verification.ShippedCommand,
		PackageVerificationInstalledCommand: verification.InstalledCommand,
		LicensingStatus:                     licensingStatus,
		GeneratedBy:                         "scripts/package-plugin",
	}, nil
}

// checkStagedDir rejects a staged directory the archive cannot run without.
func checkStagedDir(staging, rel string) error {
	info, err := os.Stat(filepath.Join(staging, filepath.FromSlash(rel)))
	if err != nil {
		return prerequisite(fmt.Errorf("staged %s: %w", rel, err))
	}
	if !info.IsDir() {
		return prerequisite(fmt.Errorf("staged %s is not a directory", rel))
	}
	return nil
}

// rejectProvisionedTree refuses to describe a staged tree that carries third-party
// package code.
//
// The Cursor SDK is proprietary and its platform package bundles native binaries whose
// license texts it does not redistribute, so staging the dependency closure inside a
// published archive would assert a redistribution right nobody has verified. The
// operator provisions that tree themselves against the shipped runtime, which is why
// the archive carries the manifest and the lockfile and nothing else.
func rejectProvisionedTree(staging string, archive packagelayout.Archive) error {
	_, err := os.Stat(filepath.Join(staging, filepath.FromSlash(archive.BridgeModulesPath())))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return prerequisite(fmt.Errorf("staged %s: %w", archive.BridgeModulesPath(), err))
	}
	return fmt.Errorf("staged %s carries third-party package code; the Cursor SDK is not redistributed, "+
		"so the archive ships %s and %s only and the operator provisions %s with: %s",
		archive.BridgeModulesPath(), archive.BridgePackageJSONPath(), archive.BridgePackageLockPath(),
		archive.BridgeModulesPath(), archive.ProvisionCommand(""))
}

// licensingStatus is the standing redistribution statement.
//
// The private Node runtime is MIT and its notices ship with it, and that runtime does
// carry third-party package code: its own bundled npm plus the dependencies npm bundles.
// Neither is covered by the Node.js MIT grant, so neither may be described by it. npm
// ships its own license text inside the staged npm tree and licenses the npm application
// under the Artistic License 2.0 while stating that its bundled Node package dependencies
// are licensed on their respective license terms; each bundled package carries its own
// license text in its own package directory, and any that ships none is named in the
// staged notice rather than left to be assumed covered. @cursor/sdk is proprietary under
// Cursor's terms and its platform package bundles native binaries whose license texts it
// does not redistribute, so this archive ships none of that: the Cursor SDK dependency
// closure is operator-provisioned, not redistributed.
//
// The non-redistribution claim is therefore scoped to the Cursor SDK and its closure. A
// bare "no third-party package code ships in this archive" would be false of the archive
// this tool writes and would contradict the npm disclosure in the same string, and it
// would leave an auditor unable to tell what third-party code the archive does ship.
const licensingStatus = "the private Node runtime is MIT, with those notices staged in LICENSES/ from " +
	"the shipped runtime LICENSE; the runtime's bundled npm and the third-party packages npm bundles " +
	"with it ship as well, npm under its own Artistic-2.0 license text inside the staged npm tree and " +
	"each bundled package under its own license text in its own package directory, with any bundled " +
	"package shipping none named in LICENSES/THIRD-PARTY-NOTICES.md; the Cursor SDK is proprietary under " +
	"Cursor's terms and is not redistributed in this archive, which therefore ships no Cursor SDK and no " +
	"Cursor SDK dependency closure"

// cursorSDKRedistribution is the non-redistribution position, recorded in every
// archive so nothing the plugin ships can be read as asserting a right it does not
// hold. The operator, not the plugin, obtains the SDK and accepts Cursor's terms.
const cursorSDKRedistribution = "@cursor/sdk is not redistributed by this archive and no redistribution " +
	"right is asserted for it or for the native binaries its platform package bundles; the archive ships " +
	"the bridge package.json and package-lock.json that pin it, and the operator provisions the SDK at the " +
	"required version into the install tree themselves"

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
	return checkDigest(digest, "outer executable")
}

// checkDigest rejects anything that is not a sha256 digest in lowercase hex, and says
// what the digest was supposed to be. It is the only thing standing between a
// caller-supplied evidence value and a release record that claims it.
func checkDigest(digest, subject string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(digest))
	if len(trimmed) != sha256.Size*2 {
		return "", fmt.Errorf("%s sha256 must be %d lowercase hex characters, got %q",
			subject, sha256.Size*2, digest)
	}
	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("%s sha256 is not hex: %w", subject, err)
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
