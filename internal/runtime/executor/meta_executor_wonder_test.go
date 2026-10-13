package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func writeMetaWonderResponsesOK(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     "resp_wonder",
			"object": "response",
			"status": "completed",
			"model":  model,
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "ok"},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
		},
	})
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
}

func metaWonderTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": serverURL,
		},
	}
}

func TestMetaExecutor_ExecuteMuseWonder(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		writeMetaWonderResponsesOK(w, gjson.GetBytes(gotBody, "model").String())
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), metaWonderTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "muse-wonder-1.0" {
		t.Fatalf("upstream model = %q, want muse-wonder-1.0", got)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "muse-wonder-1.0" {
		t.Fatalf("downstream model = %q, want muse-wonder-1.0", got)
	}
}

func TestMetaExecutor_ExecuteStreamMuseWonderContributor(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		writeMetaWonderResponsesOK(w, gjson.GetBytes(gotBody, "model").String())
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	result, err := exec.ExecuteStream(context.Background(), metaWonderTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0-contributor",
		Payload: []byte(`{"model":"muse-wonder-1.0-contributor","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "muse-wonder-1.0-contributor" {
		t.Fatalf("upstream model = %q, want muse-wonder-1.0-contributor", got)
	}
	gotChunk := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			gotChunk = true
		}
	}
	if !gotChunk {
		t.Fatal("expected at least one stream payload chunk")
	}
}

func TestMetaExecutor_MuseWonderReasoningMax(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		writeMetaWonderResponsesOK(w, "muse-wonder-1.0")
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), metaWonderTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0(max)",
		Payload: []byte(`{"model":"muse-wonder-1.0","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "muse-wonder-1.0" {
		t.Fatalf("upstream model = %q, want muse-wonder-1.0", got)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "max" {
		t.Fatalf("reasoning.effort = %q, want max (body: %s)", got, gotBody)
	}
}

func TestMetaExecutor_ExecuteMuseWonderFull(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		writeMetaWonderResponsesOK(w, gjson.GetBytes(gotBody, "model").String())
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), metaWonderTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "muse-wonder-full-1.0",
		Payload: []byte(`{"model":"muse-wonder-full-1.0","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "muse-wonder-full-1.0" {
		t.Fatalf("upstream model = %q, want muse-wonder-full-1.0", got)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "muse-wonder-full-1.0" {
		t.Fatalf("downstream model = %q, want muse-wonder-full-1.0", got)
	}
}

func TestMetaExecutor_RefreshRevalidatesWonderFullAfterDCAExpiry(t *testing.T) {
	mintCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key":
			mintCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"dca expired"}`))
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"muse-wonder-1.0"},{"id":"muse-wonder-full-1.0"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL+"/key")
	t.Setenv("META_MODELS_URL", server.URL+"/models")

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":   "LLM|minted",
			"dca_token": "dca:expired-revalidate",
			"base_url":  "https://api.meta.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "meta",
			"auth_kind":      "oauth",
			"api_key":        "LLM|minted",
			"access_token":   "LLM|minted",
			"dca_token":      "dca:expired-revalidate",
			"dca_expires_at": float64(1700000000),
			"subs_tier_name": "High Usage",
			"is_subs_active": true,
		},
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if mintCalls != 0 {
		t.Fatalf("mint calls = %d, want 0 for a known-expired DCA token", mintCalls)
	}
	if got, ok := refreshed.Metadata["wonder_full_entitled"].(bool); !ok || !got {
		t.Fatalf("wonder_full_entitled = %v, want true", refreshed.Metadata["wonder_full_entitled"])
	}
}

func TestMetaExecutor_RefreshFallsBackToWonderFullRevalidationOnMintFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"dca expired"}`))
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"muse-wonder-1.0"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL+"/key")
	t.Setenv("META_MODELS_URL", server.URL+"/models")

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":   "LLM|minted",
			"dca_token": "dca:mint-fails-revalidate",
			"base_url":  "https://api.meta.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "meta",
			"auth_kind":      "oauth",
			"api_key":        "LLM|minted",
			"access_token":   "LLM|minted",
			"dca_token":      "dca:mint-fails-revalidate",
			"subs_tier_name": "Muse Code Max Usage",
			"is_subs_active": true,
		},
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got, ok := refreshed.Metadata["wonder_full_entitled"].(bool); !ok || got {
		t.Fatalf("wonder_full_entitled = %v, want false", refreshed.Metadata["wonder_full_entitled"])
	}
}

func TestMetaExecutor_MintClearsWonderFullAttributeEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_key":"LLM|fresh-minted","subs_tier_name":"High Usage","is_subs_active":true}`))
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL)

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"auth_kind":            "oauth",
			"dca_token":            "dca:clear-attr-evidence",
			"wonder_full_entitled": "true",
		},
		Metadata: map[string]any{
			"type":                 "meta",
			"auth_kind":            "oauth",
			"dca_token":            "dca:clear-attr-evidence",
			"wonder_full_entitled": true,
		},
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if _, ok := refreshed.Metadata["wonder_full_entitled"]; ok {
		t.Fatalf("expected metadata wonder_full_entitled cleared after mint, got %v", refreshed.Metadata["wonder_full_entitled"])
	}
	if _, ok := refreshed.Attributes["wonder_full_entitled"]; ok {
		t.Fatalf("expected attribute wonder_full_entitled cleared after mint, got %q", refreshed.Attributes["wonder_full_entitled"])
	}
	if refreshed.Metadata["subs_tier_name"] != "High Usage" {
		t.Fatalf("subs_tier_name = %v, want High Usage", refreshed.Metadata["subs_tier_name"])
	}
}

func TestMetaExecutor_RefreshRevalidatesWithoutDCAToken(t *testing.T) {
	modelsHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelsHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"muse-wonder-1.0"}]}`))
	}))
	defer server.Close()
	t.Setenv("META_MODELS_URL", server.URL)

	exec := NewMetaExecutor(&config.Config{})
	oauth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "LLM|minted",
			"base_url":  "https://api.meta.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "meta",
			"auth_kind":      "oauth",
			"api_key":        "LLM|minted",
			"access_token":   "LLM|minted",
			"subs_tier_name": "Muse Code Max Usage",
			"is_subs_active": true,
		},
	}
	refreshed, err := exec.Refresh(context.Background(), oauth)
	if err != nil {
		t.Fatalf("Refresh() oauth error = %v", err)
	}
	if modelsHits != 1 {
		t.Fatalf("models hits = %d, want 1", modelsHits)
	}
	if got, ok := refreshed.Metadata["wonder_full_entitled"].(bool); !ok || got {
		t.Fatalf("wonder_full_entitled = %v, want false", refreshed.Metadata["wonder_full_entitled"])
	}

	apiKeyAuth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "meta-model-api-key",
			"base_url":  "https://api.meta.ai/v1",
		},
		Metadata: map[string]any{
			"type":      "meta",
			"auth_kind": "apikey",
			"api_key":   "meta-model-api-key",
		},
	}
	if _, errRefresh := exec.Refresh(context.Background(), apiKeyAuth); errRefresh != nil {
		t.Fatalf("Refresh() apikey error = %v", errRefresh)
	}
	if modelsHits != 1 {
		t.Fatalf("models hits after apikey refresh = %d, want still 1", modelsHits)
	}
	if _, ok := apiKeyAuth.Metadata["wonder_full_entitled"]; ok {
		t.Fatal("expected no wonder_full_entitled recorded for apikey credential")
	}
}

func TestMetaExecutor_RefreshWithoutDCATokenReturnsRevalidationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"catalog unavailable"}`))
	}))
	defer server.Close()
	t.Setenv("META_MODELS_URL", server.URL)

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "LLM|minted",
			"base_url":  "https://api.meta.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "meta",
			"auth_kind":      "oauth",
			"api_key":        "LLM|minted",
			"access_token":   "LLM|minted",
			"subs_tier_name": "High Usage",
			"is_subs_active": true,
		},
	}
	if _, err := exec.Refresh(context.Background(), auth); err == nil {
		t.Fatal("Refresh() error = nil, want revalidation failure surfaced for refresh backoff")
	}
	if _, ok := auth.Metadata["wonder_full_entitled"]; ok {
		t.Fatal("expected no wonder_full_entitled recorded after failed revalidation")
	}
}
