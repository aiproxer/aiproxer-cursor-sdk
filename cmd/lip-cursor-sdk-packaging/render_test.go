package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stagedRelease is a minimal staged install root the renderer can read: the
// outer executable, the plugin-private launcher, the private runtime with its
// bundled npm, the bridge entry, the bridge manifest and lockfile, and the
// release template. The operator-provisioned dependency tree is deliberately
// absent: the renderer refuses to describe a tree that carries one.
type stagedRelease struct {
	root           string
	release        string
	template       string
	goMod          string
	exe            string
	runtime        string
	entry          string
	npmCLI         string
	bridgeJSON     string
	bridgeLock     string
	buildID        string
	platform       string
	exeSHA256      string
	nodeSource     string
	nodeSourceKind string
	nodeVersion    string
	// sourceRevision and sourceModified are the source state the harness resolved from
	// the tree it builds in, which is what a packager passes when the toolchain stamped
	// nothing into the executable. sourceModified is the tri-state the record has to be
	// able to keep: "true", "false", "unknown", or absent for "not established".
	sourceRevision string
	sourceModified string
	// testedHosts are the host artifact digests the caller supplies as release
	// evidence. A staged tree carries none by default: the packager has no host
	// artifact to certify against, and a record that named one anyway would be
	// inventing evidence.
	testedHosts []string
}

// rootModule and acpModule are the published host contracts the plugin pins, and
// hostPinVersion is the real released version this repository builds against.
const (
	rootModule     = "github.com/matdev83/go-llm-interactive-proxy"
	acpModule      = rootModule + "/connector-support/acp"
	hostPinVersion = "v0.1.0-rc.1"
)

func TestRender_PreservesManifestIdentityAndNativePlatformClaim(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)

	manifest, compatibility := renderAndRead(t, lay)

	require.Equal(t, "golip.backendplugin.manifest/v1", manifest["schema"])
	require.Equal(t, "io.golip.backend.cursorsdk", manifest["plugin_id"])
	require.Equal(t, "0.1.0", manifest["version"])
	require.Equal(t, lay.buildID, manifest["build_id"])
	require.Equal(t, lay.exeSHA256, manifest["sha256"])
	require.Equal(t, "bin/lip-backend-cursorsdk"+lay.suffix(), manifest["executable"])

	// The exported posture is the plugin's declared trust boundary: static
	// credentials, local-only access, per-instance processes, agent runtime.
	// Rendering must carry it through unchanged rather than restating it.
	exports := decodeList(t, manifest["exports"])
	require.Len(t, exports, 1)
	require.Equal(t, map[string]any{
		"kind":            "cursorsdk",
		"credential_mode": "static",
		"access_scope":    "local_only",
		"process_sharing": "per_instance",
		"execution_class": "agent_runtime",
	}, exports[0])

	// A natively assembled archive may claim only the platform it was assembled
	// and validated on. The template's cross-platform claims must not survive
	// into a single-platform artifact.
	plats := decodeList(t, manifest["platforms"])
	require.Equal(t, []any{map[string]any{"os": lay.goos(), "arch": lay.goarch()}}, plats)

	// Release metadata is plugin-release metadata, not host manifest content: the
	// host manifest stays closed and keeps no packaging fields.
	for _, key := range []string{"private", "runtime", "checksums", "compatibility", "licenses", "platform", "goos"} {
		require.NotContains(t, manifest, key)
	}

	body := mustJSON(t, manifest)
	require.NotContains(t, body, "REPLACE_")

	// The compatibility record reports the same identity plus the facts a
	// maintainer or operator needs to audit the private runtime.
	require.Equal(t, "golip.cursorsdk.compatibility/v2", compatibility["schema"])
	require.Equal(t, "io.golip.backend.cursorsdk", compatibility["plugin_id"])
	require.Equal(t, "0.1.0", compatibility["plugin_version"])
	require.Equal(t, lay.buildID, compatibility["build_id"])
	require.Equal(t, "cursorsdk-v0.1.0", compatibility["release_tag_declared"])
	require.Equal(t, "github.com/matdev83/go-llm-interactive-proxy", compatibility["published_root_module"])
	require.Equal(t, lay.platform, compatibility["platform"])
	require.Equal(t, "private-runtime", compatibility["packaging_variant"])
	require.Equal(t, false, compatibility["external_node_required"])
	require.Equal(t, float64(1), compatibility["protocol_major"])
	require.Equal(t, float64(0), compatibility["protocol_min_minor"])
	require.Equal(t, float64(0), compatibility["protocol_max_minor"])
	require.Equal(t, lay.exeSHA256, compatibility["outer_executable_sha256"])
	require.Equal(t, "bin/lip-backend-cursorsdk"+lay.suffix(), compatibility["outer_executable"])
}

// TestRender_RecordsPrivateRuntimeAndSDKMetadataFromTheStagedTree keeps the
// recorded runtime facts derived from the staged archive itself: the SDK version
// has to resolve from the staged production tree, and the private runtime version
// has to come from the shipped executable rather than from a caller supplied
// string.
//
// The recorded provenance has to name where the runtime actually came from. The
// packager falls back to the node on PATH when it is given neither a distribution
// archive nor a runtime, and a record that prefixed every source with
// "nodejs-official-distribution" would state, about a copy of a build machine's
// working installation, that it is a copy of an official distribution.
func TestRender_RecordsPrivateRuntimeAndSDKMetadataFromTheStagedTree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind  string
		label string
		wants string
	}{
		{kind: "official-distribution", label: "node-v22.22.3-dist", wants: "nodejs-official-distribution:node-v22.22.3-dist"},
		{kind: "supplied-runtime", label: "node", wants: "nodejs-supplied-runtime:node"},
		{kind: "path-fallback", label: "node.exe", wants: "nodejs-path-fallback:node.exe"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			lay.nodeSourceKind = tc.kind
			lay.nodeSource = tc.label
			_, compatibility := renderAndRead(t, lay)

			require.Equal(t, "1.0.23", compatibility["cursor_sdk_required_version"])
			require.Equal(t, false, compatibility["cursor_sdk_bundled"])
			require.Equal(t, "0.1.0", compatibility["bridge_version"])
			require.Equal(t, ">=22.13", compatibility["bridge_node_engine"])
			require.Equal(t, lay.nodeVersion, compatibility["private_runtime_version"])
			require.Equal(t, tc.wants, compatibility["private_runtime_source"])
			require.NotEmpty(t, compatibility["private_runtime_sha256"])
			require.Equal(t, lay.platform, compatibility["native_platform_assembled"])
			require.Equal(t, "uncertified", compatibility["host_certification_state"])
			require.NotEmpty(t, compatibility["licensing_status"])
		})
	}
}

// TestRender_RecordsTheExactPinnedHostContractVersions keeps the record's host
// compatibility exact and derived.
//
// The plugin builds against published Go-LIP modules, and the versions it was built
// against are the ones a reader of the archive has to be able to check. A record that
// named the module paths without their versions would leave "compatible host" a claim
// rather than a fact, so the versions are read from the plugin's own module manifest -
// the same manifest `go build` resolved - instead of being supplied by a caller who
// could name any version at all.
func TestRender_RecordsTheExactPinnedHostContractVersions(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	require.Equal(t, rootModule, compatibility["published_root_module"])
	require.Equal(t, hostPinVersion, compatibility["host_contract_root_version"])
	require.Equal(t, acpModule, compatibility["published_acp_module"])
	require.Equal(t, hostPinVersion, compatibility["host_contract_acp_version"])
}

// TestRender_RefusesARecordItCannotPinToAReleasedHostContract keeps a record that
// could not name the contracts it was built against from being written at all.
//
// A missing pin, and a `replace` that redirects the build away from the released
// module, both produce an archive whose "supported host" line cannot be audited. The
// replace case is the dangerous one: a local replacement resolves fine, so nothing
// else in packaging would notice, and the record would name a contract version the
// bytes in the archive were never built from.
func TestRender_RefusesARecordItCannotPinToAReleasedHostContract(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name: "ACP module not required",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchGoMod(t, lay, "\t"+acpModule+" "+hostPinVersion+"\n", "")
			},
			wants: acpModule,
		},
		{
			name: "root module not required",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchGoMod(t, lay, "\t"+rootModule+" "+hostPinVersion+"\n", "")
			},
			wants: rootModule,
		},
		{
			name: "root module replaced",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchGoMod(t, lay, "go 1.26.6\n", "go 1.26.6\n\nreplace "+rootModule+" => ../host\n")
			},
			wants: "replace",
		},
		{
			name: "ACP module replaced",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchGoMod(t, lay, "go 1.26.6\n",
					"go 1.26.6\n\nreplace "+acpModule+" => ../host/connector-support/acp\n")
			},
			wants: "replace",
		},
		{
			name: "release metadata names no ACP module",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchRelease(t, lay, "published_acp_module: "+acpModule+"\n", "")
			},
			wants: "published_acp_module",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.alter(t, lay)

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
		})
	}
}

// TestRender_RecordsSourceIdentityFromTheStagedBuildStamp keeps the record's source
// identity a fact about the bytes in the archive.
//
// The revision is read from the Go build stamp inside the staged outer executable, so
// it is the revision those bytes were compiled from - not a value typed into a
// metadata file, which would go stale the moment the next commit landed. A build made
// outside a git work tree carries no stamp, and the record says exactly that instead
// of naming a revision nobody can check; a build from a dirty work tree is recorded
// as dirty, because that is what a release reviewer has to know.
func TestRender_RecordsSourceIdentityFromTheStagedBuildStamp(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	// The fixture stages a stand-in that is not a Go binary at all, and the harness
	// resolved nothing, so the record has to say that rather than inventing an identity.
	require.Equal(t, "", compatibility["source_revision"])
	require.NotContains(t, compatibility, "source_modified",
		"an unestablished source state has to be absent, not recorded as clean")
	require.Contains(t, stringField(compatibility, "source_stamp_evidence"), "no Go build VCS stamp")

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("source identity evidence needs git to stamp a build")
	}

	stamped := newStagedRelease(t)
	head := commitStampedBinary(t, stamped, false)
	_, record := renderAndRead(t, stamped)
	require.Equal(t, head, record["source_revision"],
		"the recorded revision is the one the staged executable was built from")
	require.Equal(t, false, record["source_modified"])

	dirty := newStagedRelease(t)
	commitStampedBinary(t, dirty, true)
	_, record = renderAndRead(t, dirty)
	require.NotEmpty(t, record["source_revision"])
	require.Equal(t, true, record["source_modified"],
		"a build from a dirty work tree has to be recorded as dirty, not as a clean revision")
}

// TestRender_CrossChecksAndFallsBackToTheResolvedSourceRevision keeps the source
// identity a fact about the build in the common case as well.
//
// The Go toolchain stamps a revision only for a build in a primary version-control
// checkout. This project builds in linked work trees, where it stamps nothing, so the
// packager also resolves the revision from the tree it builds in and passes it here. Two
// things have to hold: a revision that disagrees with the stamp in the executable is a
// packaging failure rather than a preference, and a resolved revision is recorded with
// the evidence that says where it came from, because it was not read out of the bytes.
func TestRender_CrossChecksAndFallsBackToTheResolvedSourceRevision(t *testing.T) {
	t.Parallel()

	t.Run("resolved revision is recorded when there is no stamp", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		lay.sourceRevision = "0123456789abcdef0123456789abcdef01234567"
		lay.sourceModified = "true"

		_, record := renderAndRead(t, lay)
		require.Equal(t, lay.sourceRevision, record["source_revision"])
		require.Equal(t, true, record["source_modified"])
		require.Contains(t, stringField(record, "source_stamp_evidence"),
			"resolved from the version-control state of the tree")
	})

	t.Run("a resolved revision that is not a revision is refused", func(t *testing.T) {
		t.Parallel()

		for _, revision := range []string{"HEAD", "chore/platform-and-script-scope", "v0.1.0", "012345", "nothex!!"} {
			lay := newStagedRelease(t)
			lay.sourceRevision = revision

			err := runRenderCmd(t, lay)
			require.Error(t, err, "revision %q was recorded as a source revision", revision)
			require.Contains(t, err.Error(), "source-revision")
		}
	})

	t.Run("a modified state without a revision is refused", func(t *testing.T) {
		t.Parallel()

		for _, state := range []string{"true", "false"} {
			lay := newStagedRelease(t)
			lay.sourceModified = state

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), "-source-modified")
		}
	})

	t.Run("an unrecognised source state is refused", func(t *testing.T) {
		t.Parallel()

		for _, state := range []string{"yes", "clean", "dirty", "1", "maybe"} {
			lay := newStagedRelease(t)
			lay.sourceRevision = "0123456789abcdef0123456789abcdef01234567"
			lay.sourceModified = state

			err := runRenderCmd(t, lay)
			require.Error(t, err, "source state %q was accepted", state)
			require.Contains(t, err.Error(), "unknown")
		}
	})

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the cross-check evidence needs git to stamp a build")
	}

	t.Run("a resolved revision that contradicts the stamp fails packaging", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		head := commitStampedBinary(t, lay, false)
		lay.sourceRevision = strings.Repeat("ab", 20)
		if strings.HasPrefix(head, strings.Repeat("ab", 20)) {
			lay.sourceRevision = strings.Repeat("cd", 20)
		}
		lay.sourceModified = "true"

		err := runRenderCmd(t, lay)
		require.Error(t, err)
		require.Contains(t, err.Error(), "-source-revision")
		require.Contains(t, err.Error(), head)
	})

	t.Run("a resolved cleanliness that contradicts the stamp fails packaging", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		head := commitStampedBinary(t, lay, false)
		lay.sourceRevision = head
		lay.sourceModified = "true"

		err := runRenderCmd(t, lay)
		require.Error(t, err)
		require.Contains(t, err.Error(), "vcs.modified")
	})

	t.Run("an unresolved cleanliness beside a stamp is not a contradiction", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		head := commitStampedBinary(t, lay, false)
		lay.sourceRevision = head
		lay.sourceModified = "unknown"

		_, record := renderAndRead(t, lay)
		require.Equal(t, head, record["source_revision"])
		require.Equal(t, false, record["source_modified"],
			"the stamp states the state, so the record states it rather than nothing")
		require.Contains(t, stringField(record, "source_stamp_evidence"), "Go build VCS stamp")
	})

	t.Run("a resolved revision that agrees with the stamp is kept", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		head := commitStampedBinary(t, lay, false)
		lay.sourceRevision = head

		_, record := renderAndRead(t, lay)
		require.Equal(t, head, record["source_revision"])
		require.Contains(t, stringField(record, "source_stamp_evidence"), "Go build VCS stamp",
			"with a stamp in the executable, the record cites the stamp")
	})
}

// TestRender_NeverRecordsAnUnestablishedSourceStateAsClean keeps cleanliness a tri-state.
//
// A build nobody could establish the state of is not a clean build, and a record that says
// it is has turned an absence of evidence into a statement about the artifact. The three
// states have to stay distinct: a dirty tree, a clean tree, and a state nothing
// established - where the last one leaves the field out of the record entirely and says so
// in words, next to the revision it did establish.
func TestRender_NeverRecordsAnUnestablishedSourceStateAsClean(t *testing.T) {
	t.Parallel()

	const revision = "0123456789abcdef0123456789abcdef01234567"

	for _, tc := range []struct {
		name     string
		revision string
		modified string
		wants    any
	}{
		{name: "no identity at all", wants: nil},
		{name: "revision with no state supplied", revision: revision, wants: nil},
		{name: "revision with an explicit unknown state", revision: revision, modified: "unknown", wants: nil},
		{name: "revision resolved clean", revision: revision, modified: "false", wants: false},
		{name: "revision resolved dirty", revision: revision, modified: "true", wants: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			lay.sourceRevision = tc.revision
			lay.sourceModified = tc.modified

			_, record := renderAndRead(t, lay)
			if tc.wants == nil {
				require.NotContains(t, record, "source_modified",
					"an unestablished source state must be absent from the record, not false")
				return
			}
			require.Equal(t, tc.wants, record["source_modified"])
			if tc.modified == "unknown" {
				require.Contains(t, stringField(record, "source_stamp_evidence"), "could not be established",
					"an unknown state has to say that it could not be established")
			}
		})
	}
}

// The host artifacts one certified release records. They are per platform on purpose: the
// real-host gate downloaded each platform's own host archive and measured the binary inside
// it, so a certification names two artifacts rather than one artifact that stood in for two
// platforms.
var (
	windowsHostDigest = strings.Repeat("6a", 32)
	linuxHostDigest   = strings.Repeat("ce", 32)
)

// certifyRelease declares the certified posture and the per-platform host artifacts a
// certified release records, replacing the uncertified posture the harness stages by
// default.
func certifyRelease(tb testing.TB, lay *stagedRelease) {
	tb.Helper()

	certifyPlatforms(tb, lay, "windows/amd64", "linux/amd64")
}

// certifyPlatforms declares the certified posture for the named platforms only, so a case
// can stage a certification that leaves one declared platform without an artifact.
func certifyPlatforms(tb testing.TB, lay *stagedRelease, platforms ...string) {
	tb.Helper()

	patchRelease(tb, lay, "host_certification: uncertified", "host_certification: certified")

	artifacts := []string{"certified_host_artifacts:"}
	for _, platform := range platforms {
		windows := platform == "windows/amd64"
		asset, binary, digest := "linux_amd64.tar.gz", "lipstd", linuxHostDigest
		contract := packagelayout.CompanionPackagedDefault
		if windows {
			asset, binary, digest = "windows_amd64.zip", "lipstd.exe", windowsHostDigest
			contract = packagelayout.CompanionExplicitBridgeExecutable
		}
		artifacts = append(artifacts,
			"  - platform: "+platform,
			"    host_project: "+rootModule,
			"    host_version: v0.1.0",
			"    host_release_asset: "+rootModule+"_0.1.0_"+asset,
			"    host_checksums_asset: checksums.txt",
			"    host_binary: "+binary,
			"    host_artifact_sha256: "+digest,
			"    companion_contract: "+string(contract))
	}

	patchRelease(tb, lay,
		"host_certification_reason: no downloadable host binary release exists to certify against",
		strings.Join(artifacts, "\n"))
}

// TestRender_RecordsHostCertificationWithoutInventingEvidence keeps the host
// certification line honest in both directions.
//
// An uncertified release has no host artifact to name and says why. A certified one carries
// the host artifacts it was measured against, one per declared platform, and the digests are
// read from that declaration rather than from anything a caller typed: a caller-supplied
// digest is now an assertion about what was measured, and it has to agree with the record.
// Every way of getting this wrong - a certified claim with nothing behind it, an uncertified
// claim carrying artifacts, an artifact that is not a digest, an artifact for a platform the
// manifest does not declare, a platform with no artifact, and a contract nobody documented -
// fails the render instead of producing a record that reads as evidence nobody produced.
func TestRender_RecordsHostCertificationWithoutInventingEvidence(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	require.Equal(t, "uncertified", compatibility["host_certification_state"])
	require.NotEmpty(t, compatibility["host_certification_reason"],
		"an uncertified record has to say why, or it reads as an oversight")
	require.Empty(t, compatibility["tested_host_artifacts"])
	require.NotContains(t, compatibility, "tested_host_artifact_sha256",
		"an empty digest field could only ever record the absence of a certification")
	require.Empty(t, compatibility["host_certification_platforms"],
		"an uncertified record names no certified platform")

	t.Run("certified with the declared artifacts", func(t *testing.T) {
		t.Parallel()

		certified := newStagedRelease(t)
		certifyRelease(t, certified)

		_, record := renderAndRead(t, certified)
		require.Equal(t, "certified", record["host_certification_state"])
		require.Equal(t, []any{linuxHostDigest, windowsHostDigest}, record["tested_host_artifacts"],
			"the recorded digests are the ones release.yaml declares, sorted by platform so the record is stable "+
				"across runs rather than dependent on the order the metadata lists them in")
	})

	t.Run("the declared digests are what the caller asserts", func(t *testing.T) {
		t.Parallel()

		certified := newStagedRelease(t)
		certifyRelease(t, certified)
		certified.testedHosts = []string{windowsHostDigest, linuxHostDigest}

		_, record := renderAndRead(t, certified)
		require.Equal(t, []any{linuxHostDigest, windowsHostDigest}, record["tested_host_artifacts"],
			"a caller may assert the measured digests in any order, but the record keeps the declared order")
	})

	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name: "certified with no declared artifact",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchRelease(t, lay, "host_certification: uncertified", "host_certification: certified")
				patchRelease(t, lay,
					"host_certification_reason: no downloadable host binary release exists to certify against",
					"host_certification_reason:")
			},
			wants: "certified_host_artifacts",
		},
		{
			name: "uncertified with a declared artifact",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyPlatforms(t, lay, "windows/amd64")
				patchRelease(t, lay, "host_certification: certified", "host_certification: uncertified")
			},
			wants: "certified_host_artifacts",
		},
		{
			name: "declared digest is not a digest",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				lay.testedHosts = []string{"v0.1.0"}
			},
			wants: "-tested-host",
		},
		{
			name: "declared artifact digest is not a digest",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, windowsHostDigest, "v0.1.0")
			},
			wants: "sha256",
		},
		{
			// Hexadecimal of the right alphabet but the wrong length: the shape check has to
			// be about the whole digest, not only about whether it is hex.
			name: "declared artifact digest is hex but the wrong length",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, windowsHostDigest, strings.Repeat("ab", 31))
			},
			wants: "64 lowercase hex characters",
		},
		{
			name: "artifact for a platform the manifest does not declare",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, "  - platform: linux/amd64", "  - platform: darwin/amd64")
			},
			wants: "platform",
		},
		{
			name: "a declared platform with no artifact",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyPlatforms(t, lay, "windows/amd64")
			},
			wants: "certified_host_artifacts for linux/amd64",
		},
		{
			name: "a companion contract nobody documented",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay,
					"    companion_contract: "+string(packagelayout.CompanionExplicitBridgeExecutable),
					"    companion_contract: maybe")
			},
			wants: "companion_contract",
		},
		{
			name: "a certified artifact missing the host it was measured against",
			alter: func(t *testing.T, lay *stagedRelease) {
				certifyRelease(t, lay)
				patchRelease(t, lay, "    host_version: v0.1.0", "    host_version:")
			},
			wants: "host_version",
		},
		{
			name: "unknown certification state",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchRelease(t, lay, "host_certification: uncertified", "host_certification: approved")
			},
			wants: "host_certification",
		},
		{
			name: "uncertified without a reason",
			alter: func(t *testing.T, lay *stagedRelease) {
				patchRelease(t, lay,
					"host_certification_reason: no downloadable host binary release exists to certify against",
					"host_certification_reason:")
			},
			wants: "host_certification_reason",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.alter(t, lay)

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
		})
	}
}

// TestRender_RecordsTheCertifiedContractPerPlatform keeps a certified record from collapsing
// its evidence into one general claim.
//
// The measured difference between the two platforms is the reason a per-platform record is
// worth carrying at all: on one platform an operator who configures nothing still reaches the
// Cursor SDK, and on the other the packaged default cannot be reached and the field naming the
// installed launcher is the supported configuration. A record that stated the certification
// without stating that contract would leave an operator on the wrong platform with either a
// bootstrap failure or a field they do not need.
//
// The record carries the platform's packaged launcher path as well, derived from the layout
// contract rather than written here, so the remedy it names is a file the archive really
// ships.
func TestRender_RecordsTheCertifiedContractPerPlatform(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	certifyRelease(t, lay)
	_, record := renderAndRead(t, lay)

	entries := decodeList(t, record["host_certification_platforms"])
	require.Len(t, entries, 2, "one certified platform per declared platform")

	contracts := map[string]string{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		require.True(t, ok, "a certified platform entry has to be an object")
		platform, _ := entry["platform"].(string)
		archive, err := packagelayout.ForPlatform(splitTestPlatform(platform))
		require.NoError(t, err)

		assert.Equal(t, rootModule, entry["host_project"])
		assert.Equal(t, "v0.1.0", entry["host_version"])
		assert.NotEmpty(t, entry["host_release_asset"])
		assert.NotEmpty(t, entry["host_binary"])
		assert.NotEmpty(t, entry["host_artifact_sha256"])
		assert.Equal(t, archive.LauncherPath(), entry["bridge_executable_rel"],
			"%s has to name the packaged launcher an operator points bridge_executable at, spelled as the "+
				"archive layout spells it", platform)

		contract, _ := entry["companion_contract"].(string)
		require.NotEmpty(t, contract, "%s has to record which companion spelling it supports", platform)
		contracts[platform] = contract
	}

	// The two platforms disagree, because the host binds the verified executable
	// differently on each; and the record has to say so rather than leaving an operator to
	// discover it.
	require.NotEqual(t, contracts["windows/amd64"], contracts["linux/amd64"],
		"the host stages the verified executable on windows/amd64 and execs the installed one on linux/amd64, so "+
			"the two platforms' supported companion spellings are different and the record has to say which is which")
}

// splitTestPlatform splits one "os/arch" platform claim.
func splitTestPlatform(platform string) (goos, goarch string) {
	goos, goarch, _ = strings.Cut(platform, "/")
	return goos, goarch
}

// TestRender_RecordsPackageVerificationAsUnperformed keeps the release record from
// claiming a verification that has not happened.
//
// The packager writes this record before any verification can run: verification is a
// separate step against an assembled tree, so a "verified" or "passed" state recorded
// here would describe a check nobody performed. The record therefore states that
// nothing has been verified yet, names the command that does the verification for each
// tree state, and stays that way until an auditor attaches a real report.
func TestRender_RecordsPackageVerificationAsUnperformed(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	require.Equal(t, "not-performed", compatibility["package_verification_state"])
	require.Equal(t, false, compatibility["package_verification_performed"])
	require.NotEmpty(t, compatibility["package_verification_reason"])
	require.NotContains(t, compatibility, "package_verification",
		"a bare command name reads as a verification that happened")

	verifier := "scripts/verify-package.sh"
	if lay.goos() == "windows" {
		verifier = "scripts/verify-package.ps1"
	}
	require.Contains(t, stringField(compatibility, "package_verification_shipped_command"),
		verifier+" --package-root <plugin-root> --tree-state shipped")
	require.Contains(t, stringField(compatibility, "package_verification_installed_command"),
		verifier+" --package-root <plugin-root> --tree-state installed")

	// Nothing in the record may read as a passing verification.
	body := strings.ToLower(mustJSON(t, compatibility))
	for _, claim := range []string{"pass", "verified\": true", "\"verified\"", "succeeded"} {
		require.NotContains(t, body, claim,
			"the record may not label a check that has not run; field value: %s", claim)
	}
}

// TestRender_RecordsTheDeclaredPlatformsAndTheOnesThisArchiveDidNotAssemble keeps
// the platform evidence in the record rather than only in the build log.
//
// One archive is assembled and verified natively, and it narrows the manifest to that
// one platform. The platforms the template declares are the project's claims, and the
// ones this artifact is not evidence for have to be readable without the CI log -
// otherwise a reader of a single archive cannot tell a deliberate limit from an
// oversight.
func TestRender_RecordsTheDeclaredPlatformsAndTheOnesThisArchiveDidNotAssemble(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	require.Equal(t, []any{"linux/amd64", "windows/amd64"}, compatibility["declared_platforms"],
		"the declared set is recorded sorted, so the record is stable across runs")

	notAssembled := make([]string, 0, 2)
	for _, platform := range decodeList(t, compatibility["declared_platforms_not_assembled"]) {
		notAssembled = append(notAssembled, platform.(string))
	}
	require.NotContains(t, notAssembled, lay.platform,
		"an archive assembled natively on this platform is evidence for it")

	assembled := append([]string{lay.platform}, notAssembled...)
	slices.Sort(assembled)
	declared := packagelayout.SupportedPlatforms()
	slices.Sort(declared)
	require.Equal(t, declared, assembled,
		"the declared set and the assembled one have to partition the declared platforms")
}

// TestRender_RefusesAnUnnamedRuntimeSourceKind keeps the recorded provenance from
// drifting back into a single prefix for every source. An unknown or missing kind is
// a packaging failure: the alternative is a runtime whose provenance the record
// cannot state honestly.
func TestRender_RefusesAnUnnamedRuntimeSourceKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", "official distribution", "path", "nodejs-official-distribution"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			lay.nodeSourceKind = kind

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), "node-source-kind")
			require.Contains(t, err.Error(), "path-fallback")
		})
	}
}

// TestRender_DoesNotClaimAReleaseExists keeps the record from reading as a
// publication. release.yaml declares the tag a future publication would carry, and
// there is no such tag: a field named release_tag would be read as one that exists.
func TestRender_DoesNotClaimAReleaseExists(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	require.Equal(t, "cursorsdk-v0.1.0", compatibility["release_tag_declared"])
	require.NotContains(t, compatibility, "release_tag")
}

// TestRender_RecordsTheCursorSDKAsRequiredAndNotBundled keeps the non-redistribution
// decision legible in the record every archive carries.
//
// The SDK is proprietary and no redistribution right for it or for the native binaries
// its platform package bundles has been verified, so the archive ships the manifest and
// the lockfile that pin the SDK and neither the SDK nor its dependency closure. A record
// that only named a version would read as "this is what the archive ships"; the record
// has to say the SDK is required at run time, not bundled, give the one command that
// provisions it, and state the non-redistribution position explicitly.
func TestRender_RecordsTheCursorSDKAsRequiredAndNotBundled(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	_, compatibility := renderAndRead(t, lay)

	archive, err := packagelayout.ForPlatform(lay.goos(), lay.goarch())
	require.NoError(t, err)

	require.Equal(t, "1.0.23", compatibility["cursor_sdk_required_version"],
		"the required version is the pin the bridge verifies at run time")
	require.Equal(t, false, compatibility["cursor_sdk_bundled"],
		"the archive ships no Cursor SDK and none of its dependency closure")
	require.Equal(t, archive.ProvisionCommand(""), compatibility["cursor_sdk_provisioning_command"],
		"the record has to carry the exact provisioning command an operator runs")
	require.NotContains(t, compatibility, "cursor_sdk_version",
		"a recorded installed version would read as a claim about what the archive ships")

	redistribution := stringField(compatibility, "cursor_sdk_redistribution")
	require.Contains(t, redistribution, "not redistributed")
	require.Contains(t, redistribution, "operator")

	// The licensing statement has to agree: it may not keep claiming the SDK's
	// redistribution rights are unresolved inside an archive that ships no SDK.
	licensing := stringField(compatibility, "licensing_status")
	require.NotContains(t, licensing, "confirm redistribution rights")
	require.Contains(t, licensing, "not redistributed")

	// The record also may not hand the shipped npm tree the Node.js MIT grant. npm
	// licenses its own application under the Artistic License 2.0 in a LICENSE inside
	// the staged npm tree and states that the packages it bundles are licensed on their
	// respective terms, so a record calling that tree MIT is false of the artifact and
	// would leave an auditor with the wrong license for most of an archive.
	require.NotContains(t, licensing,
		"including its bundled npm and the third-party dependencies npm bundles with it, is MIT",
		"the shipped npm tree is not covered by the Node.js MIT grant")
	require.NotContains(t, licensing, "whose notices cover those bundled components",
		"the Node distribution license does not carry the bundled packages' own license texts")
	require.Contains(t, licensing, "Artistic-2.0",
		"the record has to name the license npm's own staged text declares")
	require.Contains(t, licensing, "its own license text in its own package directory",
		"each package npm bundles is attributed to the license text it ships itself")
	require.Contains(t, licensing, "THIRD-PARTY-NOTICES.md",
		"the record has to point at the staged notice that names any bundled package shipping none")

	// The record describes an archive that provisions itself, so the staged tree has
	// to carry the two files that make the recorded command runnable.
	require.FileExists(t, filepath.Join(lay.root, filepath.FromSlash(archive.BridgePackageLockPath())))
	require.FileExists(t, filepath.Join(lay.root, filepath.FromSlash(archive.PrivateRuntimeNPMCLIPath())))
}

// TestRender_RefusesAStagedOperatorProvisionedTree keeps a staged dependency tree an
// explicit packaging failure.
//
// The archive must not ship the Cursor SDK's dependency closure: @cursor/sdk is
// proprietary and its platform package bundles native binaries whose license texts it
// does not redistribute, so staging the closure would assert a redistribution right
// nobody has verified. A renderer that described such a tree anyway would produce
// release metadata that reads as a redistributable bundle, which is the exact thing the
// archive must not be.
func TestRender_RefusesAStagedOperatorProvisionedTree(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	archive, err := packagelayout.ForPlatform(lay.goos(), lay.goarch())
	require.NoError(t, err)

	staged := filepath.Join(lay.root, filepath.FromSlash(archive.ProvisionedSDKPackageJSONPath()))
	writeFile(t, staged, `{"name":"@cursor/sdk","version":"1.0.23"}`)

	err = runRenderCmd(t, lay)
	require.Error(t, err)
	require.Contains(t, err.Error(), archive.BridgeModulesPath())
	require.Contains(t, err.Error(), "not redistributed")
}

// TestRender_RefusesStagedTreeWithoutTheProvisioningPrerequisites keeps a tree an
// operator could not provision an explicit packaging failure: without the shipped
// lockfile the SDK version is not pinned, and without the shipped runtime's own npm
// the recorded provisioning command cannot run without a global package manager.
func TestRender_RefusesStagedTreeWithoutTheProvisioningPrerequisites(t *testing.T) {
	t.Parallel()

	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)

	for _, tc := range []struct {
		name  string
		drop  func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name:  "shipped lockfile",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.bridgeLock)) },
			wants: archive.BridgePackageLockPath(),
		},
		{
			name:  "shipped bundled npm",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.npmCLI)) },
			wants: archive.PrivateRuntimeNPMCLIPath(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.drop(t, lay)

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
			require.Contains(t, err.Error(), "reinstall")
		})
	}
}

// TestRender_RefusesMissingPrivateRuntimeOrBridgeEntry keeps an incomplete staged
// archive an explicit prerequisite failure rather than metadata over a tree that
// cannot run.
func TestRender_RefusesMissingPrivateRuntimeOrBridgeEntry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		drop  func(t *testing.T, lay *stagedRelease)
		wants string
	}{
		{
			name:  "private runtime",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.runtime)) },
			wants: "private/node/node",
		},
		{
			name:  "bridge entry",
			drop:  func(t *testing.T, lay *stagedRelease) { require.NoError(t, os.Remove(lay.entry)) },
			wants: "private/bridge/bin/lip-cursor-sdk-bridge.js",
		},
		{
			name: "staged bridge manifest",
			drop: func(t *testing.T, lay *stagedRelease) {
				require.NoError(t, os.Remove(lay.bridgeJSON))
			},
			wants: "private/bridge/package.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			tc.drop(t, lay)

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wants)
			require.Contains(t, err.Error(), "reinstall")
		})
	}
}

// TestRender_RefusesWeakenedExportPosture keeps the closed manifest's declared
// trust boundary load-bearing: a template that quietly dropped local-only access,
// per-instance process sharing, static credentials, or the agent runtime
// execution class must not be rendered into a release.
func TestRender_RefusesWeakenedExportPosture(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ field, value, wants string }{
		{field: "access_scope", value: "shared", wants: "access_scope"},
		{field: "process_sharing", value: "shared", wants: "process_sharing"},
		{field: "credential_mode", value: "passthrough", wants: "credential_mode"},
		{field: "execution_class", value: "in_process", wants: "execution_class"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()

			lay := newStagedRelease(t)
			body := readFile(t, lay.template)
			field := regexp.MustCompile(`"` + tc.field + `":\s*"[^"]*"`)
			require.True(t, field.MatchString(body), "template has no %s field", tc.field)
			writeFile(t, lay.template, field.ReplaceAllString(body, `"`+tc.field+`": "`+tc.value+`"`))

			err := runRenderCmd(t, lay)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.field)
			require.Contains(t, err.Error(), tc.value)
		})
	}
}

// TestRender_RefusesUnknownPlaceholdersAndBadDigests keeps a template that no
// longer matches the closed manifest contract, or a caller that hands over an
// implausible executable digest, a failure instead of a manifest the host would
// reject at install time.
func TestRender_RefusesUnknownPlaceholdersAndBadDigests(t *testing.T) {
	t.Parallel()

	t.Run("leftover placeholder", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		body := readFile(t, lay.template)
		writeFile(t, lay.template, strings.Replace(body, `"protocol_major": 1,`, `"protocol_major": REPLACE_PROTOCOL_MAJOR,`, 1))

		err := runRenderCmd(t, lay)
		require.Error(t, err)
		require.Contains(t, err.Error(), "REPLACE_PROTOCOL_MAJOR")
	})

	t.Run("short executable digest", func(t *testing.T) {
		t.Parallel()

		lay := newStagedRelease(t)
		lay.exeSHA256 = "not-a-digest"

		err := runRenderCmd(t, lay)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sha256")
	})
}

// TestRender_ReportsUndeclaredPlatformAsExplicitFailure keeps a cross-platform
// claim an explicit refusal.
func TestRender_ReportsUndeclaredPlatformAsExplicitFailure(t *testing.T) {
	t.Parallel()

	lay := newStagedRelease(t)
	lay.platform = "linux/386"

	err := runRenderCmd(t, lay)
	require.Error(t, err)
	require.Contains(t, err.Error(), "linux/386")
}

func (lay *stagedRelease) goos() string {
	goos, _, _ := strings.Cut(lay.platform, "/")
	return goos
}

func (lay *stagedRelease) goarch() string {
	_, goarch, _ := strings.Cut(lay.platform, "/")
	return goarch
}

func (lay *stagedRelease) suffix() string {
	if lay.goos() == "windows" {
		return ".exe"
	}
	return ""
}

// npmRelPath is where the platform's own Node distribution keeps its bundled npm, read
// from the archive contract so the staged tree mirrors the real distribution layout.
func npmRelPath(goos string) string {
	a, err := packagelayout.ForPlatform(goos, runtime.GOARCH)
	if err != nil {
		panic("npmRelPath: " + err.Error())
	}
	return filepath.FromSlash(a.PrivateRuntimeNPMRel())
}

// newStagedRelease builds a staged install root whose private runtime is a
// compiled stand-in reporting one fixed Node version. The renderer records the
// version the shipped executable reports, so the stand-in has to be a real
// executable rather than a file with plausible bytes.
func newStagedRelease(tb testing.TB) *stagedRelease {
	tb.Helper()

	goos := runtime.GOOS
	exeSuffix := ""
	if goos == "windows" {
		exeSuffix = ".exe"
	}

	root := filepath.Join(tb.TempDir(), "staged install root with spaces")
	lay := &stagedRelease{
		root:           root,
		platform:       goos + "/" + runtime.GOARCH,
		buildID:        "localdev",
		nodeSource:     "node-v22.22.3-dist",
		nodeSourceKind: "official-distribution",
		nodeVersion:    "v22.22.3",
	}
	lay.exeSHA256 = strings.Repeat("ab", 32)
	lay.exe = writeFile(tb, filepath.Join(root, "bin", "lip-backend-cursorsdk"+exeSuffix), "outer executable")
	lay.runtime = installBinaryFile(tb, filepath.Join(root, "private", "node", "node"+exeSuffix), buildRuntimeStub(tb, lay.nodeVersion))
	lay.npmCLI = writeFile(tb, filepath.Join(root, "private", "node", npmRelPath(goos), "bin", "npm-cli.js"),
		"// shipped bundled npm entry point\n")
	lay.entry = writeFile(tb, filepath.Join(root, "private", "bridge", "bin", "lip-cursor-sdk-bridge.js"), "// bridge entry\n")
	writeFile(tb, filepath.Join(root, "private", "bridge", "lip-cursor-sdk-bridge"+exeSuffix), "launcher")
	writeFile(tb, filepath.Join(root, "private", "bridge", "dist", "main.js"), "// built\n")
	lay.bridgeJSON = writeFile(tb, filepath.Join(root, "private", "bridge", "package.json"),
		`{"name":"lip-cursor-sdk-bridge","version":"0.1.0","engines":{"node":">=22.13"},"dependencies":{"@cursor/sdk":"1.0.23"}}`)
	lay.bridgeLock = writeFile(tb, filepath.Join(root, "private", "bridge", "package-lock.json"),
		`{"name":"lip-cursor-sdk-bridge","lockfileVersion":3,"packages":{}}`)

	repo := filepath.Join(tb.TempDir(), "repo")
	lay.release = writeFile(tb, filepath.Join(repo, "release.yaml"), strings.Join([]string{
		"# Connector release metadata.",
		"schema: golip.connector.release/v1",
		"plugin_id: io.golip.backend.cursorsdk",
		"factory_kind: cursorsdk",
		"module: github.com/aiproxer/aiproxer-cursor-sdk",
		"command: ./cmd/lip-backend-cursorsdk",
		"manifest_template: manifest/template.backendplugin.json",
		"version: 0.1.0",
		"build_id: localdev",
		"tag: cursorsdk-v0.1.0",
		"profiles:",
		"  - full",
		"published_root_module: " + rootModule,
		"published_acp_module: " + acpModule,
		"replace_policy: released-dependency-pins-no-replace",
		"private_companions:",
		"  - bridge-node",
		"host_certification: uncertified",
		"host_certification_reason: no downloadable host binary release exists to certify against",
		"",
	}, "\n"))
	// The plugin module manifest is a build input too: the exact released host
	// contract versions the record names are the ones this module requires, so the
	// renderer reads them here rather than taking a caller-supplied string.
	lay.goMod = writeFile(tb, filepath.Join(repo, "go.mod"), strings.Join([]string{
		"module github.com/aiproxer/aiproxer-cursor-sdk",
		"",
		"go 1.26.6",
		"",
		"require (",
		"\t" + rootModule + " " + hostPinVersion,
		"\t" + acpModule + " " + hostPinVersion,
		"\tgithub.com/stretchr/testify v1.12.1",
		")",
		"",
	}, "\n"))
	lay.template = writeFile(tb, filepath.Join(repo, "manifest", "template.backendplugin.json"), strings.Join([]string{
		"{",
		`  "schema": "golip.backendplugin.manifest/v1",`,
		`  "plugin_id": "io.golip.backend.cursorsdk",`,
		`  "version": "0.1.0",`,
		`  "build_id": "REPLACE_BUILD_ID",`,
		`  "executable": "bin/lip-backend-cursorsdk",`,
		`  "sha256": "REPLACE_SHA256",`,
		`  "protocol_major": 1,`,
		`  "protocol_min_minor": 0,`,
		`  "protocol_max_minor": 0,`,
		`  "platforms": [`,
		`    {"os": "windows", "arch": "amd64"},`,
		`    {"os": "linux", "arch": "amd64"}`,
		`  ],`,
		`  "exports": [`,
		`    {`,
		`      "kind": "cursorsdk",`,
		`      "credential_mode": "static",`,
		`      "access_scope": "local_only",`,
		`      "process_sharing": "per_instance",`,
		`      "execution_class": "agent_runtime"`,
		`    }`,
		`  ]`,
		"}",
		"",
	}, "\n"))
	return lay
}

func renderAndRead(tb testing.TB, lay *stagedRelease) (map[string]any, map[string]any) {
	tb.Helper()

	require.NoError(tb, runRenderCmd(tb, lay))
	return decodeMap(tb, readFile(tb, filepath.Join(lay.root, "plugin.backendplugin.json"))),
		decodeMap(tb, readFile(tb, filepath.Join(lay.root, "compatibility.json")))
}

func runRenderCmd(tb testing.TB, lay *stagedRelease) error {
	tb.Helper()

	args := []string{
		"render",
		"-repo", filepath.Dir(lay.release),
		"-staging", lay.root,
		"-platform", lay.platform,
		"-exe-sha256", lay.exeSHA256,
		"-node-source", lay.nodeSource,
		"-node-source-kind", lay.nodeSourceKind,
	}
	for _, digest := range lay.testedHosts {
		args = append(args, "-tested-host", digest)
	}
	if lay.sourceRevision != "" {
		args = append(args, "-source-revision", lay.sourceRevision)
	}
	if lay.sourceModified != "" {
		args = append(args, "-source-modified", lay.sourceModified)
	}
	argv := append([]string{"run", "."}, args...)
	cmd := exec.Command(goToolPath(tb), argv...)
	cmd.Dir = thisFileDir(tb)
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &renderError{output: string(out), err: err}
	}
	return nil
}

// patchRelease rewrites one line of the fixture release metadata, so a case can
// declare a release posture the shipped file does not.
func patchRelease(tb testing.TB, lay *stagedRelease, old, replacement string) {
	tb.Helper()

	body := readFile(tb, lay.release)
	require.Contains(tb, body, old)
	writeFile(tb, lay.release, strings.Replace(body, old, replacement, 1))
}

// patchGoMod rewrites the fixture module manifest, so a case can drop or replace a
// pinned host contract.
func patchGoMod(tb testing.TB, lay *stagedRelease, old, replacement string) {
	tb.Helper()

	body := readFile(tb, lay.goMod)
	require.Contains(tb, body, old)
	writeFile(tb, lay.goMod, strings.Replace(body, old, replacement, 1))
}

// renderError carries the renderer's diagnostics so assertions can read them.
type renderError struct {
	output string
	err    error
}

func (e *renderError) Error() string { return e.output }
func (e *renderError) Unwrap() error { return e.err }

func writeFile(tb testing.TB, path, body string) string {
	tb.Helper()

	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(tb, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// installBinaryFile copies a built executable into a staged private slot.
func installBinaryFile(tb testing.TB, dst, src string) string {
	tb.Helper()

	raw, err := os.ReadFile(src)
	require.NoError(tb, err)
	require.NoError(tb, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(tb, os.WriteFile(dst, raw, 0o700))
	return dst
}

// buildRuntimeStub compiles a private-runtime stand-in that reports one fixed
// version, mirroring a staged Node executable closely enough for the renderer to
// ask it for its own version.
func buildRuntimeStub(tb testing.TB, version string) string {
	tb.Helper()

	dir := tb.TempDir()
	writeFile(tb, filepath.Join(dir, "go.mod"), "module runtimestub\n\ngo 1.26\n")
	writeFile(tb, filepath.Join(dir, "main.go"), fmt.Sprintf(
		"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(%q) }\n", version))

	out := filepath.Join(dir, "node"+packagelayout.ExeSuffixFor(runtime.GOOS))
	cmd := exec.Command(goToolPath(tb), "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	raw, err := cmd.CombinedOutput()
	require.NoError(tb, err, "build runtime stub: %s", raw)
	return out
}

// commitStampedBinary builds the outer executable inside a git work tree, so the Go
// toolchain stamps the revision it was built from into the binary, and installs that
// binary as the staged outer executable. It returns the revision the record has to
// carry.
//
// dirty adds an uncommitted edit before the build, which is what makes the toolchain
// stamp the binary as modified. A record that named the revision without saying so
// would present a build that does not match its own source as a clean one.
func commitStampedBinary(tb testing.TB, lay *stagedRelease, dirty bool) string {
	tb.Helper()

	dir := tb.TempDir()
	writeFile(tb, filepath.Join(dir, "go.mod"), "module outerstub\n\ngo 1.26\n")
	source := "package main\n\nfunc main() {}\n"
	writeFile(tb, filepath.Join(dir, "main.go"), source)

	git := func(args ...string) string {
		tb.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=packaging", "GIT_AUTHOR_EMAIL=packaging@example.invalid",
			"GIT_COMMITTER_NAME=packaging", "GIT_COMMITTER_EMAIL=packaging@example.invalid",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		out, err := cmd.CombinedOutput()
		require.NoError(tb, err, "git %s: %s", strings.Join(args, " "), out)
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "staged outer executable")
	head := git("rev-parse", "HEAD")
	if dirty {
		writeFile(tb, filepath.Join(dir, "main.go"), source+"\n// uncommitted edit\n")
	}

	out := filepath.Join(dir, "outer"+packagelayout.ExeSuffixFor(runtime.GOOS))
	cmd := exec.Command(goToolPath(tb), "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	raw, err := cmd.CombinedOutput()
	require.NoError(tb, err, "build stamped outer stub: %s", raw)

	installBinaryFile(tb, filepath.Join(lay.root, filepath.FromSlash(
		"bin/"+packagelayout.OuterExecutableName+lay.suffix())), out)
	return head
}

func readFile(tb testing.TB, path string) string {
	tb.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(tb, err)
	return string(raw)
}

func decodeMap(tb testing.TB, body string) map[string]any {
	tb.Helper()

	var out map[string]any
	require.NoError(tb, json.Unmarshal([]byte(body), &out))
	return out
}

func decodeList(tb testing.TB, v any) []any {
	tb.Helper()

	out, ok := v.([]any)
	require.True(tb, ok, "expected a JSON array, got %T", v)
	return out
}

func mustJSON(tb testing.TB, v any) string {
	tb.Helper()

	raw, err := json.MarshalIndent(v, "", "  ")
	require.NoError(tb, err)
	return string(raw)
}

func thisFileDir(tb testing.TB) string {
	tb.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(tb, ok)
	return filepath.Dir(file)
}
