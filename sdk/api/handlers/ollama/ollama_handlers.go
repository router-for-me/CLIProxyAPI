// Package ollama provides HTTP handlers for Ollama API endpoints.
// This package implements the Ollama-compatible API interface, including model listing,
// chat completion, and generate functionality. It supports both streaming and non-streaming responses.
package ollama

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/from_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

const (
	OllamaVersion = "0.12.10"
)

// OllamaAPIHandler contains the handlers for Ollama API endpoints.
type OllamaAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOllamaAPIHandler creates a new Ollama API handlers instance.
func NewOllamaAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OllamaAPIHandler {
	return &OllamaAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OllamaAPIHandler) HandlerType() string {
	return constant.Ollama
}

// Models returns a list of supported models for this API handler.
func (h *OllamaAPIHandler) Models() []map[string]any {
	// Get all available models from registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// Version handles the /api/version endpoint.
func (h *OllamaAPIHandler) Version(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))
	c.JSON(http.StatusOK, gin.H{
		"version": OllamaVersion,
	})
}

// Tags handles the /api/tags endpoint (list models).
func (h *OllamaAPIHandler) Tags(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get all available models from registry
	modelRegistry := registry.GetGlobalRegistry()
	allModels := modelRegistry.GetAvailableModels("openai")

	// Convert to Ollama format
	ollamaModels := make([]map[string]interface{}, 0)
	for _, model := range allModels {
		modelID := ""
		if id, ok := model["id"].(string); ok {
			modelID = id
		} else if idVal := model["id"]; idVal != nil {
			modelID = fmt.Sprintf("%v", idVal)
		}

		if modelID == "" {
			continue
		}

		// Remove "models/" prefix if present
		modelID = strings.TrimPrefix(modelID, "models/")

		ollamaModels = append(ollamaModels, map[string]interface{}{
			"name":        modelID,
			"model":       modelID,
			"modified_at": time.Now().UTC().Format(time.RFC3339),
			"size":        0,
			"digest":      "",
			"details": map[string]interface{}{
				"parent_model":       "",
				"format":             "gguf",
				"family":             "Ollama",
				"families":           []string{"Ollama"},
				"parameter_size":     "0B",
				"quantization_level": "Q4_0",
			},
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"models": ollamaModels,
	})
}

// Show handles the /api/show endpoint (model information).
func (h *OllamaAPIHandler) Show(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	var requestBody map[string]interface{}
	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	modelName := ""
	if name, ok := requestBody["name"].(string); ok {
		modelName = name
	} else if name, ok := requestBody["model"].(string); ok {
		modelName = name
	}

	if modelName == "" {
		modelName = "unknown"
	}

	// Generate Ollama show response
	showResponse := from_ir.ToOllamaShowResponse(modelName)
	c.Data(http.StatusOK, "application/json", showResponse)
}

// Chat handles the /api/chat endpoint.
func (h *OllamaAPIHandler) Chat(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Parse Ollama request
	ollamaRequest := gjson.ParseBytes(rawJSON)
	stream := ollamaRequest.Get("stream").Bool()

	// Extract model name
	modelName := ollamaRequest.Get("model").String()
	if modelName == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "model is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Convert Ollama request to OpenAI format using new IR translator
	irReq, err := to_ir.ParseOllamaRequest(rawJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Failed to parse request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	
	// Normalize model name for upstream request (remove prefix like "[Cline] ")
	// but keep original modelName for routing
	cleanModelName := util.NormalizeIncomingModelID(modelName)
	irReq.Model = cleanModelName

	openaiRequest, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert request: %v", err),
				Type:    "server_error",
			},
		})
		return
	}

	if stream {
		h.handleOllamaChatStream(c, openaiRequest, modelName)
	} else {
		h.handleOllamaChatNonStream(c, openaiRequest, modelName)
	}
}

// Generate handles the /api/generate endpoint.
func (h *OllamaAPIHandler) Generate(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Parse Ollama request
	ollamaRequest := gjson.ParseBytes(rawJSON)
	stream := ollamaRequest.Get("stream").Bool()

	// Extract model name
	modelName := ollamaRequest.Get("model").String()
	if modelName == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "model is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Convert Ollama request to OpenAI format using new IR translator
	irReq, err := to_ir.ParseOllamaRequest(rawJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Failed to parse request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	
	// Normalize model name for upstream request (remove prefix like "[Cline] ")
	// but keep original modelName for routing
	cleanModelName := util.NormalizeIncomingModelID(modelName)
	irReq.Model = cleanModelName

	openaiRequest, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert request: %v", err),
				Type:    "server_error",
			},
		})
		return
	}

	if stream {
		h.handleOllamaGenerateStream(c, openaiRequest, modelName)
	} else {
		h.handleOllamaGenerateNonStream(c, openaiRequest, modelName)
	}
}

// handleOllamaChatStream handles streaming chat responses
func (h *OllamaAPIHandler) handleOllamaChatStream(c *gin.Context, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Transfer-Encoding", "chunked")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get the http.Flusher interface to manually flush the response
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer func() {
		cliCancel(nil)
	}()

	// Execute streaming request using Ollama handler type (ensures new translator is used)
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, constant.Ollama, modelName, openaiRequest, h.GetAlt(c))

	// Process streaming chunks
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream ended, send final chunk with done: true
				finalChunk, _ := from_ir.OpenAIChunkToOllamaChat([]byte("[DONE]"), modelName)
				if len(finalChunk) > 0 {
					c.Writer.Write(finalChunk)
					flusher.Flush()
				}
				return
			}

			// Convert OpenAI chunk to Ollama format
			// Remove "data: " prefix if present (SSE format)
			chunkData := chunk
			if bytes.HasPrefix(chunkData, []byte("data: ")) {
				chunkData = bytes.TrimSpace(chunkData[6:])
			}

			// Skip empty chunks or [DONE] markers (we'll handle [DONE] separately)
			if len(chunkData) == 0 || bytes.Equal(chunkData, []byte("[DONE]")) {
				continue
			}

			ollamaChunk, err := from_ir.OpenAIChunkToOllamaChat(chunkData, modelName)
			if err == nil && len(ollamaChunk) > 0 {
				c.Writer.Write(ollamaChunk)
				flusher.Flush()
			}
		case errMsg, ok := <-errChan:
			if !ok {
				continue
			}
			if errMsg != nil {
				h.WriteErrorResponse(c, errMsg)
				flusher.Flush()
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cliCancel(execErr)
			return
		}
	}
}

// handleOllamaChatNonStream handles non-streaming chat responses
func (h *OllamaAPIHandler) handleOllamaChatNonStream(c *gin.Context, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer func() {
		cliCancel(nil)
	}()

	// Execute non-streaming request using Ollama handler type
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, constant.Ollama, modelName, openaiRequest, h.GetAlt(c))
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Convert OpenAI response to Ollama format
	ollamaResponse, err := from_ir.OpenAIToOllamaChat(resp, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert response: %v", err),
				Type:    "server_error",
			},
		})
		cliCancel(err)
		return
	}

	c.Data(http.StatusOK, "application/json", ollamaResponse)
	cliCancel()
}

// handleOllamaGenerateStream handles streaming generate responses
func (h *OllamaAPIHandler) handleOllamaGenerateStream(c *gin.Context, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Transfer-Encoding", "chunked")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get the http.Flusher interface to manually flush the response
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer func() {
		cliCancel(nil)
	}()

	// Execute streaming request using Ollama handler type
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, constant.Ollama, modelName, openaiRequest, h.GetAlt(c))

	// Process streaming chunks
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream ended, send final chunk with done: true
				finalChunk, _ := from_ir.OpenAIChunkToOllamaGenerate([]byte("[DONE]"), modelName)
				if len(finalChunk) > 0 {
					c.Writer.Write(finalChunk)
					flusher.Flush()
				}
				return
			}

			// Convert OpenAI chunk to Ollama format
			// Remove "data: " prefix if present (SSE format)
			chunkData := chunk
			if bytes.HasPrefix(chunkData, []byte("data: ")) {
				chunkData = bytes.TrimSpace(chunkData[6:])
			}

			// Skip empty chunks or [DONE] markers (we'll handle [DONE] separately)
			if len(chunkData) == 0 || bytes.Equal(chunkData, []byte("[DONE]")) {
				continue
			}

			ollamaChunk, err := from_ir.OpenAIChunkToOllamaGenerate(chunkData, modelName)
			if err == nil && len(ollamaChunk) > 0 {
				c.Writer.Write(ollamaChunk)
				flusher.Flush()
			}
		case errMsg, ok := <-errChan:
			if !ok {
				continue
			}
			if errMsg != nil {
				h.WriteErrorResponse(c, errMsg)
				flusher.Flush()
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cliCancel(execErr)
			return
		}
	}
}

// handleOllamaGenerateNonStream handles non-streaming generate responses
func (h *OllamaAPIHandler) handleOllamaGenerateNonStream(c *gin.Context, openaiRequest []byte, modelName string) {
	c.Header("Content-Type", "application/json")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Server", fmt.Sprintf("ollama/%s", OllamaVersion))

	// Get context with cancel
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer func() {
		cliCancel(nil)
	}()

	// Execute non-streaming request using Ollama handler type
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, constant.Ollama, modelName, openaiRequest, h.GetAlt(c))
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Convert OpenAI response to Ollama generate format
	ollamaResponse, err := from_ir.OpenAIToOllamaGenerate(resp, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Failed to convert response: %v", err),
				Type:    "server_error",
			},
		})
		cliCancel(err)
		return
	}

	c.Data(http.StatusOK, "application/json", ollamaResponse)
	cliCancel()
}
