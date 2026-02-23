// Package claude provides web search handler for Kiro translator.
// This file implements the MCP API call and response handling.
package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/util"
	log "github.com/sirupsen/logrus"
)

// fallbackFpOnce and fallbackFp provide a shared fallback fingerprint
// for WebSearchHandler when no fingerprint is provided.
var (
	fallbackFpOnce sync.Once
	fallbackFp     *kiroauth.Fingerprint
)

// WebSearchHandler handles web search requests via Kiro MCP API
type WebSearchHandler struct {
	McpEndpoint string
	HTTPClient  *http.Client
	AuthToken   string
	Fingerprint *kiroauth.Fingerprint // optional, for dynamic headers
	AuthAttrs   map[string]string     // optional, for custom headers from auth.Attributes
}

// NewWebSearchHandler creates a new WebSearchHandler.
// If httpClient is nil, a default client with 30s timeout is used.
// If fingerprint is nil, a random one-off fingerprint is generated.
// Pass a shared pooled client (e.g. from getKiroPooledHTTPClient) for connection reuse.
func NewWebSearchHandler(mcpEndpoint, authToken string, httpClient *http.Client, fp *kiroauth.Fingerprint, authAttrs map[string]string) *WebSearchHandler {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	if fp == nil {
		// Use a shared fallback fingerprint for callers without token context
		fallbackFpOnce.Do(func() {
			mgr := kiroauth.NewFingerprintManager()
			fallbackFp = mgr.GetFingerprint("mcp-fallback")
		})
		fp = fallbackFp
	}
	return &WebSearchHandler{
		McpEndpoint: mcpEndpoint,
		HTTPClient:  httpClient,
		AuthToken:   authToken,
		Fingerprint: fp,
		AuthAttrs:   authAttrs,
	}
}

// setMcpHeaders sets standard MCP API headers on the request,
// aligned with the GAR request pattern in kiro_executor.go.
func (h *WebSearchHandler) setMcpHeaders(req *http.Request) {
	fp := h.Fingerprint

	// 1. Content-Type & Accept (aligned with GAR)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	// 2. Kiro-specific headers (aligned with GAR)
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")

	// 3. Dynamic fingerprint headers
	req.Header.Set("User-Agent", fp.BuildUserAgent())
	req.Header.Set("X-Amz-User-Agent", fp.BuildAmzUserAgent())

	// 4. AWS SDK identifiers (casing aligned with GAR)
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

	// 5. Authentication
	req.Header.Set("Authorization", "Bearer "+h.AuthToken)

	// 6. Custom headers from auth attributes
	util.ApplyCustomHeadersFromAttrs(req, h.AuthAttrs)
}

// mcpMaxRetries is the maximum number of retries for MCP API calls.
const mcpMaxRetries = 2

// CallMcpAPI calls the Kiro MCP API with the given request.
// Includes retry logic with exponential backoff for retryable errors,
// aligned with the GAR request retry pattern.
func (h *WebSearchHandler) CallMcpAPI(request *McpRequest) (*McpResponse, error) {
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal MCP request: %w", err)
	}
	log.Debugf("kiro/websearch MCP request → %s (%d bytes)", h.McpEndpoint, len(requestBody))

	var lastErr error
	for attempt := 0; attempt <= mcpMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * time.Second
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
			log.Warnf("kiro/websearch: MCP retry %d/%d after %v (last error: %v)", attempt, mcpMaxRetries, backoff, lastErr)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest("POST", h.McpEndpoint, bytes.NewReader(requestBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}

		h.setMcpHeaders(req)

		resp, err := h.HTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("MCP API request failed: %w", err)
			continue // network error → retry
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read MCP response: %w", err)
			continue // read error → retry
		}
		log.Debugf("kiro/websearch MCP response ← [%d] (%d bytes)", resp.StatusCode, len(body))

		// Retryable HTTP status codes (aligned with GAR: 502, 503, 504)
		if resp.StatusCode >= 502 && resp.StatusCode <= 504 {
			lastErr = fmt.Errorf("MCP API returned retryable status %d: %s", resp.StatusCode, string(body))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("MCP API returned status %d: %s", resp.StatusCode, string(body))
		}

		var mcpResponse McpResponse
		if err := json.Unmarshal(body, &mcpResponse); err != nil {
			return nil, fmt.Errorf("failed to parse MCP response: %w", err)
		}

		if mcpResponse.Error != nil {
			code := -1
			if mcpResponse.Error.Code != nil {
				code = *mcpResponse.Error.Code
			}
			msg := "Unknown error"
			if mcpResponse.Error.Message != nil {
				msg = *mcpResponse.Error.Message
			}
			return nil, fmt.Errorf("MCP error %d: %s", code, msg)
		}

		return &mcpResponse, nil
	}

	return nil, lastErr
}
