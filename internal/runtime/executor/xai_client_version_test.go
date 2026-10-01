package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestXAIChatProxyIdentityHeadersUseDynamicGrokCLIVersion(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}}
	req, errReq := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/responses", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}

	applyXAIChatHeaders(req, nil, auth, "token", false, "")

	version := req.Header.Get(xaiClientVersionHeader)
	if version == "" {
		t.Fatalf("%s is empty", xaiClientVersionHeader)
	}
	if got := req.Header.Get("User-Agent"); got != "xai-grok-workspace/"+version {
		t.Fatalf("User-Agent = %q, want xai-grok-workspace/%s", got, version)
	}
}

func TestXAIChatProxyConfiguredGrokCLIVersionOverridesDynamicValue(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}}
	req, errReq := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/responses", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	cfg := &config.Config{XAI: config.XAIConfig{GrokCLIVersion: "9.8.7"}}

	applyXAIChatHeaders(req, cfg, auth, "token", false, "")

	if got := req.Header.Get(xaiClientVersionHeader); got != "9.8.7" {
		t.Fatalf("%s = %q, want configured 9.8.7", xaiClientVersionHeader, got)
	}
	if got, want := req.Header.Get("User-Agent"), "xai-grok-workspace/9.8.7"; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

func TestXAIChatProxyCustomHeadersOverrideConfiguredVersion(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":                        "oauth",
			"header:" + xaiClientVersionHeader: "8.7.6",
		},
	}
	req, errReq := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/responses", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	cfg := &config.Config{XAI: config.XAIConfig{GrokCLIVersion: "9.8.7"}}

	applyXAIChatHeaders(req, cfg, auth, "token", false, "")

	if got := req.Header.Get(xaiClientVersionHeader); got != "8.7.6" {
		t.Fatalf("%s = %q, want per-auth override 8.7.6", xaiClientVersionHeader, got)
	}
}
