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
	"github.com/tidwall/gjson"
)

func gjsonModel(body []byte) string {
	return gjson.GetBytes(body, "model").String()
}

// claudeOverridePayloadConfig builds a config that rewrites the outbound
// model field for the given requested model, exercising the real
// ApplyPayloadConfigWithRequestTracked path (not a mock), so these tests
// prove finalWireModel actually reads the finished body rather than the
// pre-override upstreamModel variable.
func claudeOverridePayloadConfig(requestedModel, overrideModel string) *config.Config {
	return &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: requestedModel, Protocol: "claude"}},
				Params: map[string]any{"model": overrideModel},
			}},
		},
	}
}

func claudeStructuredNotFoundBody(model string) string {
	return `{"type":"error","error":{"type":"not_found_error","message":"model ` + model + ` was not found"}}`
}

// TestClaudeExecutor_ExecutePayloadOverrideWireModelReflectsFinalBody drives
// the real Claude Execute path against an httptest upstream: a payload
// override rule rewrites the outbound model long after upstreamModel was
// computed, and the upstream's structured 404 names the override target. The
// wire model reported on the error's Response.Metadata must be the override
// target, not the pre-override upstreamModel.
func TestClaudeExecutor_ExecutePayloadOverrideWireModelReflectsFinalBody(t *testing.T) {
	const requestedModel = "claude-3-5-sonnet-20241022"
	const overrideModel = "claude-override-target-model"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(claudeStructuredNotFoundBody(overrideModel)))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(claudeOverridePayloadConfig(requestedModel, overrideModel))
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
		t.Fatalf("Response.Metadata[%s] = %v, want %q (finalWireModel must read the finished body, not the pre-override upstreamModel)", cliproxyexecutor.WireModelMetadataKey, resp.Metadata[cliproxyexecutor.WireModelMetadataKey], overrideModel)
	}
}

// TestClaudeExecutor_ExecuteStreamPayloadOverrideWireModelReflectsFinalBody
// is the ExecuteStream counterpart: StreamResult carries no Metadata, so the
// wire model must ride on the returned error via WireModel().
func TestClaudeExecutor_ExecuteStreamPayloadOverrideWireModelReflectsFinalBody(t *testing.T) {
	const requestedModel = "claude-3-5-sonnet-20241022"
	const overrideModel = "claude-override-target-model"

	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(claudeStructuredNotFoundBody(overrideModel)))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(claudeOverridePayloadConfig(requestedModel, overrideModel))
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
		t.Fatalf("WireModel() = %q, want %q (finalWireModel must read the finished body, not the pre-override upstreamModel)", got, overrideModel)
	}
}
