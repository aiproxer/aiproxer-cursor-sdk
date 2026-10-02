package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The packaged release layout places this launcher at private/bridge/ and the
// plugin-private Node runtime at private/node/. Both are fixed plugin-private
// names resolved relative to the launcher's own location: the launcher never
// consults PATH, a shell, npm, a global binary directory, or the current
// working directory, and it never downloads or installs anything.
//
// The bridge entry is the bridge package's own bin/ shim, not dist/main.js,
// because the connector's tool contract includes the shim's `--version` and
// `doctor` handling. The shim resolves dist/, node_modules/, and package.json
// relative to its own parent directory, which is exactly private/bridge/.
const (
	launcherName    = "lip-cursor-sdk-bridge"
	runtimeFileBase = "node"
	entryDirName    = "bin"
	bridgeEntryName = "lip-cursor-sdk-bridge.js"
)

// privateRuntimeRelPath and privateEntryRelPath are the slash-separated archive
// locations quoted verbatim in operator diagnostics.
const (
	privateRuntimeRelPath = "private/node/node[.exe]"
	privateEntryRelPath   = "private/bridge/bin/" + bridgeEntryName
)

// privateLayout is the resolved plugin-private runtime contract.
type privateLayout struct {
	// Root is the private bridge directory holding the launcher.
	Root string
	// Runtime is the fixed private Node executable.
	Runtime string
	// Entry is the fixed bridge entry the private runtime executes.
	Entry string
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
	self := strings.TrimSpace(launcherExecutable)
	if self == "" {
		return privateLayout{}, errors.New("lip-cursor-sdk-bridge: cannot locate the launcher executable")
	}
	root := filepath.Dir(filepath.Clean(self))
	lay := privateLayout{
		Root:    root,
		Runtime: filepath.Clean(filepath.Join(root, "..", runtimeFileBase, runtimeFileBase+platformExeSuffix())),
		Entry:   filepath.Clean(filepath.Join(root, entryDirName, bridgeEntryName)),
	}
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
// private runtime slot before any process is created.
func requirePrivateRuntimeFile(path, rel string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q not found (expected %s next to the installed plugin bridge; reinstall the Cursor plugin package)",
			path, rel)
	case err != nil:
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q is unusable (expected %s next to the installed plugin bridge): %w",
			path, rel, err)
	case info.IsDir():
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q is a directory (expected %s)",
			path, rel)
	case !info.Mode().IsRegular():
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: private runtime file %q is not a regular file (expected %s)",
			path, rel)
	}
	return nil
}

// platformExeSuffix is the executable suffix of the current platform.
func platformExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
