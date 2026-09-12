package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// getLiveAPIKey loads the real Command Code API key from ~/.commandcode/auth.json or env.
func getLiveAPIKey(t *testing.T) string {
	if env := strings.TrimSpace(os.Getenv("COMMANDCODE_API_KEY")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("COMMAND_CODE_API_KEY")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot find user home dir: %v", err)
	}
	authPath := filepath.Join(home, ".commandcode", "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Skipf("cannot read ~/.commandcode/auth.json: %v", err)
	}
	apiKey := gjson.GetBytes(data, "apiKey").String()
	if apiKey == "" {
		t.Skip("apiKey not found in ~/.commandcode/auth.json")
	}
	return apiKey
}

// TestLive_CLIProxyAPI_E2E_NonStream tests OpenAI chat completion via running CLIProxyAPI main server.
func TestLive_CLIProxyAPI_E2E_NonStream(t *testing.T) {
	if os.Getenv("RUN_LIVE_TESTS") != "1" {
		t.Skip("skipping live E2E test; set RUN_LIVE_TESTS=1 to run")
	}
	serverURL := os.Getenv("TEST_SERVER_URL")
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8317"
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model": "deepseek/deepseek-v4-flash",
		"messages": []map[string]string{
			{"role": "user", "content": "Reply exactly: E2E_NONSTREAM_OK"},
		},
		"max_tokens": 100,
		"stream":     false,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := os.Getenv("TEST_SERVER_KEY"); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	root := gjson.ParseBytes(body)
	content := root.Get("choices.0.message.content").String()
	t.Logf("E2E response: %s", content)

	if !strings.Contains(content, "E2E_NONSTREAM_OK") {
		t.Errorf("content does not contain E2E_NONSTREAM_OK, got: %q", content)
	}
}
