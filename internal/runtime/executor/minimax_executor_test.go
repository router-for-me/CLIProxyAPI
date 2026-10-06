package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	minimaxauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func newMinimaxTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "minimax-1.json",
		Provider: "minimax",
		Metadata: map[string]any{
			"type":          "minimax",
			"auth_kind":     "oauth",
			"access_token":  "access-token-1",
			"refresh_token": "refresh-token-1",
			"region":        "global",
		},
		Attributes: map[string]string{},
	}
}

func TestMinimaxExecutorIdentity(t *testing.T) {
	e := NewMinimaxExecutor(nil)
	if e.Identifier() != "minimax" {
		t.Fatalf("Identifier() = %q", e.Identifier())
	}
	if e.upstreamRequestLogProvider() != "minimax" {
		t.Fatalf("upstreamRequestLogProvider() = %q", e.upstreamRequestLogProvider())
	}
}

// TestMinimaxInjectsAnthropicBaseURL verifies the credential is retargeted at
// the Anthropic-compatible endpoint that the shared Claude builder appends
// "/v1/messages" to.
func TestMinimaxInjectsAnthropicBaseURL(t *testing.T) {
	auth := newMinimaxTestAuth()
	injectMinimaxBaseURL(auth)
	if got := auth.Attributes["base_url"]; got != "https://api.minimax.io/anthropic" {
		t.Fatalf("base_url = %q", got)
	}
	if auth.Attributes[claudeForceAPIKeyHeaderAttr] != "true" {
		t.Fatal("credential must opt in to the x-api-key header")
	}

	// A China credential is identified by its provider key, which survives the
	// metadata-only reload that a file-backed credential goes through.
	cn := newMinimaxTestAuth()
	cn.Provider = minimaxauth.ProviderCN
	cn.Attributes = nil
	injectMinimaxBaseURL(cn)
	if got := cn.Attributes["base_url"]; got != "https://api.minimax.cn/anthropic" {
		t.Fatalf("cn base_url = %q", got)
	}
}

// TestMinimaxUsesAPIKeyHeader is the regression guard for MiniMax's header rule:
// the Anthropic-compatible endpoint rejects Authorization: Bearer.
func TestMinimaxUsesAPIKeyHeader(t *testing.T) {
	auth := newMinimaxTestAuth()
	injectMinimaxBaseURL(auth)

	req, err := http.NewRequest(http.MethodPost, "https://api.minimax.io/anthropic/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err = applyClaudeHeadersWithNativeProfile(req, auth, "access-token-1", false, nil, []byte(`{}`), nil, http.Header{}, false, false); err != nil {
		t.Fatalf("applyClaudeHeadersWithNativeProfile: %v", err)
	}
	if got := req.Header.Get("x-api-key"); got != "access-token-1" {
		t.Fatalf("x-api-key = %q, want the access token", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization must be absent, got %q", got)
	}
}

// TestClaudeHeaderSeamIsDefaultOff proves the shared builder is untouched for
// every other provider.
func TestClaudeHeaderSeamIsDefaultOff(t *testing.T) {
	claudeAuth := &cliproxyauth.Auth{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-oat01-x"},
		Attributes: map[string]string{},
	}
	req, err := http.NewRequest(http.MethodPost, "https://gateway.example.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err = applyClaudeHeadersWithNativeProfile(req, claudeAuth, "sk-ant-oat01-x", false, nil, []byte(`{}`), nil, http.Header{}, false, false); err != nil {
		t.Fatalf("applyClaudeHeadersWithNativeProfile: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-x" {
		t.Fatalf("non-MiniMax gateways must keep Bearer, got %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key must stay unset, got %q", got)
	}

	// The first-party Anthropic origin keeps its existing x-api-key behavior.
	firstParty := &cliproxyauth.Auth{Provider: "claude", Metadata: map[string]any{}, Attributes: map[string]string{"api_key": "sk-ant-api"}}
	req2, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err = applyClaudeHeadersWithNativeProfile(req2, firstParty, "sk-ant-api", false, nil, []byte(`{}`), nil, http.Header{}, false, false); err != nil {
		t.Fatalf("applyClaudeHeadersWithNativeProfile: %v", err)
	}
	if got := req2.Header.Get("x-api-key"); got != "sk-ant-api" {
		t.Fatalf("first-party x-api-key = %q", got)
	}
}

func TestMinimaxPrepareRequest(t *testing.T) {
	e := NewMinimaxExecutor(nil)
	auth := newMinimaxTestAuth()
	req, err := http.NewRequest(http.MethodPost, "https://api.minimax.io/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err = e.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if got := req.Header.Get("x-api-key"); got != "access-token-1" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization must be absent, got %q", got)
	}
	// A nil request must be tolerated.
	if err = e.PrepareRequest(nil, auth); err != nil {
		t.Fatalf("PrepareRequest(nil): %v", err)
	}
}

func TestMinimaxRequestToFormat(t *testing.T) {
	e := NewMinimaxExecutor(nil)
	imageOpts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	if got := e.RequestToFormat(cliproxyexecutor.Request{}, imageOpts); got != imageOpts.SourceFormat {
		t.Fatalf("image format = %q, want it echoed through", got)
	}
	claudeOpts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	if got := e.RequestToFormat(cliproxyexecutor.Request{}, claudeOpts); got != sdktranslator.FormatClaude {
		t.Fatalf("claude format = %q", got)
	}
	openaiOpts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	if got := e.RequestToFormat(cliproxyexecutor.Request{}, openaiOpts); got != sdktranslator.FormatClaude {
		t.Fatalf("text must always target the Claude format, got %q", got)
	}
}

func TestMinimaxTokenFallsBackToMetadata(t *testing.T) {
	auth := newMinimaxTestAuth()
	if got := minimaxToken(auth); got != "access-token-1" {
		t.Fatalf("token = %q", got)
	}
	// An api_key attribute takes precedence, matching the other executors.
	auth.Attributes["api_key"] = "sk-api-explicit"
	if got := minimaxToken(auth); got != "sk-api-explicit" {
		t.Fatalf("token = %q", got)
	}
	if got := minimaxToken(nil); got != "" {
		t.Fatalf("nil auth token = %q", got)
	}
}

func TestBuildMinimaxImagePayload(t *testing.T) {
	req := []byte(`{"model":"image-01","prompt":"a cat","n":2,"response_format":"url","aspect_ratio":"16:9","seed":7,"width":1024}`)
	payload, err := buildMinimaxImagePayload(req)
	if err != nil {
		t.Fatalf("buildMinimaxImagePayload: %v", err)
	}
	if !json.Valid(payload) {
		t.Fatalf("payload must be valid JSON: %s", payload)
	}
	if gjson.GetBytes(payload, "prompt").String() != "a cat" {
		t.Fatalf("prompt = %q", gjson.GetBytes(payload, "prompt").String())
	}
	if gjson.GetBytes(payload, "n").Int() != 2 {
		t.Fatalf("n = %v", gjson.GetBytes(payload, "n").Value())
	}
	if gjson.GetBytes(payload, "response_format").String() != "url" {
		t.Fatalf("response_format = %q", gjson.GetBytes(payload, "response_format").String())
	}
	if gjson.GetBytes(payload, "aspect_ratio").String() != "16:9" {
		t.Fatalf("aspect_ratio = %q", gjson.GetBytes(payload, "aspect_ratio").String())
	}
	if gjson.GetBytes(payload, "seed").Int() != 7 {
		t.Fatalf("seed = %v", gjson.GetBytes(payload, "seed").Value())
	}
	// A missing model falls back to the provider default.
	fallback, err := buildMinimaxImagePayload([]byte(`{"prompt":"x"}`))
	if err != nil {
		t.Fatalf("buildMinimaxImagePayload fallback: %v", err)
	}
	if gjson.GetBytes(fallback, "model").String() != "image-01" {
		t.Fatalf("default model = %q", gjson.GetBytes(fallback, "model").String())
	}
	if _, err = buildMinimaxImagePayload([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for a malformed body")
	}
}

// TestNormalizeMinimaxImageResponse covers the shape translation from
// MiniMax's data.image_base64 / data.image_urls to the OpenAI data[] array.
func TestNormalizeMinimaxImageResponse(t *testing.T) {
	// Base64 and URL arrays are walked by index, so a mixed response yields one
	// entry per index rather than one entry per array.
	mixed := []byte(`{"data":{"image_base64":["QUJD","REVG"],"image_urls":["https://img/1.png"]}}`)
	out, err := normalizeMinimaxImageResponse(mixed)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	items := gjson.GetBytes(out, "data").Array()
	if len(items) != 2 {
		t.Fatalf("want 2 images, got %d: %s", len(items), out)
	}
	if items[0].Get("b64_json").String() != "QUJD" {
		t.Fatalf("first b64 = %q", items[0].Get("b64_json").String())
	}
	if items[1].Get("b64_json").String() != "REVG" {
		t.Fatalf("second b64 = %q", items[1].Get("b64_json").String())
	}
	if items[0].Get("url").String() != "https://img/1.png" {
		t.Fatalf("url attached to index 0 = %+v", items[0].Value())
	}

	// A base64-only response maps one-to-one.
	b64Only := []byte(`{"data":{"image_base64":["QQ==","Qg==","Qw=="]}}`)
	out2, err := normalizeMinimaxImageResponse(b64Only)
	if err != nil {
		t.Fatalf("normalize b64 only: %v", err)
	}
	items2 := gjson.GetBytes(out2, "data").Array()
	if len(items2) != 3 {
		t.Fatalf("want 3 images, got %d: %s", len(items2), out2)
	}
	if items2[0].Get("b64_json").String() != "QQ==" || items2[2].Get("b64_json").String() != "Qw==" {
		t.Fatalf("base64 ordering broken: %s", out2)
	}
	if items2[0].Get("url").Exists() {
		t.Fatalf("a base64-only response must not gain a url field: %s", out2)
	}

	// base_resp errors must not be reported as an empty success.
	errResp := []byte(`{"base_resp":{"status_code":1004,"status_msg":"login fail"}}`)
	if _, err = normalizeMinimaxImageResponse(errResp); err == nil {
		t.Fatal("expected an error for a provider-level failure")
	}
	if _, err = normalizeMinimaxImageResponse([]byte(`{"data":{}}`)); err == nil {
		t.Fatal("expected an error when no images are returned")
	}
	if _, err = normalizeMinimaxImageResponse([]byte(`nope`)); err == nil {
		t.Fatal("expected an error for a non-JSON body")
	}
}

func TestMinimaxImageURL(t *testing.T) {
	auth := newMinimaxTestAuth()
	if got := minimaxImageURL(auth); got != "https://api.minimax.io/v1/image_generation" {
		t.Fatalf("image URL = %q", got)
	}
	auth.Metadata["base_url"] = "https://api.minimax.cn/v1"
	if got := minimaxImageURL(auth); got != "https://api.minimax.cn/v1/image_generation" {
		t.Fatalf("image URL with /v1 base = %q", got)
	}
}

// TestExecuteMinimaxImageRoundTrip drives the full non-streaming image path
// against a local server, covering the URL, the bearer header, and the
// response normalization in one pass.
func TestExecuteMinimaxImageRoundTrip(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":0},"data":{"image_base64":["QUJD"]}}`))
	}))
	defer srv.Close()

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL

	e := NewMinimaxExecutor(nil)
	req := cliproxyexecutor.Request{
		Model:   "image-01",
		Payload: []byte(`{"model":"image-01","prompt":"a cat","response_format":"b64_json"}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}

	resp, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/v1/image_generation" {
		t.Fatalf("path = %q", gotPath)
	}
	// The image endpoint uses bearer auth, unlike the messages endpoint.
	if gotAuth != "Bearer access-token-1" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotBody["prompt"] != "a cat" {
		t.Fatalf("prompt forwarded as %v", gotBody["prompt"])
	}
	items := gjson.GetBytes(resp.Payload, "data").Array()
	if len(items) != 1 || items[0].Get("b64_json").String() != "QUJD" {
		t.Fatalf("normalized response = %s", resp.Payload)
	}
}

func TestExecuteMinimaxImageSurfacesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":1004,"status_msg":"login fail"}}`))
	}))
	defer srv.Close()

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL
	e := NewMinimaxExecutor(nil)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	req := cliproxyexecutor.Request{Model: "image-01", Payload: []byte(`{"model":"image-01","prompt":"x"}`)}

	if _, err := e.Execute(context.Background(), auth, req, opts); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestMinimaxImageRequestRequiresModel(t *testing.T) {
	e := NewMinimaxExecutor(nil)
	auth := newMinimaxTestAuth()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	if _, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{Payload: []byte(`{"prompt":"x"}`)}, opts); err == nil {
		t.Fatal("expected an error when the model is missing")
	}
}

func TestMinimaxImageStreamEmitsSingleChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"image_urls":["https://img/1.png"]}}`))
	}))
	defer srv.Close()

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL
	e := NewMinimaxExecutor(nil)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	req := cliproxyexecutor.Request{Model: "image-01", Payload: []byte(`{"model":"image-01","prompt":"x"}`)}

	stream, err := e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var chunks int
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if gjson.GetBytes(chunk.Payload, "data.0.url").String() != "https://img/1.png" {
			t.Fatalf("chunk payload = %s", chunk.Payload)
		}
		chunks++
	}
	if chunks != 1 {
		t.Fatalf("want 1 chunk, got %d", chunks)
	}
}

// TestMinimaxRefreshWritesBackMetadata ensures a refreshed token replaces the
// stored one in both metadata and typed storage.
func TestMinimaxRefreshWritesBackMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		if got := r.PostForm.Get("refresh_token"); got != "refresh-token-1" {
			t.Errorf("refresh_token = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "success",
			"access_token":  "access-token-2",
			"refresh_token": "refresh-token-2",
			"expired_in":    4102444800000,
		})
	}))
	defer srv.Close()

	client := minimaxauth.NewDeviceFlowClient(nil, minimaxauth.RegionGlobal)
	client.SetHTTPClient(srv.Client())
	client.SetEndpoints("", srv.URL+"/oauth2/token")

	auth := newMinimaxTestAuth()
	if err := applyMinimaxRefresh(context.Background(), client, auth); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := auth.Metadata["access_token"]; got != "access-token-2" {
		t.Fatalf("access_token = %v", got)
	}
	if got := auth.Metadata["refresh_token"]; got != "refresh-token-2" {
		t.Fatalf("refresh_token = %v", got)
	}
	if got := auth.Metadata["expired"]; got == nil {
		t.Fatal("expired must be written back")
	}
}

func TestMinimaxRefreshWithoutTokenIsNoop(t *testing.T) {
	e := NewMinimaxExecutor(nil)
	auth := &cliproxyauth.Auth{Provider: "minimax", Metadata: map[string]any{}}
	out, err := e.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if out != auth {
		t.Fatal("auth without a refresh token must be returned unchanged")
	}
	if _, err = e.Refresh(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil auth")
	}
}

func TestMinimaxCountTokensRejectsImageRequests(t *testing.T) {
	e := NewMinimaxExecutor(nil)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	if _, err := e.CountTokens(context.Background(), newMinimaxTestAuth(), cliproxyexecutor.Request{}, opts); err == nil {
		t.Fatal("expected an error for an image token count")
	}
}

// TestMinimaxDoesNotAffectClaudeRequests guards the isolation requirement: a
// MiniMax credential must not change how a Claude credential is sent.
func TestMinimaxDoesNotAffectClaudeRequests(t *testing.T) {
	auth := newMinimaxTestAuth()
	injectMinimaxBaseURL(auth)
	if got := auth.Attributes["base_url"]; got == "https://api.anthropic.com" {
		t.Fatal("MiniMax must never retarget the first-party Anthropic base")
	}
	if got := auth.Attributes["base_url"]; got != "https://api.minimax.io/anthropic" {
		t.Fatalf("base_url = %q", got)
	}

	// Claude traffic goes through the shared Claude executor, which must keep
	// its own header choice now that the seam exists.
	claudeAuth := &cliproxyauth.Auth{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-oat01-y"},
		Attributes: map[string]string{"base_url": "https://api.anthropic.com"},
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err = NewClaudeExecutor(nil).PrepareRequest(req, claudeAuth); err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("Claude OAuth must not gain an x-api-key header, got %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-y" {
		t.Fatalf("Claude Authorization = %q", got)
	}
}

func TestMinimaxImageHandlerTypesAlign(t *testing.T) {
	if minimaxImageHandlerType != "openai-image" {
		t.Fatalf("image handler type = %q", minimaxImageHandlerType)
	}
	if !isMinimaxImageRequest(cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}) {
		t.Fatal("image requests must be detected")
	}
	if isMinimaxImageRequest(cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}) {
		t.Fatal("text requests must not be treated as image requests")
	}
}

// TestNormalizeMinimaxImageResponseFormat locks in the translation from the
// OpenAI response_format values to the ones MiniMax accepts. Forwarding
// OpenAI's "b64_json" makes MiniMax reject the request with status 2013.
func TestNormalizeMinimaxImageResponseFormat(t *testing.T) {
	cases := map[string]string{
		"b64_json": "base64",
		"B64_JSON": "base64",
		"base64":   "base64",
		"url":      "url",
		"URL":      "url",
		"":         "base64",
		"garbage":  "base64",
	}
	for in, want := range cases {
		if got := normalizeMinimaxImageResponseFormat(in); got != want {
			t.Fatalf("normalizeMinimaxImageResponseFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildMinimaxImagePayloadUsesAcceptedFormat proves the OpenAI-facing
// request is rewritten to a value MiniMax accepts.
func TestBuildMinimaxImagePayloadUsesAcceptedFormat(t *testing.T) {
	for _, requested := range []string{"b64_json", "url", ""} {
		body := []byte(`{"model":"image-01","prompt":"x"}`)
		if requested != "" {
			body = []byte(`{"model":"image-01","prompt":"x","response_format":"` + requested + `"}`)
		}
		payload, err := buildMinimaxImagePayload(body)
		if err != nil {
			t.Fatalf("buildMinimaxImagePayload: %v", err)
		}
		got := gjson.GetBytes(payload, "response_format").String()
		if got != "base64" && got != "url" {
			t.Fatalf("requested %q produced upstream response_format %q, which MiniMax rejects", requested, got)
		}
	}
}
