package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestResolveClaudeFingerprintPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		provider         string
		apiKey           string
		attrs            map[string]string
		metadata         map[string]any
		cfg              *config.Config
		wantAuthOAuth    bool
		wantProfileOAuth bool
		wantSynthesize   bool
		wantMCP          bool
		wantDiagnostics  bool
		wantCancellation bool
	}{
		{
			name:             "real oauth token",
			apiKey:           "sk-ant-oat-real",
			attrs:            map[string]string{"api_key": "sk-ant-oat-real"},
			wantAuthOAuth:    true,
			wantProfileOAuth: true,
			wantMCP:          true,
			wantDiagnostics:  true,
			wantCancellation: true,
		},
		{
			name:             "api key default",
			apiKey:           "key-default",
			attrs:            map[string]string{"api_key": "key-default"},
			wantAuthOAuth:    false,
			wantProfileOAuth: false,
		},
		{
			name:             "official anthropic api key opts in via claude-code-cli attribute",
			apiKey:           "key-attr",
			attrs:            map[string]string{"api_key": "key-attr", "fingerprint_profile": "claude-code-cli"},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:             "official anthropic api key opts in via oauth-cli alias",
			apiKey:           "key-attr-legacy",
			attrs:            map[string]string{"api_key": "key-attr-legacy", "fingerprint_profile": "oauth-cli"},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:             "official anthropic explicit 443 api key opts in via profile",
			apiKey:           "key-official-443",
			attrs:            map[string]string{"api_key": "key-official-443", "base_url": "https://api.anthropic.com:443", "fingerprint_profile": "claude-code-cli"},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:   "api key claude-code-cli attribute on gateway",
			apiKey: "key-attr-gateway",
			attrs: map[string]string{
				"api_key":             "key-attr-gateway",
				"base_url":            "https://gateway.example",
				"fingerprint_profile": "claude-code-cli",
			},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:   "api key claude-code-cli config entry",
			apiKey: "key-config",
			attrs:  map[string]string{"api_key": "key-config", "base_url": "https://gateway.example"},
			cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{
				APIKey:             "key-config",
				BaseURL:            "https://gateway.example",
				FingerprintProfile: "claude-code-cli",
			}}},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:             "official anthropic api key opts in via metadata profile",
			apiKey:           "key-metadata",
			attrs:            map[string]string{"api_key": "key-metadata"},
			metadata:         map[string]any{"fingerprint_profile": "claude-code-cli"},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:     "kimi default token has no fingerprint",
			provider: "kimi",
			apiKey:   "kimi-access-token",
			metadata: map[string]any{"access_token": "kimi-access-token"},
		},
		{
			name:     "kimi with claude-code-cli profile opts in",
			provider: "kimi",
			apiKey:   "kimi-access-token",
			metadata: map[string]any{
				"access_token":        "kimi-access-token",
				"fingerprint_profile": "claude-code-cli",
			},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:     "kimi oauth json hyphenated fingerprint-profile opts in",
			provider: "kimi",
			apiKey:   "kimi-access-token",
			metadata: map[string]any{
				"access_token":        "kimi-access-token",
				"fingerprint-profile": "claude-code-cli",
			},
			wantProfileOAuth: true,
			wantSynthesize:   true,
			wantMCP:          true,
			wantDiagnostics:  true,
		},
		{
			name:             "unknown profile ignored",
			apiKey:           "key-unknown",
			attrs:            map[string]string{"api_key": "key-unknown", "fingerprint_profile": "not-a-profile"},
			wantAuthOAuth:    false,
			wantProfileOAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auth := &cliproxyauth.Auth{
				Provider:   tt.provider,
				Attributes: tt.attrs,
				Metadata:   tt.metadata,
			}
			fp := resolveClaudeFingerprintPolicy(tt.cfg, auth, tt.apiKey)
			if fp.AuthIsOAuthToken != tt.wantAuthOAuth {
				t.Fatalf("AuthIsOAuthToken = %v, want %v", fp.AuthIsOAuthToken, tt.wantAuthOAuth)
			}
			if fp.ProfileClaudeCodeCLI != tt.wantProfileOAuth {
				t.Fatalf("ProfileClaudeCodeCLI = %v, want %v", fp.ProfileClaudeCodeCLI, tt.wantProfileOAuth)
			}
			if fp.UseOAuthBetas != tt.wantProfileOAuth || fp.ApplyCLIIdentity != tt.wantProfileOAuth {
				t.Fatalf("UseOAuthBetas/ApplyCLIIdentity = %v/%v, want %v", fp.UseOAuthBetas, fp.ApplyCLIIdentity, tt.wantProfileOAuth)
			}
			if fp.SynthesizeIdentity != tt.wantSynthesize {
				t.Fatalf("SynthesizeIdentity = %v, want %v", fp.SynthesizeIdentity, tt.wantSynthesize)
			}
			if fp.MCPAlias != tt.wantMCP {
				t.Fatalf("MCPAlias = %v, want %v", fp.MCPAlias, tt.wantMCP)
			}
			if fp.InjectDiagnostics != tt.wantDiagnostics {
				t.Fatalf("InjectDiagnostics = %v, want %v", fp.InjectDiagnostics, tt.wantDiagnostics)
			}
			if fp.OAuthCancellation != tt.wantCancellation {
				t.Fatalf("OAuthCancellation = %v, want %v", fp.OAuthCancellation, tt.wantCancellation)
			}
		})
	}
}

func TestClaudeFingerprintProfileFromAuthConcurrentMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		claudeFingerprintProfileAttr: "claude-code-cli",
	}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1_000 {
			if got := claudeFingerprintProfileFromAuth(auth); got != claudeFingerprintProfileClaudeCodeCLI {
				t.Errorf("claudeFingerprintProfileFromAuth() = %q, want %q", got, claudeFingerprintProfileClaudeCodeCLI)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 1_000 {
			claudeauth.StoreMetadataString(
				&auth.Metadata,
				"account_uuid",
				"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			)
		}
	}()
	wg.Wait()
}

func TestApplyClaudeHeaders_ClaudeCodeCLIProfileUsesOAuthBetasWithoutPretendingToken(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":             "key-third-party",
		"base_url":            "https://gateway.example",
		"fingerprint_profile": "claude-code-cli",
	}}
	req, errReq := http.NewRequest(http.MethodPost, "https://gateway.example/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	if errHeaders := applyClaudeHeaders(req, auth, "key-third-party", false, nil, []byte(`{"model":"claude-sonnet-5"}`), &config.Config{}, nil, false, "11111111-2222-4333-8444-555555555555"); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer key-third-party" {
		t.Fatalf("Authorization = %q, want API key bearer", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want empty on third-party gateway", got)
	}
	betas := req.Header.Get("Anthropic-Beta")
	if !strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want oauth beta", betas)
	}
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("Anthropic-Beta = %q, want extended-cache-ttl", betas)
	}
	if !strings.Contains(betas, "fallback-credit-2026-06-01") {
		t.Fatalf("Anthropic-Beta = %q, want fallback-credit", betas)
	}
}

func TestApplyClaudeHeaders_OfficialAPIKeyDefaultRespectsClient(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key": "key-official",
	}}
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	if errHeaders := applyClaudeHeaders(req, auth, "key-official", false, nil, []byte(`{"model":"claude-sonnet-5"}`), &config.Config{}, incoming, false); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty on official Anthropic API key", got)
	}
	if got := req.Header.Get("x-api-key"); got != "key-official" {
		t.Fatalf("x-api-key = %q, want API key", got)
	}
	betas := req.Header.Get("Anthropic-Beta")
	if strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, default official API key must not add oauth beta", betas)
	}
	if strings.Contains(betas, "fallback-credit-2026-06-01") {
		t.Fatalf("Anthropic-Beta = %q, default official API key must not add fallback-credit", betas)
	}
	if !strings.Contains(betas, "interleaved-thinking-2025-05-14") {
		t.Fatalf("Anthropic-Beta = %q, want caller interleaved-thinking beta", betas)
	}
}

func TestApplyClaudeHeaders_OfficialAPIKeyClaudeCodeCLIProfileUsesOAuthBetas(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":             "key-official-fp",
		"fingerprint_profile": "claude-code-cli",
	}}
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	if errHeaders := applyClaudeHeaders(req, auth, "key-official-fp", false, nil, []byte(`{"model":"claude-sonnet-5"}`), &config.Config{}, nil, false, "11111111-2222-4333-8444-555555555555"); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty on official Anthropic API key", got)
	}
	if got := req.Header.Get("x-api-key"); got != "key-official-fp" {
		t.Fatalf("x-api-key = %q, want API key", got)
	}
	betas := req.Header.Get("Anthropic-Beta")
	if !strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want oauth beta after fingerprint-profile opt-in", betas)
	}
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("Anthropic-Beta = %q, want extended-cache-ttl after fingerprint-profile opt-in", betas)
	}
}

func TestClaudeExecutor_ClaudeCodeCLIFingerprintOnThirdPartyGateway(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		upstreamToolName := gjson.GetBytes(seenBody, "tools.0.name").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"` +
				upstreamToolName +
				`","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
		))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey:             "key-claude-code-cli-fp",
			BaseURL:            server.URL,
			FingerprintProfile: "claude-code-cli",
			Cloak:              &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: "claude-code-cli-api-key",
		Attributes: map[string]string{
			"api_key":             "key-claude-code-cli-fp",
			"base_url":            server.URL,
			"fingerprint_profile": "claude-code-cli",
		},
	}
	payload := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":[{"type":"text","text":"What can you do?"}]}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}}]}`)

	response, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := seenHeaders.Get("Authorization"); got != "Bearer key-claude-code-cli-fp" {
		t.Fatalf("Authorization = %q, want API key bearer", got)
	}
	wantBetas := claudeCodeCLIBetas(payload, nil, true)
	if got := seenHeaders.Get("Anthropic-Beta"); got != wantBetas {
		t.Fatalf("Anthropic-Beta = %q, want %q", got, wantBetas)
	}

	billing := gjson.GetBytes(seenBody, "system.0.text").String()
	if !strings.HasPrefix(billing, "x-anthropic-billing-header:") {
		t.Fatalf("system.0.text = %q, want billing header", billing)
	}
	// Native only emits cch for firstParty on api.anthropic.com or for vertex. A
	// third-party gateway therefore gets the billing header without a per-request
	// hash, which is both the measured shape and what keeps the gateway's prompt
	// cache stable.
	if strings.Contains(billing, "cch=") {
		t.Fatalf("billing = %q, want no cch on a third-party gateway", billing)
	}
	if got := gjson.GetBytes(seenBody, "system.1.text").String(); got != claudeCodeCLIIdentity {
		t.Fatalf("system.1.text = %q, want CLI identity", got)
	}

	userID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	if !helps.IsValidUserID(userID) {
		t.Fatalf("metadata.user_id = %q, want valid", userID)
	}
	if got := gjson.Get(userID, "account_uuid").String(); got == "" {
		t.Fatal("account_uuid is empty for claude-code-cli fingerprint identity")
	}
	sessionHeader := seenHeaders.Get("X-Claude-Code-Session-Id")
	if sessionHeader == "" {
		t.Fatal("missing X-Claude-Code-Session-Id")
	}
	if got := gjson.Get(userID, "session_id").String(); got != sessionHeader {
		t.Fatalf("metadata session_id = %q, header = %q", got, sessionHeader)
	}

	upstreamToolName := gjson.GetBytes(seenBody, "tools.0.name").String()
	if !strings.HasPrefix(upstreamToolName, "mcp__") {
		t.Fatalf("upstream tool name = %q, want OAuth CLI MCP alias", upstreamToolName)
	}
	if got := gjson.GetBytes(response.Payload, "content.0.name").String(); got != "read_file" {
		t.Fatalf("downstream tool name = %q, want restored caller name", got)
	}
}

type claudeFingerprintRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f claudeFingerprintRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClaudeExecutor_OfficialAPIKeyDefaultRespectsClient(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	})
	ctx := context.WithValue(
		context.Background(),
		"cliproxy.roundtripper",
		http.RoundTripper(transport),
	)
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey: "key-official-default",
	}}}
	auth := &cliproxyauth.Auth{
		ID: "official-api-key-default",
		Attributes: map[string]string{
			"api_key": "key-official-default",
		},
	}
	payload := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`)

	_, errExecute := NewClaudeExecutor(cfg).Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"Anthropic-Beta": []string{"caller-private-beta-2099-01-01"},
			"User-Agent":     []string{"caller-agent/1.0"},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if diagnostics := gjson.GetBytes(seenBody, "diagnostics"); diagnostics.Exists() {
		t.Fatalf("diagnostics = %s, default official API key must not inject CLI diagnostics", diagnostics.Raw)
	}
	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("tools.0.name = %q, want caller name", got)
	}
	if strings.Contains(string(seenBody), "x-anthropic-billing-header:") || strings.Contains(string(seenBody), "cch=") {
		t.Fatalf("default official API key must not inject billing/CCH: %s", seenBody)
	}
	userID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	if userID != "" && gjson.Get(userID, "account_uuid").String() != "" {
		t.Fatalf("metadata.user_id = %q, default official API key must not synthesize CLI account_uuid", userID)
	}
	betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
	if strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, default official API key must not add oauth beta", betas)
	}
	if betas != "caller-private-beta-2099-01-01" {
		t.Fatalf("Anthropic-Beta = %q, want exact caller beta", betas)
	}
	if got := claudeFingerprintHeaderValue(seenHeaders, "User-Agent"); got != "caller-agent/1.0" {
		t.Fatalf("User-Agent = %q, want caller value", got)
	}
	if got := claudeFingerprintHeaderValue(seenHeaders, "x-api-key"); got != "key-official-default" {
		t.Fatalf("x-api-key = %q, want API key auth", got)
	}
}

func TestClaudeExecutor_OfficialAPIKeyDefaultPreservesCallerCCH(t *testing.T) {
	var seenBody []byte
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{ID: "official-caller-cch", Attributes: map[string]string{"api_key": "key-official-caller-cch"}}
	payload := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=caller; cch=abcde;"},{"type":"text","text":"Keep this rule."}],"messages":[{"role":"user","content":"hello"}]}`)
	if _, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "claude-sonnet-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); got != "x-anthropic-billing-header: cc_version=caller; cch=abcde;" {
		t.Fatalf("caller billing/CCH = %q, want byte-preserved text", got)
	}
	if got := gjson.GetBytes(seenBody, "system.1.text").String(); got != "Keep this rule." {
		t.Fatalf("caller system text = %q, want preserved", got)
	}
}

func TestClaudeExecutor_OfficialAPIKeyClaudeCodeCLIFingerprintIncludesDiagnostics(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	})
	ctx := context.WithValue(
		context.Background(),
		"cliproxy.roundtripper",
		http.RoundTripper(transport),
	)
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:             "key-official-fp",
		FingerprintProfile: "claude-code-cli",
	}}}
	auth := &cliproxyauth.Auth{
		ID: "official-api-key-fp",
		Attributes: map[string]string{
			"api_key":             "key-official-fp",
			"fingerprint_profile": "claude-code-cli",
		},
	}
	payload := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hello"}]}`)

	_, errExecute := NewClaudeExecutor(cfg).Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if diagnostics := gjson.GetBytes(seenBody, "diagnostics"); !diagnostics.IsObject() {
		t.Fatalf("diagnostics = %s, want object after fingerprint-profile opt-in", diagnostics.Raw)
	}
	// api.anthropic.com is the one API-key origin where native emits cch, so the
	// opt-in must produce a finalized signature here.
	billing := gjson.GetBytes(seenBody, "system.0.text").String()
	if !strings.HasPrefix(billing, "x-anthropic-billing-header:") || !strings.Contains(billing, "cch=") {
		t.Fatalf("billing = %q, want signed cch on api.anthropic.com", billing)
	}
	if strings.Contains(billing, "cch=00000") {
		t.Fatalf("billing = %q, want finalized cch signature", billing)
	}
	userID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	if !helps.IsValidUserID(userID) || gjson.Get(userID, "account_uuid").String() == "" {
		t.Fatalf("metadata.user_id = %q, want synthesized CLI identity", userID)
	}
	betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
	if !strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want oauth beta after fingerprint-profile opt-in", betas)
	}
	if !strings.Contains(betas, claudeCacheDiagnosisBeta) {
		t.Fatalf("Anthropic-Beta = %q, want %q", betas, claudeCacheDiagnosisBeta)
	}
	if got := claudeFingerprintHeaderValue(seenHeaders, "x-api-key"); got != "key-official-fp" {
		t.Fatalf("x-api-key = %q, want API key auth", got)
	}
}

func TestClaudeExecutor_ClaudeCodeCLIFingerprintStreamMatchesWirePolicy(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream_1\"}}\n\n" +
				"event: message_stop\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
		))
	}))
	defer server.Close()

	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:             "key-claude-code-cli-stream",
		BaseURL:            server.URL,
		FingerprintProfile: "claude-code-cli",
		Cloak:              &config.CloakConfig{Mode: "always"},
	}}}
	auth := &cliproxyauth.Auth{
		ID: "claude-code-cli-stream",
		Attributes: map[string]string{
			"api_key":             "key-claude-code-cli-stream",
			"base_url":            server.URL,
			"fingerprint_profile": "claude-code-cli",
		},
	}
	payload := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`)

	result, errStream := NewClaudeExecutor(cfg).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
	if diagnostics := gjson.GetBytes(seenBody, "diagnostics"); diagnostics.Exists() {
		t.Fatalf("diagnostics = %s, custom gateway must not inherit official diagnostics", diagnostics.Raw)
	}
	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); !strings.HasPrefix(got, "mcp__") {
		t.Fatalf("stream tool name = %q, want OAuth CLI MCP alias", got)
	}
	betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
	if !strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want oauth beta on custom gateway", betas)
	}
}

func TestClaudeExecutor_ClaudeCodeCLIFingerprintCountTokensKeepsNativeShape(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":12}`)),
		}, nil
	})
	ctx := context.WithValue(
		context.Background(),
		"cliproxy.roundtripper",
		http.RoundTripper(transport),
	)
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:             "key-claude-code-cli-count",
		FingerprintProfile: "claude-code-cli",
	}}}
	auth := &cliproxyauth.Auth{
		ID: "claude-code-cli-count",
		Attributes: map[string]string{
			"api_key":             "key-claude-code-cli-count",
			"fingerprint_profile": "claude-code-cli",
		},
	}
	payload := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"metadata":{"user_id":"remove"},"diagnostics":{"previous_message_id":"remove"}}`)

	_, errCount := NewClaudeExecutor(cfg).countTokensUpstream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount != nil {
		t.Fatalf("countTokensUpstream() error = %v", errCount)
	}
	for _, field := range []string{"system", "metadata", "context_management", "diagnostics"} {
		if got := gjson.GetBytes(seenBody, field); got.Exists() {
			t.Fatalf("count_tokens %s = %s, want absent", field, got.Raw)
		}
	}
	if strings.Contains(string(seenBody), "cch=") {
		t.Fatalf("count_tokens body contains CCH: %s", seenBody)
	}
	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); !strings.HasPrefix(got, "mcp__") {
		t.Fatalf("count_tokens tool name = %q, want OAuth CLI MCP alias after fingerprint-profile opt-in", got)
	}
	if got, want := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta"), claudeCountTokensBetasForCredential(true); got != want {
		t.Fatalf("Anthropic-Beta = %q, want %q", got, want)
	}
}

func TestKimiExecutor_ClaudeMessagesWithAndWithoutClaudeCodeCLIFingerprint(t *testing.T) {
	var seenBodies [][]byte
	var seenHeaders []http.Header
	var mu sync.Mutex
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		seenBodies = append(seenBodies, body)
		seenHeaders = append(seenHeaders, req.Header.Clone())
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_test","type":"message","role":"assistant","model":"k2.5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{})
	payload := []byte(`{"model":"kimi-k2.5(max)","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)

	// 1. Default Kimi OAuth: no fingerprint injection.
	defaultAuth := &cliproxyauth.Auth{
		ID:         "kimi-auth-default",
		Provider:   "kimi",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	_, errDefault := executor.Execute(ctx, defaultAuth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5(max)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"Anthropic-Beta": []string{"kimi-caller-beta"},
			"User-Agent":     []string{"kimi-caller/1.0"},
		},
	})
	if errDefault != nil {
		t.Fatalf("default Execute() error = %v", errDefault)
	}

	// 2. Kimi OAuth with fingerprint_profile: "claude-code-cli": opts into Claude Code CLI fingerprint.
	fpAuth := &cliproxyauth.Auth{
		ID:         "kimi-auth-profile",
		Provider:   "kimi",
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"access_token":        "test-token",
			"fingerprint_profile": "claude-code-cli",
		},
	}
	_, errFP := executor.Execute(ctx, fpAuth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5(max)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if errFP != nil {
		t.Fatalf("fingerprint Execute() error = %v", errFP)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenBodies) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(seenBodies))
	}

	// Default Kimi preserves caller fingerprint headers and strips billing/CCH.
	if got := gjson.GetBytes(seenBodies[0], "metadata.user_id").String(); got != "" {
		t.Fatalf("default Kimi request should not have metadata.user_id: %q", got)
	}
	if strings.Contains(string(seenBodies[0]), "cch=") || strings.Contains(string(seenBodies[0]), "x-anthropic-billing-header:") {
		t.Fatalf("default Kimi request should not have billing/CCH: %s", seenBodies[0])
	}
	if got := claudeFingerprintHeaderValue(seenHeaders[0], "Anthropic-Beta"); got != "kimi-caller-beta" {
		t.Fatalf("default Kimi Anthropic-Beta = %q, want caller beta", got)
	}
	if got := claudeFingerprintHeaderValue(seenHeaders[0], "User-Agent"); got != "kimi-caller/1.0" {
		t.Fatalf("default Kimi User-Agent = %q, want caller value", got)
	}

	// Opt-in Kimi requests use the complete CLI fingerprint. Kimi is not a native
	// cch origin, so the billing header goes out unsigned, exactly as native does
	// against a non-first-party base URL.
	userID := gjson.GetBytes(seenBodies[1], "metadata.user_id").String()
	if !helps.IsValidUserID(userID) {
		t.Fatalf("opt-in Kimi metadata.user_id = %q, want valid synthesized user_id", userID)
	}
	if got := gjson.Get(userID, "account_uuid").String(); got == "" {
		t.Fatal("opt-in Kimi request should have non-empty synthesized account_uuid")
	}
	billing := gjson.GetBytes(seenBodies[1], "system.0.text").String()
	if !strings.HasPrefix(billing, "x-anthropic-billing-header:") {
		t.Fatalf("opt-in Kimi request must carry Claude billing attribution: %s", seenBodies[1])
	}
	if strings.Contains(billing, "cch=") {
		t.Fatalf("opt-in Kimi billing = %q, want no cch off first-party origins", billing)
	}
	betas := claudeFingerprintHeaderValue(seenHeaders[1], "Anthropic-Beta")
	if !strings.Contains(betas, "oauth-2025-04-20") || !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("opt-in Kimi Anthropic-Beta = %q, want full OAuth CLI beta set", betas)
	}
}

func TestKimiExecutor_ClaudeCodeCLIProfileKeepsUnsignedBillingAndNativeCountTokens(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *KimiExecutor, *cliproxyauth.Auth, []byte) error
	}{
		{name: "stream", run: func(ctx context.Context, executor *KimiExecutor, auth *cliproxyauth.Auth, payload []byte) error {
			result, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k2.5(max)", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload})
			if errStream != nil {
				return errStream
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					return chunk.Err
				}
			}
			return nil
		}},
		{name: "count tokens", run: func(ctx context.Context, executor *KimiExecutor, auth *cliproxyauth.Auth, payload []byte) error {
			_, errCount := executor.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k2.5(max)", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload})
			return errCount
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var seenBody []byte
			var seenHeaders http.Header
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				seenBody, _ = io.ReadAll(req.Body)
				seenHeaders = req.Header.Clone()
				if strings.Contains(req.URL.Path, "count_tokens") {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":7}`))}, nil
				}
				stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"k2.5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
			}))
			auth := &cliproxyauth.Auth{
				ID:         "kimi-profile-" + test.name,
				Provider:   "kimi",
				Attributes: map[string]string{},
				Metadata: map[string]any{
					"access_token":        "test-token",
					"fingerprint_profile": "claude-code-cli",
				},
			}
			payload := []byte(`{"model":"kimi-k2.5(max)","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
			if errRun := test.run(ctx, NewKimiExecutor(&config.Config{}), auth, payload); errRun != nil {
				t.Fatalf("request error = %v", errRun)
			}
			betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
			if test.name == "count tokens" {
				for _, field := range []string{"system", "metadata", "context_management", "diagnostics"} {
					if got := gjson.GetBytes(seenBody, field); got.Exists() {
						t.Fatalf("count_tokens %s = %s, want absent", field, got.Raw)
					}
				}
				if strings.Contains(string(seenBody), "cch=") || strings.Contains(string(seenBody), "currentDate") {
					t.Fatalf("count_tokens must keep the native shape without CCH/currentDate: %s", seenBody)
				}
				if want := claudeCountTokensBetasForCredential(true); betas != want {
					t.Fatalf("Anthropic-Beta = %q, want count_tokens CLI set %q", betas, want)
				}
				return
			}
			billing := gjson.GetBytes(seenBody, "system.0.text").String()
			if !strings.HasPrefix(billing, "x-anthropic-billing-header:") {
				t.Fatalf("upstream body is missing opt-in billing attribution: %s", seenBody)
			}
			if strings.Contains(billing, "cch=") {
				t.Fatalf("opt-in Kimi billing = %q, want no cch off first-party origins", billing)
			}
			if !strings.Contains(betas, "oauth-2025-04-20") || !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
				t.Fatalf("Anthropic-Beta = %q, want full OAuth CLI beta set", betas)
			}
		})
	}
}

// The custom-header escape hatch must have the same scope in caller-owned mode as
// it has on the CLI path: a non-streaming third-party gateway keeps operator
// overrides, while api.anthropic.com and streaming requests claw them back.
func TestApplyClaudeHeaders_CallerOwnedScopesOperatorHeaderOverrides(t *testing.T) {
	newAuth := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID: "caller-owned-operator-headers",
			Attributes: map[string]string{
				"api_key":                "key-operator-headers",
				"header:Accept":          "application/vnd.gateway+json",
				"header:Accept-Encoding": "identity",
			},
		}
	}
	body := []byte(`{"model":"claude-opus-4-6"}`)
	// The caller deliberately sends neither header; only the operator configured them.
	incoming := http.Header{}

	// Non-streaming custom gateway: the documented escape hatch wins. This is the
	// case the caller-owned branch used to break by resetting on "caller sent none"
	// instead of on upstream/stream scope.
	gatewayReq := httptest.NewRequest(http.MethodPost, "https://gateway.example/v1/messages", nil)
	if err := applyClaudeHeaders(gatewayReq, newAuth(), "key-operator-headers", false, nil, body, nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders(gateway) error = %v", err)
	}
	if got := gatewayReq.Header.Get("Accept"); got != "application/vnd.gateway+json" {
		t.Fatalf("gateway Accept = %q, want the operator override preserved", got)
	}
	if got := gatewayReq.Header.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("gateway Accept-Encoding = %q, want the operator override preserved", got)
	}

	// Streaming custom gateway: transport negotiation is restored so an Accept
	// override cannot silently disable SSE.
	streamReq := httptest.NewRequest(http.MethodPost, "https://gateway.example/v1/messages", nil)
	if err := applyClaudeHeaders(streamReq, newAuth(), "key-operator-headers", true, nil, body, nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders(stream) error = %v", err)
	}
	if got := streamReq.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("stream Accept = %q, want event-stream negotiation restored", got)
	}

	// api.anthropic.com: first-party identity is never operator-overridable.
	directReq := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(directReq, newAuth(), "key-operator-headers", false, nil, body, nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders(direct) error = %v", err)
	}
	if got := directReq.Header.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
		t.Fatalf("direct Accept-Encoding = %q, want the operator override clawed back", got)
	}
}

// Restoring transport negotiation must restore the caller's own choice, not CPA's
// default: this mode is caller-owned.
func TestApplyClaudeHeaders_CallerOwnedRestoreKeepsCallerAccept(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID: "caller-owned-restore",
		Attributes: map[string]string{
			"api_key":       "key-restore",
			"header:Accept": "application/vnd.operator+json",
		},
	}
	incoming := http.Header{"Accept": {"application/vnd.caller+json"}}
	req := newClaudeHeaderTestRequest(t, incoming)
	if err := applyClaudeHeaders(req, auth, "key-restore", false, nil,
		[]byte(`{"model":"claude-opus-4-6"}`), nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Accept"); got != "application/vnd.caller+json" {
		t.Fatalf("Accept = %q, want the caller value restored rather than a CPA default", got)
	}
}

// A caller that sends no User-Agent must not reach the upstream as Go's transport
// default, which reads as a bot signature. Uses a real socket because that default
// is added by the transport, not by the header builder.
func TestClaudeExecutor_CallerOwnedNeverSendsGoTransportUserAgent(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "caller-owned-ua", Attributes: map[string]string{
		"api_key":  "key-caller-owned-ua",
		"base_url": server.URL,
	}}
	if _, err := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth,
		cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := seen.Get("User-Agent")
	if strings.HasPrefix(got, "Go-http-client") {
		t.Fatalf("User-Agent = %q, want CPA's own identity rather than Go's transport default", got)
	}
	if !strings.HasPrefix(got, "CLIProxyAPI/") {
		t.Fatalf("User-Agent = %q, want a CLIProxyAPI/<version> fallback", got)
	}
}

// A caller that does send a User-Agent keeps it verbatim.
func TestClaudeExecutor_CallerOwnedForwardsCallerUserAgent(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "caller-owned-ua-keep", Attributes: map[string]string{
		"api_key":  "key-caller-owned-ua-keep",
		"base_url": server.URL,
	}}
	if _, err := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth,
		cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)},
		cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Headers:      http.Header{"User-Agent": {"my-sdk/1.2.3"}},
		}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := seen.Get("User-Agent"); got != "my-sdk/1.2.3" {
		t.Fatalf("User-Agent = %q, want the caller value forwarded verbatim", got)
	}
}

// Without a profile opt-in the caller owns its count_tokens body. A caller that
// deliberately sends context_management expects the returned count to reflect it,
// so CPA must not quietly reshape the request into the CLI contract.
func TestKimiExecutor_DefaultCountTokensRespectsCallerBody(t *testing.T) {
	var seenBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":7}`)),
		}, nil
	}))
	auth := &cliproxyauth.Auth{
		ID:         "kimi-default-count-tokens",
		Provider:   "kimi",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	payload := []byte(`{"model":"kimi-k2.5(max)","system":"caller system","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"caller-user"},"context_management":{"edits":[]}}`)
	if _, errCount := NewKimiExecutor(&config.Config{}).CountTokens(ctx, auth,
		cliproxyexecutor.Request{Model: "kimi-k2.5(max)", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}); errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected an upstream count_tokens request")
	}
	// Positive pins first: assert the request really arrived for this model, so the
	// preservation assertions below cannot pass vacuously.
	if got := gjson.GetBytes(seenBody, "model").String(); got == "" {
		t.Fatalf("upstream model is empty: %s", seenBody)
	}
	if got := gjson.GetBytes(seenBody, "messages.#").Int(); got != 1 {
		t.Fatalf("upstream messages length = %d, want 1: %s", got, seenBody)
	}
	for _, field := range []string{"system", "metadata", "context_management"} {
		if !gjson.GetBytes(seenBody, field).Exists() {
			t.Fatalf("default count_tokens dropped caller-owned %q: %s", field, seenBody)
		}
	}
	if got := gjson.GetBytes(seenBody, "metadata.user_id").String(); got != "caller-user" {
		t.Fatalf("metadata.user_id = %q, want the caller value preserved", got)
	}
	// Default mode must not add the CLI billing/CCH attribution either.
	if strings.Contains(string(seenBody), "x-anthropic-billing-header:") || strings.Contains(string(seenBody), "cch=") {
		t.Fatalf("default count_tokens must not inject billing/CCH: %s", seenBody)
	}
}

// api.anthropic.com rejects metadata/context_management/diagnostics on
// count_tokens regardless of profile, so upstream compatibility still strips them
// for an unprofiled first-party API key.
func TestClaudeExecutor_DefaultCountTokensStillStripsAnthropicRejectedFields(t *testing.T) {
	var seenBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":11}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{ID: "anthropic-default-count", Attributes: map[string]string{"api_key": "key-default-count"}}
	payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"caller-user"},"context_management":{"edits":[]},"diagnostics":{"previous_message_id":null}}`)
	if _, errCount := NewClaudeExecutor(&config.Config{}).CountTokens(ctx, auth,
		cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}); errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected an upstream count_tokens request")
	}
	if got := gjson.GetBytes(seenBody, "messages.#").Int(); got != 1 {
		t.Fatalf("upstream messages length = %d, want 1: %s", got, seenBody)
	}
	for _, field := range []string{"metadata", "context_management", "diagnostics"} {
		if got := gjson.GetBytes(seenBody, field); got.Exists() {
			t.Fatalf("api.anthropic.com count_tokens %s = %s, want stripped", field, got.Raw)
		}
	}
}

func TestKimiExecutor_ClaudeCodeCLIIdentitySurvivesAccessTokenRotation(t *testing.T) {
	var seenBodies [][]byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		seenBodies = append(seenBodies, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_test","type":"message","role":"assistant","model":"k2.5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)),
		}, nil
	}))
	payload := []byte(`{"model":"kimi-k2.5(max)","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
	for _, token := range []string{"token-before-refresh", "token-after-refresh"} {
		auth := &cliproxyauth.Auth{
			ID:         "stable-kimi-auth",
			Provider:   "kimi",
			Attributes: map[string]string{},
			Metadata: map[string]any{
				"access_token":        token,
				"fingerprint_profile": "claude-code-cli",
			},
		}
		if _, errExecute := NewKimiExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   "kimi-k2.5(max)",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}); errExecute != nil {
			t.Fatalf("Execute(%q) error = %v", token, errExecute)
		}
	}
	if len(seenBodies) != 2 {
		t.Fatalf("captured %d requests, want 2", len(seenBodies))
	}
	firstUserID := gjson.GetBytes(seenBodies[0], "metadata.user_id").String()
	secondUserID := gjson.GetBytes(seenBodies[1], "metadata.user_id").String()
	for _, field := range []string{"account_uuid", "device_id"} {
		if first, second := gjson.Get(firstUserID, field).String(), gjson.Get(secondUserID, field).String(); first == "" || first != second {
			t.Fatalf("%s changed across access token refresh: %q vs %q", field, first, second)
		}
	}
}

func TestStripDefaultKimiClaudeCodeAttributionRespectsProfile(t *testing.T) {
	t.Parallel()

	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220; cch=abcde;"},{"type":"text","text":"Keep this rule."}],"messages":[]}`)
	kimiAuth := &cliproxyauth.Auth{Provider: "kimi"}
	if got := stripDefaultKimiClaudeCodeAttribution(kimiAuth, "https://api.kimi.com/coding/v1/messages", false, body); strings.Contains(string(got), "cch=") || !strings.Contains(string(got), "Keep this rule.") {
		t.Fatalf("default Kimi stripping produced %s", got)
	}
	if got := stripDefaultKimiClaudeCodeAttribution(kimiAuth, "https://api.kimi.com/coding/v1/messages", true, body); !strings.Contains(string(got), "cch=abcde") {
		t.Fatalf("profiled Kimi request lost caller CCH: %s", got)
	}
}

func TestKimiExecutor_StripsCallerClaudeCodeCCH(t *testing.T) {
	var seenBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_test","type":"message","role":"assistant","model":"k2.5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	}))
	payload := []byte(`{"model":"kimi-k2.5(max)","max_tokens":32,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220; cch=abcde;"},{"type":"text","text":"Keep this rule."}],"messages":[{"role":"user","content":"hello"}]}`)
	_, errExecute := NewKimiExecutor(&config.Config{}).Execute(ctx, &cliproxyauth.Auth{
		Provider:   "kimi",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}, cliproxyexecutor.Request{
		Model:   "kimi-k2.5(max)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if strings.Contains(string(seenBody), "cch=") || strings.Contains(string(seenBody), "x-anthropic-billing-header:") {
		t.Fatalf("Kimi upstream body still has Claude CCH attribution: %s", seenBody)
	}
	if !strings.Contains(string(seenBody), "Keep this rule.") {
		t.Fatalf("Kimi upstream body dropped caller system text: %s", seenBody)
	}
}

// Profile resolution runs several times per request, so an unrecognized value must
// not turn one config typo into a per-request log flood.
func TestNormalizeClaudeFingerprintProfileWarnsOncePerValue(t *testing.T) {
	var buf bytes.Buffer
	previous := log.StandardLogger().Out
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	const (
		firstTypo  = "claude-code-cli-test-typo-a"
		secondTypo = "claude-code-cli-test-typo-b"
	)
	defer claudeFingerprintProfileWarned.Delete(firstTypo)
	defer claudeFingerprintProfileWarned.Delete(secondTypo)

	for i := 0; i < 5; i++ {
		if got := normalizeClaudeFingerprintProfile(firstTypo); got != claudeFingerprintProfileDefault {
			t.Fatalf("normalizeClaudeFingerprintProfile(%q) = %q, want default", firstTypo, got)
		}
	}
	normalizeClaudeFingerprintProfile(secondTypo)
	for i := 0; i < 3; i++ {
		normalizeClaudeFingerprintProfile("claude-code-cli")
		normalizeClaudeFingerprintProfile("")
	}

	if got := strings.Count(buf.String(), firstTypo); got != 1 {
		t.Fatalf("warnings for %q = %d, want exactly 1: %s", firstTypo, got, buf.String())
	}
	if got := strings.Count(buf.String(), secondTypo); got != 1 {
		t.Fatalf("warnings for %q = %d, want exactly 1: %s", secondTypo, got, buf.String())
	}
	if got := strings.Count(buf.String(), "unrecognized claude fingerprint-profile"); got != 2 {
		t.Fatalf("total warnings = %d, want 2 (one per distinct value): %s", got, buf.String())
	}
}

// The CLI profile is credential-scoped: the same credential resolves the same
// policy regardless of which upstream URL the request is being built for. Origin
// only decides CCH signing, through claudeCCHSigningEnabled.
func TestResolveClaudeFingerprintPolicyIsOriginIndependent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:             "key-origin-independent",
		FingerprintProfile: "claude-code-cli",
	}}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-origin-independent"}}

	policy := resolveClaudeFingerprintPolicy(cfg, auth, "key-origin-independent")
	if !policy.ProfileClaudeCodeCLI || policy.AuthIsOAuthToken {
		t.Fatalf("policy = %+v, want opted-in non-OAuth profile", policy)
	}
	for _, origin := range []string{
		"https://api.anthropic.com/v1/messages?beta=true",
		"https://gateway.example/v1/messages?beta=true",
		"https://api.kimi.com/v1/messages",
	} {
		wantCCH := origin == "https://api.anthropic.com/v1/messages?beta=true"
		if got := claudeCCHSigningEnabled("key-origin-independent", claudeCCHUpstreamAnthropic, policy.ProfileClaudeCodeCLI, origin); got != wantCCH {
			t.Fatalf("claudeCCHSigningEnabled(%q) = %t, want %t", origin, got, wantCCH)
		}
	}
}

func claudeFingerprintHeaderValue(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return strings.Join(values, ",")
		}
	}
	return ""
}
