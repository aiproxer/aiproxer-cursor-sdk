package service

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

// EnvAPIKeyRoot is the canonical nonsecret host environment namespace for
// Cursor SDK credentials: CURSOR_API_KEY. Secrets never appear in connector
// YAML; the host delivers exactly one ordered slot via Configure secrets and
// the instance binds it for every agent session (session_admission: no
// rotation, no replay after effects).
const EnvAPIKeyRoot = "CURSOR_API_KEY"

// QuotaDomain is the explicit nonsecret quota scope for Cursor SDK
// credential binding: the single admitted key is never pooled with an
// unrelated provider or project cooldown.
const QuotaDomain = "cursor"

// Ordered slot budgets. They mirror the host capture budgets so
// connector-side validation can never accept a bundle the host could not
// have emitted.
const (
	maxCredentialSlotCount       = 256
	maxCredentialSlotSecretBytes = 64 << 10
	maxCredentialSlotTotalBytes  = 256 << 10
)

// slotSecret is one validated Configure secret bound to its nonsecret
// canonical slot name. Secret is an owned copy the caller may retain;
// Name is nonsecret and safe for diagnostics.
type slotSecret struct {
	Name   string
	Secret string
}

// forbiddenSecretFields are top-level YAML configuration keys that must never
// carry provider credential material or credential-file pointers in an
// executable-connector config. Comparison is case-insensitive with -/_
// unified. Values are never echoed: rejections name only the field path and
// the approved environment root.
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {}, "iam_token": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "client_secret": {},
	"private_key": {}, "privatekey": {}, "pat": {}, "password": {},
	"auth_json": {}, "auth_json_path": {}, "authjson": {},
	"dotenv": {}, "env_file": {}, "credential_file": {}, "credentials_file": {},
	"key_file": {}, "secrets_file": {}, "token_file": {},
	"authorization": {},
}

// rejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field, including legacy singular/list aliases and
// case variants. The Cursor SDK export is session_admission over
// host-delivered slots, never YAML-carried material.
func rejectSecretFields(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("cursorsdk: config: invalid YAML")
	}
	for name := range fields {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("cursorsdk: config field %q is forbidden; provide credentials via host environment %s[_N] delivered as Configure secrets", name, EnvAPIKeyRoot)
		}
	}
	return nil
}

// decodeAPIKeySlotSecrets validates the canonical ordered host-delivered
// api_key slots and returns each secret bound to its nonsecret canonical slot
// name: api_key, api_key_2, ... Every rejection is fail-closed and
// secret-free: errors name only slot names, never secret bytes.
func decodeAPIKeySlotSecrets(bundle backendplugin.SecretBundle) ([]slotSecret, error) {
	const base = "api_key"
	if len(bundle.Values) == 0 {
		return nil, fmt.Errorf("cursorsdk: missing %q slot (want %s via host Configure secrets)", base, EnvAPIKeyRoot)
	}
	if len(bundle.Values) > maxCredentialSlotCount {
		return nil, fmt.Errorf("cursorsdk: %d slots exceed %d", len(bundle.Values), maxCredentialSlotCount)
	}
	names := make([]string, 0, len(bundle.Values))
	for name := range bundle.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	type ordered struct {
		name  string
		index int
		value string
	}
	slots := make([]ordered, 0, len(names))
	seenValues := make(map[string]string, len(names))
	total := 0
	for _, name := range names {
		index, ok := parseSlotIndex(name)
		if !ok {
			return nil, fmt.Errorf("cursorsdk: unexpected secret name %q for slot %q", name, base)
		}
		value := bundle.Values[name]
		secret := strings.TrimSpace(string(value))
		if secret == "" {
			return nil, fmt.Errorf("cursorsdk: empty value for slot %q", name)
		}
		if bytes.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("cursorsdk: invalid secret bytes for slot %q", name)
		}
		if len(value) > maxCredentialSlotSecretBytes {
			return nil, fmt.Errorf("cursorsdk: value for slot %q exceeds %d bytes", name, maxCredentialSlotSecretBytes)
		}
		if first, dup := seenValues[secret]; dup {
			return nil, fmt.Errorf("cursorsdk: duplicate value in slots %q and %q", first, name)
		}
		seenValues[secret] = name
		total += len(value)
		if total > maxCredentialSlotTotalBytes {
			return nil, fmt.Errorf("cursorsdk: slots for %q exceed %d total bytes", base, maxCredentialSlotTotalBytes)
		}
		slots = append(slots, ordered{name: name, index: index, value: secret})
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].index != slots[j].index {
			return slots[i].index < slots[j].index
		}
		return slots[i].name < slots[j].name
	})
	out := make([]slotSecret, 0, len(slots))
	for _, slot := range slots {
		out = append(out, slotSecret{Name: slot.name, Secret: slot.value})
	}
	return out, nil
}

// parseSlotIndex accepts the bare base (slot 1) and canonical base_N
// suffixes with 2 <= N <= maxCredentialSlotCount. The suffix must be the
// canonical decimal form (no padding): _0, _1, non-numeric and out-of-range
// suffixes are rejected, matching the host resolver that never emits them.
func parseSlotIndex(name string) (int, bool) {
	const base = "api_key"
	if name == base {
		return 1, true
	}
	rest, ok := strings.CutPrefix(name, base+"_")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 2 || n > maxCredentialSlotCount {
		return 0, false
	}
	if strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}
