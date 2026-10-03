package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
)

// The packaged release layout places this launcher at private/bridge/ and the
// plugin-private Node runtime at private/node/. Both are fixed plugin-private
// names resolved relative to the launcher's own location: the launcher never
// consults PATH, a shell, npm, a global binary directory, or the current
// working directory, and it never downloads or installs anything.
//
// The archive layout itself is not owned here. internal/packagelayout is the
// single contract that the release packaging scripts stage and the package
// verification script require, so this launcher consumes it instead of
// restating the names: a layout that moved the bridge entry, the entry
// directory, or the runtime directory would otherwise make this launcher look
// for a file the archive does not contain.
//
// The bridge entry is the bridge package's own bin/ shim, not dist/main.js,
// because the connector's tool contract includes the shim's `--version` and
// `doctor` handling. The shim resolves dist/, node_modules/, and package.json
// relative to its own parent directory, which is exactly private/bridge/.
const (
	launcherName    = packagelayout.LauncherName
	entryDirName    = "bin"
	bridgeEntryName = packagelayout.BridgeEntryName
)

// privateRuntimeRelPath and privateEntryRelPath are the slash-separated archive
// locations quoted verbatim in operator diagnostics.
const (
	privateRuntimeRelPath = packagelayout.RuntimeRelDoc
	privateEntryRelPath   = packagelayout.PrivatePrefix + "bridge/" + entryDirName + "/" + bridgeEntryName
)

// privateLayout is the resolved plugin-private runtime contract.
type privateLayout struct {
	// Runtime is the fixed private Node executable.
	Runtime string
	// Entry is the fixed bridge entry the private runtime executes.
	Entry string
	// private is the archive-level contract the same resolution produced, including the
	// operator-provisioned SDK slots and the provisioning command.
	private packagelayout.Private
}

// argv is the fixed private runtime invocation with the launcher's own arguments
// appended unchanged.
func (l privateLayout) argv(args []string) []string {
	return append([]string{l.Runtime, l.Entry}, args...)
}

// resolvePrivateLayout resolves the private runtime for the running launcher.
func resolvePrivateLayout() (privateLayout, error) {
	self, err := os.Executable()
	if err != nil {
		return privateLayout{}, fmt.Errorf("lip-cursor-sdk-bridge: cannot locate the launcher executable: %w", err)
	}
	return privateLayoutFor(self)
}

// privateLayoutFor is [resolvePrivateLayout] for an explicit launcher path. A
// missing private runtime or bridge entry is an explicit prerequisite failure
// that names the expected archive location and offers no fallback.
func privateLayoutFor(launcherExecutable string) (privateLayout, error) {
	priv, err := packagelayout.PrivateFor(launcherExecutable, runtime.GOOS)
	if err != nil {
		if errors.Is(err, packagelayout.ErrUnknownLauncherPath) {
			return privateLayout{}, errors.New("lip-cursor-sdk-bridge: cannot locate the launcher executable")
		}
		return privateLayout{}, fmt.Errorf("lip-cursor-sdk-bridge: %w", err)
	}
	lay := privateLayout{Runtime: priv.Runtime, Entry: priv.Entry, private: priv}
	if err := lay.validate(); err != nil {
		return privateLayout{}, err
	}
	return lay, nil
}

// validate keeps an incomplete private layout an explicit prerequisite failure.
// The launcher re-checks it before acquiring anything, so a launcher that owns a
// runtime always owns a complete one.
func (l privateLayout) validate() error {
	if err := requirePrivateRuntimeFile(l.Runtime, privateRuntimeRelPath); err != nil {
		return err
	}
	return requirePrivateRuntimeFile(l.Entry, privateEntryRelPath)
}

// requirePrivateRuntimeFile rejects an absent, unreadable, or non-regular
// private runtime slot before any process is created. Slot classification is
// shared with the packaging tooling; the wording stays launcher-specific and
// names the packaged location plus the operator remedy.
func requirePrivateRuntimeFile(path, rel string) error {
	err := packagelayout.CheckSlot(path, rel)
	if err == nil {
		return nil
	}
	var slot *packagelayout.SlotError
	if !errors.As(err, &slot) {
		return fmt.Errorf("lip-cursor-sdk-bridge: %w", err)
	}
	switch slot.Kind {
	case packagelayout.SlotMissing:
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q not found (expected %s next to the installed plugin bridge; reinstall the Cursor plugin package)",
			path, rel)
	case packagelayout.SlotUnusable:
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q is unusable (expected %s next to the installed plugin bridge): %w",
			path, rel, slot.Err)
	case packagelayout.SlotDirectory:
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q is a directory (expected %s)",
			path, rel)
	default:
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q is not a regular file (expected %s)",
			path, rel)
	}
}

// platformExeSuffix is the executable suffix of the current platform.
func platformExeSuffix() string {
	return strings.TrimSpace(packagelayout.ExeSuffixFor(runtime.GOOS))
}
