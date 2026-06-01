// Package main is a minimal, runnable example of pointing an OpenCode-style client
// at CLIProxyAPI's dedicated /opencode/... route namespace.
//
// OpenCode (https://opencode.ai) is configured with a custom provider whose baseURL
// points at the proxy. This program does two things:
//
//  1. Prints a ready-to-use opencode.json provider block for the configured baseURL.
//  2. Sends a sample OpenAI Chat Completions request to /opencode/v1/chat/completions,
//     exactly as OpenCode's @ai-sdk/openai-compatible package would.
//
// No secrets are hardcoded: the proxy base URL and API key are read from the
// environment (CLIPROXY_BASE_URL, CLIPROXY_API_KEY, CLIPROXY_MODEL).
//
// Usage:
//
//	export CLIPROXY_API_KEY="<one of your proxy api-keys>"
//	export CLIPROXY_BASE_URL="http://127.0.0.1:8317"   # optional, this is the default
//	export CLIPROXY_MODEL="gpt-5"                       # optional, this is the default
//	go run ./examples/opencode-provider
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// opencodeConfig renders the opencode.json provider block for the given baseURL.
func opencodeConfig(baseURL, model string) string {
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"cliproxy": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "CLIProxyAPI",
				"options": map[string]any{
					"baseURL": strings.TrimRight(baseURL, "/") + "/opencode/v1",
					"apiKey":  "{env:CLIPROXY_API_KEY}",
				},
				"models": map[string]any{
					model: map[string]any{},
				},
			},
		},
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	return string(out)
}

func main() {
	baseURL := env("CLIPROXY_BASE_URL", "http://127.0.0.1:8317")
	apiKey := strings.TrimSpace(os.Getenv("CLIPROXY_API_KEY"))
	model := env("CLIPROXY_MODEL", "gpt-5")

	fmt.Println("# Add this provider block to your opencode.json:")
	fmt.Println(opencodeConfig(baseURL, model))
	fmt.Println()

	if apiKey == "" {
		fmt.Println("CLIPROXY_API_KEY not set; skipping live request.")
		fmt.Println("Set it to one of your proxy api-keys and re-run to exercise the endpoint.")
		return
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/opencode/v1/chat/completions"
	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Say hello from OpenCode via CLIProxyAPI."},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		fmt.Printf("build request error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("request error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("POST %s -> %d\n%s\n", endpoint, resp.StatusCode, body)
}
