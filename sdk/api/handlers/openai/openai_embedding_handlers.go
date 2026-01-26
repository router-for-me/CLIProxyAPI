// Package openai provides HTTP handlers for OpenAI API endpoints.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// embeddingModelMapping maps OpenAI embedding models to Gemini embedding models.
var embeddingModelMapping = map[string]string{
	"text-embedding-ada-002": "text-embedding-004",
	"text-embedding-3-small": "text-embedding-004",
	"text-embedding-3-large": "text-embedding-004",
	"text-embedding-004":     "text-embedding-004",
	"embedding-001":          "embedding-001",
	"gemini-embedding-001":   "gemini-embedding-001",
	"gemini-embedding-1.0":   "gemini-embedding-001", // Map to actual API model name
}

// embeddingHTTPClient is a shared HTTP client for embedding requests.
// It is safe for concurrent use and configured with reasonable timeouts.
var embeddingHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	},
}

// embeddingKeyIndex is a global counter for round-robin API key selection.
var embeddingKeyIndex uint64

// Embeddings handles the /v1/embeddings endpoint.
// It translates OpenAI embedding requests to Gemini embedContent API
// and returns responses in OpenAI-compatible format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) {
	requestedAt := time.Now()

	var req EmbeddingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request body: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 1. Parse input - OpenAI supports string or array of strings
	var inputs []string
	switch v := req.Input.(type) {
	case string:
		inputs = []string{v}
	case []interface{}:
		for _, item := range v {
			if str, ok := item.(string); ok {
				inputs = append(inputs, str)
			}
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid input format: expected string or array of strings",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	if len(inputs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Input cannot be empty",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 2. Get API key - prefer server-side config with round-robin, fall back to request header
	// This allows clients to send dummy keys while server uses real credentials
	var apiKey string
	if h.Cfg != nil && len(h.Cfg.EmbeddingAPIKeys) > 0 {
		// Round-robin key selection for load balancing across multiple accounts
		idx := atomic.AddUint64(&embeddingKeyIndex, 1) % uint64(len(h.Cfg.EmbeddingAPIKeys))
		apiKey = h.Cfg.EmbeddingAPIKeys[idx]
	}
	if apiKey == "" {
		// Fall back to request header if no server config
		apiKey = extractEmbeddingAPIKey(c)
	}
	if apiKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"message": "Missing API key. Configure gemini-api-key in server config or include it in the Authorization header",
				"type":    "authentication_error",
			},
		})
		return
	}

	// Derive usage source identifier from API key (masked for privacy)
	usageSource := "embedding"
	if len(apiKey) > 10 {
		usageSource = "key_..." + apiKey[len(apiKey)-6:]
	}

	// 3. Map model name to Gemini model
	geminiModel := "text-embedding-004" // default
	if mapped, ok := embeddingModelMapping[req.Model]; ok {
		geminiModel = mapped
	}

	// 4. Execute embedding requests concurrently with cancellation support
	respData := make([]EmbeddingData, len(inputs))
	var wg sync.WaitGroup
	var errMutex sync.Mutex
	var firstErr error
	var totalTokens int64

	// Create cancellable context - cancel all requests on first error
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	for i, text := range inputs {
		wg.Add(1)
		go func(idx int, txt string) {
			defer wg.Done()

			// Check if context is already cancelled
			select {
			case <-ctx.Done():
				return
			default:
			}

			values, err := callGeminiEmbedContentWithClient(ctx, embeddingHTTPClient, geminiModel, apiKey, txt)
			if err != nil {
				errMutex.Lock()
				if firstErr == nil {
					firstErr = err
					cancel() // Cancel other ongoing requests
				}
				errMutex.Unlock()
				return
			}

			respData[idx] = EmbeddingData{
				Object:    "embedding",
				Embedding: values,
				Index:     idx,
			}

			// Estimate token count (Gemini doesn't return token usage)
			// Using rough approximation: 1 token ≈ 4 characters
			localTokens := len(txt) / 4
			if localTokens == 0 {
				localTokens = 1
			}

			atomic.AddInt64(&totalTokens, int64(localTokens))
		}(i, text)
	}

	wg.Wait()

	// Publish usage record (whether success or failure)
	finalTokens := atomic.LoadInt64(&totalTokens)
	usageRecord := usage.Record{
		Provider:    "gemini",
		Model:       geminiModel,
		APIKey:      apiKey, // Include actual API key for proper Dashboard grouping
		Source:      usageSource,
		RequestedAt: requestedAt,
		Failed:      firstErr != nil,
		Detail: usage.Detail{
			InputTokens: finalTokens,
			TotalTokens: finalTokens,
		},
	}
	usage.PublishRecord(c.Request.Context(), usageRecord)

	if firstErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": fmt.Sprintf("Upstream error: %v", firstErr),
				"type":    "api_error",
			},
		})
		return
	}

	// 5. Build OpenAI-compatible response
	response := EmbeddingResponse{
		Object: "list",
		Data:   respData,
		Model:  req.Model,
		Usage: EmbeddingUsage{
			PromptTokens: int(totalTokens),
			TotalTokens:  int(totalTokens),
		},
	}

	c.JSON(http.StatusOK, response)
}

// extractEmbeddingAPIKey extracts the API key from the Authorization header or query parameter.
func extractEmbeddingAPIKey(c *gin.Context) string {
	// Try Authorization header first
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// Fall back to query parameter
	if key := c.Query("key"); key != "" {
		return key
	}

	// Also check x-api-key header
	if key := c.GetHeader("x-api-key"); key != "" {
		return key
	}

	return ""
}

// callGeminiEmbedContentWithClient calls the Gemini embedContent API using a shared HTTP client.
func callGeminiEmbedContentWithClient(ctx context.Context, client *http.Client, model, apiKey, text string) ([]float64, error) {
	// Build Gemini API URL
	baseURL := "https://generativelanguage.googleapis.com"
	url := fmt.Sprintf("%s/v1beta/models/%s:embedContent?key=%s", baseURL, model, apiKey)

	// Build request body
	geminiReq := GeminiEmbedContentRequest{
		Content: GeminiContent{
			Parts: []GeminiPart{{Text: text}},
		},
		TaskType: "RETRIEVAL_DOCUMENT", // Optimized for document retrieval
	}

	jsonBody, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request with context
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Execute request using shared client
	resp, err := client.Do(httpReq)
	if err != nil {
		// Check if cancelled
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for error status
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var geminiResp GeminiEmbedContentResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(geminiResp.Embedding.Values) == 0 {
		return nil, fmt.Errorf("no embedding values returned")
	}

	return geminiResp.Embedding.Values, nil
}
