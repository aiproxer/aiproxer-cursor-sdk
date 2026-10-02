package main

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
)

// report is the JSON shape the packaging scripts consume.
type report struct {
	Schema            string   `json:"schema"`
	Platform          string   `json:"platform"`
	OS                string   `json:"os"`
	Arch              string   `json:"arch"`
	ExeSuffix         string   `json:"exe_suffix"`
	Manifest          string   `json:"manifest"`
	OuterExecutable   string   `json:"outer_executable"`
	Launcher          string   `json:"launcher"`
	BridgePackageDir  string   `json:"bridge_package_dir"`
	BridgeEntry       string   `json:"bridge_entry"`
	BridgeDist        string   `json:"bridge_dist"`
	BridgeModules     string   `json:"bridge_modules"`
	BridgePackageJSON string   `json:"bridge_package_json"`
	PrivateRuntime    string   `json:"private_runtime"`
	LauncherName      string   `json:"launcher_name"`
	PrivateRuntimeDoc string   `json:"private_runtime_doc"`
	PrivatePrefix     string   `json:"private_prefix"`
	Compatibility     string   `json:"compatibility"`
	Checksums         string   `json:"checksums"`
	LicensesDir       string   `json:"licenses_dir"`
	RequiredEntries   []string `json:"required_entries"`
	PrivateEntries    []string `json:"private_entries"`
	DeclaredPlatforms []string `json:"declared_platforms"`
	// ChecksumSeparator is the digest/path separator of checksums.sha256. It is
	// reported so the packaging scripts write the same line format the verifier
	// parses, in the sha256sum order "<digest><separator><install-root-relative path>".
	ChecksumSeparator string `json:"checksum_separator"`
}

// runLayout prints the archive layout report of the requested, or the host,
// platform.
func runLayout(args []string) error {
	fs := newFlagSet("layout")
	platform := fs.String("platform", "", "os/arch to report (defaults to the host platform)")
	if err := fs.Parse(args); err != nil {
		return reportError("layout", err)
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	if *platform != "" {
		goos, goarch = splitPlatform(*platform)
	}
	archive, err := packagelayout.ForPlatform(goos, goarch)
	if err != nil {
		return reportError("layout", err)
	}

	rep := report{
		Schema:            reportSchema,
		Platform:          archive.Platform(),
		OS:                archive.OS(),
		Arch:              archive.Arch(),
		ExeSuffix:         archive.ExeSuffix(),
		Manifest:          archive.ManifestPath(),
		OuterExecutable:   archive.OuterExecutablePath(),
		Launcher:          archive.LauncherPath(),
		BridgePackageDir:  archive.BridgePackageDirPath(),
		BridgeEntry:       archive.BridgeEntryPath(),
		BridgeDist:        archive.BridgeDistPath(),
		BridgeModules:     archive.BridgeModulesPath(),
		BridgePackageJSON: archive.BridgePackageJSONPath(),
		PrivateRuntime:    archive.PrivateRuntimePath(),
		LauncherName:      packagelayout.LauncherName,
		PrivateRuntimeDoc: packagelayout.RuntimeRelDoc,
		PrivatePrefix:     packagelayout.PrivatePrefix,
		Compatibility:     archive.CompatibilityPath(),
		Checksums:         archive.ChecksumsPath(),
		LicensesDir:       archive.LicensesDir(),
		RequiredEntries:   archive.RequiredEntries(),
		PrivateEntries:    archive.PrivateEntries(),
		DeclaredPlatforms: packagelayout.SupportedPlatforms(),
		// ChecksumSeparator is the digest/path separator of checksums.sha256, in
		// the sha256sum order "<digest><separator><install-root-relative path>".
		ChecksumSeparator: packagelayout.ChecksumSeparator,
	}
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return reportError("layout", err)
	}
	if _, err := os.Stdout.Write(append(body, '\n')); err != nil {
		return reportError("layout", err)
	}
	return nil
}

// splitPlatform splits an "os/arch" request. A malformed request is passed through
// so packagelayout reports exactly what was asked for.
func splitPlatform(platform string) (goos, goarch string) {
	if i := strings.Index(platform, "/"); i >= 0 {
		return platform[:i], platform[i+1:]
	}
	return platform, ""
}
