package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// drainStream reads every chunk from a StreamResult, failing the test on any
// chunk error, and returns the concatenated payload.
func drainStream(t *testing.T, streamResult *cliproxyexecutor.StreamResult) []byte {
	t.Helper()
	var buf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		buf.Write(chunk.Payload)
	}
	return buf.Bytes()
}

// TestOpenAICompatExecutor_ExecuteStreamSuccessReportsOverriddenWireModel
// covers Codex/team-lead's finding at openai_compat_executor.go:576: a
// SUCCESSFUL chat completion stream, after a payload-override rule rewrites
// the outbound model, returned a StreamResult with no Metadata at all - so
// wireModelOrSentStream fell back to the pre-override sent model. This proves
// the fix (finalWireModel(translated, baseModel) attached to the success
// StreamResult) on the happy path, not just the existing 404 coverage.
func TestOpenAICompatExecutor_ExecuteStreamSuccessReportsOverriddenWireModel(t *testing.T) {
	const requestedModel = "gpt-4o-mini"
	const overrideModel = "compat-override-target-model"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("compat-test", openaiCompatOverridePayloadConfig(requestedModel, overrideModel))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"` + requestedModel + `","messages":[{"role":"user","content":"hi"}]}`)

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: payload,
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() unexpectedly failed: %v", err)
	}
	drainStream(t, streamResult)

	if got := gjsonModel(seenBody); got != overrideModel {
		t.Fatalf("upstream saw model = %q, want override %q (payload override rule did not fire; test setup is wrong)", got, overrideModel)
	}
	wire, ok := streamResult.Metadata[cliproxyexecutor.WireModelMetadataKey].(string)
	if !ok || wire != overrideModel {
		t.Fatalf("StreamResult.Metadata[%s] = %v, want %q (finalWireModel must read the finished body on the success path)", cliproxyexecutor.WireModelMetadataKey, streamResult.Metadata[cliproxyexecutor.WireModelMetadataKey], overrideModel)
	}
}

// TestOpenAICompatExecutor_ImagesGenerationsStreamSuccessReportsWireModel
// covers Codex/team-lead's finding at openai_compat_executor.go:693: a
// successful images/generations stream returned a StreamResult with no
// Metadata, so a thinking-suffixed requested model (which prepareOpenAICompat
// -ImagesPayload strips before sending) was never distinguishable from the
// actually-sent wire model downstream.
func TestOpenAICompatExecutor_ImagesGenerationsStreamSuccessReportsWireModel(t *testing.T) {
	const requestedModel = "compat-image(high)"
	const wantWireModel = "compat-image"

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: image_generation.partial\ndata: {\"type\":\"image_generation.partial\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: []byte(`{"model":"` + requestedModel + `","prompt":"draw","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() unexpectedly failed: %v", err)
	}
	drainStream(t, streamResult)

	if got := gjsonModel(gotBody); got != wantWireModel {
		t.Fatalf("upstream saw model = %q, want %q (test setup is wrong)", got, wantWireModel)
	}
	wire, ok := streamResult.Metadata[cliproxyexecutor.WireModelMetadataKey].(string)
	if !ok || wire != wantWireModel {
		t.Fatalf("StreamResult.Metadata[%s] = %v, want %q (finalWireModel must read the finished body on the success path)", cliproxyexecutor.WireModelMetadataKey, streamResult.Metadata[cliproxyexecutor.WireModelMetadataKey], wantWireModel)
	}
}

// TestGeminiExecutor_ExecuteInteractionsStreamSuccessReportsOverriddenWireModel
// covers Codex/team-lead's finding at gemini_executor.go:627: a SUCCESSFUL
// interactions stream, after a payload-override rule rewrites the outbound
// model, returned a StreamResult with no Metadata - so the conductor could
// not tell what model was actually sent for a completed request.
func TestGeminiExecutor_ExecuteInteractionsStreamSuccessReportsOverriddenWireModel(t *testing.T) {
	const requestedModel = "gemini-3-flash"
	const overrideModel = "gemini-interactions-override-target"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed"}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(geminiInteractionsOverridePayloadConfig(requestedModel, overrideModel))
	auth := &cliproxyauth.Auth{Provider: "gemini-interactions", Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"` + requestedModel + `","messages":[{"role":"user","content":"hi"}]}`)

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() unexpectedly failed: %v", err)
	}
	drainStream(t, streamResult)

	if got := gjsonModel(seenBody); got != overrideModel {
		t.Fatalf("upstream saw model = %q, want override %q (payload override rule did not fire, or did not route through executeInteractionsStream; test setup is wrong)", got, overrideModel)
	}
	wire, ok := streamResult.Metadata[cliproxyexecutor.WireModelMetadataKey].(string)
	if !ok || wire != overrideModel {
		t.Fatalf("StreamResult.Metadata[%s] = %v, want %q (finalWireModel must read the finished body on the success path)", cliproxyexecutor.WireModelMetadataKey, streamResult.Metadata[cliproxyexecutor.WireModelMetadataKey], overrideModel)
	}
}
