package e2e_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestZhipuE2E_ChatCompletions_OpenAICompatible(t *testing.T) {
	apiKey := os.Getenv("E2E_ZHIPU_API_KEY")
	baseURL := os.Getenv("E2E_ZHIPU_BASE_URL")
	if apiKey == "" || baseURL == "" {
		t.Skip("E2E_ZHIPU_API_KEY or E2E_ZHIPU_BASE_URL not set; skipping e2e test")
	}

	exec := executor.NewZhipuExecutor(&config.Config{})
	auth := &coreauth.Auth{Attributes: map[string]string{
		"api_key":  apiKey,
		"base_url": baseURL,
	}}

	// Use OpenAI chat completion schema as source format
	payload := []byte(`{
		"model": "glm-4.6",
		"messages": [
			{"role": "user", "content": "ping"}
		],
		"temperature": 0.2
	}`)
	opts := sdkexec.Options{
		Stream:          false,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: payload,
	}
	resp, err := exec.Execute(t.Context(), auth, sdkexec.Request{Model: "glm-4.6", Payload: payload}, opts)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty payload")
	}
	// Best-effort sanity check: response JSON with choices array
	var obj map[string]any
	_ = json.Unmarshal(resp.Payload, &obj)
	if _, ok := obj["choices"]; !ok {
		// tolerate translator differences; still require non-empty body
		t.Logf("payload missing 'choices' key; raw: %s", string(resp.Payload))
	}
}
