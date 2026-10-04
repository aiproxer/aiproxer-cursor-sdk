package cursorsdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the standalone-module contract: the guarantee that this plugin
// resolves its host contracts from published releases and nothing else.
//
// `go mod tidy -diff` in the verify lane is not that guarantee. Tidy accepts a
// `replace` directive and keeps it, and it resolves a workspace when one is present
// and says nothing about it. Either one would make the whole suite pass while the
// bytes under test were built against a sibling checkout, which is exactly the
// dependency this repository exists to remove. So the two absences are asserted here,
// where every lane runs them.

// TestStandaloneModule_HasNoReplaceDirective keeps the plugin's build honest about
// where its host contracts come from.
func TestStandaloneModule_HasNoReplaceDirective(t *testing.T) {
	t.Parallel()

	body := readFileText(t, filepath.Join(repoRoot(t), "go.mod"))
	directives := replaceDirectives(t, body)

	assert.Empty(t, directives,
		"go.mod replaces %v. The release record names published module versions, so a replaced "+
			"module means the certified bytes were not built from the contract the record claims. "+
			"`go mod tidy -diff` accepts a replace and will not catch this", directives)
}

// replaceDirectives reports every replace the manifest declares. Both the parenthesised
// block form and the single-line form are accepted by the toolchain, so both are read.
func replaceDirectives(tb testing.TB, body string) []string {
	tb.Helper()

	var out []string
	inBlock := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "replace ("):
			inBlock = true
			out = append(out, trimmed)
		case inBlock && trimmed == ")":
			inBlock = false
		case strings.HasPrefix(trimmed, "replace "):
			out = append(out, trimmed)
		case inBlock:
			out = append(out, trimmed)
		}
	}
	return out
}

// TestStandaloneModule_HasNoWorkspaceAboveIt keeps a workspace from quietly supplying
// the dependency graph.
//
// The toolchain searches every parent directory for `go.work`, so a workspace anywhere
// above this checkout would change what the build resolves while leaving every file in
// this repository untouched. `GOWORK=off` in CI prevents that on the runner; this
// assertion is what makes a developer's local run mean the same thing as the lane's.
func TestStandaloneModule_HasNoWorkspaceAboveIt(t *testing.T) {
	t.Parallel()

	for _, dir := range ancestorsAbove(repoRoot(t)) {
		for _, name := range []string{"go.work", "go.work.sum"} {
			path := filepath.Join(dir, name)
			_, err := os.Stat(path)
			if err == nil {
				t.Fatalf("%s exists: the toolchain would resolve this module through a workspace "+
					"instead of the published contracts in go.mod. GOWORK=off hides it in CI and "+
					"changes nothing on a developer machine", path)
			}
			require.ErrorIs(t, err, os.ErrNotExist,
				"%s could not be inspected, so the absence of a workspace is unproven", path)
		}
	}
}

// ancestorsAbove lists the repository root and every directory above it, stopping at
// the filesystem root.
func ancestorsAbove(root string) []string {
	var out []string
	dir := filepath.Clean(root)
	for {
		out = append(out, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return out
		}
		dir = parent
	}
}

// TestStandaloneModule_ResolvesOnlyThePublishedHostContracts keeps the declared
// dependency set closed.
//
// The two host modules are the whole external surface. Anything else that resolves
// into the build from outside this repository is a dependency nobody declared, and the
// conformance suite would still pass while the artifact carried it.
func TestStandaloneModule_ResolvesOnlyThePublishedHostContracts(t *testing.T) {
	t.Parallel()

	body := readFileText(t, filepath.Join(repoRoot(t), "go.mod"))
	meta := readReleaseMetadata(t)

	require.NotEmpty(t, meta.PublishedRootModule)
	require.NotEmpty(t, meta.PublishedACPModule)

	for _, host := range []string{meta.PublishedRootModule, meta.PublishedACPModule} {
		require.Contains(t, body, host,
			"go.mod has to require %s; the certified build resolves its host contracts from it", host)
		assert.NotContains(t, body, host+"/internal/",
			"%s internals are not a public contract and are unreachable from a released module", host)
	}

	assert.NotContains(t, body, "matdev83/go-llm-interactive-proxy/connectors/",
		"the plugin must not depend on a connector module; the released host surface is pkg/ and connector-support/")
}
