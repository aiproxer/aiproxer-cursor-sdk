package pinnedcontracts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	hostModule = "github.com/matdev83/go-llm-interactive-proxy"
	acpModule  = hostModule + "/connector-support/acp"
	pluginMod  = "github.com/aiproxer/aiproxer-cursor-sdk"
)

// TestModuleResolvesOnlyPublishedContracts guards the standalone boundary: this
// module must build from released Go-LIP versions alone, so a replace directive
// or an unpublished placeholder version fails instead of silently resolving
// through a sibling checkout.
func TestModuleResolvesOnlyPublishedContracts(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	var (
		modulePath string
		required   = map[string]string{}
		inBlock    bool
	)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "replace"):
			t.Fatalf("go.mod must not replace dependencies; found %q", line)
		case strings.HasPrefix(line, "require ("):
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case strings.HasPrefix(line, "require "):
			recordRequire(t, required, strings.TrimSpace(strings.TrimPrefix(line, "require ")))
		case inBlock:
			recordRequire(t, required, line)
		case strings.HasPrefix(line, "module "):
			modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}

	if modulePath != pluginMod {
		t.Errorf("module path = %q, want %q", modulePath, pluginMod)
	}
	for _, want := range []string{hostModule, acpModule} {
		version, ok := required[want]
		if !ok {
			t.Errorf("go.mod must require %s", want)
			continue
		}
		if version == "" || version == "v0.0.0" {
			t.Errorf("require %s %q is not a released version", want, version)
		}
	}
}

func recordRequire(t *testing.T, required map[string]string, line string) {
	t.Helper()

	path, version, ok := strings.Cut(line, " ")
	if !ok {
		t.Fatalf("malformed require line %q", line)
	}
	required[path] = strings.TrimSpace(version)
}

func moduleRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
