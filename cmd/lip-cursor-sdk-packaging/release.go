package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file reports the release a repository declares, which is what a release process reads
// instead of parsing release metadata itself.
//
// A tag, a version, a set of platforms, and the host artifacts each platform was certified
// against are five separate facts that have to agree with each other. Every consumer that
// wanted them - a packaging script, the release workflow's guard, the certification gate -
// would otherwise read release.yaml with its own parser and its own idea of what a missing
// key means, and the disagreement would only surface in the artifact, which is the last
// place anybody can still fix it cheaply.
//
// So the declaration is read and validated here, once, through the same code the renderer
// writes into an archive, and reported as a document. The host download URLs are derived
// rather than declared, because a URL composed from a recorded project, tag, and asset name
// cannot name a host release this repository does not name.

// releaseReportSchema versions the reported release document.
const releaseReportSchema = "golip.cursorsdk.release/v1"

// hostReleaseURL composes the download URL of one host release asset.
//
// It is composed rather than declared so that a workflow cannot publish a link to something
// other than what was measured: the project, the tag, and the asset name are all recorded,
// and the GitHub release layout for them is fixed. The project is recorded as the module
// path the host publishes, so the host is the authority on it and the scheme and host name
// are not stated twice.
func hostReleaseURL(project, version, asset string) string {
	return "https://" + project + "/releases/download/" + version + "/" + asset
}

// releaseReport is the declared release identity and its certified host evidence.
type releaseReport struct {
	Schema            string   `json:"schema"`
	PluginID          string   `json:"plugin_id"`
	PluginVersion     string   `json:"plugin_version"`
	Tag               string   `json:"tag"`
	BuildID           string   `json:"build_id"`
	ManifestTemplate  string   `json:"manifest_template"`
	HostCertification string   `json:"host_certification"`
	DeclaredPlatforms []string `json:"declared_platforms"`
	// Platforms is one entry per declared platform. It is empty for an uncertified
	// release, which is exactly what such a release has to say about its host evidence.
	Platforms []releaseReportPlatform `json:"platforms"`
}

// releaseReportPlatform is one declared platform's release inputs, including where its
// certified host artifact is downloaded from.
type releaseReportPlatform struct {
	Platform            string `json:"platform"`
	HostProject         string `json:"host_project"`
	HostVersion         string `json:"host_version"`
	HostReleaseAsset    string `json:"host_release_asset"`
	HostReleaseURL      string `json:"host_release_url"`
	HostChecksumsAsset  string `json:"host_checksums_asset"`
	HostChecksumsURL    string `json:"host_checksums_url"`
	HostBinary          string `json:"host_binary"`
	HostArtifactSHA256  string `json:"host_artifact_sha256"`
	CompanionContract   string `json:"companion_contract"`
	BridgeExecutableRel string `json:"bridge_executable_rel"`
}

// runRelease reports what this repository declares a release to be.
func runRelease(args []string) error {
	fs := newFlagSet("release")
	repo := fs.String("repo", ".", "plugin repository root holding release.yaml and the manifest template")
	if err := fs.Parse(args); err != nil {
		return reportError("release", err)
	}

	meta, err := loadRelease(filepath.Join(*repo, "release.yaml"))
	if err != nil {
		return reportError("release", err)
	}
	if strings.TrimSpace(meta.ManifestTemplate) == "" {
		return reportError("release", errors.New("release.yaml has no manifest_template"))
	}
	template, err := os.ReadFile(filepath.Join(*repo, filepath.FromSlash(meta.ManifestTemplate)))
	if err != nil {
		return reportError("release", fmt.Errorf("manifest template %s: %w", meta.ManifestTemplate, err))
	}
	var manifest map[string]any
	if err := json.Unmarshal(template, &manifest); err != nil {
		return reportError("release", fmt.Errorf("manifest template %s: %w", meta.ManifestTemplate, err))
	}
	declared, err := declaredPlatforms(manifest["platforms"])
	if err != nil {
		return reportError("release", err)
	}
	// No caller-supplied digests here: this report is the declaration, and a certification
	// that needed a packaging run to corroborate it would not be a declaration at all.
	certification, err := hostCertification(meta, nil, declared)
	if err != nil {
		return reportError("release", err)
	}

	report := releaseReport{
		Schema:            releaseReportSchema,
		PluginID:          meta.PluginID,
		PluginVersion:     meta.Version,
		Tag:               meta.Tag,
		BuildID:           meta.BuildID,
		ManifestTemplate:  meta.ManifestTemplate,
		HostCertification: certification.State,
		DeclaredPlatforms: declared,
		Platforms:         make([]releaseReportPlatform, 0, len(certification.Platforms)),
	}
	for _, platform := range certification.Platforms {
		report.Platforms = append(report.Platforms, releaseReportPlatform{
			Platform:            platform.Platform,
			HostProject:         platform.HostProject,
			HostVersion:         platform.HostVersion,
			HostReleaseAsset:    platform.HostReleaseAsset,
			HostReleaseURL:      hostReleaseURL(platform.HostProject, platform.HostVersion, platform.HostReleaseAsset),
			HostChecksumsAsset:  platform.HostChecksumsAsset,
			HostChecksumsURL:    hostReleaseURL(platform.HostProject, platform.HostVersion, platform.HostChecksumsAsset),
			HostBinary:          platform.HostBinary,
			HostArtifactSHA256:  platform.HostArtifactSHA256,
			CompanionContract:   platform.CompanionContract,
			BridgeExecutableRel: platform.BridgeExecutableRel,
		})
	}

	rendered, err := marshalJSON(report)
	if err != nil {
		return reportError("release", err)
	}
	fmt.Print(string(rendered))
	return nil
}
