package cursorsdk_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/protocol"
	"github.com/aiproxer/aiproxer-cursor-sdk/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func cursorReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
	values := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		values[name] = []byte(value)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "cred-contract",
		ConfigYAML:    []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: values},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

// TestCredentialContract_RejectsYAMLAPIKey proves literal static keys in
// connector YAML fail closed: the Cursor SDK export is session_admission over
// host-delivered slots, never YAML-carried material.
func TestCredentialContract_RejectsYAMLAPIKey(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"api_key: hunter2\n",
		"api_keys: [hunter2]\n",
		"API_KEY: hunter2\n",
	} {
		_, err := service.New().Configure(context.Background(), cursorReq(
			raw,
			map[string]string{"api_key": "host-key"},
		))
		if err == nil {
			t.Fatalf("expected literal YAML key to be rejected (yaml=%q)", raw)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("rejection leaks secret value: %v", err)
		}
		lowered := strings.ToLower(err.Error())
		if !strings.Contains(lowered, "api_key") || !strings.Contains(err.Error(), "CURSOR_API_KEY") {
			t.Fatalf("rejection must name field path and env root: %v", err)
		}
	}
}

// TestCredentialContract_RequiresHostSecrets proves Configure fails closed
// without host-delivered slots, with an actionable secret-free error naming
// the approved environment root.
func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), cursorReq("", nil))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail closed")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "CURSOR_API_KEY") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "Configure secrets") {
		t.Fatalf("rejection must direct the operator to host Configure secrets: %v", err)
	}
}

// TestCredentialContract_RejectsSecondSlot proves session_admission binds
// exactly one host slot: a spare numbered slot fails closed instead of being
// silently ignored or pooled across sessions.
func TestCredentialContract_RejectsSecondSlot(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), cursorReq(
		"",
		map[string]string{"api_key": "first-key", "api_key_2": "second-key"},
	))
	if err == nil {
		t.Fatal("expected a second host slot to fail closed for session_admission")
	}
	if strings.Contains(err.Error(), "first-key") || strings.Contains(err.Error(), "second-key") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key_2") {
		t.Fatalf("rejection must name the surplus slot: %v", err)
	}
}

// TestCredentialContract_AdmitsSingleHostSlot proves the session_admission
// Configure path end to end: exactly one host slot admits an instance with
// no YAML key material anywhere. A hermetic stub bridge executable plus a
// static model source keeps the test spawn-free; per-session key binding is
// proven by the product-level session admission suite.
func TestCredentialContract_AdmitsSingleHostSlot(t *testing.T) {
	t.Parallel()
	bridge := filepath.Join(t.TempDir(), "lip-cursor-sdk-bridge")
	if err := os.WriteFile(bridge, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	product.ResetLookPathCache()
	svc := &service.Service{
		ModelListSource: product.StaticModelListSource{
			Rows: []protocol.ModelRow{{ID: "cursor-model"}},
		},
	}
	inst, err := svc.Configure(context.Background(), cursorReq(
		"bridge_executable: "+bridge+"\n",
		map[string]string{"api_key": "host-key"},
	))
	if err != nil {
		t.Fatalf("single host slot must admit: %v", err)
	}
	if inst == nil {
		t.Fatal("expected a configured instance")
	}
}

// credential contract for the agent_runtime export: api_key class, host_env
// source, CURSOR_API_KEY root, api_key slot and session_admission selection.
// The runtime descriptor must agree on posture; the contract decision lives
// in the manifest, never in connector code.
// TestCredentialContract_ManifestSessionAdmission pins the packaged manifest
func TestCredentialContract_ManifestSessionAdmission(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exports []struct {
			Kind               string `json:"kind"`
			CredentialMode     string `json:"credential_mode"`
			AccessScope        string `json:"access_scope"`
			CredentialContract *struct {
				Version     int    `json:"version"`
				Class       string `json:"class"`
				Source      string `json:"source"`
				EnvRoot     string `json:"env_root"`
				Slot        string `json:"slot"`
				Selection   string `json:"selection"`
				QuotaDomain string `json:"quota_domain"`
			} `json:"credential_contract"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Exports) != 1 {
		t.Fatalf("exports=%d, want exactly the single agent_runtime export", len(doc.Exports))
	}
	ex := doc.Exports[0]
	if ex.Kind != service.FactoryKind {
		t.Fatalf("kind=%q", ex.Kind)
	}
	if ex.CredentialMode != "static" || ex.AccessScope != "local_only" {
		t.Fatalf("posture=%q/%q, want static/local_only", ex.CredentialMode, ex.AccessScope)
	}
	if ex.CredentialContract == nil {
		t.Fatal("missing credential_contract for the static export")
	}
	cc := ex.CredentialContract
	if cc.Version != 2 || cc.Class != "api_key" || cc.Source != "host_env" ||
		cc.EnvRoot != "CURSOR_API_KEY" || cc.Slot != "api_key" ||
		cc.Selection != "session_admission" || cc.QuotaDomain != "cursor" {
		t.Fatalf("credential_contract=%+v", cc)
	}
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Factories) != 1 || d.Factories[0].Kind != service.FactoryKind ||
		d.Factories[0].CredentialMode != backendplugin.CredentialModeStatic ||
		d.Factories[0].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("descriptor disagrees with manifest posture: %+v", d.Factories)
	}
}
