package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestHostPayloadFinalizeAppliesConfiguredRulesForExplicitProtocol(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "copilot-model", Protocol: "openai-response", FromProtocol: "claude", Headers: map[string]string{"X-Test": "yes"}}},
		Params: map[string]any{"max_output_tokens": 321},
	}}}}
	prepared := preparedExecutorCall{
		req: coreexecutor.Request{
			Model:   "copilot-model",
			Payload: []byte(`{"model":"copilot-model"}`),
			Format:  sdktranslator.FormatOpenAIResponse,
		},
		opts: coreexecutor.Options{
			Headers:      http.Header{"X-Test": {"yes"}},
			SourceFormat: sdktranslator.FormatOpenAI,
		},
		inputRequested: sdktranslator.FormatClaude,
		inputFormat:    sdktranslator.FormatOpenAI,
	}
	state := newHostPayloadFinalizationContext(cfg, "copilot", prepared)
	host := New()
	callbackID, closeCallback := host.openCallbackContextForPlugin(withHostPayloadFinalization(context.Background(), state), "copilot")
	defer closeCallback()

	request := rpcHostPayloadFinalizeRequest{
		HostCallbackID: callbackID,
		Protocol:       "openai-response",
		Body:           []byte(`{"model":"copilot-model","input":"hello"}`),
	}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	call := func() []byte {
		rawResponse, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostPayloadFinalize, rawRequest)
		if errCall != nil {
			t.Fatalf("host payload callback error = %v", errCall)
		}
		response, errDecode := decodeRPCEnvelope[rpcHostPayloadFinalizeResponse](rawResponse)
		if errDecode != nil {
			t.Fatalf("decode payload callback response: %v", errDecode)
		}
		return response.Body
	}

	finalized := call()
	if got := gjson.GetBytes(finalized, "max_output_tokens").Int(); got != 321 {
		t.Fatalf("max_output_tokens = %d, want 321 in %s", got, finalized)
	}
	if repeated := call(); !bytes.Equal(repeated, finalized) {
		t.Fatalf("repeated finalization = %s, want cached %s", repeated, finalized)
	}
	if len(state.results) != 1 {
		t.Fatalf("finalization cache entries = %d, want one for repeated protocol/body", len(state.results))
	}

	request.Protocol = "claude"
	rawRequest, errMarshal = json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawResponse, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostPayloadFinalize, rawRequest)
	if errCall != nil {
		t.Fatalf("protocol-specific host payload callback error = %v", errCall)
	}
	response, errDecode := decodeRPCEnvelope[rpcHostPayloadFinalizeResponse](rawResponse)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if gjson.GetBytes(response.Body, "max_output_tokens").Exists() {
		t.Fatalf("rule for openai-response applied to claude protocol: %s", response.Body)
	}
	if len(state.results) != 2 {
		t.Fatalf("finalization cache entries = %d, want one per protocol/body", len(state.results))
	}
}

func TestHostPayloadFinalizeRequiresExecutorCallbackContext(t *testing.T) {
	host := New()
	request, errMarshal := json.Marshal(rpcHostPayloadFinalizeRequest{Protocol: "openai", Body: []byte(`{"model":"m"}`)})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostPayloadFinalize, request); errCall == nil {
		t.Fatal("host payload finalization succeeded without a callback context")
	}

	callbackID, closeCallback := host.openCallbackContext(context.Background())
	defer closeCallback()
	request, errMarshal = json.Marshal(rpcHostPayloadFinalizeRequest{HostCallbackID: callbackID, Protocol: "openai", Body: []byte(`{"model":"m"}`)})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostPayloadFinalize, request); errCall == nil {
		t.Fatal("host payload finalization succeeded without an execution finalizer")
	}
}

func TestHostHTTPDoDoesNotTreatCredentialRequestsAsModelPayloads(t *testing.T) {
	body := []byte(`{"model":"copilot-model","max_output_tokens":64}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("generic host HTTP request body = %s, want unchanged %s", got, body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	state := newHostPayloadFinalizationContext(&config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "copilot-model"}},
		Params: map[string]any{"max_output_tokens": 321},
	}}}}, "copilot", preparedExecutorCall{req: coreexecutor.Request{Model: "copilot-model", Payload: body}})
	client := New().newHTTPClient(nil)
	_, errDo := client.Do(withHostPayloadFinalization(context.Background(), state), pluginapi.HTTPRequest{Method: http.MethodPost, URL: server.URL, Body: bytes.Clone(body)})
	if errDo != nil {
		t.Fatalf("generic host HTTP request error = %v", errDo)
	}
}
