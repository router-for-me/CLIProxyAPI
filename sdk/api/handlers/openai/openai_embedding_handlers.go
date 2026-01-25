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
	"time"

	"github.com/gin-gonic/gin"
)

// embeddingModelMapping maps OpenAI embedding models to Gemini embedding models.
var embeddingModelMapping = map[string]string{
	"text-embedding-ada-002":   "text-embedding-004",
	"text-embedding-3-small":   "text-embedding-004",
	"text-embedding-3-large":   "text-embedding-004",
	"text-embedding-004":       "text-embedding-004",
	"embedding-001":            "embedding-001",
}

// Embeddings handles the /v1/embeddings endpoint.
// It translates OpenAI embedding requests to Gemini embedContent API
// and returns responses in OpenAI-compatible format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) {
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

	// 2. Get API key from Authorization header
	apiKey := extractAPIKey(c)
	if apiKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"message": "Missing API key. Include it in the Authorization header as 'Bearer YOUR_API_KEY'",
				"type":    "authentication_error",
			},
		})
		return
	}

	// 3. Map model name to Gemini model
	geminiModel := "text-embedding-004" // default
	if mapped, ok := embeddingModelMapping[req.Model]; ok {
		geminiModel = mapped
	}

	// 4. Execute embedding requests concurrently
	respData := make([]EmbeddingData, len(inputs))
	var wg sync.WaitGroup
	var errMutex sync.Mutex
	var lastErr error
	var totalTokens int

	ctx := c.Request.Context()

	for i, text := range inputs {
		wg.Add(1)
		go func(idx int, txt string) {
			defer wg.Done()

			values, err := callGeminiEmbedContent(ctx, geminiModel, apiKey, txt)
			if err != nil {
				errMutex.Lock()
				lastErr = err
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

			errMutex.Lock()
			totalTokens += localTokens
			errMutex.Unlock()
		}(i, text)
	}

	wg.Wait()

	if lastErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": fmt.Sprintf("Upstream error: %v", lastErr),
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
			PromptTokens: totalTokens,
			TotalTokens:  totalTokens,
		},
	}

	c.JSON(http.StatusOK, response)
}

// extractAPIKey extracts the API key from the Authorization header or query parameter.
func extractAPIKey(c *gin.Context) string {
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

// callGeminiEmbedContent calls the Gemini embedContent API for a single text input.
func callGeminiEmbedContent(ctx context.Context, model, apiKey, text string) ([]float64, error) {
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

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Execute request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
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
