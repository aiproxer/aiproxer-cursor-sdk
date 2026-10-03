package cursorsdk_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// nativeRunnerPlatforms maps a CI runner label to the single os/arch pair a native
// job on that runner assembles and validates.
//
// The map is closed on purpose. An unrecognised label has to fail this test rather
// than be skipped: a matrix leg nobody can map is a platform claim whose native
// evidence cannot be located, and silently ignoring it would let a new platform be
// declared without anyone deciding what proves it. Adding a platform means adding
// its runner here and a native leg in the `package` matrix, in the same change.
var nativeRunnerPlatforms = map[string]string{
	"ubuntu-latest":  "linux/amd64",
	"windows-latest": "windows/amd64",
}

// TestManifestScope_TemplateDeclaresExactlyThePlatformsThePipelineAssembles keeps
// the manifest's platform claim and the pipeline's ability to assemble an artifact
// for it from drifting apart.
//
// The renderer narrows every assembled archive to the platform it was assembled on,
// so an overclaimed template does not produce a lying archive by itself. It is still
// an overclaim: the template is the plugin's published statement of which platforms
// it supports, the layout contract's error message names exactly these platforms,
// and an operator reads the template - not one rendered archive - to decide whether
// the plugin runs on their machine. Declaring a platform the pipeline cannot
// assemble natively is how an unvalidated claim gets made by accident, so the
// declared set, the layout contract's set, and the native CI runners' set have to
// be the same set.
func TestManifestScope_TemplateDeclaresExactlyThePlatformsThePipelineAssembles(t *testing.T) {
	t.Parallel()

	declared := manifestTemplatePlatforms(t)
	assembled := nativePackageMatrixPlatforms(t)

	require.Equal(t, assembled, declared,
		"manifest/template.backendplugin.json declares %v, but the packaging pipeline can only "+
			"natively assemble %v; a platform nobody assembles natively may not be declared",
		declared, assembled)

	contract := packagelayout.SupportedPlatforms()
	slices.Sort(contract)
	require.Equal(t, assembled, contract,
		"internal/packagelayout declares %v, but the packaging pipeline can only natively "+
			"assemble %v; the renderer resolves an archive layout from this set, so a platform "+
			"the pipeline cannot assemble must not be resolvable here either",
		contract, assembled)
}

// TestManifestScope_RendererRefusesAPlatformThePipelineCannotAssemble keeps the
// renderer from emitting a manifest for a platform no archive exists for.
//
// The packaging scripts refuse to cross-compile, but the renderer is a separate
// entry point a maintainer can invoke directly with a hand-built staged tree. It has
// to resolve an archive layout for exactly the natively assembled platforms and for
// nothing else, in both directions: accepting one it cannot assemble lets a rendered
// manifest advertise support that no artifact and no native run ever backed, and
// refusing one it can assemble breaks the pipeline on its own platform.
func TestManifestScope_RendererRefusesAPlatformThePipelineCannotAssemble(t *testing.T) {
	t.Parallel()

	assembled := packagelayout.SupportedPlatforms()
	require.NotEmpty(t, assembled, "no platform is declared assemblable")

	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64", "386"} {
			platform := goos + "/" + goarch
			t.Run(platform, func(t *testing.T) {
				t.Parallel()

				archive, err := packagelayout.ForPlatform(goos, goarch)
				if !slices.Contains(assembled, platform) {
					require.Error(t, err,
						"%s is not a natively assembled platform; an archive layout it names could "+
							"only come from a cross-compiled claim", platform)
					require.Contains(t, err.Error(), platform)
					require.Empty(t, archive.OS())
					require.Empty(t, archive.Arch())
					return
				}

				require.NoError(t, err,
					"%s is assembled natively by the package matrix; refusing it would break the "+
						"pipeline on its own platform", platform)
				require.Equal(t, platform, archive.Platform())
			})
		}
	}
}

// manifestTemplatePlatforms reads the platform set the manifest template declares.
// The result is sorted: this is a set comparison, and the order the template happens
// to list its platforms in is presentation, not a contract.
func manifestTemplatePlatforms(tb testing.TB) []string {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(repoRoot(tb), "manifest", "template.backendplugin.json"))
	var doc struct {
		Platforms []struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"platforms"`
	}
	require.NoError(tb, json.Unmarshal([]byte(raw), &doc))
	require.NotEmpty(tb, doc.Platforms, "the template declares no platform at all")

	out := make([]string, 0, len(doc.Platforms))
	for _, p := range doc.Platforms {
		out = append(out, p.OS+"/"+p.Arch)
	}
	slices.Sort(out)
	return out
}

// nativePackageMatrixPlatforms reads the platforms the `package` lane natively
// assembles, from the runner matrix that lane declares in the verify workflow.
func nativePackageMatrixPlatforms(tb testing.TB) []string {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(repoRoot(tb), ".github", "workflows", "verify.yml"))

	var workflow struct {
		Jobs struct {
			Package struct {
				Strategy struct {
					Matrix struct {
						OS []string `yaml:"os"`
					} `yaml:"matrix"`
				} `yaml:"strategy"`
			} `yaml:"package"`
		} `yaml:"jobs"`
	}
	require.NoError(tb, yaml.Unmarshal([]byte(raw), &workflow))
	require.NotEmpty(tb, workflow.Jobs.Package.Strategy.Matrix.OS,
		"the verify workflow's package job declares no runner matrix, so no platform has native evidence")

	var out []string
	for _, runner := range workflow.Jobs.Package.Strategy.Matrix.OS {
		platform, ok := nativeRunnerPlatforms[runner]
		require.True(tb, ok,
			"the package matrix runner %q is not in nativeRunnerPlatforms; map it to the platform a "+
				"native job on it assembles, or remove the leg. An unmappable runner cannot be used as "+
				"evidence for any platform claim", runner)
		require.NotContains(tb, out, platform,
			"the package matrix assembles %s on more than one runner", platform)
		out = append(out, platform)
	}
	slices.Sort(out)
	return out
}
