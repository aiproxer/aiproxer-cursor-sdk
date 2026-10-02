// Package packagelayout is the plugin's single source of truth for the release
// archive layout and for the plugin-private runtime layout the launcher resolves
// at run time.
//
// Three consumers depend on it: the packaging scripts stage exactly the files
// named here, the verification script requires exactly those files, and
// cmd/lip-cursor-sdk-bridge resolves the private Node runtime and bridge entry
// from its own location. Keeping one contract is what makes an archive
// trustworthy: a script cannot stage one layout while the launcher looks for
// another, and a layout change has to be made here rather than in three places.
//
// Every path in this package is slash-separated and relative to the plugin
// install root, because that is the spelling an operator sees in diagnostics and
// the spelling both packaging scripts must not duplicate as literals.
package packagelayout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File and directory names inside the plugin install root. The private runtime
// files are fixed plugin-private names resolved relative to the launcher: the
// runtime is never looked up on PATH, through a shell, through npm, through a
// global binary directory, or through the current working directory.
const (
	// ManifestFileName is the closed host manifest the plugin installs as.
	ManifestFileName = "plugin.backendplugin.json"
	// CompatibilityFileName holds plugin release metadata. It is plugin-release
	// metadata, not a host manifest field.
	CompatibilityFileName = "compatibility.json"
	// ChecksumsFileName covers every archive file, plugin-private files included.
	ChecksumsFileName = "checksums.sha256"
	// LicensesDirName holds runtime license and provenance notices.
	LicensesDirName = "LICENSES"

	// OuterExecutableName is the outer plugin executable the host starts.
	OuterExecutableName = "lip-backend-cursorsdk"
	// LauncherName is the plugin-private bridge launcher executable.
	LauncherName = "lip-cursor-sdk-bridge"
	// BridgeEntryName is the bridge package's own CLI shim and therefore the
	// launcher's entrypoint: `--version` and `doctor` exist only in the shim, so
	// executing dist/main.js directly would break the connector's tool contract.
	BridgeEntryName = "lip-cursor-sdk-bridge.js"
	// PrivateRuntimeName is the private Node runtime executable.
	PrivateRuntimeName = "node"

	// PrivatePrefix marks plugin-private archive content. Nothing under it is
	// authenticated by the host; the host's executable digest stays the authority
	// for the outer process only.
	PrivatePrefix = "private/"

	binDirName            = "bin"
	privateDirName        = "private"
	bridgeDirName         = "bridge"
	runtimeDirName        = "node"
	bridgeDistDirName     = "dist"
	bridgeModulesDirName  = "node_modules"
	bridgePackageJSONName = "package.json"
)

// RuntimeRelDoc is the documented, platform-agnostic spelling of the private
// runtime location. Operator diagnostics quote it verbatim, and the packaging
// scripts stage the platform-suffixed form that [Archive.PrivateRuntimePath]
// returns.
const RuntimeRelDoc = PrivatePrefix + runtimeDirName + "/" + PrivateRuntimeName + "[.exe]"

// ChecksumSeparator separates the digest from the install-root-relative path in
// a checksums.sha256 line. The record uses the sha256sum line order
// ("<digest><separator><path>") so an operator can check it with the platform
// tool of their choice, and it covers plugin-private files as well as the
// manifest and the outer executable.
const ChecksumSeparator = "  "

// entryRelPath is the archive location of the bridge entry, independent of any
// platform.
var entryRelPath = PrivatePrefix + bridgeDirName + "/" + binDirName + "/" + BridgeEntryName

// supportedPlatforms is the set of platforms the manifest template declares.
// A platform is only packageable because it is declared here; the packaging
// scripts additionally refuse to build for anything but the host platform, so a
// declared platform still needs native validation before its claim is truthful.
var supportedPlatforms = []string{"windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64"}

// ExeSuffixFor is the executable suffix of goos.
func ExeSuffixFor(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

// Archive is the release archive layout of one plugin platform.
type Archive struct {
	goos   string
	goarch string
}

// ForPlatform returns the archive layout for a declared plugin platform.
func ForPlatform(goos, goarch string) (Archive, error) {
	platform := goos + "/" + goarch
	if !slices.Contains(supportedPlatforms, platform) {
		return Archive{}, fmt.Errorf(
			"packagelayout: platform %q is not a declared plugin platform (declared: %s); "+
				"declare it in the manifest template and validate it natively before packaging",
			platform, strings.Join(supportedPlatforms, ", "))
	}
	return Archive{goos: goos, goarch: goarch}, nil
}

// SupportedPlatforms lists the declared plugin platforms.
func SupportedPlatforms() []string {
	return slices.Clone(supportedPlatforms)
}

// OS is the archive operating system.
func (a Archive) OS() string { return a.goos }

// Arch is the archive architecture.
func (a Archive) Arch() string { return a.goarch }

// Platform is the archive platform as "os/arch".
func (a Archive) Platform() string { return a.goos + "/" + a.goarch }

// ExeSuffix is the platform executable suffix.
func (a Archive) ExeSuffix() string { return ExeSuffixFor(a.goos) }

// ManifestPath is the installed host manifest.
func (a Archive) ManifestPath() string { return ManifestFileName }

// CompatibilityPath is the installed plugin release metadata.
func (a Archive) CompatibilityPath() string { return CompatibilityFileName }

// ChecksumsPath is the installed checksum record covering every archive file.
func (a Archive) ChecksumsPath() string { return ChecksumsFileName }

// LicensesDir is the installed runtime license and provenance notice directory.
func (a Archive) LicensesDir() string { return LicensesDirName }

// OuterExecutablePath is the outer plugin executable the host starts and whose
// digest the host manifest carries.
func (a Archive) OuterExecutablePath() string {
	return binDirName + "/" + OuterExecutableName + a.ExeSuffix()
}

// LauncherPath is the plugin-private bridge launcher executable.
func (a Archive) LauncherPath() string {
	return PrivatePrefix + bridgeDirName + "/" + LauncherName + a.ExeSuffix()
}

// BridgePackageDirPath is the plugin-private bridge package directory. The
// bridge entry shim resolves dist/, node_modules/, and package.json relative to
// this directory, which is why the layout is fixed.
func (a Archive) BridgePackageDirPath() string {
	return PrivatePrefix + bridgeDirName
}

// BridgeEntryPath is the bridge entrypoint the launcher executes.
func (a Archive) BridgeEntryPath() string { return entryRelPath }

// BridgeDistPath is the built production JavaScript directory.
func (a Archive) BridgeDistPath() string { return a.BridgePackageDirPath() + "/" + bridgeDistDirName }

// BridgeModulesPath is the staged production npm dependency tree.
func (a Archive) BridgeModulesPath() string {
	return a.BridgePackageDirPath() + "/" + bridgeModulesDirName
}

// BridgePackageJSONPath is the bridge package manifest the entrypoint reads to
// resolve its own version and to verify the installed SDK version.
func (a Archive) BridgePackageJSONPath() string {
	return a.BridgePackageDirPath() + "/" + bridgePackageJSONName
}

// PrivateRuntimePath is the private Node runtime executable.
func (a Archive) PrivateRuntimePath() string {
	return PrivatePrefix + runtimeDirName + "/" + PrivateRuntimeName + a.ExeSuffix()
}

// RequiredEntries is the exact set of install-root entries an installable
// archive must contain. Directories are included because an archive without the
// production JavaScript or the production dependency tree is not installable
// even when every fixed file is present.
func (a Archive) RequiredEntries() []string {
	entries := []string{
		a.ManifestPath(),
		a.OuterExecutablePath(),
		a.LauncherPath(),
		a.BridgeEntryPath(),
		a.BridgeDistPath(),
		a.BridgeModulesPath(),
		a.BridgePackageJSONPath(),
		a.PrivateRuntimePath(),
		a.CompatibilityPath(),
		a.ChecksumsPath(),
		a.LicensesDir(),
	}
	slices.Sort(entries)
	return entries
}

// PrivateEntries is the required subset that lives under the plugin-private
// prefix. These files are covered by the archive checksums but are not
// authenticated by the host.
func (a Archive) PrivateEntries() []string {
	var out []string
	for _, rel := range a.RequiredEntries() {
		if strings.HasPrefix(rel, PrivatePrefix) {
			out = append(out, rel)
		}
	}
	return out
}

// Private is the resolved plugin-private runtime contract for one installed
// launcher executable.
type Private struct {
	// Runtime is the fixed private Node runtime executable.
	Runtime string
	// Entry is the fixed bridge entry the private runtime executes.
	Entry string
	// RuntimeRel and EntryRel are the slash-separated archive locations of the
	// runtime and the entry, relative to the install root.
	RuntimeRel string
	EntryRel   string
}

// ErrUnknownLauncherPath marks a launcher path that cannot name a private
// runtime root at all, as opposed to a resolved slot that turns out to be
// unusable.
var ErrUnknownLauncherPath = errors.New("cannot locate the launcher executable")

// PrivateFor resolves the private runtime and bridge entry for a launcher
// executable installed at a plugin-private archive location. It touches no
// filesystem: checking the slots is a separate step so a caller can report an
// unusable launcher path without probing a guessed root.
func PrivateFor(launcherExecutable, goos string) (Private, error) {
	self := strings.TrimSpace(launcherExecutable)
	if self == "" {
		return Private{}, fmt.Errorf("packagelayout: %w", ErrUnknownLauncherPath)
	}
	// The launcher lives in private/bridge/, so the runtime is its sibling
	// private/node/ directory and the entry is its own bin/ subdirectory.
	root := filepath.Dir(filepath.Clean(self))
	return Private{
		Runtime:    filepath.Clean(filepath.Join(root, "..", runtimeDirName, PrivateRuntimeName+ExeSuffixFor(goos))),
		Entry:      filepath.Clean(filepath.Join(root, binDirName, BridgeEntryName)),
		RuntimeRel: PrivatePrefix + runtimeDirName + "/" + PrivateRuntimeName + ExeSuffixFor(goos),
		EntryRel:   entryRelPath,
	}, nil
}

// SlotErrorKind classifies why a plugin-private file slot is unusable.
type SlotErrorKind int

const (
	// SlotOK means the slot holds a readable regular file.
	SlotOK SlotErrorKind = iota
	// SlotMissing means nothing exists at the slot.
	SlotMissing
	// SlotUnusable means the slot could not be inspected.
	SlotUnusable
	// SlotDirectory means the slot is a directory.
	SlotDirectory
	// SlotNotRegular means the slot is a symlink, socket, or device.
	SlotNotRegular
)

// SlotError is an explicit prerequisite failure for one plugin-private file
// slot. It carries the classified kind, the resolved path, and the packaged
// location so callers can render a diagnostic that names the archive.
type SlotError struct {
	Kind SlotErrorKind
	Path string
	Rel  string
	Err  error
}

// Error names the unusable slot and its packaged location.
func (e *SlotError) Error() string {
	base := fmt.Sprintf("private runtime file %q", e.Path)
	switch e.Kind {
	case SlotMissing:
		return fmt.Sprintf("%s not found (expected %s)", base, e.Rel)
	case SlotDirectory:
		return fmt.Sprintf("%s is a directory (expected %s)", base, e.Rel)
	case SlotNotRegular:
		return fmt.Sprintf("%s is not a regular file (expected %s)", base, e.Rel)
	case SlotUnusable:
		if e.Err != nil {
			return fmt.Sprintf("%s is unusable (expected %s): %v", base, e.Rel, e.Err)
		}
		return fmt.Sprintf("%s is unusable (expected %s)", base, e.Rel)
	default:
		return fmt.Sprintf("%s is unusable (expected %s)", base, e.Rel)
	}
}

// Unwrap exposes the underlying inspection failure, if any.
func (e *SlotError) Unwrap() error { return e.Err }

// CheckSlot rejects an absent, uninspectable, or non-regular plugin-private file
// slot. rel is the packaged location to name in the failure.
func CheckSlot(path, rel string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &SlotError{Kind: SlotMissing, Path: path, Rel: rel}
	case err != nil:
		return &SlotError{Kind: SlotUnusable, Path: path, Rel: rel, Err: err}
	case info.IsDir():
		return &SlotError{Kind: SlotDirectory, Path: path, Rel: rel}
	case !info.Mode().IsRegular():
		return &SlotError{Kind: SlotNotRegular, Path: path, Rel: rel}
	}
	return nil
}
