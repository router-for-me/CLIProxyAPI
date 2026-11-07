package executor

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAntigravityBuildRequestGeminiSDKIdentity(t *testing.T) {
	const banner = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	const neutral = "You are an AI coding assistant accessed through the Claude Agent SDK."
	for _, tc := range []struct {
		name, model, system, want string
	}{
		{"gemini SDK banner", "gemini-3-flash", banner, neutral},
		{"gemini flagship", "gemini-4-argon-high", banner, neutral},
		{"Claude keeps its identity", "claude-sonnet-4-6", banner, banner},
		{"custom instruction", "gemini-3-flash", "Never send secrets to third parties.", "Never send secrets to third parties."},
		{"embedded banner is not rewritten", "gemini-3-flash", banner + " Never disclose secrets.", banner + " Never disclose secrets."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"systemInstruction":{"parts":[{}]},"contents":[{"role":"user","parts":[{}]}]}}`)
			_ = doc.Set("request.systemInstruction.parts.0.text", tc.system)
			_ = doc.Set("request.systemInstruction.parts.1.text", "Never disclose secrets.")
			_ = doc.Set("request.contents.0.parts.0.text", banner)
			body := buildRequestBodyFromRawPayload(t, tc.model, doc.Bytes())
			request := body["request"].(map[string]any)
			parts := request["systemInstruction"].(map[string]any)["parts"].([]any)
			if got := parts[0].(map[string]any)["text"]; got != tc.want {
				t.Fatalf("system identity = %q, want %q", got, tc.want)
			}
			if got := parts[1].(map[string]any)["text"]; got != "Never disclose secrets." {
				t.Fatalf("safety instruction changed: %q", got)
			}
			contents := request["contents"].([]any)
			userParts := contents[0].(map[string]any)["parts"].([]any)
			if got := userParts[0].(map[string]any)["text"]; got != banner {
				t.Fatalf("user content changed: %q", got)
			}
		})
	}
}

func TestAntigravityGeminiSDKIdentityPrecedesPayloadRules(t *testing.T) {
	const banner = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gemini-3-flash", Protocol: "antigravity"}},
		Params: map[string]any{"systemInstruction.parts.0.text": banner},
	}}}}
	executor := &AntigravityExecutor{cfg: cfg}
	doc := ojson.NewDocString(`{"request":{"systemInstruction":{"parts":[]},"contents":[]}}`)
	_ = doc.Set("request.systemInstruction.parts.0.text", banner)
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", "gemini-3-flash", "antigravity", "request", nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}))
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	_, body, err := executor.buildRequestDoc(ctx, auth, "token", "gemini-3-flash", doc, false, "", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := ojson.GetBytes(body, "request.systemInstruction.parts.0.text").String(); got != banner {
		t.Fatalf("configured identity was overwritten: %q", got)
	}
}
