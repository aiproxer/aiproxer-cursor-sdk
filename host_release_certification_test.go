package cursorsdk_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/packagelayout"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the real-host certification gate for task 4.2.
//
// Every other suite in this repository puts the plugin behind this repository's own
// released host *contracts*. This one puts it behind a real, downloadable, versioned host
// *binary* - the artifact an operator actually has - and drives that binary's own
// operator surface: the install location, `check-config`, `inspect`, `inventory`, `doctor`,
// and `serve`.
//
// It exists because the boundary between "the plugin satisfies the contract" and "the
// plugin satisfies the contract *through a host*" is where a real host can disagree with
// this repository's assumptions, and nothing shorter than running the real host finds
// that out. That is not hypothetical: this gate is what established that the released
// host does not hand a discovered plugin its install tree on Windows, which is why
// release.yaml is still `uncertified` and why the packaged-default companion claim is not
// published. See docs/certification.md.
//
// Three properties make the evidence trustworthy rather than anecdotal:
//
//   - The host binary is supplied by the operator together with the digest they expect.
//     The gate refuses to run without both and re-checks the digest when it finishes, so a
//     run that rebuilt, patched, or replaced the host cannot pass as host evidence.
//   - The plugin is a real release install tree, checked against the shipped
//     `checksums.sha256` before anything starts and again at the end.
//   - Nothing here consumes provider quota or reads a real credential. The deterministic
//     fake bridge stands in for the Cursor SDK only; the host, the outer plugin
//     executable, the secure channel, the canonical stream, and - in the two cases that
//     need them - the shipped private runtime and its operator-provisioned SDK are all
//     real. The cases that address the private runtime end in an explicit prerequisite
//     failure, never in provider work.
//
// The gate is opt-in and off by default: it needs a host binary this repository neither
// builds nor ships.

const (
	// hostCertGateEnv opts a checkout into the real-host certification gate.
	//
	// It is opt-in for the same reason the package gate is: the evidence needs an
	// artifact a unit test must not fetch, build, or substitute. The opt-in is named in
	// docs/certification.md, so a green default run cannot be read as host certification.
	hostCertGateEnv = "LIP_HOST_CERT_GATE"
	// hostCertBinaryEnv is the released host executable under test.
	hostCertBinaryEnv = "LIP_HOST_CERT_HOST_BINARY"
	// hostCertSHA256Env is the digest the operator expects that executable to have. It is
	// required rather than optional: a gate that measured an artifact without knowing
	// which artifact it measured cannot be cited as evidence for a version.
	hostCertSHA256Env = "LIP_HOST_CERT_HOST_SHA256"
	// hostCertPluginRootEnv is an unpacked release install tree - the single directory the
	// host's discovery paths point at.
	hostCertPluginRootEnv = "LIP_HOST_CERT_PLUGIN_ROOT"
	// hostCertPluginArchiveEnv and hostCertPluginArchiveSHAEnv optionally name the archive
	// the install tree was unpacked from and the digest that archive must have, so a
	// recorded run is tied to a specific artifact rather than to a directory.
	hostCertPluginArchiveEnv    = "LIP_HOST_CERT_PLUGIN_ARCHIVE"
	hostCertPluginArchiveSHAEnv = "LIP_HOST_CERT_PLUGIN_ARCHIVE_SHA256"

	// hostCertFactoryKind and hostCertPluginID are the identity the shipped manifest
	// declares. Every assertion about what the host reported is made against the
	// artifact's own bytes, so the gate cannot drift from the release it certifies.
	hostCertFactoryKind = "cursorsdk"
	hostCertPluginID    = "io.golip.backend.cursorsdk"

	// hostCertInstanceID is the configured backend instance the certified runs activate.
	hostCertInstanceID = "cursor-sdk"
	// hostCertModel is the canonical selector the certified execution routes to. The
	// deterministic bridge advertises it, so the route names a real model identity rather
	// than a literal the router would accept without asking the plugin anything.
	hostCertModel = "cursor/gpt-5.3-codex"
	// hostCertAPIKey is a fixed non-secret string. A certification must never read a real
	// credential, and the negative cases deliberately rely on it not being one.
	hostCertAPIKey = "certification-not-a-real-credential"

	hostCertCommandTimeout = 4 * time.Minute
	hostCertStartupBudget  = 90 * time.Second
	hostCertRequestTimeout = 2 * time.Minute
)

// Markers the gate reads out of the host's own log to decide what a run observed.
//
// They are connector and host diagnostics rather than strings this gate invents, so the
// decision is made by the real components: a bridge that exited before answering says
// `cursor_sdk_bridge_exited`, a companion the connector could not resolve says `not found`
// and names the expected relative location, and a bridge that answered models/list says so.
const (
	// hostCertBridgeExited is the connector's code for a bridge that died before it could
	// answer.
	hostCertBridgeExited = "cursor_sdk_bridge_exited"
	// hostCertCompanionUnreachable is the connector's own wording for a packaged companion
	// it could not resolve relative to the running executable.
	hostCertCompanionUnreachable = "not found"
	// hostCertModelsListReached is proof the bridge answered at all.
	hostCertModelsListReached = "models/list"
)

// TestHostReleaseCertification_RealHostInstallTrustAndOptionalActivation is the task 4.2
// gate: trusted discovery, manifest identity, secure negotiation, inventory listing,
// canonical execution, explicit capability errors, inactive discovery, default-deny
// multi-user behaviour, and explicit prerequisite failures for a missing plugin or a
// missing runtime - all against a versioned host binary that is never recompiled.
func TestHostReleaseCertification_RealHostInstallTrustAndOptionalActivation(t *testing.T) {
	if testing.Short() {
		t.Skip("real-host certification drives a released host binary")
	}
	env := resolveHostCertInputs(t)

	// The host binary is measured once here and once more when the whole gate finishes.
	// Every subtest below runs the same bytes, so drift across all of them is a failure
	// of the evidence rather than of one case.
	t.Cleanup(func() {
		assert.Equal(t, env.hostSHA256, fileSHA256(t, env.hostBinary),
			"the host binary changed while the certification ran; a rebuilt or replaced host is not host evidence")
	})

	t.Run("host_binary_is_the_artifact_the_operator_named", func(t *testing.T) {
		version, _, err := env.runInfo(t, "version")
		require.NoError(t, err, "the supplied host binary does not run")
		assert.Contains(t, version, "lipstd",
			"the certified binary has to identify itself as the host distribution")

		// The host's own usage text is the operator surface this gate drives. If the
		// subcommands changed, every assertion below would be describing something an
		// operator cannot run, so the gate checks the surface it depends on. The released
		// binary writes usage to stderr, so both streams count.
		stdout, stderr, err := env.runInfo(t, "--help")
		require.NoError(t, err)
		usage := stdout + stderr
		for _, subcommand := range []string{"check-config", "routes", "inventory", "inspect", "doctor", "serve"} {
			assert.Contains(t, usage, subcommand,
				"the certified host has to expose %s; this gate drives it as an operator would", subcommand)
		}
	})

	t.Run("installed_plugin_is_trusted_and_discovered_with_manifest_identity", func(t *testing.T) {
		// Discovery is asserted on an unconfigured install, so the row under inspection is
		// purely the host's view of an installed artifact. Activation is asserted in the
		// negotiation case below, where the same row has to change state.
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, false, ""))

		entry := requireHostCertEntry(t, host.inspect(t))
		assert.Equal(t, "discovered", entry.Source,
			"a plugin installed under a configured discovery path is discovered, not builtin")
		assert.Equal(t, "discovered", entry.State)
		assert.Equal(t, "ok", entry.Reason,
			"a trusted, digest-bound, parseable manifest has to be accepted")
		assert.Equal(t, hostCertPluginID, entry.PluginID)
		assert.Equal(t, hostCertFactoryKind, entry.Kind)

		// Identity is cross-checked against the artifact's own bytes rather than against
		// this repository's release.yaml: the host read the manifest that ships in the
		// archive, so its report and that manifest have to be the same document.
		manifest := env.tree.manifest
		assert.Equal(t, manifest["plugin_id"], entry.PluginID,
			"the host reported a plugin id the shipped manifest does not declare")
		assert.Equal(t, declaredPlatformOf(t, manifest), env.tree.declaredPlatform,
			"the shipped manifest has to declare the platform this host runs")

		// The closed trust boundary is what the host composes from, so a narrowed or
		// widened export would change the host's behaviour. It is asserted here so the
		// certification cannot be cited for a manifest whose posture nobody checked.
		exports, ok := manifest["exports"].([]any)
		require.True(t, ok, "the shipped manifest carries no exports")
		require.Len(t, exports, 1, "the shipped manifest has to export exactly one kind")
		export, ok := exports[0].(map[string]any)
		require.True(t, ok, "a manifest export has to be an object")
		for field, want := range map[string]string{
			"kind":            hostCertFactoryKind,
			"credential_mode": "static",
			"access_scope":    "local_only",
			"process_sharing": "per_instance",
			"execution_class": "agent_runtime",
		} {
			assert.Equal(t, want, export[field],
				"the certified manifest has to declare %s=%q; that is the posture the host enforces", field, want)
		}
	})

	t.Run("inactive_install_is_reported_without_starting_the_runtime", func(t *testing.T) {
		// A bridge path that cannot exist, so a launch would produce an explicit
		// diagnostic naming it. The enabled control below turns the same field on and
		// fails, which is what makes this a proof of absence rather than an observation
		// that nothing happened to break.
		absentBridge := filepath.Join(t.TempDir(), "absent-companion"+exeSuffix())
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, false, absentBridge))

		report := host.inspect(t)
		entry := requireHostCertEntry(t, report)
		assert.Equal(t, "discovered", entry.Source)
		assert.False(t, entry.ActivationRequired,
			"an installed but unconfigured plugin must not require activation")
		assert.Empty(t, entry.InstanceID,
			"an unconfigured plugin has no configured instance to belong to")
		for _, other := range report.Entries {
			if other.Kind != hostCertFactoryKind {
				continue
			}
			assert.NotEqual(t, "builtin", other.Source,
				"an externally installed plugin must never be classified as a builtin")
		}
		assert.NotContains(t, host.combinedOutput(), absentBridge,
			"inspect must not launch anything; the absent bridge was reachable only by starting the plugin")

		// The control: the identical configuration with the instance enabled has to fail
		// explicitly. Without it, the pass above could also be satisfied by a host that
		// never launches a plugin at all.
		enabled := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, absentBridge))
		_, _, err := enabled.run(t, "serve")
		require.Error(t, err, "enabling the instance has to reach the bridge rather than succeed")
		assert.Contains(t, enabled.combinedOutput(), filepath.ToSlash(absentBridge),
			"the enabled control has to name the unreachable bridge, which is what proves the inactive pass started nothing")
	})

	t.Run("secure_negotiation_activates_the_configured_instance", func(t *testing.T) {
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, ""))

		// Configuring the instance has to move the discovered row to an activated one, or
		// "trusted discovery" and "optional activation" would be one claim rather than two.
		entry := requireHostCertEntry(t, host.inspect(t))
		assert.Equal(t, "configured", entry.State,
			"configuring the instance has to activate the discovered export")
		assert.True(t, entry.ActivationRequired,
			"an activated export reports that it required activation")

		var report hostCertDoctorReport
		stdout, _, err := host.run(t, "doctor", "--instance", hostCertInstanceID)
		require.NoError(t, err, "doctor has to succeed for a configured, trusted instance:\n%s", host.combinedOutput())
		require.NoError(t, json.Unmarshal([]byte(stdout), &report), "doctor report:\n%s", stdout)
		require.Len(t, report.Results, 1, "doctor reports one row for the instance it was asked about")
		result := report.Results[0]
		assert.Equal(t, hostCertInstanceID, result.InstanceID)
		assert.Equal(t, hostCertFactoryKind, result.Kind)
		assert.Equal(t, "active", result.State)
		assert.Equal(t, "ok", result.Reason)
		assert.True(t, result.Launched,
			"doctor reports the plugin process as launched; a negotiation that launched nothing is not negotiation")
		assert.Contains(t, strings.ToLower(result.Guidance), "secure",
			"the host has to state that the channel it used is the approved secure one")
	})

	t.Run("inventory_lists_the_configured_plugin_instance", func(t *testing.T) {
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, ""))

		var snapshot struct {
			Backends []struct {
				ID          string `json:"id"`
				FactoryKind string `json:"factory_kind"`
				Enabled     bool   `json:"enabled"`
			} `json:"backends"`
		}
		stdout, _, err := host.run(t, "inventory")
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal([]byte(stdout), &snapshot), "inventory report:\n%s", stdout)

		found := false
		for _, backend := range snapshot.Backends {
			if backend.FactoryKind != hostCertFactoryKind {
				continue
			}
			found = true
			assert.Equal(t, hostCertInstanceID, backend.ID)
			assert.True(t, backend.Enabled, "a configured instance has to be inventoried as enabled")
		}
		assert.True(t, found,
			"the configured cursorsdk instance has to appear in the host inventory; a plugin that composes "+
				"but is not inventoried cannot be selected by a route")
	})

	t.Run("canonical_execution_serves_a_canonical_response_over_http", func(t *testing.T) {
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, fakebridge.BuildExe(t)))

		host.serve(t, func(client *http.Client, addr string) {
			status, body := postResponses(t, client, addr, `{"model":"`+hostCertModel+`","input":"certification ping","stream":false}`)
			require.Equal(t, http.StatusOK, status,
				"a canonical request through the plugin has to be served:\n%s\nhost output:\n%s", body, host.combinedOutput())

			var response struct {
				Status string `json:"status"`
				Model  string `json:"model"`
				Output []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &response), "response body:\n%s", body)
			assert.Equal(t, "completed", response.Status)
			assert.Equal(t, hostCertModel, response.Model)
			assert.NotEmpty(t, hostCertResponseText(response.Output),
				"the certified run produced no canonical content, so it proved transport rather than streaming")
		})

		// The host's own attempt log is what distinguishes "the plugin served this" from
		// "the host answered from somewhere else": one opened attempt on the plugin's own
		// candidate key, and no credential anywhere.
		output := host.combinedOutput()
		assert.Contains(t, output, "backend_attempt_opened",
			"a served request has to open a backend attempt in the host log")
		assert.Contains(t, output, hostCertInstanceID+":"+hostCertModel,
			"the served request has to be routed to the plugin's own candidate key")
		assert.NotContains(t, output, hostCertAPIKey,
			"the static credential must not appear anywhere in the host's own output")
	})

	t.Run("a_capability_the_plugin_does_not_serve_is_refused_explicitly", func(t *testing.T) {
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, fakebridge.BuildExe(t)))

		host.serve(t, func(client *http.Client, addr string) {
			// The resolved profile advertises streaming and reasoning only, so a request
			// carrying tools has to be refused by name. A host that narrowed the request
			// and served it anyway would pass the execution case and fail this one.
			status, body := postResponses(t, client, addr,
				`{"model":"`+hostCertModel+`","input":"certification ping","stream":false,`+
					`"tools":[{"type":"function","name":"noop","description":"n","parameters":{"type":"object","properties":{}}}]}`)
			require.Equal(t, http.StatusBadRequest, status,
				"an unserved capability has to be refused, not dropped:\n%s\nhost output:\n%s", body, host.combinedOutput())
			assert.Contains(t, body, "tools",
				"the refusal has to name the capability that was required")
			assert.NotContains(t, body, "output_text",
				"a refused request must not carry provider content")

			// The capability proof means nothing without a positive control on the same
			// host and the same instance.
			status, body = postResponses(t, client, addr, `{"model":"`+hostCertModel+`","input":"certification ping","stream":false}`)
			assert.Equal(t, http.StatusOK, status,
				"the same instance has to serve the plain request, or the refusal proves nothing:\n%s\nhost output:\n%s",
				body, host.combinedOutput())
		})
	})

	t.Run("multi_user_access_denies_the_local_only_agent_runtime", func(t *testing.T) {
		host := newHostCertRun(t, env, env.pluginRoot, hostCertMultiUser, hostCertBackend(t, true, ""))
		_, _, err := host.run(t, "serve")
		require.Error(t, err,
			"a local_only agent runtime must not compose in multi_user mode; acceptance here is the whole failure this proves")

		denial := host.combinedOutput()
		assert.Contains(t, denial, "multi_user",
			"the denial has to name the access mode that caused it")
		assert.Contains(t, denial, hostCertInstanceID,
			"the denial has to name the instance an operator would have to change")
		assert.Contains(t, denial, hostCertFactoryKind)
		assert.NotContains(t, denial, "listening addr=",
			"a denied composition must not start listening")

		// Default-deny is absence of approval rather than a plugin-side switch, so the
		// same host and the same artifact still compose in single-user mode.
		single := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, ""))
		_, _, err = single.run(t, "check-config")
		assert.NoError(t, err,
			"the instance has to compose in single-user mode, or the denial above proves nothing about multi_user")
	})

	t.Run("a_missing_plugin_artifact_fails_closed_without_automatic_installation", func(t *testing.T) {
		host := newHostCertRun(t, env, t.TempDir(), hostCertSingleUser, hostCertBackend(t, true, ""))

		_, _, err := host.run(t, "check-config")
		require.Error(t, err,
			"an enabled backend whose plugin is absent has to fail closed at composition")
		assert.Contains(t, host.combinedOutput(), "unresolved",
			"the failure has to name the unresolved backend kind rather than degrade to another provider")
		assert.NotContains(t, host.combinedOutput(), "npm ci",
			"the host must never provision anything on an operator's behalf")

		_, _, err = host.run(t, "serve")
		require.Error(t, err, "serve has to fail closed on the same configuration")
		assert.NotContains(t, host.combinedOutput(), "listening addr=",
			"a configuration with an unresolved backend must not start listening")
		assert.NotContains(t, host.combinedOutput(), hostCertAPIKey)
	})

	t.Run("a_missing_private_runtime_is_an_explicit_prerequisite", func(t *testing.T) {
		// The real shipped launcher and the real shipped bridge, with the private Node
		// runtime removed. The launcher is addressed explicitly because that is how an
		// operator reaches it on a host that stages the outer executable; what this case
		// asserts is what the plugin says when its runtime is gone.
		tree := env.copyInstallTree(t)
		require.NoError(t, os.RemoveAll(filepath.Join(tree.root, filepath.FromSlash(env.archive.PrivateRuntimeDirPath()))))

		host := newHostCertRun(t, env, tree.root, hostCertSingleUser, hostCertBackend(t, true, tree.launcherPath(t)))
		host.serve(t, func(client *http.Client, addr string) {
			status, body := postResponses(t, client, addr, `{"model":"`+hostCertModel+`","input":"certification ping","stream":false}`)
			require.NotEqual(t, http.StatusOK, status,
				"a plugin with no private runtime must not serve a request:\n%s", body)
		})

		denial := host.combinedOutput()
		assert.Contains(t, denial, "private runtime",
			"the diagnostic has to name the missing runtime resource")
		assert.Contains(t, denial, packagelayout.RuntimeRelDoc,
			"the diagnostic has to name the packaged location the runtime was expected at, in the "+
				"documented spelling an operator would look for")
		assert.Contains(t, denial, "reinstall",
			"the diagnostic has to name the operator remedy")
		assert.NotContains(t, denial, "npm ci",
			"the plugin must not fetch its own runtime")
		assert.NotContains(t, denial, hostCertAPIKey,
			"a prerequisite failure must not carry the configured credential")
	})

	t.Run("an_unprovisioned_cursor_sdk_is_an_explicit_prerequisite", func(t *testing.T) {
		// The archive ships no Cursor SDK by design, so this is the state a freshly
		// installed tree is in until the operator provisions it. It has to be reported as
		// a missing prerequisite carrying the exact command, never worked around.
		tree := env.copyInstallTree(t)
		require.NoError(t, os.RemoveAll(filepath.Join(tree.root, filepath.FromSlash(env.archive.BridgeModulesPath()))))

		host := newHostCertRun(t, env, tree.root, hostCertSingleUser, hostCertBackend(t, true, tree.launcherPath(t)))
		host.serve(t, func(client *http.Client, addr string) {
			status, body := postResponses(t, client, addr, `{"model":"`+hostCertModel+`","input":"certification ping","stream":false}`)
			require.NotEqual(t, http.StatusOK, status,
				"an unprovisioned SDK must not serve a request:\n%s", body)
		})

		denial := host.combinedOutput()
		assert.Contains(t, denial, "not provisioned",
			"the diagnostic has to say the SDK is not provisioned rather than reporting a provider failure")
		assert.Contains(t, denial, packagelayout.SDKPackageName,
			"the diagnostic has to name the package that is missing")
		assert.Contains(t, denial, env.archive.ProvisionInvocation(),
			"the diagnostic has to carry the exact per-platform npm invocation for this tree")
		assert.Contains(t, denial, "ci --omit=dev",
			"the provisioning command has to be the shipped lockfile's npm ci --omit=dev")
		assert.NotContains(t, denial, hostCertAPIKey,
			"a prerequisite failure must not carry the configured credential")
	})

	t.Run("the_packaged_default_bridge_resolution_is_what_the_operator_gets", func(t *testing.T) {
		// The archive's headline convenience is that an operator who configures nothing
		// gets the packaged private companion, resolved relative to the installed outer
		// executable. This case runs exactly that configuration - no bridge_executable at
		// all - and decides, from the host's own log, whether the chain actually ran.
		//
		// The outcome is platform-dependent and is a measurement rather than a verdict,
		// because it depends on how the host binds the verified executable - the host's
		// decision, not this release's. What is asserted on both platforms is the
		// contract: the chain either reaches the Cursor SDK, or it fails explicitly,
		// naming the packaged location, offering the operator remedy, and provisioning or
		// falling back to nothing.
		host := newHostCertRun(t, env, env.pluginRoot, hostCertSingleUser, hostCertBackend(t, true, ""))
		host.serve(t, func(client *http.Client, addr string) {
			// The request is deliberately best-effort: on a platform where the default
			// cannot be reached the host fails at bootstrap and never listens, which is
			// itself the outcome this case decides on.
			tryPostResponses(t, client, addr, `{"model":"`+hostCertModel+`","input":"certification ping","stream":false}`)
		})

		denial := host.combinedOutput()
		if !strings.Contains(denial, hostCertCompanionUnreachable) {
			// The chain ran. Reaching the SDK at all is the proof: the private launcher
			// started the packaged Node runtime, which loaded the operator-provisioned SDK
			// and answered models/list, and only the credential is wrong. A companion that
			// was never reached could not produce a Cursor-side answer.
			assert.Contains(t, denial, hostCertModelsListReached,
				"the packaged default reached the Cursor SDK, so the log has to show models/list answering; "+
					"host output:\n%s", denial)
			assert.NotContains(t, denial, hostCertBridgeExited,
				"the bridge ran, so it cannot also have exited before answering")
			t.Logf("MEASURED packaged default companion resolution on %s: REACHABLE", hostCertPlatformLabel())
			return
		}

		assert.Contains(t, denial, env.archive.LauncherPath(),
			"the diagnostic has to name the packaged companion location")
		assert.Contains(t, denial, "bridge_executable",
			"the diagnostic has to name the operator remedy")
		assert.NotContains(t, denial, "npm ci",
			"the plugin must not provision anything to reach its companion")
		assert.NotContains(t, denial, hostCertAPIKey,
			"a prerequisite failure must not carry the configured credential")
		t.Logf("MEASURED packaged default companion resolution on %s: UNREACHABLE", hostCertPlatformLabel())
	})

	t.Run("the_installed_tree_is_unchanged_by_the_certified_run", func(t *testing.T) {
		// The plugin authenticates what it ships; the host authenticates the outer
		// executable only. Nothing in a certified run may rewrite the install tree, or the
		// archive an operator verified is not the one that served the request.
		env.verifyShippedChecksums(t, env.pluginRoot)
	})
}

// hostCertAccessMode is the deployment posture one certified run composes for.
type hostCertAccessMode string

const (
	hostCertSingleUser hostCertAccessMode = "single_user"
	hostCertMultiUser  hostCertAccessMode = "multi_user"
)

// hostCertInputs is the resolved opt-in contract for one certified run.
type hostCertInputs struct {
	hostBinary string
	hostSHA256 string
	pluginRoot string
	archive    packagelayout.Archive

	// tree is the operator-supplied install tree, resolved and checked once.
	tree *hostCertInstallTree
}

// hostCertInstallTree is one install tree plus the shipped metadata the gate reads.
type hostCertInstallTree struct {
	root             string
	manifest         map[string]any
	declaredPlatform string
}

// resolveHostCertInputs resolves and validates the opt-in environment.
//
// Every input is checked before any subtest runs, so a misconfigured run fails once with
// a clear message instead of halfway through with a confusing one.
func resolveHostCertInputs(tb testing.TB) *hostCertInputs {
	tb.Helper()

	if strings.TrimSpace(os.Getenv(hostCertGateEnv)) != "1" {
		tb.Skipf("set %s=1 together with %s, %s, %s, and %s to certify this release against a released host binary",
			hostCertGateEnv, hostCertBinaryEnv, hostCertSHA256Env, hostCertPluginRootEnv, hostCertPluginArchiveEnv)
	}

	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(tb, err,
		"the host certification runs on the host's own platform, and this one is not a platform the plugin declares")

	binary := requireHostCertInput(tb, hostCertBinaryEnv, false, "the host executable itself")
	wantSHA := requireHostCertDigest(tb, hostCertSHA256Env)
	root := requireHostCertInput(tb, hostCertPluginRootEnv, true,
		"one install tree: the host scans each configured path non-recursively for "+
			"*.backendplugin.json, so a parent directory of several trees discovers nothing")

	// The archive digest is optional, but a named archive requires a named digest: an
	// artifact named without a measured digest is not a recorded artifact.
	if archiveFile := strings.TrimSpace(os.Getenv(hostCertPluginArchiveEnv)); archiveFile != "" {
		require.NotEmpty(tb, strings.TrimSpace(os.Getenv(hostCertPluginArchiveSHAEnv)),
			"naming a plugin archive requires naming the digest it is expected to have")
		assert.Equal(tb, requireHostCertDigest(tb, hostCertPluginArchiveSHAEnv), fileSHA256(tb, archiveFile),
			"the supplied plugin archive does not have the digest the operator expected")
	}

	inputs := &hostCertInputs{hostBinary: binary, hostSHA256: wantSHA, pluginRoot: root, archive: archive}
	inputs.tree = inputs.openInstallTree(tb, root)
	inputs.verifyShippedChecksums(tb, root)
	return inputs
}

// requireHostCertInput resolves one required opt-in path and checks what it has to be.
//
// wantDir says what the input is: the host executable itself, or one unpacked install tree.
// The install-tree case carries the extra constraint that matters, because the host scans
// each configured discovery path non-recursively - a path that names a parent directory of
// several trees discovers nothing at all, and the gate would then measure an empty catalog.
func requireHostCertInput(tb testing.TB, name string, wantDir bool, whyNot string) string {
	tb.Helper()

	value := strings.TrimSpace(os.Getenv(name))
	require.NotEmpty(tb, value, "%s is required: the gate certifies a supplied artifact, it does not build one", name)
	info, err := os.Stat(value)
	require.NoError(tb, err, "%s=%s does not exist", name, value)
	require.Equal(tb, wantDir, info.IsDir(), "%s=%s is not %s", name, value, whyNot)
	return value
}

func requireHostCertDigest(tb testing.TB, name string) string {
	tb.Helper()

	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	require.Len(tb, value, hex.EncodedLen(32), "%s has to be a sha256 digest", name)
	_, err := hex.DecodeString(value)
	require.NoError(tb, err, "%s is not hex: %v", name, err)
	return value
}

// openInstallTree reads the shipped metadata of one install tree and checks that the
// record and the manifest describe the same artifact on this host's platform.
func (in *hostCertInputs) openInstallTree(tb testing.TB, root string) *hostCertInstallTree {
	tb.Helper()

	manifest := decodeJSONObject(tb, filepath.Join(root, packagelayout.ManifestFileName))
	record := decodeJSONObject(tb, filepath.Join(root, packagelayout.CompatibilityFileName))
	declared := declaredPlatformOf(tb, manifest)

	require.Equal(tb, in.archive.Platform(), declared,
		"the install tree was assembled for another platform than the host certifying it runs on")
	require.Equal(tb, hostCertPluginID, manifest["plugin_id"])
	require.Equal(tb, in.archive.Platform(), record["native_platform_assembled"],
		"the release record and the shipped manifest disagree about the assembled platform")
	require.Equal(tb, "private-runtime", record["packaging_variant"])
	require.Equal(tb, false, record["external_node_required"])
	require.Equal(tb, false, record["cursor_sdk_bundled"],
		"the Cursor SDK is operator-provisioned; an archive that bundles it is not this release")

	return &hostCertInstallTree{root: root, manifest: manifest, declaredPlatform: declared}
}

// copyInstallTree copies the supplied tree into this test's own directory.
//
// Mutating cases need a tree they can break. Copying rather than mutating in place keeps
// every case independent and leaves the operator's tree usable by the next one; the copy
// is verified against the shipped checksum record afterwards, so a case that silently
// repaired what it removed would fail rather than pass.
func (in *hostCertInputs) copyInstallTree(tb testing.TB) *hostCertInstallTree {
	tb.Helper()

	root := filepath.Join(tb.TempDir(), filepath.Base(in.pluginRoot))
	require.NoError(tb, copyHostCertTree(in.pluginRoot, root), "the install tree has to be copyable for the mutating cases")
	tree := in.openInstallTree(tb, root)
	return tree
}

// launcherPath is the packaged private launcher inside this tree, addressed the way an
// operator addresses it on a host that stages the outer executable.
func (t *hostCertInstallTree) launcherPath(tb testing.TB) string {
	tb.Helper()

	archive, err := packagelayout.ForPlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(tb, err)
	return filepath.Join(t.root, filepath.FromSlash(archive.LauncherPath()))
}

// verifyShippedChecksums re-derives the shipped checksum record against the tree.
//
// The record is unsigned, so this proves internal consistency and tamper detection rather
// than provenance. It is still the check the gate needs: it ties every certified run to
// bytes whose shipped manifest, release record, and executable all agree.
func (in *hostCertInputs) verifyShippedChecksums(tb testing.TB, root string) {
	tb.Helper()

	raw := readFileText(tb, filepath.Join(root, packagelayout.ChecksumsFileName))
	lines := 0
	for _, line := range strings.Split(raw, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		digest, name, found := strings.Cut(entry, packagelayout.ChecksumSeparator)
		require.True(tb, found, "checksums.sha256 line %q is not a digest and a path", entry)
		name = strings.TrimSpace(name)
		require.False(tb, strings.HasPrefix(name, packagelayout.ProvisionedPrefix),
			"the shipped record must not cover the operator-provisioned tree; %s is operator-owned", name)
		assert.Equal(tb, strings.ToLower(digest), fileSHA256(tb, filepath.Join(root, filepath.FromSlash(name))),
			"shipped file %s does not match the archive's checksum record", name)
		lines++
	}
	require.NotZero(tb, lines, "the shipped checksum record is empty")

	// The shipped record is deliberately partial, and the tree is deliberately allowed to
	// be a provisioned one: an operator provisions node_modules into the installed tree, so
	// its presence is expected. What must hold is the split the provenance claims - the
	// record covers shipped files only, the release record says the SDK is not bundled, and
	// a provisioned SDK resolves at exactly the version the record requires.
	require.FileExists(tb, filepath.Join(root, filepath.FromSlash(in.archive.PrivateRuntimeNPMCLIPath())),
		"the shipped runtime's own bundled npm is what makes provisioning possible without a global toolchain")

	record := decodeJSONObject(tb, filepath.Join(root, packagelayout.CompatibilityFileName))
	required, _ := record["cursor_sdk_required_version"].(string)
	require.NotEmpty(tb, required, "the release record has to name the SDK version a tree has to resolve")
	if sdkMetadata := filepath.Join(root, filepath.FromSlash(in.archive.ProvisionedSDKPackageJSONPath())); fileExists(sdkMetadata) {
		provisioned := decodeJSONObject(tb, sdkMetadata)
		assert.Equal(tb, required, provisioned["version"],
			"a provisioned tree has to resolve the SDK at the version the release record requires; "+
				"anything else is a tree the plugin will refuse at run time")
	}
}

// runInfo drives a host command that is not a configuration subcommand.
//
// `version` and `--help` are answered before the host parses flags, so passing `--config`
// would turn them into an unknown-flag failure. That is a property of the released binary's
// argument handling rather than a convenience of this gate, so the gate has to respect it.
func (in *hostCertInputs) runInfo(tb testing.TB, args ...string) (stdout, stderr string, err error) {
	tb.Helper()

	run := newHostCertRun(tb, in, in.pluginRoot, hostCertSingleUser, hostCertBackend(tb, false, ""))
	return run.runBare(tb, args...)
}

// declaredPlatformOf reads the single platform claim out of a shipped manifest.
func declaredPlatformOf(tb testing.TB, manifest map[string]any) string {
	tb.Helper()

	platforms, ok := manifest["platforms"].([]any)
	require.True(tb, ok && len(platforms) == 1,
		"a native archive narrows its manifest to exactly the platform it was assembled on")
	entry, ok := platforms[0].(map[string]any)
	require.True(tb, ok, "a manifest platform claim has to be an object")
	return fmt.Sprintf("%v/%v", entry["os"], entry["arch"])
}

// hostCertInspectEntry is one bounded host inspect row.
type hostCertInspectEntry struct {
	Source             string `json:"source"`
	InstanceID         string `json:"instance_id"`
	PluginID           string `json:"plugin_id"`
	Kind               string `json:"kind"`
	State              string `json:"state"`
	Reason             string `json:"reason"`
	ActivationRequired bool   `json:"activation_required"`
}

// hostCertInspectReport is the host's non-executing plugin snapshot.
type hostCertInspectReport struct {
	Entries []hostCertInspectEntry `json:"entries"`
}

// hostCertDoctorResult is one host doctor row.
type hostCertDoctorResult struct {
	InstanceID string `json:"instance_id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	Guidance   string `json:"guidance"`
	Launched   bool   `json:"launched"`
}

type hostCertDoctorReport struct {
	Results []hostCertDoctorResult `json:"results"`
}

// hostCertSink accumulates one output stream of a running host process.
//
// The gate reads a host's output while the process is still writing it - the readiness
// check, and every assertion about a diagnostic that arrived before the request did - so
// the buffer cannot be a plain bytes.Buffer: those readers and the pipe-draining goroutine
// would race on it, and a racing read is exactly the kind of nondeterminism that makes a
// certification unciteable.
type hostCertSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *hostCertSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *hostCertSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// hostCertRun is one host process' worth of certified work: a configuration, a temporary
// home, and everything the host wrote.
type hostCertRun struct {
	inputs *hostCertInputs
	access hostCertAccessMode
	// dir is this run's private directory: the host's working directory, its HOME, and
	// the parent of its configuration. One directory keeps a certified host from reading
	// or writing anything of the operator's.
	dir    string
	config string
	addr   string

	stdout hostCertSink
	stderr hostCertSink
}

func newHostCertRun(tb testing.TB, inputs *hostCertInputs, pluginRoot string,
	access hostCertAccessMode, backendYAML string) *hostCertRun {
	tb.Helper()

	dir := tb.TempDir()
	run := &hostCertRun{inputs: inputs, access: access, dir: dir}
	run.addr = freeHostCertAddr(tb)
	run.config = filepath.Join(dir, "config.yaml")
	require.NoError(tb, os.WriteFile(run.config, []byte(hostCertConfigYAML(
		pluginRoot, backendYAML, run.addr, access)), 0o600))
	return run
}

// hostCertEnvironment is the environment a certified host process runs with.
//
// It is deliberately minimal: the gate measures a released artifact, so nothing about the
// operator's machine may leak into it, and Cursor credentials are not forwarded because no
// case here may reach a provider.
func (r *hostCertRun) hostCertEnvironment() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.dir,
		"USERPROFILE=" + r.dir,
		"TEMP=" + os.Getenv("TEMP"),
		"TMP=" + os.Getenv("TMP"),
	}
	if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
		env = append(env, "SYSTEMROOT="+systemRoot)
	}
	return env
}

// run drives one host subcommand and returns its stdout and stderr.
//
// Both streams are captured rather than only the one a passing assertion needs: every
// failure case in this file is observed through the host's own diagnostics.
// run drives one host operator subcommand with this run's configuration.
//
// The host collects flags that precede the subcommand and rejects them after it, so the
// multi-user opt-in goes in front of the command name - exactly where an operator types
// it.
func (r *hostCertRun) run(tb testing.TB, args ...string) (stdout, stderr string, err error) {
	tb.Helper()

	prefix := []string{"--config", r.config}
	if r.access == hostCertMultiUser {
		prefix = append(prefix, "-multi-user")
	}
	return r.runBare(tb, append(prefix, args...)...)
}

// runBare runs the host with an explicit argument vector and no implicit configuration.
func (r *hostCertRun) runBare(tb testing.TB, args ...string) (stdout, stderr string, err error) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), hostCertCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.inputs.hostBinary, args...)
	cmd.Dir = r.dir
	cmd.Env = r.hostCertEnvironment()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()

	_, _ = r.stdout.Write(outBuf.Bytes())
	_, _ = r.stderr.Write(errBuf.Bytes())
	return outBuf.String(), errBuf.String(), err
}

// combinedOutput is everything this host process wrote.
func (r *hostCertRun) combinedOutput() string {
	return r.stdout.String() + "\n" + r.stderr.String()
}

// hostCertSink implements io.Writer, so the readiness check and the assertions below can
// read a host's output while it is still running.
var _ io.Writer = (*hostCertSink)(nil)

// inspect is the host's non-executing plugin snapshot.
func (r *hostCertRun) inspect(tb testing.TB) hostCertInspectReport {
	tb.Helper()

	stdout, _, err := r.run(tb, "inspect")
	require.NoError(tb, err, "inspect has to succeed for a trusted install:\n%s", r.combinedOutput())
	var report hostCertInspectReport
	require.NoError(tb, json.Unmarshal([]byte(stdout), &report), "inspect report:\n%s", stdout)
	return report
}

// serve starts the host, waits for it to listen, runs body against it, and stops it.
//
// The host is stopped rather than asked to shut down: several certified cases are cases
// whose composition is expected to fail, there is nothing to shut down cleanly in those,
// and the plugin processes are owned by the host's own process-tree policy. A case that
// expects bootstrap to fail simply finds no listener and reads the diagnostics, so this
// one helper serves both shapes.
func (r *hostCertRun) serve(tb testing.TB, body func(client *http.Client, addr string)) {
	tb.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	argv := []string{r.inputs.hostBinary, "--config", r.config, "serve"}
	if r.access == hostCertMultiUser {
		argv = []string{r.inputs.hostBinary, "--config", r.config, "-multi-user", "serve"}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.dir
	cmd.Env = r.hostCertEnvironment()

	stdout, err := cmd.StdoutPipe()
	require.NoError(tb, err)
	stderr, err := cmd.StderrPipe()
	require.NoError(tb, err)
	require.NoError(tb, cmd.Start())

	drained := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(&r.stdout, stdout)
		drained <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&r.stderr, stderr)
		drained <- struct{}{}
	}()

	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		for i := 0; i < 2; i++ {
			select {
			case <-drained:
			case <-time.After(hostCertStartupBudget):
			}
		}
	}()

	// Body runs once the host is listening, or once it is clear it never will be. Waiting
	// for the listener rather than sleeping keeps the case fast on a healthy host and
	// still bounded on a broken one.
	deadline := time.Now().Add(hostCertStartupBudget)
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		for time.Now().Before(deadline) {
			if strings.Contains(r.combinedOutput(), "listening addr=") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	<-ready

	body(&http.Client{Timeout: hostCertRequestTimeout}, r.addr)
}

// freeHostCertAddr reserves and releases a loopback address for one host run.
func freeHostCertAddr(tb testing.TB) string {
	tb.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err, "the gate has to be able to bind a loopback port for the host")
	addr := listener.Addr().String()
	require.NoError(tb, listener.Close())
	return addr
}

// postResponses sends one OpenAI Responses request through the host's frontend.
func postResponses(tb testing.TB, client *http.Client, addr, body string) (int, string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), hostCertRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/responses", strings.NewReader(body))
	require.NoError(tb, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+hostCertAPIKey)

	resp, err := client.Do(req)
	require.NoError(tb, err, "the host did not answer on %s", addr)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(tb, err)
	return resp.StatusCode, string(raw)
}

// tryPostResponses sends one request and reports the outcome without asserting anything.
//
// It exists for the cases where "the host answered" is not the question - a case that has to
// discover whether the host came up at all, or is deliberately observing a bootstrap that
// fails. A refused connection is a result, not a failure of the harness.
func tryPostResponses(tb testing.TB, client *http.Client, addr, body string) (int, string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), hostCertRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/responses", strings.NewReader(body))
	if err != nil {
		tb.Logf("request to %s could not be built: %v", addr, err)
		return 0, ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+hostCertAPIKey)

	resp, err := client.Do(req)
	if err != nil {
		tb.Logf("the host did not answer on %s: %v", addr, err)
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Logf("the host's response on %s could not be read: %v", addr, err)
		return resp.StatusCode, ""
	}
	return resp.StatusCode, string(raw)
}

// hostCertResponseText flattens canonical output text out of a response body.
func hostCertResponseText(output []struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}) string {
	var sb strings.Builder
	for _, item := range output {
		for _, part := range item.Content {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

// requireHostCertEntry finds the single inspect row describing the installed artifact.
//
// The host reports a configured plugin twice - once as the export the instance activated
// and once for the configured instance row - so the row under test is selected by the
// plugin id, which only the artifact's own row carries.
func requireHostCertEntry(tb testing.TB, report hostCertInspectReport) hostCertInspectEntry {
	tb.Helper()

	var found []hostCertInspectEntry
	for _, entry := range report.Entries {
		if entry.PluginID == hostCertPluginID {
			found = append(found, entry)
		}
	}
	require.Len(tb, found, 1,
		"the host's inspect snapshot has to carry exactly one row for the installed artifact")
	return found[0]
}

// hostCertBackend renders the plugin-owned opaque backend configuration.
//
// An empty bridgeExecutable exercises the packaged default companion resolution, which is
// what an operator gets when they configure nothing. A non-empty one is an explicit direct
// bridge binary, which is the remedy the host's own diagnostic names when the default
// cannot be reached from a staged executable. The workspace is always explicit: it is the
// directory a real Cursor agent runs in, and a run that omitted it would measure the
// connector's handling of a missing optional field rather than the host's handling of a
// configured plugin.
func hostCertBackend(tb testing.TB, enabled bool, bridgeExecutable string) string {
	tb.Helper()

	workspace := filepath.Join(tb.TempDir(), "workspace")
	require.NoError(tb, os.MkdirAll(workspace, 0o755), "the certified configuration needs a workspace directory")

	var sb strings.Builder
	fmt.Fprintf(&sb, "    - kind: %s\n      id: %s\n      enabled: %t\n      config:\n",
		hostCertFactoryKind, hostCertInstanceID, enabled)
	fmt.Fprintf(&sb, "        api_key: %q\n", hostCertAPIKey)
	if bridgeExecutable != "" {
		fmt.Fprintf(&sb, "        bridge_executable: %q\n", filepath.ToSlash(bridgeExecutable))
	}
	fmt.Fprintf(&sb, "        default_workspace: %q\n", filepath.ToSlash(workspace))
	sb.WriteString("        sandbox_mode: \"off\"\n")
	sb.WriteString("        max_agents: 4\n")
	sb.WriteString("        max_concurrent_runs: 2\n")
	sb.WriteString("        bridge_start_timeout_seconds: 60\n")
	sb.WriteString("        cancel_timeout_seconds: 10\n")
	sb.WriteString("        shutdown_timeout_seconds: 15\n")
	return sb.String()
}

// hostCertConfigYAML is the host configuration one certified run uses.
//
// Two details of it are load-bearing and are stated here because they look incidental:
// discovery points at the install tree itself, because the host scans each configured path
// non-recursively for `*.backendplugin.json` - a path is an install directory, not a parent
// of several - and discovery_mode stays off so the run exercises the production posture
// rather than the development convenience.
func hostCertConfigYAML(pluginRoot, backendYAML, addr string, access hostCertAccessMode) string {
	head := []string{
		"server:",
		"  address: " + hostCertQuote(addr),
	}
	if access == hostCertMultiUser {
		// The host requires multi_user to be authenticated before it will even consider a
		// backend, so the denial under test has to be reached past that check rather than
		// being short-circuited by it.
		head = append(head,
			"  auth_mode: external",
			"access:",
			"  mode: multi_user",
			"auth:",
			"  handler: local_api_key",
			"  required_level: api_key",
			"  event_failure_policy: fail_closed",
			"  local_api_keys:",
			"    - key_id: cert-device",
			"      principal_id: cert-user",
			`      key: "certification-local-api-key-0001"`,
		)
	} else {
		head = append(head, "access:", "  mode: single_user")
	}

	tail := []string{
		"routing:",
		"  max_attempts: 1",
		`  default_route: "` + hostCertInstanceID + ":" + hostCertModel + `"`,
		"continuity:",
		"  in_memory: true",
		"  store: memory",
		"logging:",
		"  level: info",
		"  format: text",
		"diagnostics:",
		"  enabled: false",
		"plugins:",
		"  backend_discovery:",
		"    enabled: true",
		"    paths:",
		"      - " + hostCertQuote(filepath.ToSlash(pluginRoot)),
		"    strict: true",
		"    development_mode: false",
		"  frontends:",
		"    - id: openai-responses",
		"      enabled: true",
		"      config: {}",
		"  backends:",
		strings.TrimRight(backendYAML, "\n"),
		"  features:",
		"    - id: submit-noop",
		"      enabled: true",
		"      config: {}",
		"    - id: parts-noop",
		"      enabled: true",
		"      config: {}",
		"    - id: tool-reactor-noop",
		"      enabled: true",
		"      config: {}",
	}
	return strings.Join(append(head, tail...), "\n") + "\n"
}

func hostCertQuote(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// fileExists reports whether a regular file is present.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// hostCertPlatformLabel names the platform a certified run measured.
func hostCertPlatformLabel() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// hostCertFirstLines trims a diagnostic to its first n lines, so a recorded measurement
// stays readable in test output instead of dumping a whole host log.
func hostCertFirstLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// copyHostCertTree copies a directory tree, preserving the executable bit on POSIX.
func copyHostCertTree(src, dst string) error {
	return filepath.WalkDir(src, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, current)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, readErr := os.Readlink(current)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(link, target)
		}
		raw, readErr := os.ReadFile(current)
		if readErr != nil {
			return readErr
		}
		mode := os.FileMode(0o644)
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 != 0 {
			mode = 0o755
		}
		return os.WriteFile(target, raw, mode)
	})
}
