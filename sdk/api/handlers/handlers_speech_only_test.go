package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const speechOnlyRouteHint = "/v1/audio/speech"

var speechOnlyModels = []string{
	"grok-tts",
	"xai/grok-tts",
	"XAI/Grok-TTS",
	"grok-tts(auto)",
	"grok-voice-tts-1.0",
	"xai/grok-voice-tts-1.0",
}

func registerSpeechOnlyTestModels(t *testing.T) {
	t.Helper()
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "test-speech-only-xai"
	modelRegistry.RegisterClient(clientID, "xai", []*registry.ModelInfo{
		{ID: "grok-tts", Created: time.Now().Unix()},
		{ID: "grok-voice-tts-1.0", Created: time.Now().Unix()},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})
}

func requireSpeechOnlyError(t *testing.T, errMsg *interfaces.ErrorMessage) {
	t.Helper()
	if errMsg == nil {
		t.Fatal("error = nil, want speech-only rejection")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
	if errMsg.Error == nil || !strings.Contains(errMsg.Error.Error(), speechOnlyRouteHint) {
		t.Fatalf("error = %+v, want message mentioning %s", errMsg.Error, speechOnlyRouteHint)
	}
}

func requireNotSpeechOnlyError(t *testing.T, errMsg *interfaces.ErrorMessage) {
	t.Helper()
	if errMsg != nil && errMsg.Error != nil && strings.Contains(errMsg.Error.Error(), speechOnlyRouteHint) {
		t.Fatalf("speech endpoint rejected speech-only model: %+v", errMsg)
	}
}

func TestGetRequestDetails_SpeechOnlyModelReturns400(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	for _, model := range speechOnlyModels {
		t.Run(model, func(t *testing.T) {
			providers, _, errMsg := handler.getRequestDetails(model)
			if errMsg == nil {
				t.Fatalf("getRequestDetails(%q) providers = %v, want speech-only rejection", model, providers)
			}
			requireSpeechOnlyError(t, errMsg)
		})
	}
}

func TestExecuteWithAuthManager_RejectsSpeechOnlyModelOnNonSpeechProtocols(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	protocols := []string{"openai", "openai-response", "claude", "gemini", "openai-image", "openai-video"}
	for _, protocol := range protocols {
		for _, model := range speechOnlyModels {
			t.Run(protocol+"/"+model, func(t *testing.T) {
				body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)

				_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), protocol, model, body, "")
				requireSpeechOnlyError(t, errMsg)

				dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), protocol, model, body, "")
				if dataChan != nil {
					t.Fatal("stream returned a data channel for a speech-only model")
				}
				requireSpeechOnlyError(t, <-errChan)

				_, _, errMsg = handler.ExecuteCountWithAuthManager(context.Background(), protocol, model, body, "")
				requireSpeechOnlyError(t, errMsg)

				_, _, errMsg = handler.ExecuteImageWithAuthManager(context.Background(), protocol, model, body, "")
				requireSpeechOnlyError(t, errMsg)
			})
		}
	}
}

func TestExecuteWithAuthManager_AllowsSpeechOnlyModelOnSpeechProtocol(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	for _, model := range speechOnlyModels {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"text":"hello","voice_id":"eve"}`)
			_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai-speech", model, body, "")
			if errMsg == nil {
				t.Fatal("expected auth selection error on empty manager, got nil")
			}
			requireNotSpeechOnlyError(t, errMsg)
		})
	}
}

func TestModelExecutionRejectsSpeechOnlyModelOutsideSpeechProtocol(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	body := []byte(`{"model":"grok-tts"}`)

	_, errMsg := handler.ExecuteProtocolWithAuthManager(context.Background(), ProtocolExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "grok-tts",
		Body:          body,
	})
	requireSpeechOnlyError(t, errMsg)

	_, errMsg = handler.ExecuteModel(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "grok-tts",
		Body:          body,
	})
	requireSpeechOnlyError(t, errMsg)

	_, errMsg = handler.ExecuteProtocolWithAuthManager(context.Background(), ProtocolExecutionRequest{
		EntryProtocol: "openai-speech",
		ExitProtocol:  "openai-speech",
		Model:         "grok-tts",
		Body:          body,
	})
	requireNotSpeechOnlyError(t, errMsg)

	_, errMsg = handler.ExecuteModel(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai-speech",
		ExitProtocol:  "openai-speech",
		Model:         "grok-tts",
		Body:          body,
	})
	requireNotSpeechOnlyError(t, errMsg)
}

func TestHandlerProvidersForExecutionRejectsSpeechOnlyModelOnProviderRoute(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	cases := []struct {
		name          string
		modelName     string
		originalModel string
		decision      modelRouteDecision
		execOptions   modelExecutionOptions
	}{
		{
			name:          "target-model",
			modelName:     "ignored",
			originalModel: "original-model",
			decision:      modelRouteDecision{Provider: "xai", Model: "grok-tts"},
		},
		{
			name:          "original-model-thinking-suffix",
			modelName:     "ignored",
			originalModel: "grok-voice-tts-1.0(auto)",
			decision:      modelRouteDecision{Provider: "xai"},
		},
		{
			name:          "forced-provider",
			modelName:     "xai/grok-tts",
			originalModel: "xai/grok-tts",
			execOptions:   modelExecutionOptions{ForcedProvider: "xai"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errMsg := handler.providersForExecution(tc.modelName, tc.originalModel, false, tc.decision, tc.execOptions)
			requireSpeechOnlyError(t, errMsg)
		})
	}
}

func TestSpeechOnlyModelsKeepImageAndVideoBehavior(t *testing.T) {
	for _, model := range speechOnlyModels {
		if isOpenAIImageOnlyModel(model) {
			t.Fatalf("isOpenAIImageOnlyModel(%q) = true, want false", model)
		}
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	if errMsg := handler.validateImageOnlyModel("grok-imagine-video", false); errMsg != nil {
		t.Fatalf("validateImageOnlyModel(video) = %+v, want nil", errMsg)
	}
	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai-video", "grok-imagine-video", []byte(`{}`), "")
	requireNotSpeechOnlyError(t, errMsg)
}

func speechOnlyChatBody(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
}

func TestSpeechOnlyModelRejectedWhenOnlyExitProtocolIsSpeech(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	for _, model := range speechOnlyModels {
		t.Run(model, func(t *testing.T) {
			body := speechOnlyChatBody(model)

			_, _, errMsg := handler.executeWithAuthManagerFormats(context.Background(), "openai", "openai-speech", model, body, "", false, modelExecutionOptions{})
			requireSpeechOnlyError(t, errMsg)

			_, errMsg = handler.ExecuteProtocolWithAuthManager(context.Background(), ProtocolExecutionRequest{
				EntryProtocol: "openai",
				ExitProtocol:  "openai-speech",
				Model:         model,
				Body:          body,
			})
			requireSpeechOnlyError(t, errMsg)
		})
	}
}

func TestSpeechOnlyModelStreamRejectedWhenOnlyExitProtocolIsSpeech(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	for _, model := range speechOnlyModels {
		t.Run(model, func(t *testing.T) {
			body := speechOnlyChatBody(model)

			dataChan, _, errChan := handler.executeStreamWithAuthManagerFormats(context.Background(), "openai", "openai-speech", model, body, "", false, modelExecutionOptions{})
			if dataChan != nil {
				t.Fatal("stream returned a data channel for a speech-only model on a chat entry")
			}
			requireSpeechOnlyError(t, <-errChan)

			_, errMsg := handler.ExecuteProtocolStreamWithAuthManager(context.Background(), ProtocolExecutionRequest{
				EntryProtocol: "openai",
				ExitProtocol:  "openai-speech",
				Model:         model,
				Body:          body,
				Stream:        true,
			})
			requireSpeechOnlyError(t, errMsg)
		})
	}
}

func TestSpeechOnlyModelAllowedWhenEntryAndExitAreSpeech(t *testing.T) {
	registerSpeechOnlyTestModels(t)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	for _, model := range speechOnlyModels {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"text":"hello","voice_id":"eve"}`)

			_, _, errMsg := handler.executeWithAuthManagerFormats(context.Background(), "openai-speech", "openai-speech", model, body, "", false, modelExecutionOptions{})
			if errMsg == nil {
				t.Fatal("expected auth selection error on empty manager, got nil")
			}
			requireNotSpeechOnlyError(t, errMsg)

			_, _, errChan := handler.executeStreamWithAuthManagerFormats(context.Background(), "openai-speech", "openai-speech", model, body, "", false, modelExecutionOptions{})
			errMsg = <-errChan
			if errMsg == nil {
				t.Fatal("expected auth selection error on empty manager, got nil")
			}
			requireNotSpeechOnlyError(t, errMsg)
		})
	}
}
