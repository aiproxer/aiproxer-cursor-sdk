package product

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func admissionCall(sessionID string) *lipapi.Call {
	return &lipapi.Call{
		ID:      "attempt-" + sessionID,
		Session: lipapi.SessionRef{AuthoritativeSessionID: sessionID},
	}
}

// TestSessionAdmission_KeyBoundPerSession proves the session_admission
// contract: every agent session binds the single Configure-delivered key.
// Distinct sessions share one key fingerprint; a different delivered key
// yields a different fingerprint. Fingerprints are nonsecret digests: the
// comparison proves binding without handling key material.
func TestSessionAdmission_KeyBoundPerSession(t *testing.T) {
	t.Parallel()
	cfg := Config{APIKey: "host-delivered-key"}
	keyA := buildAgentKey(cfg, admissionCall("sess-a"), "cursor-model", "/ws", json.RawMessage(`{}`))
	keyB := buildAgentKey(cfg, admissionCall("sess-b"), "cursor-model", "/ws", json.RawMessage(`{}`))
	require.Equal(t, "sess-a", keyA.SessionID)
	require.Equal(t, "sess-b", keyB.SessionID)
	require.NotEmpty(t, keyA.KeyFingerprint)
	assert.Equal(t, keyA.KeyFingerprint, keyB.KeyFingerprint,
		"sessions of one admitted instance must bind the same key")
	assert.Equal(t, FingerprintSecret("host-delivered-key"), keyA.KeyFingerprint)

	rotated := Config{APIKey: "other-key"}
	keyC := buildAgentKey(rotated, admissionCall("sess-a"), "cursor-model", "/ws", json.RawMessage(`{}`))
	assert.NotEqual(t, keyA.KeyFingerprint, keyC.KeyFingerprint,
		"a different delivered key must bind a different session identity")
}

// TestSessionAdmission_NoReplayAfterEffects proves the at-most-once
// boundary: once output or an agent effect has committed, no failure —
// including authentication failures — is recoverable for transparent
// replay. There is no second credential to rotate to; the single bound key
// stays bound and the failure surfaces without another provider execution.
func TestSessionAdmission_NoReplayAfterEffects(t *testing.T) {
	t.Parallel()
	committed := true
	for name, err := range map[string]error{
		"auth":      BridgeExited(nil, "stderr=unauthorized"),
		"transport": BridgeExited(nil, "connection reset"),
		"generic":   errors.New("boom"),
	} {
		mapped := ClassifyAndMap(err, committed, "bound-key")
		require.Error(t, mapped, name)
		assert.False(t, lipapi.IsRecoverablePreOutput(mapped),
			"%s: committed failure must never be replayable", name)
		var cf *ClassifiedFailure
		require.True(t, errors.As(mapped, &cf), name)
		assert.Equal(t, lipapi.PhasePostOutput, cf.Phase, name)
		assert.NotContains(t, mapped.Error(), "bound-key",
			"%s: classified failure must not leak the bound key", name)
	}
}
