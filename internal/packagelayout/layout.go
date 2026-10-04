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

	// ModulesDirName is the npm dependency directory name. Inside the bridge package
	// directory it is operator-provisioned and never shipped: the proprietary Cursor
	// SDK is not redistributed, so the archive stages the manifest and the lockfile
	// that pin it and nothing else. Inside the private runtime directory it is the
	// runtime's own bundled npm, which does ship, because provisioning has to run
	// without a global package manager. It is named here rather than in a packaging
	// script so that a script which has to know it reads it from this contract
	// instead of restating it.
	ModulesDirName = "node_modules"

	// SDKPackageName is the proprietary package the operator provisions.
	SDKPackageName = "@cursor/sdk"

	// BridgeDistDirName is the built production JavaScript directory inside the bridge
	// package directory. Both packagers have to name it in the source tree as well as
	// in the archive, and the source-tree name is [Archive.BridgeDistPath] reduced to
	// its last element, so it is exported here for the same reason ModulesDirName is:
	// a script derives the path from this contract rather than spelling the
	// directory out, and a static guard can then see that it did.
	BridgeDistDirName = "dist"

	// BridgeLockFileName is the lockfile that pins the SDK version and the undici
	// override. The archive ships it so the operator's `npm ci` resolves exactly the
	// versions the bridge verifies at run time.
	BridgeLockFileName = "package-lock.json"

	binDirName            = "bin"
	privateDirName        = "private"
	bridgeDirName         = "bridge"
	runtimeDirName        = "node"
	bridgePackageJSONName = "package.json"
	// licenseFileName is the name npm's own license text carries inside its package
	// directory. npm is licensed separately from Node.js - its LICENSE states that the
	// npm application is licensed under the Artistic License 2.0 and that npm's bundled
	// Node package dependencies are licensed on their respective license terms - so the
	// notice names this file rather than the Node distribution license.
	licenseFileName = "LICENSE"
	// npmDirName is the runtime's own bundled npm, and libDirName is the POSIX
	// distribution's location of it: node-vXX-linux-x64 keeps the runtime in bin/ and
	// npm under lib/, while node-vXX-win-x64 keeps both at the distribution root. The
	// archive stages npm where its own distribution keeps it, so nothing inside the
	// npm tree is renamed and the entry point the provisioning command names is the
	// real one.
	npmDirName    = "npm"
	libDirName    = "lib"
	npmCLIName    = "npm-cli.js"
	npmBinDirName = "bin"
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
var entryRelPath = PrivatePrefix + bridgeDirName + "/" + BridgeEntrySourceRel()

// BridgeEntrySourceRel is the bridge entry relative to the bridge package directory.
// The bridge source tree stages the same relative path under bridge-node/, which is
// why the packager derives the file it copies from this and never names the entry
// directory itself.
func BridgeEntrySourceRel() string { return binDirName + "/" + BridgeEntryName }

// supportedPlatforms is the set of platforms the packaging pipeline can natively
// assemble and the manifest template may declare.
//
// Those are the same set on purpose. A platform becomes one only by being assembled
// and run on its own native runner by the `package` lane in
// .github/workflows/verify.yml: cross-compilation is not native validation, so an
// architecture with no runner cannot be assembled, cannot be verified, and therefore
// cannot be claimed. Listing an unassembled platform here would let the renderer
// resolve an archive layout for it and hand it to a hand-built staged tree, which is
// exactly how an unvalidated platform gets claimed by accident. Declaring a new
// platform means adding its native matrix leg and this entry in the same change, and
// the root test tying this set to the template and to the CI matrix holds the three
// together.
var supportedPlatforms = []string{"windows/amd64", "linux/amd64"}

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
			"packagelayout: platform %q is not a natively assembled plugin platform (assembled: %s); "+
				"assemble it on its own native runner, validate the archive there, and declare it in the "+
				"manifest template and this contract in the same change",
			platform, strings.Join(supportedPlatforms, ", "))
	}
	return Archive{goos: goos, goarch: goarch}, nil
}

// SupportedPlatforms lists the plugin platforms the packaging pipeline can natively
// assemble, and therefore the ones the manifest template may declare.
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
func (a Archive) BridgeDistPath() string { return a.BridgePackageDirPath() + "/" + BridgeDistDirName }

// BridgeModulesPath is the operator-provisioned npm dependency tree of the bridge
// package. It is not archive content: the Cursor SDK is proprietary and is not
// redistributed, so the archive ships the manifest and the lockfile and the operator
// resolves the tree. Everything that has to talk about the tree resolves it here.
func (a Archive) BridgeModulesPath() string {
	return a.BridgePackageDirPath() + "/" + ModulesDirName
}

// BridgePackageJSONPath is the bridge package manifest the entrypoint reads to
// resolve its own version and to verify the installed SDK version.
func (a Archive) BridgePackageJSONPath() string {
	return a.BridgePackageDirPath() + "/" + bridgePackageJSONName
}

// BridgePackageLockPath is the shipped lockfile that pins the SDK and the undici
// security override. It is archive content and required: without it the operator
// could not provision a pinned, reproducible tree.
func (a Archive) BridgePackageLockPath() string {
	return a.BridgePackageDirPath() + "/" + BridgeLockFileName
}

// ProvisionedSDKDirPath is the operator-provisioned SDK package directory.
func (a Archive) ProvisionedSDKDirPath() string {
	return a.BridgeModulesPath() + "/" + SDKPackageName
}

// ProvisionedSDKPackageJSONPath is the provisioned SDK package metadata the bridge
// entry and the run-time preflight read to learn the installed SDK version.
func (a Archive) ProvisionedSDKPackageJSONPath() string {
	return a.ProvisionedSDKDirPath() + "/" + bridgePackageJSONName
}

// PrivateRuntimeDirPath is the plugin-private runtime directory. It holds the
// runtime executable and that runtime's own bundled npm.
func (a Archive) PrivateRuntimeDirPath() string {
	return PrivatePrefix + runtimeDirName
}

// PrivateRuntimeNPMRel is the bundled npm tree's path relative to the private
// runtime directory, which is where the platform's own Node distribution keeps it.
func (a Archive) PrivateRuntimeNPMRel() string {
	if a.goos == "windows" {
		return ModulesDirName + "/" + npmDirName
	}
	return libDirName + "/" + ModulesDirName + "/" + npmDirName
}

// PrivateRuntimeNPMRootPath is the shipped npm package root.
func (a Archive) PrivateRuntimeNPMRootPath() string {
	return a.PrivateRuntimeDirPath() + "/" + a.PrivateRuntimeNPMRel()
}

// PrivateRuntimeNPMCLIPath is the npm entry point the provisioning command names.
func (a Archive) PrivateRuntimeNPMCLIPath() string {
	return a.PrivateRuntimeNPMRootPath() + "/" + npmBinDirName + "/" + npmCLIName
}

// PrivateRuntimeNPMLicensePath is npm's own license text, which ships inside the
// staged npm tree rather than being collected into the notice directory.
//
// It is a separate archive location from the Node distribution license for the same
// reason npm is a separate license: the staged LICENSE states that the npm application
// is licensed under the Artistic License 2.0 and that npm's bundled Node package
// dependencies are licensed on their respective license terms, so the Node.js MIT
// grant does not reach either. The notice points here rather than at
// LICENSES/nodejs-LICENSE, which carries the Node grant.
func (a Archive) PrivateRuntimeNPMLicensePath() string {
	return a.PrivateRuntimeNPMRootPath() + "/" + licenseFileName
}

// PrivateRuntimeNPMModulesPath is the dependency directory npm bundles inside its own
// package, and this archive ships it with npm. Each package in it carries its own
// license text in its own package directory, so the notice attributes each bundled
// component to that text rather than to a single distribution-wide license.
func (a Archive) PrivateRuntimeNPMModulesPath() string {
	return a.PrivateRuntimeNPMRootPath() + "/" + ModulesDirName
}

// provisionCommandDocPrefix is the placeholder a documented provisioning command
// carries in place of the install root, which documentation and release metadata do
// not know. An operator substitutes it; the launcher prints the resolved root.
const provisionCommandDocPrefix = "<plugin-root>"

// provisionArgs is the npm invocation that provisions the pinned tree. --omit=dev
// keeps the development toolchain out of an install tree, and `ci` installs exactly
// what the shipped lockfile pins.
//
// npm specifically, and not any package manager: the `overrides` block in the bridge
// manifest is what pins the undici security fix, and overrides are npm semantics that
// another package manager resolves differently. A tree provisioned with one is
// unsupported.
const provisionArgs = "ci --omit=dev"

// ProvisionCommand is the one command an operator runs to provision the Cursor SDK
// into an installed tree at installRoot. Both operands are inside the archive and
// relative to the directory the command changes into, so the command needs no global
// Node and no global npm: it runs the runtime the archive ships, through that
// runtime's own bundled npm. An empty installRoot renders the documented placeholder
// spelling.
func (a Archive) ProvisionCommand(installRoot string) string {
	fromBridgeDir := "../" + runtimeDirName + "/"
	runtime := fromBridgeDir + PrivateRuntimeName + a.ExeSuffix()
	npmCLI := fromBridgeDir + a.PrivateRuntimeNPMRel() + "/" + npmBinDirName + "/" + npmCLIName

	dir := a.BridgePackageDirPath()
	if installRoot != "" {
		dir = strings.TrimRight(filepath.ToSlash(installRoot), "/") + "/" + dir
	} else {
		dir = provisionCommandDocPrefix + "/" + dir
	}
	return "cd " + dir + " && " + runtime + " " + npmCLI + " " + provisionArgs
}

// PrivateRuntimePath is the private Node runtime executable.
func (a Archive) PrivateRuntimePath() string {
	return a.PrivateRuntimeDirPath() + "/" + PrivateRuntimeName + a.ExeSuffix()
}

// ProvisionedPrefix is the install-root-relative prefix of every operator-provisioned
// file, with its trailing separator. It is platform-independent because the
// provisioned tree lives in the bridge package directory. The shipped checksum
// record covers shipped files only, so this prefix is exactly the scope outside it:
// the plugin authenticates what it ships and the operator authenticates what they
// provisioned.
const ProvisionedPrefix = PrivatePrefix + bridgeDirName + "/" + ModulesDirName + "/"

// RequiredEntries is the exact set of install-root entries an installable
// archive must contain. Directories are included because an archive without the
// production JavaScript or the runtime's bundled npm is not installable even when
// every fixed file is present.
//
// The operator-provisioned dependency tree is deliberately absent: it is not archive
// content, and requiring it would make the archive claim a bundle it must not ship.
func (a Archive) RequiredEntries() []string {
	entries := []string{
		a.ManifestPath(),
		a.OuterExecutablePath(),
		a.LauncherPath(),
		a.BridgeEntryPath(),
		a.BridgeDistPath(),
		a.BridgePackageJSONPath(),
		a.BridgePackageLockPath(),
		a.PrivateRuntimePath(),
		a.PrivateRuntimeNPMCLIPath(),
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
	// PackageDir is the bridge package directory. It holds the shipped bridge
	// manifest that names the pinned SDK version and is the directory the
	// provisioning command changes into.
	PackageDir string
	// SDKPackageJSON is the operator-provisioned SDK package metadata, absent until
	// the operator provisions it.
	SDKPackageJSON string
	// NPMCLI is the shipped npm entry point the provisioning command runs.
	NPMCLI string
	// ProvisionedPrefix is the install-root-relative prefix of every
	// operator-provisioned file, with its trailing separator.
	ProvisionedPrefix string
	// ProvisionCommand is the exact command that provisions the pinned SDK into this
	// install tree, with the resolved package directory.
	ProvisionCommand string
}

// ErrUnknownLauncherPath marks a launcher path that cannot name a private
// runtime root at all, as opposed to a resolved slot that turns out to be
// unusable.
var ErrUnknownLauncherPath = errors.New("cannot locate the launcher executable")

// PrivateFor resolves the private runtime, the bridge entry, and the provisioning
// slots for a launcher executable installed at a plugin-private archive location. It
// touches no filesystem: checking the slots is a separate step so a caller can report
// an unusable launcher path without probing a guessed root.
func PrivateFor(launcherExecutable, goos string) (Private, error) {
	self := strings.TrimSpace(launcherExecutable)
	if self == "" {
		return Private{}, fmt.Errorf("packagelayout: %w", ErrUnknownLauncherPath)
	}
	// The launcher lives in private/bridge/, so the runtime is its sibling
	// private/node/ directory and the entry is its own bin/ subdirectory.
	root := filepath.Dir(filepath.Clean(self))
	installRoot := filepath.Clean(filepath.Join(root, "..", ".."))
	suffix := ExeSuffixFor(goos)
	packageDir := filepath.Clean(root)
	// The npm tree is staged where the platform's Node distribution keeps it, so the
	// entry point the provisioning command names is the real one rather than a path
	// invented for the archive.
	npmRel := ModulesDirName + "/" + npmDirName
	if goos != "windows" {
		npmRel = libDirName + "/" + ModulesDirName + "/" + npmDirName
	}
	archive := Archive{goos: goos}
	return Private{
		Runtime:    filepath.Join(root, "..", runtimeDirName, PrivateRuntimeName+suffix),
		Entry:      filepath.Join(root, binDirName, BridgeEntryName),
		RuntimeRel: PrivatePrefix + runtimeDirName + "/" + PrivateRuntimeName + suffix,
		EntryRel:   entryRelPath,
		PackageDir: packageDir,
		SDKPackageJSON: filepath.Join(packageDir, ModulesDirName,
			filepath.FromSlash(SDKPackageName), bridgePackageJSONName),
		NPMCLI:            filepath.Join(root, "..", runtimeDirName, filepath.FromSlash(npmRel), npmBinDirName, npmCLIName),
		ProvisionedPrefix: archive.BridgeModulesPath() + "/",
		ProvisionCommand:  archive.ProvisionCommand(filepath.ToSlash(installRoot)),
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
