package executor

import (
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

// geminiInteractionsOverridePayloadConfig builds a config that rewrites the
// outbound model for the given requested model on the interactions protocol.
// Unlike GeminiExecutor.Execute's main (non-interactions) path,
// executeInteractions/executeInteractionsStream call SetStringIfDifferent
// BEFORE ApplyPayloadConfigWithRequest, so a payload rule genuinely changes
// what's sent — this is the gap Codex flagged that the blanket Gemini N/A
// claim missed.
func geminiInteractionsOverridePayloadConfig(requestedModel, overrideModel string) *config.Config {
	return &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: requestedModel, Protocol: "interactions"}},
				Params: map[string]any{"model": overrideModel},
			}},
		},
	}
}

func geminiInteractionsStructuredNotFoundBody(model string) string {
	return `{"error":{"type":"not_found_error","message":"model ` + model + ` does not exist"}}`
}

func TestGeminiExecutor_ExecuteInteractionsPayloadOverrideWireModelReflectsFinalBody(t *testing.T) {
	const requestedModel = "gemini-3-flash"
	const overrideModel = "gemini-interactions-override-target"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(geminiInteractionsStructuredNotFoundBody(overrideModel)))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(geminiInteractionsOverridePayloadConfig(requestedModel, overrideModel))
	auth := &cliproxyauth.Auth{Provider: "gemini-interactions", Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"` + requestedModel + `","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatal("Execute() unexpectedly succeeded")
	}
	if got := gjsonModel(seenBody); got != overrideModel {
		t.Fatalf("upstream saw model = %q, want override %q (payload override rule did not fire, or did not route through executeInteractions; test setup is wrong)", got, overrideModel)
	}
	wire, ok := resp.Metadata[cliproxyexecutor.WireModelMetadataKey].(string)
	if !ok || wire != overrideModel {
		t.Fatalf("Response.Metadata[%s] = %v, want %q (finalWireModel must read the finished body)", cliproxyexecutor.WireModelMetadataKey, resp.Metadata[cliproxyexecutor.WireModelMetadataKey], overrideModel)
	}
	type wireModelProvider interface {
		WireModel() string
	}
	wmp, ok := err.(wireModelProvider)
	if !ok {
		t.Fatalf("Execute() error = %v (%T), want a WireModel()-carrying error", err, err)
	}
	if got := wmp.WireModel(); got != overrideModel {
		t.Fatalf("err.WireModel() = %q, want %q", got, overrideModel)
	}
}

func TestGeminiExecutor_ExecuteInteractionsStreamPayloadOverrideWireModelReflectsFinalBody(t *testing.T) {
	const requestedModel = "gemini-3-flash"
	const overrideModel = "gemini-interactions-override-target"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(geminiInteractionsStructuredNotFoundBody(overrideModel)))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(geminiInteractionsOverridePayloadConfig(requestedModel, overrideModel))
	auth := &cliproxyauth.Auth{Provider: "gemini-interactions", Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"` + requestedModel + `","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatal("ExecuteStream() unexpectedly succeeded")
	}
	if got := gjsonModel(seenBody); got != overrideModel {
		t.Fatalf("upstream saw model = %q, want override %q (payload override rule did not fire, or did not route through executeInteractionsStream; test setup is wrong)", got, overrideModel)
	}
	type wireModelProvider interface {
		WireModel() string
	}
	wmp, ok := err.(wireModelProvider)
	if !ok {
		t.Fatalf("ExecuteStream() error = %v (%T), want a WireModel()-carrying error", err, err)
	}
	if got := wmp.WireModel(); got != overrideModel {
		t.Fatalf("WireModel() = %q, want %q", got, overrideModel)
	}
}

// TestOpenAICompatExecutor_NonNotFoundErrorKeepsConcreteStatusErrType proves
// the withWireModelIfNotFound gate: any non-404 status must return exactly
// the origin/dev statusErr value (not wrapped in wireModelErr), so a direct
// type assertion elsewhere on statusErr keeps working unchanged.
func TestOpenAICompatExecutor_NonNotFoundErrorKeepsConcreteStatusErrType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("compat-test", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: payload,
	}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute() unexpectedly succeeded")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("Execute() error = %v (%T), want the exact statusErr concrete type for a non-404 response (got a wrapped type instead)", err, err)
	}
	if se.code != http.StatusTooManyRequests {
		t.Fatalf("statusErr.code = %d, want %d", se.code, http.StatusTooManyRequests)
	}
}
