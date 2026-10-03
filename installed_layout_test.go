package cursorsdk_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	installedOuterName      = "lip-backend-cursorsdk"
	installedCompanionName  = "lip-cursor-sdk-bridge"
	installedCompanionDir   = "private/bridge"
	installedPluginAPIKey   = "installed-layout-cursor-key"
	installedRPCDeadline    = 60 * time.Second
	installedStartupTimeout = 60 * time.Second
	installedBuildTimeout   = 120 * time.Second
)

// TestInstalledLayout_PrivateCompanionResolution exercises the packaged default
// bridge resolution contract from a real installed plugin layout: the outer
// plugin executable must start its plugin-private companion by default, must
// honor an explicit bridge_executable override, and must never resolve through a
// shell, npm, a global binary directory, or the current working directory.
func TestInstalledLayout_PrivateCompanionResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("installed-layout launch builds and runs the plugin executable")
	}

	t.Run("default_starts_plugin_local_companion", func(t *testing.T) {
		layout := newInstalledLayout(t, true)
		// PATH and the working directory both hold a decoy named exactly like the
		// packaged companion. They are intentionally unstartable, so a resolution
		// that escaped the installed layout would fail the launch instead of
		// silently passing.
		decoys := newBridgeDecoys(t, false)
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		token := negotiateToken(t, pluginProc)
		instanceID := "installed-default-companion"
		require.NoError(t, configureInstance(t, pluginProc, token, instanceID, instanceConfigYAML("", t.TempDir())))

		models, err := pluginProc.listModels(t, instanceID)
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())
		require.NotEmpty(t, models.GetModels())
		require.Equal(t, "gpt-5.3-codex", models.GetModels()[0].GetNativeModelId())
		require.Equal(t, "cursor/gpt-5.3-codex", models.GetModels()[0].GetCanonicalModelId())
		require.Equal(t, "cursorsdk", models.GetModels()[0].GetFactoryKind())
	})

	t.Run("default_starts_plugin_local_companion_under_shell_metacharacter_install_root", func(t *testing.T) {
		// `$` and `&` are legal in a POSIX install root. The packaged default is
		// plugin-derived, not operator-supplied, so an install root carrying shell
		// metacharacters must start the companion instead of failing with the
		// operator-facing bridge_executable rejection that names a field the
		// operator never set.
		layout := newInstalledLayoutRooted(t, true, "plugin $root & co")
		decoys := newBridgeDecoys(t, true)
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		token := negotiateToken(t, pluginProc)
		instanceID := "installed-metacharacter-companion"
		err := configureInstance(t, pluginProc, token, instanceID, instanceConfigYAML("", t.TempDir()))
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())

		models, err := pluginProc.listModels(t, instanceID)
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())
		require.NotEmpty(t, models.GetModels())
	})

	t.Run("default_launcher_starts_private_node_runtime", func(t *testing.T) {
		// The packaged companion is the real launcher executable, which then
		// starts the packaged private Node runtime. PATH and the working
		// directory both hold decoys named like the companion, and PATH also
		// holds a startable `node`, so a resolution that escaped the installed
		// layout would either fail or run the wrong runtime.
		layout, privateNode, runtimeSelfLog := newInstalledLauncherLayout(t)
		decoys := newBridgeDecoys(t, false)
		nodePathDir := t.TempDir()
		installBinary(t, filepath.Join(nodePathDir, "node"+exeSuffix()), fakebridge.BuildNodeExe(t))

		instanceID := "installed-launcher-private-runtime"
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(nodePathDir))

		token := negotiateToken(t, pluginProc)
		require.NoError(t, configureInstance(t, pluginProc, token, instanceID, instanceConfigYAML("", t.TempDir())))

		models, err := pluginProc.listModels(t, instanceID)
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())
		require.NotEmpty(t, models.GetModels())
		require.Equal(t, "gpt-5.3-codex", models.GetModels()[0].GetNativeModelId())
		require.Equal(t, "cursorsdk", models.GetModels()[0].GetFactoryKind())

		// The runtime that served the request is the packaged private Node
		// executable, not a `node` reachable through PATH or the working
		// directory.
		selfRaw, err := os.ReadFile(runtimeSelfLog)
		require.NoError(t, err, "the private runtime never recorded its own path")
		require.Equal(t, privateNode, strings.TrimSpace(string(selfRaw)))
	})

	t.Run("unprovisioned_sdk_is_an_explicit_prerequisite_for_the_connector", func(t *testing.T) {
		// The Cursor SDK is not redistributed, so an installed tree only serves
		// requests once the operator has provisioned it. The connector has to report
		// that as a missing prerequisite carrying the exact command, not as a provider
		// error and not by installing anything itself.
		layout, _, _ := newInstalledLauncherLayoutWithSDK(t, false)
		decoys := newBridgeDecoys(t, false)

		instanceID := "installed-unprovisioned-sdk"
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		token := negotiateToken(t, pluginProc)
		require.NoError(t, configureInstance(t, pluginProc, token, instanceID, instanceConfigYAML("", t.TempDir())))

		_, err := pluginProc.listModels(t, instanceID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not provisioned")
		require.Contains(t, err.Error(), pinnedSDKVersion)
		require.Contains(t, err.Error(), "ci --omit=dev")
		require.NotContains(t, err.Error(), installedPluginAPIKey)
	})

	t.Run("explicit_override_is_honored_without_packaged_companion", func(t *testing.T) {
		layout := newInstalledLayout(t, false)
		decoys := newBridgeDecoys(t, false)
		override := installBinary(t,
			filepath.Join(t.TempDir(), "override dir with spaces", "custom-bridge"+exeSuffix()),
			fakebridge.BuildExe(t))
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		token := negotiateToken(t, pluginProc)
		instanceID := "installed-explicit-override"
		require.NoError(t, configureInstance(t, pluginProc, token, instanceID, instanceConfigYAML(override, t.TempDir())))

		models, err := pluginProc.listModels(t, instanceID)
		require.NoError(t, err, "plugin stderr:\n%s", pluginProc.stderrText())
		require.NotEmpty(t, models.GetModels())
	})

	t.Run("missing_companion_fails_explicitly_without_fallback", func(t *testing.T) {
		layout := newInstalledLayout(t, false)
		// Both decoys are startable: a silent fallback to PATH or to the working
		// directory would succeed, so an explicit failure proves neither is used.
		decoys := newBridgeDecoys(t, true)
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		token := negotiateToken(t, pluginProc)
		err := configureInstance(t, pluginProc, token, "installed-missing-companion", instanceConfigYAML("", t.TempDir()))
		require.Error(t, err)
		require.Contains(t, err.Error(), installedCompanionDir+"/"+installedCompanionName)
		require.Contains(t, err.Error(), "bridge_executable")
		require.NotContains(t, err.Error(), "npm install")
		require.NotContains(t, err.Error(), installedPluginAPIKey)
	})

	t.Run("explicit_override_keeps_direct_executable_validation", func(t *testing.T) {
		layout := newInstalledLayout(t, true)
		decoys := newBridgeDecoys(t, false)
		pluginProc := startInstalledPlugin(t, layout, decoys.workDir, childEnvWithPathOnly(decoys.pathDir))

		err := configureInstance(t, pluginProc, negotiateToken(t, pluginProc), "installed-override-npm",
			instanceConfigYAML("npm", t.TempDir()))
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be a direct bridge binary, not shell or npm launcher")

		missing := filepath.Join(t.TempDir(), "absent-bridge"+exeSuffix())
		err = configureInstance(t, pluginProc, negotiateToken(t, pluginProc), "installed-override-missing",
			instanceConfigYAML(missing, t.TempDir()))
		require.Error(t, err)
		require.Contains(t, err.Error(), "not found (direct PATH/absolute lookup only; Go-LIP never runs npm install)")

		// The shell/npm guard stays scoped to operator-supplied values: skipping it
		// for the plugin-derived default must not relax it for bridge_executable.
		err = configureInstance(t, pluginProc, negotiateToken(t, pluginProc), "installed-override-metacharacter",
			instanceConfigYAML("bridge && curl evil", t.TempDir()))
		require.Error(t, err)
		require.Contains(t, err.Error(), "must not contain shell metacharacters")
	})
}

// installedLayout is one packaged plugin root: the outer plugin executable plus
// an optional plugin-private companion at the release archive location.
type installedLayout struct {
	root  string
	outer string
}

func newInstalledLayout(t *testing.T, withCompanion bool) installedLayout {
	t.Helper()
	return newInstalledLayoutRooted(t, withCompanion, "plugin root with spaces")
}

// newInstalledLayoutRooted lets the packaged root directory name carry
// characters that only a shell would interpret, proving that companion
// resolution and launch stay shell-free.
func newInstalledLayoutRooted(t *testing.T, withCompanion bool, rootName string) installedLayout {
	t.Helper()
	root := filepath.Join(t.TempDir(), rootName)
	outer := installBinary(t, filepath.Join(root, "bin", installedOuterName+exeSuffix()), buildOuterPluginExe(t))
	if withCompanion {
		installBinary(t,
			filepath.Join(root, filepath.FromSlash(installedCompanionDir), installedCompanionName+exeSuffix()),
			fakebridge.BuildExe(t))
	}
	return installedLayout{root: root, outer: outer}
}

// bridgeDecoys holds a PATH directory and a working directory that each contain
// a bridge executable named exactly like the packaged companion.
type bridgeDecoys struct {
	pathDir string
	workDir string
}

func newBridgeDecoys(t *testing.T, startable bool) bridgeDecoys {
	t.Helper()
	base := t.TempDir()
	decoys := bridgeDecoys{
		pathDir: filepath.Join(base, "global bin decoy"),
		workDir: filepath.Join(base, "cwd decoy dir"),
	}
	targets := []string{
		filepath.Join(decoys.pathDir, installedCompanionName+exeSuffix()),
		filepath.Join(decoys.workDir, filepath.FromSlash(installedCompanionDir), installedCompanionName+exeSuffix()),
	}
	fake := ""
	if startable {
		fake = fakebridge.BuildExe(t)
	}
	for _, target := range targets {
		if startable {
			installBinary(t, target, fake)
			continue
		}
		writeUnstartableFile(t, target)
	}
	return decoys
}

// installedPlugin is a running outer plugin executable reached over its
// development loopback listener.
type installedPlugin struct {
	client backendpluginv1.BackendPluginClient
	conn   *grpc.ClientConn
	cmd    *exec.Cmd

	mu       sync.Mutex
	stderr   []string
	instance string
}

func startInstalledPlugin(t *testing.T, layout installedLayout, workDir string, env []string) *installedPlugin {
	t.Helper()

	cmd := exec.Command(layout.outer, "-listen", "127.0.0.1:0")
	cmd.Dir = workDir
	cmd.Env = env
	stderrPipe, err := cmd.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	proc := &installedPlugin{cmd: cmd}
	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			proc.mu.Lock()
			proc.stderr = append(proc.stderr, line)
			proc.mu.Unlock()
			if _, addr, ok := strings.Cut(line, "listening on "); ok {
				select {
				case addrCh <- strings.TrimSpace(addr):
				default:
				}
			}
		}
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), installedRPCDeadline)
		defer cancel()
		if proc.currentInstance() != "" && proc.client != nil {
			_, _ = proc.client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: proc.currentInstance()})
		}
		if proc.client != nil {
			_, _ = proc.client.GracefulShutdown(ctx, &backendpluginv1.GracefulShutdownRequest{DrainTimeoutMs: 1000})
		}
		if proc.conn != nil {
			_ = proc.conn.Close()
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(installedStartupTimeout):
		t.Fatalf("plugin executable at %s never reported a listen address; stderr:\n%s", layout.root, proc.stderrText())
	}
	conn, err := grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	proc.conn = conn
	proc.client = backendpluginv1.NewBackendPluginClient(conn)
	return proc
}

func (p *installedPlugin) currentInstance() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instance
}

func (p *installedPlugin) stderrText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.stderr, "\n")
}

func (p *installedPlugin) listModels(t *testing.T, instanceID string) (*backendpluginv1.ListModelsResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), installedRPCDeadline)
	defer cancel()
	return p.client.ListModels(ctx, &backendpluginv1.ListModelsRequest{InstanceId: instanceID, MaxModels: 8})
}

func negotiateToken(t *testing.T, proc *installedPlugin) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), installedRPCDeadline)
	defer cancel()
	resp, err := proc.client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: backendplugin.ProtocolMajorV1,
		HostMinor: backendplugin.ProtocolMinorCancellationHandshake,
		HostFeatures: []*backendpluginv1.Feature{
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		DisableTransportRetries: true,
	})
	require.NoError(t, err, "negotiate failed; plugin stderr:\n%s", proc.stderrText())
	require.True(t, resp.GetCompatible(), "incompatible negotiation: %s", resp.GetRejectReason())
	require.NotEmpty(t, resp.GetNegotiationToken())
	return resp.GetNegotiationToken()
}

func configureInstance(t *testing.T, proc *installedPlugin, token, instanceID, configYAML string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), installedRPCDeadline)
	defer cancel()
	_, err := proc.client.Configure(ctx, &backendpluginv1.ConfigureRequest{
		InstanceId:       instanceID,
		FactoryKind:      "cursorsdk",
		ConfigYaml:       []byte(configYAML),
		Secrets:          &backendpluginv1.SecretBundle{Values: map[string][]byte{"api_key": []byte(installedPluginAPIKey)}},
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
		NegotiationToken: token,
	})
	if err != nil {
		return fmt.Errorf("configure: %w; plugin stderr:\n%s", err, proc.stderrText())
	}
	proc.mu.Lock()
	proc.instance = instanceID
	proc.mu.Unlock()
	return nil
}

// instanceConfigYAML is the plugin-owned opaque configuration under test. An
// empty bridgeExecutable exercises the packaged default resolution.
func instanceConfigYAML(bridgeExecutable, workspace string) string {
	var sb strings.Builder
	if bridgeExecutable != "" {
		fmt.Fprintf(&sb, "bridge_executable: %s\n", strconv.Quote(bridgeExecutable))
	}
	fmt.Fprintf(&sb, "default_workspace: %s\n", strconv.Quote(workspace))
	sb.WriteString("sandbox_mode: \"off\"\n")
	sb.WriteString("max_agents: 4\nmax_concurrent_runs: 2\nbridge_start_timeout_seconds: 20\n")
	return sb.String()
}

func childEnvWithPathOnly(pathDir string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PATH="+pathDir)
}

func installBinary(t *testing.T, dst, src string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, raw, 0o700))
	return dst
}

func writeUnstartableFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("this is not a startable bridge executable\n"), 0o700))
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// pinnedSDKVersion is the SDK version the bridge manifest in this test's installed
// layout pins, and the version an operator has to provision for it.
const pinnedSDKVersion = "1.0.23"

// newInstalledLauncherLayout installs the packaged private runtime layout around
// the real launcher executable: the launcher at private/bridge, the private Node
// runtime at private/node, and the bridge entry at private/bridge/bin. The
// private runtime is the deterministic fake Node binary, which records the path
// of the runtime binary that actually served a request. The tree is provisioned,
// because the launcher refuses to serve one that is not.
func newInstalledLauncherLayout(t *testing.T) (installedLayout, string, string) {
	t.Helper()
	return newInstalledLauncherLayoutWithSDK(t, true)
}

// newInstalledLauncherLayoutWithSDK builds the same layout with or without the
// operator-provisioned SDK tree, so both the serving path and the missing-prerequisite
// path can be exercised through the connector.
func newInstalledLauncherLayoutWithSDK(t *testing.T, provisioned bool) (installedLayout, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugin root with spaces")
	outer := installBinary(t, filepath.Join(root, "bin", installedOuterName+exeSuffix()), buildOuterPluginExe(t))
	bridgeDir := filepath.Join(root, filepath.FromSlash(installedCompanionDir))
	installBinary(t, filepath.Join(bridgeDir, installedCompanionName+exeSuffix()), buildLauncherExe(t))

	privateNode := filepath.Join(root, "private", "node", "node"+exeSuffix())
	installBinary(t, privateNode, fakebridge.BuildNodeExe(t))

	runtimeSelfLog := filepath.Join(t.TempDir(), "runtime-self.log")
	entry := filepath.Join(bridgeDir, "bin", "lip-cursor-sdk-bridge.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(entry), 0o755))
	script := fmt.Sprintf(`{"mode":"bridge","selfLog":%s}`, strconv.Quote(runtimeSelfLog))
	require.NoError(t, os.WriteFile(entry, []byte(script), 0o644))

	// The launcher checks the operator-provisioned SDK tree before it starts the
	// runtime, so an installed tree here is a provisioned one: the shipped bridge
	// manifest pins the SDK and the provisioned package metadata resolves to that pin.
	// What this layout proves is which runtime served the request, not the SDK's
	// presence; the provisioning preflight has its own tests.
	require.NoError(t, os.WriteFile(filepath.Join(bridgeDir, "package.json"),
		[]byte(`{"name":"lip-cursor-sdk-bridge","version":"0.1.0","dependencies":{"@cursor/sdk":"`+
			pinnedSDKVersion+`"}}`), 0o644))
	if provisioned {
		sdkMetadata := filepath.Join(bridgeDir, "node_modules", "@cursor", "sdk", "package.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(sdkMetadata), 0o755))
		require.NoError(t, os.WriteFile(sdkMetadata,
			[]byte(`{"name":"@cursor/sdk","version":"`+pinnedSDKVersion+`"}`), 0o644))
	}

	return installedLayout{root: root, outer: outer}, privateNode, runtimeSelfLog
}

var (
	buildLauncherOnce sync.Once
	buildLauncherPath string
	buildLauncherErr  error
)

// buildLauncherExe compiles the plugin-private bridge launcher once for the whole
// package, mirroring buildOuterPluginExe.
func buildLauncherExe(tb testing.TB) string {
	tb.Helper()
	buildLauncherOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			buildLauncherErr = errors.New("runtime.Caller failed")
			return
		}
		dir, err := os.MkdirTemp("", "lip-cursor-sdk-bridge-build-")
		if err != nil {
			buildLauncherErr = err
			return
		}
		exe := filepath.Join(dir, installedCompanionName+exeSuffix())
		ctx, cancel := context.WithTimeout(context.Background(), installedBuildTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/lip-cursor-sdk-bridge")
		cmd.Dir = filepath.Dir(thisFile)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildLauncherErr = fmt.Errorf("go build lip-cursor-sdk-bridge: %w\n%s", err, out)
			return
		}
		buildLauncherPath = exe
	})
	if buildLauncherErr != nil {
		tb.Fatal(buildLauncherErr)
	}
	return buildLauncherPath
}

var (
	buildOuterOnce sync.Once
	buildOuterPath string
	buildOuterErr  error
)

// buildOuterPluginExe compiles the real outer plugin executable once for the
// whole package; the installed-layout tests copy it into a packaged layout.
func buildOuterPluginExe(tb testing.TB) string {
	tb.Helper()
	buildOuterOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			buildOuterErr = errors.New("runtime.Caller failed")
			return
		}
		dir, err := os.MkdirTemp("", "lip-backend-cursorsdk-build-")
		if err != nil {
			buildOuterErr = err
			return
		}
		exe := filepath.Join(dir, installedOuterName+exeSuffix())
		ctx, cancel := context.WithTimeout(context.Background(), installedBuildTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/lip-backend-cursorsdk")
		cmd.Dir = filepath.Dir(thisFile)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildOuterErr = fmt.Errorf("go build lip-backend-cursorsdk: %w\n%s", err, out)
			return
		}
		buildOuterPath = exe
	})
	if buildOuterErr != nil {
		tb.Fatal(buildOuterErr)
	}
	return buildOuterPath
}
