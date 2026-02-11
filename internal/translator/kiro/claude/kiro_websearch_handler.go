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
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// Cached web_search tool description fetched from MCP tools/list.
// Uses atomic.Pointer[sync.Once] for lock-free reads with retry-on-failure:
// - sync.Once prevents race conditions and deduplicates concurrent calls
// - On failure, a fresh sync.Once is swapped in to allow retry on next call
// - On success, sync.Once stays "done" forever — zero overhead for subsequent calls
var (
	cachedToolDescription atomic.Value // stores string
	toolDescOnce          atomic.Pointer[sync.Once]
	fallbackFpOnce        sync.Once
	fallbackFp            *kiroauth.Fingerprint
)

func init() {
	toolDescOnce.Store(&sync.Once{})
}

// FetchToolDescription calls MCP tools/list to get the web_search tool description
// and caches it. Safe to call concurrently — only one goroutine fetches at a time.
// If the fetch fails, subsequent calls will retry. On success, no further fetches occur.
// The httpClient parameter allows reusing a shared pooled HTTP client.
func FetchToolDescription(mcpEndpoint, authToken string, httpClient *http.Client, fp *kiroauth.Fingerprint, authAttrs map[string]string) {
	toolDescOnce.Load().Do(func() {
		handler := NewWebSearchHandler(mcpEndpoint, authToken, httpClient, fp, authAttrs)
		reqBody := []byte(`{"id":"tools_list","jsonrpc":"2.0","method":"tools/list"}`)
		log.Debugf("kiro/websearch MCP tools/list request: %d bytes", len(reqBody))

		req, err := http.NewRequest("POST", mcpEndpoint, bytes.NewReader(reqBody))
		if err != nil {
			log.Warnf("kiro/websearch: failed to create tools/list request: %v", err)
			toolDescOnce.Store(&sync.Once{}) // allow retry
			return
		}

		// Reuse same headers as CallMcpAPI
		handler.setMcpHeaders(req)

		resp, err := handler.HTTPClient.Do(req)
		if err != nil {
			log.Warnf("kiro/websearch: tools/list request failed: %v", err)
			toolDescOnce.Store(&sync.Once{}) // allow retry
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			log.Warnf("kiro/websearch: tools/list returned status %d", resp.StatusCode)
			toolDescOnce.Store(&sync.Once{}) // allow retry
			return
		}
		log.Debugf("kiro/websearch MCP tools/list response: [%d] %d bytes", resp.StatusCode, len(body))

		// Parse: {"result":{"tools":[{"name":"web_search","description":"..."}]}}
		var result struct {
			Result *struct {
				Tools []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.Result == nil {
			log.Warnf("kiro/websearch: failed to parse tools/list response")
			toolDescOnce.Store(&sync.Once{}) // allow retry
			return
		}

		for _, tool := range result.Result.Tools {
			if tool.Name == "web_search" && tool.Description != "" {
				cachedToolDescription.Store(tool.Description)
				log.Infof("kiro/websearch: cached web_search description from tools/list (%d bytes)", len(tool.Description))
				return // success — sync.Once stays "done", no more fetches
			}
		}

		// web_search tool not found in response
		toolDescOnce.Store(&sync.Once{}) // allow retry
	})
}

// GetWebSearchDescription returns the cached web_search tool description,
// or empty string if not yet fetched. Lock-free via atomic.Value.
func GetWebSearchDescription() string {
	if v := cachedToolDescription.Load(); v != nil {
		return v.(string)
	}
	return ""
}

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
		resp.Body.Close()
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

// ParseSearchResults extracts WebSearchResults from MCP response
func ParseSearchResults(response *McpResponse) *WebSearchResults {
	if response == nil || response.Result == nil || len(response.Result.Content) == 0 {
		return nil
	}

	content := response.Result.Content[0]
	if content.ContentType != "text" {
		return nil
	}

	var results WebSearchResults
	if err := json.Unmarshal([]byte(content.Text), &results); err != nil {
		log.Warnf("kiro/websearch: failed to parse search results: %v", err)
		return nil
	}

	return &results
}
