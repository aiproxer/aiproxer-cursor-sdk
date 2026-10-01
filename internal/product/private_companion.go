package product

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// privateCompanionRelDir is the packaged plugin-private bridge directory,
// relative to the directory holding the installed outer plugin executable. It
// mirrors the release archive layout: bin/lip-backend-cursorsdk[.exe] next to
// private/bridge/lip-cursor-sdk-bridge[.exe].
const privateCompanionRelDir = "../private/bridge"

// privateCompanionRelPath returns the packaged private bridge launcher path
// relative to the outer plugin executable directory. It is always
// slash-separated so diagnostics read identically on every platform.
func privateCompanionRelPath() string {
	name := DefaultBridgeExecutable
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return privateCompanionRelDir + "/" + name
}

// defaultPrivateCompanionPath resolves the packaged default bridge launcher for
// the running outer plugin executable. Resolution is a direct plugin-local
// path: it never consults PATH, a shell, npm, a global binary directory, or the
// current working directory, and it never downloads or falls back to another
// Cursor integration.
func defaultPrivateCompanionPath() (string, error) {
	outer, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cursorsdk: cannot locate the outer plugin executable: %w", err)
	}
	return privateCompanionPathFor(outer)
}

// privateCompanionPathFor is [defaultPrivateCompanionPath] for an explicit outer
// executable path. A missing companion is an explicit prerequisite failure that
// names the expected relative location.
func privateCompanionPathFor(outerExecutable string) (string, error) {
	outer := strings.TrimSpace(outerExecutable)
	if outer == "" {
		return "", errors.New("cursorsdk: cannot locate the outer plugin executable for private bridge resolution")
	}
	rel := privateCompanionRelPath()
	candidate := filepath.Clean(filepath.Join(filepath.Dir(outer), filepath.FromSlash(rel)))
	if info, err := os.Stat(candidate); err != nil || info.IsDir() {
		return "", fmt.Errorf(
			"cursorsdk: private bridge launcher %q not found (expected %s next to the installed plugin executable; reinstall the Cursor plugin package or set bridge_executable to a direct bridge binary)",
			candidate, rel)
	}
	return candidate, nil
}
