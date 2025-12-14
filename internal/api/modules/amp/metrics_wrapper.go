package amp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// EXAMPLE: This shows how to modify handlers to provide metrics data
// Apply this pattern to ALL handlers that process LLM requests:
// - openaiHandlers.ChatCompletions
// - openaiHandlers.Completions
// - claudeCodeHandlers.ClaudeMessages
// - geminiHandlers.GeminiHandler
// - openaiResponsesHandlers.Responses

// MetricsMiddlewareWrapper wraps existing handlers to extract and set metrics
// This is a NON-INVASIVE approach that doesn't require modifying original handler code
func MetricsMiddlewareWrapper(originalHandler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Create a response writer wrapper to capture response
		blw := &bodyLogWriter{body: &bytes.Buffer{}, ResponseWriter: c.Writer}
		c.Writer = blw

		// Call original handler
		originalHandler(c)

		// After handler completes, extract metrics from response
		if blw.body.Len() > 0 && c.Writer.Status() == http.StatusOK {
			extractAndSetMetrics(c, blw.body.Bytes())
		}
	}
}

// bodyLogWriter captures response body for metrics extraction
type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b) // Capture for metrics
	return w.ResponseWriter.Write(b)
}

func (w *bodyLogWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// extractAndSetMetrics parses response and sets metrics in context
func extractAndSetMetrics(c *gin.Context, responseBody []byte) {
	// Determine provider from path
	provider := c.Param("provider")
	if provider == "" {
		provider = "unknown"
	}

	switch provider {
	case "openai", "groq", "cerebras", "deepseek":
		extractOpenAIMetrics(c, responseBody)
	case "anthropic":
		extractClaudeMetrics(c, responseBody)
	case "google":
		extractGeminiMetrics(c, responseBody)
	default:
		// Try OpenAI format as default
		extractOpenAIMetrics(c, responseBody)
	}
}

// OpenAI/Compatible response structure
type OpenAIResponse struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func extractOpenAIMetrics(c *gin.Context, body []byte) {
	var resp OpenAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Debugf("Failed to parse OpenAI response for metrics: %v", err)
		return
	}

	// Set metrics in context for middleware to collect
	c.Set("model", resp.Model)
	c.Set("prompt_tokens", resp.Usage.PromptTokens)
	c.Set("completion_tokens", resp.Usage.CompletionTokens)
	c.Set("total_tokens", resp.Usage.TotalTokens)
}

// Claude/Anthropic response structure
type ClaudeResponse struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func extractClaudeMetrics(c *gin.Context, body []byte) {
	var resp ClaudeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Debugf("Failed to parse Claude response for metrics: %v", err)
		return
	}

	c.Set("model", resp.Model)
	c.Set("prompt_tokens", resp.Usage.InputTokens)
	c.Set("completion_tokens", resp.Usage.OutputTokens)
	c.Set("total_tokens", resp.Usage.InputTokens+resp.Usage.OutputTokens)
}

// Gemini response structure
type GeminiResponse struct {
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func extractGeminiMetrics(c *gin.Context, body []byte) {
	var resp GeminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Debugf("Failed to parse Gemini response for metrics: %v", err)
		return
	}

	// Extract model from request path (Gemini includes model in URL)
	model := extractGeminiModelFromPath(c.Request.URL.Path)
	
	c.Set("model", model)
	c.Set("prompt_tokens", resp.UsageMetadata.PromptTokenCount)
	c.Set("completion_tokens", resp.UsageMetadata.CandidatesTokenCount)
	c.Set("total_tokens", resp.UsageMetadata.TotalTokenCount)
}

func extractGeminiModelFromPath(path string) string {
	// Path format: /api/provider/google/v1beta/models/{model}:generateContent
	// Extract model name from URL
	if idx := bytes.Index([]byte(path), []byte("/models/")); idx != -1 {
		modelPart := path[idx+8:] // Skip "/models/"
		if colonIdx := bytes.IndexByte([]byte(modelPart), ':'); colonIdx != -1 {
			return modelPart[:colonIdx]
		}
		return modelPart
	}
	return "unknown"
}

// extractModelFromRequestBody extracts model from request body
// Note: This is different from extractModelFromRequest in fallback_handlers.go
// which has signature: extractModelFromRequest(body []byte, c *gin.Context) string
func extractModelFromRequestBody(c *gin.Context) string {
	// Read request body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "unknown"
	}
	// Restore body for handler to use
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	// Parse JSON to get model
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "unknown"
	}
	return req.Model
}

// StreamingMetricsWrapper is for streaming responses (SSE)
// For streaming, set model immediately and accumulate tokens
func StreamingMetricsWrapper(originalHandler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Set model from request
		model := extractModelFromRequestBody(c)
		c.Set("model", model)
		c.Set("is_streaming", true)
		
		// Initialize token counters
		c.Set("prompt_tokens", 0)
		c.Set("completion_tokens", 0)
		c.Set("total_tokens", 0)
		
		// Call original handler (which will update tokens during streaming)
		originalHandler(c)
	}
}

// HOW TO INTEGRATE THIS INTO routes.go:
//
// Option 1: Wrap ALL handlers with metrics middleware (RECOMMENDED)
// Replace these lines in registerProviderAliases():
//
//   provider.POST("/chat/completions", fallbackHandler.WrapHandler(openaiHandlers.ChatCompletions))
//
// With:
//
//   provider.POST("/chat/completions", 
//       fallbackHandler.WrapHandler(
//           MetricsMiddlewareWrapper(openaiHandlers.ChatCompletions)))
//
// Do this for ALL POST endpoints:
// - /chat/completions
// - /completions
// - /responses
// - /messages
// - /messages/count_tokens
// - /models/:action (Gemini)

// Option 2: Apply as group middleware (ALTERNATIVE)
// Add to the beginning of registerProviderAliases():
//
//   ampProviders.Use(func(c *gin.Context) {
//       if c.Request.Method == "POST" {
//           MetricsMiddlewareWrapper(func(c *gin.Context) { c.Next() })(c)
//       } else {
//           c.Next()
//       }
//   })