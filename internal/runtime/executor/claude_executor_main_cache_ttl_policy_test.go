package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const mainCacheTTLTestUserID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`

const mainCacheTTLTestGateway = "https://gateway.example.test"

func mainCacheTTLTestPayload(thread string) []byte {
	threadField := ""
	if thread != "" {
		threadField = `"thread":` + thread + `,`
	}
	return []byte(`{"model":"claude-opus-4-6",` + threadField + `"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"metadata":{"user_id":` + fmt.Sprintf("%q", mainCacheTTLTestUserID) + `}}`)
}

func mainCacheTTLTestAuth(baseURL string, attrs map[string]string) *cliproxyauth.Auth {
	merged := map[string]string{"api_key": "sk-ant-oat-main-cache-ttl"}
	if baseURL != "" {
		merged["base_url"] = baseURL
	}
	for name, value := range attrs {
		merged[name] = value
	}
	return &cliproxyauth.Auth{ID: "main-cache-ttl-policy", Metadata: claudeOAuthTestMetadata(), Attributes: merged}
}

type mainCacheTTLCapture struct {
	body   []byte
	header http.Header
}

// runMainCacheTTLNativeOAuth sends one confirmed native Claude Code request and
// records the body and headers that reached upstream.
func runMainCacheTTLNativeOAuth(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte, stream bool, messageID string) (mainCacheTTLCapture, error) {
	t.Helper()
	var seen mainCacheTTLCapture
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		seen = mainCacheTTLCapture{body: body, header: req.Header.Clone()}
		if stream {
			return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", claudeThreadTestSSE(messageID)), nil
		}
		return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage(messageID)), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	incoming := http.Header{
		"User-Agent":               {"claude-cli/2.1.321 (external, cli)"},
		"X-App":                    {"cli"},
		"Anthropic-Beta":           {"claude-code-20250219"},
		"X-Claude-Code-Session-Id": {"11111111-2222-4333-8444-555555555555"},
	}
	if !helps.DetectClaudeCodeRequest(incoming, payload, false).Confirmed {
		t.Fatal("fixture must be classified as a native Claude Code request")
	}
	req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}
	opts := cliproxyexecutor.Options{
		Stream:          stream,
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
	}
	if stream {
		result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			return seen, errStream
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return seen, chunk.Err
			}
		}
		return seen, nil
	}
	_, errExecute := executor.Execute(ctx, auth, req, opts)
	return seen, errExecute
}

func mainCacheTTLSentBetas(header http.Header) map[string]bool {
	return claudeRequestedBetas(strings.Join(helps.HeaderValuesCaseInsensitive(header, "Anthropic-Beta"), ","), nil)
}

// mainCacheTTLMarkerTTLs returns the ttl of each cache_control marker, "" when absent.
func mainCacheTTLMarkerTTLs(t *testing.T, body []byte) []string {
	t.Helper()
	var ttls []string
	forEachClaudeCacheControlBlock(body, func(path string, block ojson.Result) {
		if cacheControl := block.Get("cache_control"); cacheControl.IsObject() {
			ttls = append(ttls, cacheControl.Get("ttl").String())
		}
	})
	if len(ttls) == 0 {
		t.Fatalf("no cache_control markers reached upstream: %s", body)
	}
	return ttls
}

func assertMainCacheTTLMarkers(t *testing.T, body []byte, want string) {
	t.Helper()
	for i, ttl := range mainCacheTTLMarkerTTLs(t, body) {
		if ttl != want {
			t.Errorf("marker %d ttl = %q, want %q; body=%s", i, ttl, want, body)
		}
	}
}

func TestClaudeExecutor_MainCacheTTL1hSkipsWhenExtendedCacheBetaNotSent(t *testing.T) {
	policies := []struct {
		name    string
		strip   []string
		baseURL string
		attrs   map[string]string
	}{
		{name: "header-defaults strip", strip: []string{claudeExtendedCacheTTLBeta}},
		{name: "header-defaults strip on gateway", strip: []string{claudeExtendedCacheTTLBeta}, baseURL: mainCacheTTLTestGateway},
		{name: "auth header override on gateway", baseURL: mainCacheTTLTestGateway, attrs: map[string]string{"header:Anthropic-Beta": "claude-code-20250219,oauth-2025-04-20"}},
	}
	for _, policy := range policies {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", policy.name, stream), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.ClaudeCode.MainCacheTTL1h = true
				cfg.ClaudeHeaderDefaults.AnthropicBetaStrip = policy.strip
				seen, err := runMainCacheTTLNativeOAuth(t, NewClaudeExecutor(cfg), mainCacheTTLTestAuth(policy.baseURL, policy.attrs), mainCacheTTLTestPayload(`{"type":"create"}`), stream, "msg_ttl_1")
				if err != nil {
					t.Fatalf("request error = %v", err)
				}
				if mainCacheTTLSentBetas(seen.header)[claudeExtendedCacheTTLBeta] {
					t.Fatalf("operator header policy was overridden: Anthropic-Beta = %q", seen.header.Get("Anthropic-Beta"))
				}
				assertMainCacheTTLMarkers(t, seen.body, "")
			})
		}
	}
}

// On api.anthropic.com the executor rebuilds Anthropic-Beta after credential
// headers, so an auth override cannot remove the beta and the upgrade still applies.
func TestClaudeExecutor_MainCacheTTL1hKeepsUpgradeWhenBetaSurvives(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		attrs   map[string]string
		strip   []string
		append  []string
	}{
		{name: "auth header override on anthropic", attrs: map[string]string{"header:Anthropic-Beta": "claude-code-20250219"}},
		{name: "auth header override keeps beta on gateway", baseURL: mainCacheTTLTestGateway, attrs: map[string]string{"header:Anthropic-Beta": "claude-code-20250219," + claudeExtendedCacheTTLBeta}},
		{name: "header-defaults append", append: []string{"context-1m-2025-08-07"}},
		{name: "header-defaults strip then append", strip: []string{claudeExtendedCacheTTLBeta}, append: []string{claudeExtendedCacheTTLBeta}},
		{name: "strip replaced by gateway override that keeps beta", strip: []string{claudeExtendedCacheTTLBeta}, baseURL: mainCacheTTLTestGateway, attrs: map[string]string{"header:Anthropic-Beta": "claude-code-20250219,oauth-2025-04-20," + claudeExtendedCacheTTLBeta}},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.ClaudeCode.MainCacheTTL1h = true
				cfg.ClaudeHeaderDefaults.AnthropicBetaStrip = tc.strip
				cfg.ClaudeHeaderDefaults.AnthropicBetaAppend = tc.append
				seen, err := runMainCacheTTLNativeOAuth(t, NewClaudeExecutor(cfg), mainCacheTTLTestAuth(tc.baseURL, tc.attrs), mainCacheTTLTestPayload(`{"type":"create"}`), stream, "msg_ttl_1")
				if err != nil {
					t.Fatalf("request error = %v", err)
				}
				if !mainCacheTTLSentBetas(seen.header)[claudeExtendedCacheTTLBeta] {
					t.Fatalf("Anthropic-Beta = %q, want %s", seen.header.Get("Anthropic-Beta"), claudeExtendedCacheTTLBeta)
				}
				assertMainCacheTTLMarkers(t, seen.body, claudeCacheControlTTL1h)
			})
		}
	}
}

// A Thread created before the option was enabled froze a profile that already
// carries the extended-cache beta, so turning the option on mid-Thread is safe.
func TestClaudeExecutor_MainCacheTTL1hEnabledMidThread(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			claudeThreadTestKVStore(t)
			cfg := &config.Config{}
			executor := NewClaudeExecutor(cfg)
			auth := mainCacheTTLTestAuth("", nil)
			created, err := runMainCacheTTLNativeOAuth(t, executor, auth, mainCacheTTLTestPayload(`{"type":"create"}`), stream, "msg_ttl_thread_1")
			if err != nil {
				t.Fatalf("create error = %v", err)
			}
			assertMainCacheTTLMarkers(t, created.body, "")
			cfg.ClaudeCode.MainCacheTTL1h = true
			continued, err := runMainCacheTTLNativeOAuth(t, executor, auth, mainCacheTTLTestPayload(`{"type":"continue","previous_message_id":"msg_ttl_thread_1"}`), stream, "msg_ttl_thread_2")
			if err != nil {
				t.Fatalf("continue error = %v", err)
			}
			assertMainCacheTTLMarkers(t, continued.body, claudeCacheControlTTL1h)
		})
	}
}

// The upgrade must run before the payload rules: a filter that removes the ttl
// but keeps the marker has the last word.
func TestClaudeExecutor_MainCacheTTL1hPayloadFilterRemovesTTL(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			cfg := &config.Config{}
			cfg.ClaudeCode.MainCacheTTL1h = true
			cfg.Payload.Filter = []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: "claude-opus-4-6", Protocol: "claude"}},
				Params: []string{"system.1.cache_control.ttl", "messages.0.content.0.cache_control.ttl"},
			}}
			seen, err := runMainCacheTTLNativeOAuth(t, NewClaudeExecutor(cfg), mainCacheTTLTestAuth("", nil), mainCacheTTLTestPayload(""), stream, "msg_ttl_1")
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			assertMainCacheTTLMarkers(t, seen.body, "")
		})
	}
}
