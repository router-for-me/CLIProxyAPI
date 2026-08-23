package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCredentialGroupsRouteToDistinctHTTPUpstreams(t *testing.T) {
	const (
		provider = "openai-compatibility:credential-groups-http-test"
		model    = "credential-groups-http-test-model"
	)

	var callsA atomic.Int32
	var callsB atomic.Int32
	var invalidAuthHeaders atomic.Int32
	var failA atomic.Bool

	newUpstream := func(accountID, apiKey string, calls *atomic.Int32, shouldFail *atomic.Bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			if request.URL.Path != "/chat/completions" {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			if request.Header.Get("Authorization") != "Bearer "+apiKey {
				invalidAuthHeaders.Add(1)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			if shouldFail != nil && shouldFail.Load() {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusInternalServerError)
				_, _ = writer.Write([]byte(`{"error":{"message":"simulated account failure"}}`))
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"role":"assistant","content":"` + accountID + `"}}]}`))
		}))
	}

	upstreamA := newUpstream("account-a", "upstream-key-a", &callsA, &failA)
	defer upstreamA.Close()
	upstreamB := newUpstream("account-b", "upstream-key-b", &callsB, nil)
	defer upstreamB.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 2)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, &config.Config{}))

	credentials := []*cliproxyauth.Auth{
		{
			ID:       "credential-http-a",
			Provider: provider,
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"base_url":         upstreamA.URL,
				"api_key":          "upstream-key-a",
				"credential_group": "account-a-group",
			},
		},
		{
			ID:       "credential-http-b",
			Provider: provider,
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"base_url":         upstreamB.URL,
				"api_key":          "upstream-key-b",
				"credential_group": "account-b-group",
			},
		},
	}

	modelRegistry := registry.GetGlobalRegistry()
	for _, credential := range credentials {
		modelRegistry.RegisterClient(credential.ID, provider, []*registry.ModelInfo{{ID: model, Type: provider}})
		credentialID := credential.ID
		t.Cleanup(func() { modelRegistry.UnregisterClient(credentialID) })
		if _, errRegister := manager.Register(context.Background(), credential); errRegister != nil {
			t.Fatalf("register %s: %v", credential.ID, errRegister)
		}
	}

	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"identify the upstream account"}]}`)
	execute := func(group string) (string, error) {
		response, errExecute := manager.Execute(
			context.Background(),
			[]string{provider},
			cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAI,
				OriginalRequest: payload,
				Metadata: map[string]any{
					cliproxyexecutor.CredentialGroupsMetadataKey: []string{group},
				},
			},
		)
		if errExecute != nil {
			return "", errExecute
		}
		var decoded struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if errDecode := json.Unmarshal(response.Payload, &decoded); errDecode != nil {
			t.Fatalf("decode response: %v; payload = %s", errDecode, response.Payload)
		}
		if len(decoded.Choices) != 1 {
			t.Fatalf("choices = %#v, want one choice", decoded.Choices)
		}
		return decoded.Choices[0].Message.Content, nil
	}

	accountA, errExecuteA := execute("account-a-group")
	if errExecuteA != nil {
		t.Fatalf("execute account A: %v", errExecuteA)
	}
	if accountA != "account-a" {
		t.Fatalf("account A response = %q, want account-a", accountA)
	}

	accountB, errExecuteB := execute("account-b-group")
	if errExecuteB != nil {
		t.Fatalf("execute account B: %v", errExecuteB)
	}
	if accountB != "account-b" {
		t.Fatalf("account B response = %q, want account-b", accountB)
	}
	if callsA.Load() != 1 || callsB.Load() != 1 {
		t.Fatalf("successful upstream calls = (%d, %d), want (1, 1)", callsA.Load(), callsB.Load())
	}
	if invalidAuthHeaders.Load() != 0 {
		t.Fatalf("invalid upstream authorization headers = %d, want 0", invalidAuthHeaders.Load())
	}

	failA.Store(true)
	callsBBeforeFailure := callsB.Load()
	if _, errExecuteFailure := execute("account-a-group"); errExecuteFailure == nil {
		t.Fatal("account A failure unexpectedly succeeded")
	}
	if callsB.Load() != callsBBeforeFailure {
		t.Fatalf("account A failure reached account B: calls before = %d, after = %d", callsBBeforeFailure, callsB.Load())
	}
}
