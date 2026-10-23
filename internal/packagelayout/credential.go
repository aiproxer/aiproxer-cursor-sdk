package packagelayout

// CredentialContract is the versioned static-key registration the host reads
// from a manifest export.
//
// The host's manifest schema is closed and its namespace registry is closed
// too: a static export has to declare which environment root carries its key,
// which slot the key occupies, how the key is selected for a session, and
// which quota domain it belongs to. A manifest that leaves the registration
// out is refused at discovery time, so the packaged manifest and this
// declaration have to stay in agreement.
type CredentialContract struct {
	Version     uint32 `json:"version"`
	Class       string `json:"class"`
	Source      string `json:"source"`
	EnvRoot     string `json:"env_root"`
	Slot        string `json:"slot"`
	Selection   string `json:"selection"`
	QuotaDomain string `json:"quota_domain"`
}

// CursorSDKCredentialContract is the registration the host holds for the
// cursorsdk kind: one static API key, taken from the host environment root
// CURSOR_API_KEY, admitted once per session.
//
// The values are the host's closed registration for this kind, not a choice
// this plugin makes. The plugin's own credential handling already resolves
// the same root and the same slot, and the manifest is what the host reads
// before it will compose the connector at all.
var CursorSDKCredentialContract = CredentialContract{
	Version:     2,
	Class:       "api_key",
	Source:      "host_env",
	EnvRoot:     "CURSOR_API_KEY",
	Slot:        "api_key",
	Selection:   "session_admission",
	QuotaDomain: "cursor",
}
