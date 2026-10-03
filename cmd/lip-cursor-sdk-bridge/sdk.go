package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
)

// The Cursor SDK is not redistributed by this plugin, so an installed tree does not
// contain it until the operator provisions it against the shipped private runtime.
//
// The launcher checks that before it starts the runtime, for one reason: without the
// SDK the bridge fails inside the JavaScript runtime with a module-resolution stack
// that names nothing an operator can act on, and the connector reports that as a
// provider failure rather than as a missing prerequisite. Here it is one line, it names
// the SDK, and it prints the command that provisions it.
//
// The launcher still never installs anything itself. It runs no shell, no npm, and no
// download, and it does not look for a global Node or npm: the only command it names is
// the one the archive is built for, and the operator runs it.

// bridgeManifestRelPath is the slash-separated archive location of the shipped bridge
// manifest, quoted in the diagnostic when it is missing.
var bridgeManifestRelPath = packagelayout.PrivatePrefix + "bridge/package.json"

// preflightSDK decides whether the operator-provisioned SDK tree can serve a request,
// before any process is created.
//
// The pinned version is read from the shipped bridge manifest rather than from a
// constant in this binary, so the launcher and the bridge cannot disagree about what
// the install tree is supposed to contain, and an archive that upgrades its pin needs no
// launcher change.
func preflightSDK(priv packagelayout.Private) error {
	pinned, err := bridgeManifestSDKPin(filepath.Join(priv.PackageDir, "package.json"))
	if err != nil {
		return err
	}

	version, err := provisionedSDKVersion(priv.SDKPackageJSON)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: %s is not provisioned: %q not found, and this archive ships "+
				"no %s; required version %s. Provision it once with:\n  %s",
			packagelayout.SDKPackageName, priv.SDKPackageJSON, packagelayout.SDKPackageName,
			pinned, priv.ProvisionCommand)
	case errors.Is(err, errSDKMetadataUnreadable):
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: provisioned %s metadata %q is unreadable (%v), so the launcher cannot "+
				"confirm the pinned %s version; re-provision it with:\n  %s",
			packagelayout.SDKPackageName, priv.SDKPackageJSON, err, pinned, priv.ProvisionCommand)
	case err != nil:
		return err
	case version != pinned:
		return fmt.Errorf(
			"lip-cursor-sdk-bridge: provisioned %s is %s but the bridge pins %s; provision the pinned "+
				"version with:\n  %s",
			packagelayout.SDKPackageName, version, pinned, priv.ProvisionCommand)
	}
	return nil
}

// errSDKMetadataUnreadable marks provisioned package metadata that exists but does not
// say which version it is. It is a different remedy from an absent package, so it is a
// different diagnostic.
var errSDKMetadataUnreadable = errors.New("provisioned SDK metadata is unreadable")

// bridgeManifestSDKPin reads the exact SDK version the bridge verifies at run time.
func bridgeManifestSDKPin(path string) (string, error) {
	manifest, err := readPackageJSON(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf(
				"lip-cursor-sdk-bridge: shipped bridge manifest %q not found (expected %s); "+
					"reinstall the Cursor plugin package", path, bridgeManifestRelPath)
		}
		return "", fmt.Errorf(
			"lip-cursor-sdk-bridge: shipped bridge manifest %q is unreadable (expected %s); "+
				"reinstall the Cursor plugin package: %w", path, bridgeManifestRelPath, err)
	}
	dependencies, ok := manifest["dependencies"].(map[string]any)
	if !ok {
		return "", fmt.Errorf(
			"lip-cursor-sdk-bridge: shipped bridge manifest %q pins no %s (expected %s); "+
				"reinstall the Cursor plugin package", path, packagelayout.SDKPackageName, bridgeManifestRelPath)
	}
	pinned, ok := dependencies[packagelayout.SDKPackageName].(string)
	if !ok || strings.TrimSpace(pinned) == "" {
		return "", fmt.Errorf(
			"lip-cursor-sdk-bridge: shipped bridge manifest %q does not pin %s (expected %s); "+
				"reinstall the Cursor plugin package", path, packagelayout.SDKPackageName, bridgeManifestRelPath)
	}
	return pinned, nil
}

// provisionedSDKVersion reads the version the operator installed, without importing the
// package: the metadata is the fact, and importing it would be the thing this check
// exists to avoid.
//
// A package manifest that exists but does not say which version it is is unreadable
// evidence, not an empty version: an empty string would match no pin, and reporting it
// as "missing" would send the operator to the wrong remedy.
func provisionedSDKVersion(path string) (string, error) {
	record, err := readPackageJSON(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("%w: %w", errSDKMetadataUnreadable, err)
	}
	version, ok := record["version"].(string)
	if !ok || strings.TrimSpace(version) == "" {
		return "", fmt.Errorf("%w: the manifest declares no version", errSDKMetadataUnreadable)
	}
	return version, nil
}

// readPackageJSON reads one package manifest. A missing file keeps its os.ErrNotExist
// identity so a caller can tell it apart from unreadable content.
func readPackageJSON(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.ToSlash(path), err)
	}
	return out, nil
}
