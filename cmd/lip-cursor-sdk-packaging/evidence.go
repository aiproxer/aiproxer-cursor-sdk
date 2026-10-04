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
	// TestedArtifacts are the host artifact digests supplied as evidence. It is empty
	// for every uncertified record, and never anything but caller-supplied digests.
	TestedArtifacts []string
}

// hostCertification resolves the declared certification posture against the evidence
// the caller supplied.
//
// The posture is declared in release.yaml so it is reviewable, and the evidence is a
// digest the release operator passes in, so it can only be what someone actually
// measured. The two are checked against each other because either one alone is
// misleading: a certified record with no digest behind it is a bare claim, and an
// uncertified record carrying a digest contradicts itself.
func hostCertification(meta releaseMeta, supplied []string) (hostCertificationRecord, error) {
	state := strings.TrimSpace(meta.HostCertification)
	reason := strings.TrimSpace(meta.HostCertificationReason)

	switch state {
	case hostUncertified:
		if len(supplied) > 0 {
			return hostCertificationRecord{}, fmt.Errorf(
				"%d -tested-host digest(s) were supplied but release.yaml declares host_certification: %s; "+
					"either the release is certified against those artifacts, which is host_certification: %s "+
					"with an empty host_certification_reason, or those digests are not evidence for this "+
					"archive and -tested-host should not name any",
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
		if len(supplied) == 0 {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares host_certification: %s but no host artifact digest was supplied; "+
					"pass -tested-host <sha256> once per host artifact this archive was certified against, "+
					"or declare host_certification: %s with the reason it is not certified",
				hostCertified, hostUncertified)
		}
		if reason != "" {
			return hostCertificationRecord{}, fmt.Errorf(
				"release.yaml declares host_certification: %s and also gives a host_certification_reason; "+
					"the reason is what an uncertified record carries", hostCertified)
		}
		artifacts := make([]string, 0, len(supplied))
		for _, supplied := range supplied {
			digest, err := checkDigest(supplied, "host artifact")
			if err != nil {
				return hostCertificationRecord{}, err
			}
			if !slices.Contains(artifacts, digest) {
				artifacts = append(artifacts, digest)
			}
		}
		return hostCertificationRecord{State: hostCertified, TestedArtifacts: artifacts}, nil
	case "":
		return hostCertificationRecord{}, fmt.Errorf(
			"release.yaml has no host_certification; declare %s with the reason, or %s together with the "+
				"host artifact digests the release was certified against, instead of leaving an absent key "+
				"to read as an unremarked absence of evidence", hostUncertified, hostCertified)
	default:
		return hostCertificationRecord{}, fmt.Errorf(
			"release.yaml declares host_certification %q; the postures are %s and %s",
			state, hostUncertified, hostCertified)
	}
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
