package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAnthropicUpstreamAuth(t *testing.T) {
	if anthropicUpstreamAuth(nil) {
		t.Fatal("nil auth must not be flagged anthropic")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"upstream_protocol": "anthropic"}}
	if !anthropicUpstreamAuth(auth) {
		t.Fatal("anthropic flag not detected")
	}
	auth = &cliproxyauth.Auth{Attributes: map[string]string{"upstream_protocol": "openai"}}
	if anthropicUpstreamAuth(auth) {
		t.Fatal("openai flag must not be treated as anthropic")
	}
}

func TestOpenAICompatExecutorDelegatesAnthropicUpstream(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	exec := NewOpenAICompatExecutor("zai", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "zai",
		ID:       "zai-start-plan-auth",
		Metadata: map[string]any{"device_mid": "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a"},
		Attributes: map[string]string{
			"api_key":           "zcode-jwt",
			"base_url":          upstream.URL + "/zcode-plan/anthropic",
			"upstream_protocol": "anthropic",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/zcode-plan/anthropic/v1/messages" {
		t.Fatalf("upstream path = %s, want /zcode-plan/anthropic/v1/messages", gotPath)
	}
	if gotAuth != "Bearer zcode-jwt" {
		t.Fatalf("upstream authorization = %q, want Bearer zcode-jwt", gotAuth)
	}
	if !strings.Contains(string(resp.Payload), `"text":"ok"`) {
		t.Fatalf("unexpected response payload: %s", resp.Payload)
	}
	blocks := gjson.GetBytes(gotBody, "system").Array()
	if len(blocks) != 2 || blocks[0].Get("text").String() != zaiStartPlanSystemBlockOne || blocks[1].Get("text").String() != zaiStartPlanSystemBlockTwo {
		t.Fatalf("non-streaming upstream body must carry the two official system blocks, got %d blocks", len(blocks))
	}
	// The official client also attaches device/session attribution metadata to
	// every zcode-plan model request.
	userID := gjson.GetBytes(gotBody, "metadata.user_id").String()
	if userID == "" {
		t.Fatalf("upstream body missing metadata.user_id: %s", gotBody)
	}
	var identity struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if errParse := json.Unmarshal([]byte(userID), &identity); errParse != nil {
		t.Fatalf("metadata.user_id is not the official JSON envelope: %v (%q)", errParse, userID)
	}
	if identity.DeviceID != "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a" {
		t.Fatalf("metadata device_id = %q, want the auth device_mid", identity.DeviceID)
	}
	if identity.SessionID == "" {
		t.Fatalf("metadata session_id is empty")
	}
}

func TestOpenAICompatExecutorAnthropicUpstreamSkipsMetadataForOtherProviders(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	exec := NewOpenAICompatExecutor("other", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "other",
		ID:       "other-auth",
		Metadata: map[string]any{"device_mid": "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a"},
		Attributes: map[string]string{
			"api_key":           "key",
			"base_url":          upstream.URL,
			"upstream_protocol": "anthropic",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "m",
		Payload: []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(gotBody, "metadata.user_id").Exists() {
		t.Fatalf("non-Z.ai anthropic upstream must not receive ZCode attribution metadata: %s", gotBody)
	}
}

func TestOpenAICompatExecutorAnthropicUpstreamInjectsMetadataOnStream(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":1}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	exec := NewOpenAICompatExecutor("zai", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "zai",
		ID:       "zai-start-plan-auth",
		Metadata: map[string]any{"device_mid": "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a"},
		Attributes: map[string]string{
			"api_key":           "zcode-jwt",
			"base_url":          upstream.URL,
			"upstream_protocol": "anthropic",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true}
	streamResult, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
	userID := gjson.GetBytes(gotBody, "metadata.user_id").String()
	if userID == "" {
		t.Fatalf("streaming upstream body missing metadata.user_id: %s", gotBody)
	}
	if !gjson.Get(userID, "device_id").Exists() || !gjson.Get(userID, "session_id").Exists() {
		t.Fatalf("metadata.user_id lacks the official device/session envelope: %q", userID)
	}
}
