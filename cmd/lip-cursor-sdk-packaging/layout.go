package main

import (
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
)

// treeStates are the two states a tree under verification can be in. A shipped
// archive is one the plugin assembled and nobody has provisioned yet, so it must
// carry no third-party package code; an installed tree is one an operator has
// provisioned, so the SDK has to resolve at the pinned version. Both verifiers
// accept this report's list, so neither spells a state of its own.
var treeStates = []string{"shipped", "installed"}

// report is the JSON shape the packaging scripts consume.
type report struct {
	Schema            string `json:"schema"`
	Platform          string `json:"platform"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	ExeSuffix         string `json:"exe_suffix"`
	Manifest          string `json:"manifest"`
	OuterExecutable   string `json:"outer_executable"`
	Launcher          string `json:"launcher"`
	BridgePackageDir  string `json:"bridge_package_dir"`
	BridgeEntry       string `json:"bridge_entry"`
	BridgeDist        string `json:"bridge_dist"`
	BridgePackageJSON string `json:"bridge_package_json"`
	BridgePackageLock string `json:"bridge_package_lock"`
	// BridgeModules is the operator-provisioned dependency tree. It is not archive
	// content: the Cursor SDK is proprietary and is not redistributed, so this path is
	// reported to be absent from a shipped archive and, when an operator has
	// provisioned it, to be outside the shipped checksum record.
	BridgeModules string `json:"bridge_modules"`
	// ProvisionedPrefix is BridgeModules with its trailing separator: the exact
	// prefix a verifier excludes from the shipped checksum record.
	ProvisionedPrefix string `json:"provisioned_prefix"`
	// SDKPackageJSON is the operator-provisioned SDK package metadata.
	SDKPackageJSON string `json:"sdk_package_json"`
	// SDKPackageName is the package the operator provisions. It is reported so a
	// script can name the missing SDK without restating a name the layout contract
	// owns.
	SDKPackageName string `json:"sdk_package_name"`
	// SDKProvisionCommand is the one command an operator runs, with the install root
	// spelled as a placeholder.
	SDKProvisionCommand string   `json:"sdk_provision_command"`
	PrivateRuntime      string   `json:"private_runtime"`
	PrivateRuntimeDir   string   `json:"private_runtime_dir"`
	PrivateNPMRoot      string   `json:"private_npm_root"`
	PrivateNPMCLI       string   `json:"private_npm_cli"`
	LauncherName        string   `json:"launcher_name"`
	PrivateRuntimeDoc   string   `json:"private_runtime_doc"`
	PrivatePrefix       string   `json:"private_prefix"`
	Compatibility       string   `json:"compatibility"`
	Checksums           string   `json:"checksums"`
	LicensesDir         string   `json:"licenses_dir"`
	RequiredEntries     []string `json:"required_entries"`
	PrivateEntries      []string `json:"private_entries"`
	TreeStates          []string `json:"tree_states"`
	DeclaredPlatforms   []string `json:"declared_platforms"`
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
		Schema:              reportSchema,
		Platform:            archive.Platform(),
		OS:                  archive.OS(),
		Arch:                archive.Arch(),
		ExeSuffix:           archive.ExeSuffix(),
		Manifest:            archive.ManifestPath(),
		OuterExecutable:     archive.OuterExecutablePath(),
		Launcher:            archive.LauncherPath(),
		BridgePackageDir:    archive.BridgePackageDirPath(),
		BridgeEntry:         archive.BridgeEntryPath(),
		BridgeDist:          archive.BridgeDistPath(),
		BridgePackageJSON:   archive.BridgePackageJSONPath(),
		BridgePackageLock:   archive.BridgePackageLockPath(),
		BridgeModules:       archive.BridgeModulesPath(),
		ProvisionedPrefix:   packagelayout.ProvisionedPrefix,
		SDKPackageJSON:      archive.ProvisionedSDKPackageJSONPath(),
		SDKPackageName:      packagelayout.SDKPackageName,
		SDKProvisionCommand: archive.ProvisionCommand(""),
		PrivateRuntime:      archive.PrivateRuntimePath(),
		PrivateRuntimeDir:   archive.PrivateRuntimeDirPath(),
		PrivateNPMRoot:      archive.PrivateRuntimeNPMRootPath(),
		PrivateNPMCLI:       archive.PrivateRuntimeNPMCLIPath(),
		LauncherName:        packagelayout.LauncherName,
		PrivateRuntimeDoc:   packagelayout.RuntimeRelDoc,
		PrivatePrefix:       packagelayout.PrivatePrefix,
		Compatibility:       archive.CompatibilityPath(),
		Checksums:           archive.ChecksumsPath(),
		LicensesDir:         archive.LicensesDir(),
		RequiredEntries:     archive.RequiredEntries(),
		PrivateEntries:      archive.PrivateEntries(),
		TreeStates:          slices.Clone(treeStates),
		DeclaredPlatforms:   packagelayout.SupportedPlatforms(),
		// ChecksumSeparator is the digest/path separator of checksums.sha256, in
		// the sha256sum order "<digest><separator><install-root-relative path>".
		ChecksumSeparator: packagelayout.ChecksumSeparator,
	}
	// The report is rendered the way the packager writes its own metadata, with HTML
	// escaping off. The provisioning command contains `&&` and the documented
	// placeholder `<plugin-root>`, and a shell script that reads the field out of this
	// report with a plain text reader would receive the escaped spelling instead of
	// the command an operator has to run.
	body, err := marshalJSON(rep)
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
