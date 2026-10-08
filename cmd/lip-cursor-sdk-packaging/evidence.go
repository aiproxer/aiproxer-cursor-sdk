package main

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
)

// This file derives the release metadata an auditor cannot reconstruct from the
// archive alone: which published host contracts the bytes were built against, which
// source revision produced them, what has and has not been verified about them, and
// what has been certified about the host binary.
//
// Every value here comes from a validated build input - the plugin's own module
// manifest, the staged executable, the manifest template, and the release metadata -
// rather than from a string a caller typed. That is the whole point: a release record
// whose host compatibility, source identity, or verification state could be supplied
// by whoever ran the packager would be decoration, and the two ways to get that wrong
// are to record a fact nobody checked and to leave a fact unrecorded. Both fail here
// instead.

// hostCertification states the two supported certification postures.
//
// There is no third one on purpose. "Unknown" and "assumed compatible" are the two
// answers a record reaches by not answering, and both read as a claim.
const (
	hostUncertified = "uncertified"
	hostCertified   = "certified"
)

// verificationNotPerformed is the only state a packaged record can carry for the
// package verification itself.
//
// The packager writes this record while it assembles the tree, so no verification can
// have run yet: verification is a separate step, against an assembled or installed
// tree, and its report is release evidence kept outside the archive. Recording a pass
// here would describe a check nobody performed.
const verificationNotPerformed = "not-performed"

// releasedVersion is the shape of a downloadable module version.
//
// This is a shape check, not a resolution check: it rejects a placeholder and a local
// path in the position of a version. Resolution is proven where it actually happens -
// the packager builds the outer executable from this module before the record is
// written, so a version that cannot be downloaded fails packaging before it can be
// recorded.
var releasedVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// hostContractVersions are the exact published host contract versions one archive was
// built against.
type hostContractVersions struct {
	Root string
	ACP  string
}

// hostContractPins reads the exact released host contract versions out of the plugin's
// own module manifest.
//
// This is the manifest `go build` resolved to produce the executable in this archive,
// so it is the one input that cannot disagree with the bytes. release.yaml names the
// two module paths it believes are pinned, and this refuses to describe a build that
// does not pin them - including the case that resolves perfectly well locally: a
// `replace` pointing at a sibling checkout. A record that named a released version for
// a build that never used it would be the most misleading line the archive could carry.
func hostContractPins(repo string, meta releaseMeta) (hostContractVersions, error) {
	var pins hostContractVersions

	modules := []struct{ path, declared string }{
		{meta.PublishedRootModule, "published_root_module"},
		{meta.PublishedACPModule, "published_acp_module"},
	}
	required := map[string]string{}
	for _, module := range modules {
		if strings.TrimSpace(module.path) == "" {
			return pins, fmt.Errorf("release.yaml has no %s; the record has to name the published host "+
				"contracts this build is pinned to, and the module manifest has to require them", module.declared)
		}
		required[module.path] = module.declared
	}

	raw, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		return pins, fmt.Errorf("plugin module manifest: %w", err)
	}
	requiredVersions, replaced := modulePins(raw)

	for _, module := range modules {
		if _, ok := replaced[module.path]; ok {
			return pins, fmt.Errorf("plugin module manifest replaces %s; release metadata records the "+
				"published version this build is pinned to, and a replace resolves the build from "+
				"somewhere else entirely, so the record would describe contracts the bytes were "+
				"never built from (remove the replace and pin the released version)",
				module.path)
		}
		version, ok := requiredVersions[module.path]
		if !ok {
			return pins, fmt.Errorf("plugin module manifest does not require %s (%s); the record names "+
				"the released host contracts this build resolved, and without that require line there "+
				"is nothing to name", module.path, required[module.path])
		}
		if !releasedVersion.MatchString(version) {
			return pins, fmt.Errorf("plugin module manifest requires %s at %q, which is not a released "+
				"module version; release metadata records exact downloadable versions, not placeholders",
				module.path, version)
		}
	}

	return hostContractVersions{Root: requiredVersions[meta.PublishedRootModule], ACP: requiredVersions[meta.PublishedACPModule]}, nil
}

// modulePins reads the `require` and `replace` directives of a module manifest.
//
// It reads the file's own lines rather than resolving the build graph: the pins have to
// be visible in the manifest a reviewer can open, and `go list -m` would answer for
// whatever the caller's module cache and network happen to hold. Block and single-line
// spellings are both handled because both are valid.
func modulePins(raw []byte) (required, replaced map[string]string) {
	required = map[string]string{}
	replaced = map[string]string{}

	block := ""
	for _, text := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(text)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if block != "" {
			if line == ")" {
				block = ""
				continue
			}
			recordDirective(block, line, required, replaced)
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "require", "replace":
			if fields[1] == "(" {
				block = fields[0]
				continue
			}
			recordDirective(fields[0], strings.Join(fields[1:], " "), required, replaced)
		}
	}
	return required, replaced
}

// recordDirective records one entry of a require or replace directive.
func recordDirective(directive, entry string, required, replaced map[string]string) {
	fields := strings.Fields(entry)
	if len(fields) == 0 {
		return
	}
	switch directive {
	case "require":
		if len(fields) >= 2 {
			required[fields[0]] = fields[1]
		}
	case "replace":
		if len(fields) >= 3 && fields[1] == "=>" {
			replaced[fields[0]] = strings.Join(fields[2:], " ")
		}
	}
}

// sourceIdentity is the source provenance of one staged executable.
type sourceIdentity struct {
	// Revision is the revision the archive's bytes were built from. It is empty when
	// nothing established one, never a stand-in value.
	Revision string
	// Modified is whether the build came from a tree with uncommitted changes. It is
	// nil when nothing established it, which is not the same answer as false: an
	// unestablished state is absent from the record rather than recorded as clean.
	Modified *bool
	// Evidence says where the values above came from, so a reader of the archive can
	// tell a stamped revision from a resolved one, and a stated state from an absent
	// one.
	Evidence string
	// Stamped reports whether the executable itself carried the revision.
	Stamped bool
}

// stateOf is the tri-state a source cleanliness claim can hold. The three answers are
// kept apart on purpose: a dirty tree, a clean tree, and a state nothing established are
// three different facts, and the third one recorded as the second is a false statement
// about an artifact that would be read as evidence.
type stateOf int

const (
	// stateUnestablished is the state of a build nobody could establish anything about.
	stateUnestablished stateOf = iota
	// stateUnknown is the state a caller declared after failing to establish it.
	stateUnknown
	// stateClean is a tree with no uncommitted changes.
	stateClean
	// stateDirty is a tree with uncommitted changes.
	stateDirty
)

// parseSourceState reads the caller's claim about the tree a build ran in.
func parseSourceState(value string) (stateOf, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return stateUnestablished, nil
	case "unknown":
		return stateUnknown, nil
	case "true":
		return stateDirty, nil
	case "false":
		return stateClean, nil
	default:
		return stateUnestablished, fmt.Errorf(
			"-source-modified %q is not a source state; the states are true, false, and unknown", value)
	}
}

// modifiedOf is the recorded cleanliness of one state, which is nil unless the state
// established it.
func modifiedOf(state stateOf) *bool {
	switch state {
	case stateDirty:
		return boolRef(true)
	case stateClean:
		return boolRef(false)
	default:
		return nil
	}
}

// boolRef is the recorded form of an established boolean.
func boolRef(value bool) *bool { return &value }

// revisionPattern is the shape of a version-control revision this tool records.
//
// It is checked because the value ends up inside a release artifact that claims to
// identify a build: `HEAD`, a branch name, or a version string in that position would
// name something nobody can resolve to a commit.
var revisionPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// stagedSource reads the source identity out of the staged executable itself.
//
// The Go toolchain stamps the revision it built from into the binary when the build
// happens in a primary version-control checkout, so this is a fact about the bytes the
// archive ships rather than a value typed into a metadata file. It is absent more often
// than one would like - the toolchain does not stamp inside a linked work tree, nor with
// -buildvcs=false, nor outside version control - which is why the caller can also supply
// what it resolved from the tree it built in, and why an absent stamp is reported as
// absent rather than filled in with a value the artifact cannot support.
func stagedSource(outerPath, outerRel string) sourceIdentity {
	unavailable := func(evidence string) sourceIdentity {
		return sourceIdentity{Evidence: "the staged " + outerRel + " carries no Go build VCS stamp - " +
			evidence + " - so this record names no source revision rather than one nobody can check"}
	}

	info, err := buildinfo.ReadFile(outerPath)
	if err != nil {
		return unavailable("the staged file is not a Go executable this tool can read (" + err.Error() + ")")
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	revision := strings.ToLower(strings.TrimSpace(settings["vcs.revision"]))
	if revision == "" {
		return unavailable("the Go toolchain stamps one only for a build in a primary version-control " +
			"checkout, not for a linked work tree, -buildvcs=false, or a build outside version control")
	}
	if !revisionPattern.MatchString(revision) {
		return unavailable("the stamp it carries (" + revision + ") is not a revision this tool will record")
	}
	return sourceIdentity{
		Revision: revision,
		Modified: boolRef(strings.EqualFold(strings.TrimSpace(settings["vcs.modified"]), "true")),
		Evidence: "Go build VCS stamp inside the staged " + outerRel +
			"; its vcs.modified is recorded with it, so whether the build came from a tree with " +
			"uncommitted changes is established rather than assumed",
		Stamped: true,
	}
}

// resolvedSourceBasis is the basis for a revision the packager resolved from its own tree
// rather than read out of the bytes.
const resolvedSourceBasis = "resolved from the version-control state of the tree scripts/package-plugin " +
	"built in - its HEAD revision - because the Go toolchain stamped no VCS information into that build " +
	"(a linked work tree, -buildvcs=false, or a build outside version control); the staged executable " +
	"carries no stamp to cross-check it against, so the packaging evidence for this revision is the " +
	"resolved value itself"

// resolvedSourceUnknownState is the same basis, for a revision whose tree state could not
// be read. The record then states no cleanliness at all, because that is what it knows.
const resolvedSourceUnknownState = resolvedSourceBasis + "; whether that tree had uncommitted changes " +
	"could not be established, so this record states no source_modified rather than a clean one"

// resolveSource decides which source identity the record states.
//
// The stamp in the executable wins when it is there, and it also acts as a check on
// anything the caller resolved: a build-time revision that disagrees with the stamp is a
// packaging failure, because one of the two is about a different source than the bytes in
// the archive. The same is true of a stated cleanliness, except that "unknown" is not a
// disagreement - it is the absence of a claim, and the stamp's own answer stands.
//
// When there is no stamp, a caller-resolved revision is recorded with the basis that says
// where it came from. That is the common case, because the toolchain does not stamp inside
// a linked work tree and that is where this project builds. When nothing established
// anything, the record states no revision and no cleanliness rather than a default for
// either.
func resolveSource(stamp sourceIdentity, suppliedRevision, suppliedModified string) (sourceIdentity, error) {
	supplied := strings.ToLower(strings.TrimSpace(suppliedRevision))
	state, err := parseSourceState(suppliedModified)
	if err != nil {
		return sourceIdentity{}, err
	}

	// A stated cleanliness is recorded only together with the revision it was read at.
	// Without a revision there is nothing for "modified" to be relative to, and recording
	// it anyway is how a dirty build ends up described as a clean one.
	if state != stateUnestablished && state != stateUnknown && supplied == "" {
		return sourceIdentity{}, fmt.Errorf(
			"-source-modified was given without -source-revision; whether a build came from a tree with " +
				"uncommitted changes is recorded together with the revision it was read at, or not at all")
	}

	if stamp.Stamped {
		if supplied != "" && supplied != stamp.Revision {
			return sourceIdentity{}, fmt.Errorf(
				"the staged executable was built from %s but -source-revision says %s; the record cannot "+
					"state a source revision that is not the one the bytes were built from", stamp.Revision, supplied)
		}
		if claimed := modifiedOf(state); claimed != nil && *claimed != *stamp.Modified {
			return sourceIdentity{}, fmt.Errorf(
				"the staged executable records vcs.modified=%t but -source-modified says %t; the record "+
					"cannot state a source state that is not the one the bytes were built from",
				*stamp.Modified, *claimed)
		}
		return stamp, nil
	}

	if supplied == "" {
		return stamp, nil
	}
	if !revisionPattern.MatchString(supplied) {
		return sourceIdentity{}, fmt.Errorf(
			"-source-revision %q is not a version-control revision; the record names the revision the "+
				"archive was built from, which is a commit id and not a branch, tag, or version", suppliedRevision)
	}
	evidence := resolvedSourceBasis
	if state == stateUnestablished || state == stateUnknown {
		evidence = resolvedSourceUnknownState
	}
	return sourceIdentity{
		Revision: supplied,
		Modified: modifiedOf(state),
		Evidence: evidence,
	}, nil
}

// hostCertificationRecord is the host certification posture one archive carries.
type hostCertificationRecord struct {
	// State is hostUncertified or hostCertified.
	State string
	// Reason explains an uncertified posture and is empty when certified.
	Reason string
	// TestedArtifacts are the host artifact digests this record carries, sorted so the
	// record is byte-stable across runs. It is empty for every uncertified record, and
	// never anything but the digests the release metadata declares.
	TestedArtifacts []string
	// Platforms is that same evidence per platform: which host release and asset were
	// measured, at which digest, and which companion spelling that platform supports.
	Platforms []hostCertificationPlatform
}

// hostCertificationPlatform is one declared platform's certified host artifact as the
// archive records it.
//
// The packaged launcher path is derived from the archive layout rather than written here,
// because the remedy an operator on this platform has to configure is a file the archive
// really ships, and a path recorded by hand would be a path nothing checks.
type hostCertificationPlatform struct {
	Platform            string `json:"platform"`
	HostProject         string `json:"host_project"`
	HostVersion         string `json:"host_version"`
	HostReleaseAsset    string `json:"host_release_asset"`
	HostChecksumsAsset  string `json:"host_checksums_asset"`
	HostBinary          string `json:"host_binary"`
	HostArtifactSHA256  string `json:"host_artifact_sha256"`
	CompanionContract   string `json:"companion_contract"`
	BridgeExecutableRel string `json:"bridge_executable_rel"`
}

// hostCertification resolves the declared certification posture against the evidence the
// release metadata declares.
//
// The posture is declared in release.yaml so it is reviewable, and the host artifacts it was
// measured against are declared beside it, so the evidence is reviewable too and cannot be a
// digest somebody typed into a command line for one run. A caller may still pass
// -tested-host to assert what it believes was measured; the assertion is checked against the
// declaration rather than recorded in place of it, so a packaging run can corroborate the
// record but never substitute a host of its own choosing.
//
// The two are checked against each other because either one alone is misleading: a certified
// record with no artifact behind it is a bare claim, an uncertified record carrying artifacts
// contradicts itself, and a certified record that leaves a declared platform without an
// artifact would claim compatibility for a platform nothing was measured on.
func hostCertification(meta releaseMeta, supplied, declared []string) (hostCertificationRecord, error) {
	state := strings.TrimSpace(meta.HostCertification)
	reason := strings.TrimSpace(meta.HostCertificationReason)

	switch state {
	case hostUncertified:
		if len(meta.CertifiedHostArtifacts) > 0 {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares host_certification: %s with %d certified_host_artifacts; the artifacts are "+
					"what a certified record carries, so an uncertified one carrying them contradicts itself",
				hostUncertified, len(meta.CertifiedHostArtifacts))
		}
		if len(supplied) > 0 {
			return hostCertificationRecord{}, fmt.Errorf(
				"%d -tested-host digest(s) were supplied but release.yaml declares host_certification: %s; "+
					"either the release is certified against those artifacts, which is host_certification: %s "+
					"together with one certified_host_artifacts entry per declared platform, or those digests "+
					"are not evidence for this archive and -tested-host should not name any",
				len(supplied), hostUncertified, hostCertified)
		}
		if reason == "" {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares host_certification: %s with no host_certification_reason; an archive "+
					"that is not certified against a host artifact has to say why, or an auditor cannot "+
					"tell a deliberate limit from an oversight", hostUncertified)
		}
		return hostCertificationRecord{State: hostUncertified, Reason: reason, TestedArtifacts: []string{}}, nil
	case hostCertified:
		if reason != "" {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares host_certification: %s and also gives a host_certification_reason; "+
					"the reason is what an uncertified record carries, and a certified one carries the "+
					"certified_host_artifacts in its place", hostCertified)
		}
		if len(meta.CertifiedHostArtifacts) == 0 {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares host_certification: %s but declares no certified_host_artifacts; one "+
					"entry per declared platform names the host release, the asset, the binary and the measured "+
					"sha256 the certification ran against, and without them the posture is a bare claim",
				hostCertified)
		}
		record, err := certifiedPlatforms(meta.CertifiedHostArtifacts, declared)
		if err != nil {
			return hostCertificationRecord{}, err
		}
		if err := checkSuppliedHosts(supplied, record.TestedArtifacts); err != nil {
			return hostCertificationRecord{}, err
		}
		return record, nil
	case "":
		return hostCertificationRecord{}, fmt.Errorf(
			"release.yaml has no host_certification; declare %s with the reason, or %s together with the "+
				"certified_host_artifacts the release was measured against, instead of leaving an absent key "+
				"to read as an unremarked absence of evidence", hostUncertified, hostCertified)
	default:
		return hostCertificationRecord{}, fmt.Errorf(
			"release.yaml declares host_certification %q; the postures are %s and %s",
			state, hostUncertified, hostCertified)
	}
}

// certifiedPlatforms validates the declared host artifacts and turns them into the record's
// per-platform evidence.
//
// Coverage is exact rather than partial: every declared platform needs an artifact it was
// measured against, and an artifact for a platform the manifest does not declare would be a
// claim outside the published platform set. The output is sorted by platform so the rendered
// record does not depend on the order the YAML happens to list its entries in.
func certifiedPlatforms(artifacts []certifiedHostArtifact, declared []string) (hostCertificationRecord, error) {
	seen := make(map[string]bool, len(artifacts))
	platforms := make([]hostCertificationPlatform, 0, len(artifacts))
	digests := make([]string, 0, len(artifacts))

	for _, artifact := range artifacts {
		if seen[artifact.Platform] {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares certified_host_artifacts twice for %s; one artifact that certified a "+
					"platform twice would make the per-platform record ambiguous", artifact.Platform)
		}
		seen[artifact.Platform] = true
		if !slices.Contains(declared, artifact.Platform) {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml certifies a host artifact for platform %s, which the manifest does not declare "+
					"(declared: %s); a certification for an undeclared platform is a claim this release "+
					"publishes nowhere", artifact.Platform, strings.Join(declared, ", "))
		}
		for _, field := range []struct{ name, value string }{
			{"host_project", artifact.HostProject},
			{"host_version", artifact.HostVersion},
			{"host_release_asset", artifact.HostReleaseAsset},
			{"host_checksums_asset", artifact.HostChecksumsAsset},
			{"host_binary", artifact.HostBinary},
		} {
			if strings.TrimSpace(field.value) == "" {
				return hostCertificationRecord{}, fmt.Errorf(
					"release.yaml declares a certified host artifact for %s with no %s; the measurement names the "+
						"host release it ran against, so a digest without it is not tied to a release anybody can "+
						"download", artifact.Platform, field.name)
			}
		}
		digest, err := checkDigest(artifact.HostArtifactSHA256, "host artifact for "+artifact.Platform)
		if err != nil {
			return hostCertificationRecord{}, err
		}
		contract, err := packagelayout.ParseCompanionContract(artifact.CompanionContract)
		if err != nil {
			return hostCertificationRecord{}, fmt.Errorf("%s declares companion_contract %q: %w",
				artifact.Platform, artifact.CompanionContract, err)
		}
		platformArchive, err := packagelayout.ForPlatform(splitPlatform(artifact.Platform))
		if err != nil {
			return hostCertificationRecord{}, err
		}
		platforms = append(platforms, hostCertificationPlatform{
			Platform:            artifact.Platform,
			HostProject:         artifact.HostProject,
			HostVersion:         artifact.HostVersion,
			HostReleaseAsset:    artifact.HostReleaseAsset,
			HostChecksumsAsset:  artifact.HostChecksumsAsset,
			HostBinary:          artifact.HostBinary,
			HostArtifactSHA256:  digest,
			CompanionContract:   string(contract),
			BridgeExecutableRel: platformArchive.LauncherPath(),
		})
		digests = append(digests, digest)
	}

	var missing []string
	for _, platform := range declared {
		if !seen[platform] {
			missing = append(missing, platform)
		}
	}
	if len(missing) > 0 {
		return hostCertificationRecord{}, fmt.Errorf(
			"release.yaml declares host_certification: %s with no certified_host_artifacts for %s; every platform "+
				"this release publishes needs the host artifact it was measured against",
			hostCertified, strings.Join(missing, ", "))
	}

	slices.SortStableFunc(platforms, func(left, right hostCertificationPlatform) int {
		return strings.Compare(left.Platform, right.Platform)
	})
	// The flat digest list is read in the same platform order as the per-platform record
	// above it, so the two halves of the evidence line up instead of the flat list being a
	// second, differently ordered statement of the same fact.
	digests = digests[:0]
	for _, platform := range platforms {
		digests = append(digests, platform.HostArtifactSHA256)
	}
	return hostCertificationRecord{
		State:           hostCertified,
		Reason:          "",
		TestedArtifacts: digests,
		Platforms:       platforms,
	}, nil
}

// checkSuppliedHosts holds a caller-supplied assertion to the declared evidence.
//
// The digests a packaging run passes are a claim about what it measured, and they are checked
// against the declaration rather than recorded. That direction matters: a run that certified
// against a host this repository does not record would otherwise produce an archive naming an
// artifact no reader can check, while a run that re-asserts the declared digests is
// corroboration and nothing more.
func checkSuppliedHosts(supplied, declared []string) error {
	for _, supplied := range supplied {
		normalized, err := checkDigest(supplied, "-tested-host")
		if err != nil {
			return err
		}
		if !slices.Contains(declared, normalized) {
			return fmt.Errorf(
				"-tested-host %s is not a host artifact this release declares; the certified evidence is "+
					"release.yaml's certified_host_artifacts (%s), and a packaging run may assert those but "+
					"cannot substitute a host of its own", normalized, strings.Join(declared, ", "))
		}
	}
	return nil
}

// packageVerificationRecord is what the packaged record says about the verification of
// the package it describes.
type packageVerificationRecord struct {
	// State is verificationNotPerformed for every archive this tool writes.
	State string
	// Performed is false for every archive this tool writes.
	Performed bool
	// Reason says why the state is what it is.
	Reason string
	// ShippedCommand and InstalledCommand are the two verification runs, named for this
	// archive's platform, that an auditor has to perform and attach.
	ShippedCommand   string
	InstalledCommand string
}

// packageVerification states, truthfully, that nothing has been verified yet.
//
// The packager writes the record while it assembles the tree, so it cannot know the
// outcome of a verification that has not run. It names the two runs instead - one over
// a freshly unpacked archive, one over a provisioned install tree - because a record
// that merely named the script would leave the reader with nothing to run and nothing
// to check.
func packageVerification(archive packagelayout.Archive) packageVerificationRecord {
	verifier := "scripts/verify-package.sh"
	if archive.OS() == "windows" {
		verifier = "scripts/verify-package.ps1"
	}
	command := func(state string) string {
		return verifier + " --package-root <plugin-root> --tree-state " + state
	}
	return packageVerificationRecord{
		State:     verificationNotPerformed,
		Performed: false,
		Reason: "this record is written while the archive is assembled, before any verification can run; " +
			"the runs below are what has to be performed and their reports are release evidence kept " +
			"outside the archive, so nothing here records an outcome",
		ShippedCommand:   command("shipped"),
		InstalledCommand: command("installed"),
	}
}

// declaredPlatforms reads the platform claims out of the closed manifest template.
//
// The template holds every platform this project claims; one archive is assembled and
// verified natively and narrows the rendered manifest to that one. The record keeps both
// halves, so a reader of a single artifact can see which claims the project makes and
// which of them this artifact is evidence for.
func declaredPlatforms(raw any) ([]string, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("template has no platforms array")
	}
	var out []string
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("template platform entry %v is not an object", item)
		}
		os, _ := entry["os"].(string)
		arch, _ := entry["arch"].(string)
		if os == "" || arch == "" {
			return nil, fmt.Errorf("template platform entry %v names no os/arch", item)
		}
		out = append(out, os+"/"+arch)
	}
	if len(out) == 0 {
		return nil, errors.New("template declares no platforms")
	}
	slices.Sort(out)
	if dup := slices.Compact(slices.Clone(out)); len(dup) != len(out) {
		return nil, fmt.Errorf("template declares platform %s twice", dup[0])
	}
	return out, nil
}

// platformsNotAssembled is the declared platform set minus the platform this archive
// was assembled on.
func platformsNotAssembled(declared []string, assembled string) []string {
	out := make([]string, 0, len(declared))
	for _, platform := range declared {
		if platform != assembled {
			out = append(out, platform)
		}
	}
	return out
}

// digestList collects a repeatable digest option.
type digestList []string

// String renders the collected digests, so the flag's own help text stays readable.
func (l *digestList) String() string { return strings.Join(*l, ",") }

// Set records one digest. Validation happens where the record is written, so an
// invalid digest is reported with the context of the posture it contradicts.
func (l *digestList) Set(value string) error {
	*l = append(*l, value)
	return nil
}
