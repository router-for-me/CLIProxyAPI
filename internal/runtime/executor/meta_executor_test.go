package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestMetaExecutorIdentifier(t *testing.T) {
	executor := NewMetaExecutor(nil)
	if got := executor.Identifier(); got != "meta" {
		t.Fatalf("Identifier() = %q, want meta", got)
	}
}

func TestMetaExecutorRefreshRemintsFromRefreshToken(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-minted-access",
			"refresh_token": "new-minted-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	executor := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"type":           "meta",
			"access_token":   "old-access",
			"refresh_token":  "old-refresh",
			"token_endpoint": server.URL,
		},
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
	refreshed, err := executor.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed == nil {
		t.Fatal("Refresh() = nil auth")
	}
	if got := metaMetadataString(refreshed.Metadata, "access_token"); got != "new-minted-access" {
		t.Fatalf("access token = %q, want new-minted-access", got)
	}
	if got := metaMetadataString(refreshed.Metadata, "refresh_token"); got != "new-minted-refresh" {
		t.Fatalf("refresh token = %q, want new-minted-refresh", got)
	}
	if got := metaMetadataString(refreshed.Metadata, "type"); got != "meta" {
		t.Fatalf("type = %q, want meta", got)
	}
	if got := refreshed.Attributes["auth_kind"]; got != "oauth" {
		t.Fatalf("auth_kind = %q, want oauth", got)
	}
	if gotForm.Get("grant_type") != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", gotForm.Get("grant_type"))
	}
	if gotForm.Get("refresh_token") != "old-refresh" {
		t.Fatalf("refresh_token = %q, want old-refresh", gotForm.Get("refresh_token"))
	}
}

func TestMetaExecutorRefreshNoopWithoutRefreshToken(t *testing.T) {
	executor := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"type":         "meta",
			"access_token": "static-key",
		},
	}
	refreshed, err := executor.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed == nil {
		t.Fatal("Refresh() = nil auth")
	}
	if got := metaMetadataString(refreshed.Metadata, "access_token"); got != "static-key" {
		t.Fatalf("access token = %q, want static-key", got)
	}
}

func TestMetaExecutorExecuteDelegatesToOpenAICompat(t *testing.T) {
	var gotAuthHeader string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"muse-code","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "meta-minted-key",
			"base_url": server.URL,
		},
	}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-code",
		Payload: []byte(`{"model":"muse-code","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.HasPrefix(gotAuthHeader, "Bearer ") {
		t.Fatalf("Authorization header = %q, want Bearer", gotAuthHeader)
	}
	if gotModel := gotBody["model"]; gotModel != "muse-code" {
		t.Fatalf("body model = %v, want muse-code", gotModel)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("Execute() = empty payload")
	}
}
