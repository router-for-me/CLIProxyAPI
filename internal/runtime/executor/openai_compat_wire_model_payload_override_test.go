package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// openaiCompatOverridePayloadConfig builds a config that rewrites the
// outbound model field for the given requested model, exercising the real
// ApplyPayloadConfigWithRequest path. Unlike Claude/Kimi, OpenAICompatExecutor
// never overwrites the model field again after applying payload rules, so
// this is the executor Codex flagged as never reporting a wire model at all.
func openaiCompatOverridePayloadConfig(requestedModel, overrideModel string) *config.Config {
	return &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: requestedModel, Protocol: "openai"}},
				Params: map[string]any{"model": overrideModel},
			}},
		},
	}
}

func openaiCompatStructuredNotFoundBody(model string) string {
	return `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model ` + model + ` does not exist"}}`
}

func TestOpenAICompatExecutor_ExecutePayloadOverrideWireModelReflectsFinalBody(t *testing.T) {
	const requestedModel = "gpt-4o-mini"
	const overrideModel = "compat-override-target-model"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(openaiCompatStructuredNotFoundBody(overrideModel)))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("compat-test", openaiCompatOverridePayloadConfig(requestedModel, overrideModel))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"` + requestedModel + `","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: payload,
	}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute() unexpectedly succeeded")
	}
	if got := gjsonModel(seenBody); got != overrideModel {
		t.Fatalf("upstream saw model = %q, want override %q (payload override rule did not fire; test setup is wrong)", got, overrideModel)
	}
	wire, ok := resp.Metadata[cliproxyexecutor.WireModelMetadataKey].(string)
	if !ok || wire != overrideModel {
		t.Fatalf("Response.Metadata[%s] = %v, want %q (finalWireModel must read the finished body)", cliproxyexecutor.WireModelMetadataKey, resp.Metadata[cliproxyexecutor.WireModelMetadataKey], overrideModel)
	}
	type wireModelProvider interface {
		WireModel() string
	}
	if wmp, ok := err.(wireModelProvider); ok {
		if got := wmp.WireModel(); got != overrideModel {
			t.Fatalf("err.WireModel() = %q, want %q", got, overrideModel)
		}
	} else {
		t.Fatalf("Execute() error = %v (%T), want a WireModel()-carrying error", err, err)
	}
}

func TestOpenAICompatExecutor_ExecuteStreamPayloadOverrideWireModelReflectsFinalBody(t *testing.T) {
	const requestedModel = "gpt-4o-mini"
	const overrideModel = "compat-override-target-model"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(openaiCompatStructuredNotFoundBody(overrideModel)))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("compat-test", openaiCompatOverridePayloadConfig(requestedModel, overrideModel))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"` + requestedModel + `","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: payload,
	}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("ExecuteStream() unexpectedly succeeded")
	}
	if got := gjsonModel(seenBody); got != overrideModel {
		t.Fatalf("upstream saw model = %q, want override %q (payload override rule did not fire; test setup is wrong)", got, overrideModel)
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
