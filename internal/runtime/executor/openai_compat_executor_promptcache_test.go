package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const compatPromptCacheSessionID = "compat-cache-session-1"

func newCompatPromptCacheExecutor(provider string, optIn bool) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	executor := NewOpenAICompatExecutor(provider, &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: provider, PromptCacheKey: optIn},
		},
	})
	auth := &cliproxyauth.Auth{Provider: provider, Attributes: map[string]string{"api_key": "test"}}
	return executor, auth
}

func expectedClaudeCodePromptCacheKey(model, sessionID, agentID string) string {
	executionScope := "claude:" + sessionID + ":agent:" + agentID
	identity := "cli-proxy-api:codex:claude-code" + "\x00" + model + "\x00" + executionScope
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
}

func TestOpenAICompatPromptCacheKeyDerivesDeterministicKeyWhenOptedIn(t *testing.T) {
	executor, auth := newCompatPromptCacheExecutor("cached-provider", true)
	req := cliproxyexecutor.Request{
		Model:   "gpt-oss-120b",
		Payload: []byte(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"first"}]}`),
	}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, compatPromptCacheSessionID)
	opts := cliproxyexecutor.Options{Headers: headers}
	first := executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, opts, []byte(`{"model":"gpt-oss-120b","messages":[]}`))
	second := executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, opts, []byte(`{"model":"gpt-oss-120b","messages":[]}`))
	expectedKey := expectedClaudeCodePromptCacheKey("gpt-oss-120b", compatPromptCacheSessionID, helps.ClaudeCodeMainAgentID)
	if gotKey := gjson.GetBytes(first, "prompt_cache_key").String(); gotKey != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedKey)
	}
	if gotKey2 := gjson.GetBytes(second, "prompt_cache_key").String(); gotKey2 != expectedKey {
		t.Fatalf("prompt_cache_key (second call) = %q, want %q", gotKey2, expectedKey)
	}
	if _, errParse := uuid.Parse(expectedKey); errParse != nil {
		t.Fatalf("derived prompt cache key %q is not a UUID: %v", expectedKey, errParse)
	}
}

func TestOpenAICompatPromptCacheKeyOffWhenNotOptedIn(t *testing.T) {
	executor, auth := newCompatPromptCacheExecutor("uncached-provider", false)
	req := cliproxyexecutor.Request{
		Model:   "gpt-oss-120b",
		Payload: []byte(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`),
	}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, compatPromptCacheSessionID)
	opts := cliproxyexecutor.Options{Headers: headers}
	body := executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, opts, []byte(`{"model":"gpt-oss-120b","messages":[]}`))
	if gjson.GetBytes(body, "prompt_cache_key").Exists() {
		t.Fatalf("prompt_cache_key must be absent when opt-in is off, body=%s", body)
	}
}

func TestOpenAICompatPromptCacheKeyExplicitKeyWinsOverDerived(t *testing.T) {
	executor, auth := newCompatPromptCacheExecutor("cached-provider", true)
	req := cliproxyexecutor.Request{
		Model:   "gpt-oss-120b",
		Payload: []byte(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`),
	}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, compatPromptCacheSessionID)
	opts := cliproxyexecutor.Options{Headers: headers}
	const explicitKey = "caller-supplied-key"
	body := executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, opts, []byte(`{"model":"gpt-oss-120b","prompt_cache_key":"`+explicitKey+`"}`))
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != explicitKey {
		t.Fatalf("prompt_cache_key = %q, want %q (explicit must win)", got, explicitKey)
	}
}

func TestOpenAICompatPromptCacheKeyAbsentSessionFallsBackToRandomUUID(t *testing.T) {
	executor, auth := newCompatPromptCacheExecutor("cached-provider", true)
	// No X-Claude-Code-Session-Id header and no metadata.user_id session suffix.
	req := cliproxyexecutor.Request{
		Model:   "gpt-oss-120b",
		Payload: []byte(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{Headers: http.Header{}}
	in := []byte(`{"model":"gpt-oss-120b","messages":[]}`)
	body := executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, opts, in)
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey == "" {
		t.Fatalf("prompt_cache_key must fall back to a random UUID, body=%s", body)
	}
	if _, errParse := uuid.Parse(gotKey); errParse != nil {
		t.Fatalf("fallback prompt_cache_key %q is not a UUID: %v", gotKey, errParse)
	}
}

func TestOpenAICompatPromptCacheKeyAgentScopeIsolatesKeys(t *testing.T) {
	executor, auth := newCompatPromptCacheExecutor("cached-provider", true)
	req := cliproxyexecutor.Request{
		Model:   "gpt-oss-120b",
		Payload: []byte(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hello"}]}`),
	}
	rootHeaders := http.Header{}
	rootHeaders.Set(helps.ClaudeCodeSessionHeader, compatPromptCacheSessionID)
	childHeaders := rootHeaders.Clone()
	childHeaders.Set(helps.ClaudeCodeAgentHeader, "agent-a")
	rootKey := gjson.GetBytes(executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, cliproxyexecutor.Options{Headers: rootHeaders}, []byte(`{"model":"gpt-oss-120b","messages":[]}`)), "prompt_cache_key").String()
	childKey := gjson.GetBytes(executor.applyCompatPromptCacheKey(context.Background(), auth, "gpt-oss-120b", req, cliproxyexecutor.Options{Headers: childHeaders}, []byte(`{"model":"gpt-oss-120b","messages":[]}`)), "prompt_cache_key").String()
	if rootKey == "" || childKey == "" || rootKey == childKey {
		t.Fatalf("agent prompt keys are not isolated: root=%q child=%q", rootKey, childKey)
	}
}
