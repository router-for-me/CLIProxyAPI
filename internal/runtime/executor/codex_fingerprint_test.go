package executor

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexApplicationIdentityProjectsCanonicalMetadata(t *testing.T) {
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{
		ID:       "oauth-account-a",
		Metadata: map[string]any{"access_token": "oauth-token"},
	}
	body := []byte(`{
		"prompt_cache_key":"client-session-a",
		"client_metadata":{
			"custom-key":"preserved",
			"x-codex-parent-thread-id":"parent-thread-a",
			"x-openai-subagent":"collab_spawn"
		}
	}`)
	const requestURL = "https://chatgpt.com/backend-api/codex/responses"

	firstBody, first := applyCodexOfficialApplicationIdentity(cfg, auth, requestURL, body)
	if !first.enabled {
		t.Fatal("application identity is disabled for official OAuth request")
	}
	assertUUIDVersion(t, first.windowID, 7)
	assertUUIDVersion(t, first.turnID, 7)
	if _, err := uuid.Parse(first.installationID); err != nil {
		t.Fatalf("installation ID %q is not a UUID: %v", first.installationID, err)
	}
	if got := gjson.GetBytes(firstBody, "client_metadata.custom-key").String(); got != "preserved" {
		t.Fatalf("custom client metadata = %q, want preserved", got)
	}

	profile := registry.GetCodexFingerprintProfile()
	if got := gjson.GetBytes(firstBody, "client_metadata."+profile.Headers.InstallationID).String(); got != first.installationID {
		t.Fatalf("body installation ID = %q, want %q", got, first.installationID)
	}
	if got := gjson.GetBytes(firstBody, "client_metadata."+profile.MetadataKeys.SessionID).String(); got != first.sessionID {
		t.Fatalf("body session ID = %q, want %q", got, first.sessionID)
	}
	if got := gjson.GetBytes(firstBody, "client_metadata."+profile.MetadataKeys.ThreadID).String(); got != first.threadID {
		t.Fatalf("body thread ID = %q, want %q", got, first.threadID)
	}
	if got := gjson.GetBytes(firstBody, "client_metadata."+profile.Headers.WindowID).String(); got != first.windowID {
		t.Fatalf("body window ID = %q, want %q", got, first.windowID)
	}

	turnMetadataRaw := gjson.GetBytes(firstBody, "client_metadata."+profile.Headers.TurnMetadata).String()
	var turnMetadata map[string]any
	if err := json.Unmarshal([]byte(turnMetadataRaw), &turnMetadata); err != nil {
		t.Fatalf("turn metadata is not JSON: %v", err)
	}
	for key, want := range map[string]string{
		profile.MetadataKeys.InstallationID: first.installationID,
		profile.MetadataKeys.SessionID:      first.sessionID,
		profile.MetadataKeys.ThreadID:       first.threadID,
		profile.MetadataKeys.TurnID:         first.turnID,
		profile.MetadataKeys.WindowID:       first.windowID,
		profile.MetadataKeys.RequestKind:    "turn",
		profile.MetadataKeys.ParentThreadID: "parent-thread-a",
		profile.MetadataKeys.SubagentKind:   "collab_spawn",
	} {
		if got, _ := turnMetadata[key].(string); got != want {
			t.Fatalf("turn metadata %s = %q, want %q", key, got, want)
		}
	}
	if _, ok := turnMetadata[profile.MetadataKeys.TurnStartedAtUnixMS].(float64); !ok {
		t.Fatalf("turn metadata %s is missing or not numeric", profile.MetadataKeys.TurnStartedAtUnixMS)
	}

	headers := http.Header{}
	applyCodexOfficialApplicationIdentityHeaders(headers, &first)
	if got := headerValueCaseInsensitive(headers, profile.Headers.SessionID); got != first.sessionID {
		t.Fatalf("session header = %q, want %q", got, first.sessionID)
	}
	if got := headers.Get(profile.Headers.ClientRequestID); got != "" {
		t.Fatalf("HTTP client request header = %q, want empty", got)
	}
	if got := headers.Get(profile.Headers.TurnMetadata); got != turnMetadataRaw {
		t.Fatalf("turn metadata header differs from client_metadata: %q != %q", got, turnMetadataRaw)
	}
	if got := headers.Get(profile.Headers.ParentThreadID); got != "parent-thread-a" {
		t.Fatalf("parent thread header = %q", got)
	}
	if got := headers.Get(profile.Headers.Subagent); got != "collab_spawn" {
		t.Fatalf("subagent header = %q", got)
	}

	_, second := applyCodexOfficialApplicationIdentity(cfg, auth, requestURL, body)
	if second.installationID != first.installationID ||
		second.sessionID != first.sessionID ||
		second.threadID != first.threadID ||
		second.windowID != first.windowID {
		t.Fatalf("stable identity changed: first=%+v second=%+v", first, second)
	}
	if second.turnID == first.turnID {
		t.Fatalf("turn ID was reused: %q", second.turnID)
	}
}

func TestCodexApplicationIdentityMarksCompaction(t *testing.T) {
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{ID: "oauth-compact", Metadata: map[string]any{"access_token": "oauth-token"}}
	body, identity := applyCodexOfficialApplicationIdentity(
		cfg,
		auth,
		"https://chatgpt.com/backend-api/codex/responses/compact",
		[]byte(`{"prompt_cache_key":"compact-session"}`),
	)
	if !identity.enabled {
		t.Fatal("compaction identity is disabled")
	}
	profile := registry.GetCodexFingerprintProfile()
	raw := gjson.GetBytes(body, "client_metadata."+profile.Headers.TurnMetadata).String()
	if got := gjson.Get(raw, profile.MetadataKeys.RequestKind).String(); got != "compaction" {
		t.Fatalf("request kind = %q, want compaction", got)
	}
	headers := http.Header{}
	applyCodexOfficialApplicationIdentityHeaders(headers, &identity)
	if got := headers.Get(profile.Headers.InstallationID); got != identity.installationID {
		t.Fatalf("compact installation header = %q, want %q", got, identity.installationID)
	}
}

func TestApplyCodexOfficialFingerprintHeadersUsesProfile(t *testing.T) {
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{ID: "oauth-software-profile", Metadata: map[string]any{"access_token": "oauth-token"}}
	_, identity := applyCodexOfficialApplicationIdentity(
		cfg,
		auth,
		"https://chatgpt.com/backend-api/codex/responses",
		[]byte(`{"prompt_cache_key":"software-profile-session"}`),
	)
	headers := http.Header{
		"User-Agent": {"stale-client/0.1.0"},
		"Originator": {"stale-client"},
		"Version":    {"0.1.0"},
	}
	applyCodexOfficialApplicationIdentityHeaders(headers, &identity)

	profile := registry.GetCodexFingerprintProfile()
	if got := headers.Get("User-Agent"); got != profile.UserAgent() {
		t.Fatalf("User-Agent = %q, want %q", got, profile.UserAgent())
	}
	if got := headers.Get("Originator"); got != profile.Originator {
		t.Fatalf("Originator = %q, want %q", got, profile.Originator)
	}
	if got := headers.Get("Version"); got != profile.Version {
		t.Fatalf("Version = %q, want %q", got, profile.Version)
	}
}

func TestApplyCodexWebsocketFingerprintUsesProfile(t *testing.T) {
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{ID: "oauth-ws-profile", Metadata: map[string]any{"access_token": "oauth-token"}}
	_, identity := applyCodexOfficialApplicationIdentity(
		cfg,
		auth,
		"wss://chatgpt.com/backend-api/codex/responses",
		[]byte(`{"prompt_cache_key":"ws-profile-session"}`),
	)
	headers := http.Header{"OpenAI-Beta": {"responses_websockets=2000-01-01"}}
	applyCodexOfficialApplicationIdentityHeaders(headers, &identity)

	profile := registry.GetCodexFingerprintProfile()
	if got := headers.Get("OpenAI-Beta"); got != profile.WebsocketBeta {
		t.Fatalf("OpenAI-Beta = %q, want %q", got, profile.WebsocketBeta)
	}
	if got := headers.Get("Version"); got != profile.Version {
		t.Fatalf("Version = %q, want %q", got, profile.Version)
	}
	if got := headers.Get(profile.Headers.ClientRequestID); got != identity.threadID {
		t.Fatalf("websocket client request header = %q, want %q", got, identity.threadID)
	}
}

func TestCodexOfficialFingerprintScopeBypassesExcludedRequests(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		auth       *cliproxyauth.Auth
		requestURL string
	}{
		{
			name:       "api key",
			cfg:        &config.Config{},
			auth:       &cliproxyauth.Auth{ID: "api-key", Attributes: map[string]string{"api_key": "sk-test"}},
			requestURL: "https://chatgpt.com/backend-api/codex/responses",
		},
		{
			name:       "custom auth base URL",
			cfg:        &config.Config{},
			auth:       &cliproxyauth.Auth{ID: "custom", Attributes: map[string]string{"base_url": "https://gateway.example.com"}},
			requestURL: "https://gateway.example.com/responses",
		},
		{
			name:       "cloaking disabled",
			cfg:        &config.Config{Codex: config.CodexConfig{DisableCodexCloaking: true}},
			auth:       &cliproxyauth.Auth{ID: "oauth", Metadata: map[string]any{"access_token": "oauth-token"}},
			requestURL: "https://chatgpt.com/backend-api/codex/responses",
		},
		{
			name:       "custom target",
			cfg:        &config.Config{},
			auth:       &cliproxyauth.Auth{ID: "oauth", Metadata: map[string]any{"access_token": "oauth-token"}},
			requestURL: "https://gateway.example.com/responses",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{"prompt_cache_key":"scope-session"}`)
			got, identity := applyCodexOfficialApplicationIdentity(tt.cfg, tt.auth, tt.requestURL, input)
			if identity.enabled {
				t.Fatalf("identity enabled for excluded scope: %+v", identity)
			}
			if string(got) != string(input) {
				t.Fatalf("excluded request body changed: %s", got)
			}
		})
	}
}

func assertUUIDVersion(t *testing.T, value string, want int) {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("%q is not a UUID: %v", value, err)
	}
	if got := int(parsed.Version()); got != want {
		t.Fatalf("UUID %q version = %d, want %d", value, got, want)
	}
}
