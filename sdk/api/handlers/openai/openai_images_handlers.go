// Package openai provides HTTP handlers for OpenAI API endpoints.
// This file implements the OpenAI Images API for image generation.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	constant "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIImageFormat represents the OpenAI Images API format identifier.
const OpenAIImageFormat = "openai-images"

// ImageGenerationRequest represents the OpenAI image generation request format.
type ImageGenerationRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Quality        string `json:"quality,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	Size           string `json:"size,omitempty"`
	Style          string `json:"style,omitempty"`
	User           string `json:"user,omitempty"`
}

// ImageGenerationResponse represents the OpenAI image generation response format.
type ImageGenerationResponse struct {
	Created int64       `json:"created"`
	Data    []ImageData `json:"data"`
}

// ImageData represents a single generated image.
type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// OpenAIImagesAPIHandler contains the handlers for OpenAI Images API endpoints.
type OpenAIImagesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIImagesAPIHandler creates a new OpenAI Images API handlers instance.
func NewOpenAIImagesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIImagesAPIHandler {
	return &OpenAIImagesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIImagesAPIHandler) HandlerType() string {
	return OpenAIImageFormat
}

// Models returns the image-capable models supported by this handler.
func (h *OpenAIImagesAPIHandler) Models() []map[string]any {
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// ImageGenerations handles the /v1/images/generations endpoint.
// It supports OpenAI DALL-E and Gemini Imagen models through a unified interface.
//
// Request format (OpenAI-compatible):
//
//	{
//	  "model": "dall-e-3" | "imagen-4.0-generate-001" | "gemini-2.5-flash-image",
//	  "prompt": "A white siamese cat",
//	  "n": 1,
//	  "quality": "standard" | "hd",
//	  "response_format": "url" | "b64_json",
//	  "size": "1024x1024" | "1024x1792" | "1792x1024",
//	  "style": "vivid" | "natural"
//	}
//
// Response format:
//
//	{
//	  "created": 1589478378,
//	  "data": [
//	    {
//	      "url": "https://..." | "b64_json": "base64...",
//	      "revised_prompt": "..."
//	    }
//	  ]
//	}
func (h *OpenAIImagesAPIHandler) ImageGenerations(c *gin.Context) {
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

	modelName := gjson.GetBytes(rawJSON, "model").String()
	if modelName == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "model is required",
				Type:    "invalid_request_error",
				Code:    "missing_model",
			},
		})
		return
	}

	prompt := gjson.GetBytes(rawJSON, "prompt").String()
	if prompt == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "prompt is required",
				Type:    "invalid_request_error",
				Code:    "missing_prompt",
			},
		})
		return
	}

	// Convert OpenAI Images request to provider-specific format
	providerPayload := h.convertToProviderFormat(modelName, rawJSON)

	// Determine the handler type based on model
	handlerType := h.determineHandlerType(modelName)

	// Execute the request
	c.Header("Content-Type", "application/json")
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, handlerType, modelName, providerPayload, h.GetAlt(c))
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

	// Convert provider response to OpenAI Images format
<<<<<<< HEAD
	openAIResponse := h.convertToOpenAIFormat(resp, modelName, prompt)
=======
	responseFormat := gjson.GetBytes(rawJSON, "response_format").String()
	openAIResponse := h.convertToOpenAIFormat(resp, modelName, prompt, responseFormat)
>>>>>>> archive/pr-234-head-20260223

	c.JSON(http.StatusOK, openAIResponse)
	cliCancel()
}

// convertToProviderFormat converts OpenAI Images API request to provider-specific format.
func (h *OpenAIImagesAPIHandler) convertToProviderFormat(modelName string, rawJSON []byte) []byte {
	lowerModel := modelName
	// Check if this is a Gemini/Imagen model
	if h.isGeminiImageModel(lowerModel) {
		return h.convertToGeminiFormat(rawJSON)
	}

	// For OpenAI DALL-E and other models, pass through with minimal transformation
	// The OpenAI compatibility executor handles the rest
	return rawJSON
}

// convertToGeminiFormat converts OpenAI Images request to Gemini format.
func (h *OpenAIImagesAPIHandler) convertToGeminiFormat(rawJSON []byte) []byte {
	prompt := gjson.GetBytes(rawJSON, "prompt").String()
	model := gjson.GetBytes(rawJSON, "model").String()
	n := gjson.GetBytes(rawJSON, "n").Int()
	size := gjson.GetBytes(rawJSON, "size").String()

	// Build Gemini-style request
	// Using contents format that the Gemini executors understand
	geminiReq := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]any{{"text": prompt}},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"IMAGE", "TEXT"},
		},
	}

	// Map size to aspect ratio for Gemini
	if size != "" {
		aspectRatio := h.mapSizeToAspectRatio(size)
		if aspectRatio != "" {
			geminiReq["generationConfig"].(map[string]any)["imageConfig"] = map[string]any{
				"aspectRatio": aspectRatio,
			}
		}
	}

	// Handle n (number of images) - Gemini uses sampleCount
	if n > 1 {
		geminiReq["generationConfig"].(map[string]any)["sampleCount"] = int(n)
	}

	// Set model if available
	if model != "" {
		geminiReq["model"] = model
	}

	result, err := json.Marshal(geminiReq)
	if err != nil {
		return rawJSON
	}
	return result
}

// mapSizeToAspectRatio maps OpenAI image sizes to Gemini aspect ratios.
func (h *OpenAIImagesAPIHandler) mapSizeToAspectRatio(size string) string {
	switch size {
	case "1024x1024":
		return "1:1"
	case "1792x1024":
		return "16:9"
	case "1024x1792":
		return "9:16"
	case "512x512":
		return "1:1"
	case "256x256":
		return "1:1"
	default:
		return "1:1"
	}
}

// isGeminiImageModel checks if the model is a Gemini or Imagen image model.
func (h *OpenAIImagesAPIHandler) isGeminiImageModel(model string) bool {
	lowerModel := model
	return contains(lowerModel, "imagen") ||
		contains(lowerModel, "gemini-2.5-flash-image") ||
		contains(lowerModel, "gemini-3-pro-image")
}

// determineHandlerType determines the handler type based on the model name.
func (h *OpenAIImagesAPIHandler) determineHandlerType(modelName string) string {
	lowerModel := modelName

	// Gemini/Imagen models
	if h.isGeminiImageModel(lowerModel) {
		return constant.Gemini
	}

	// Default to OpenAI for DALL-E and other models
	return constant.OpenAI
}

// convertToOpenAIFormat converts provider response to OpenAI Images API response format.
<<<<<<< HEAD
func (h *OpenAIImagesAPIHandler) convertToOpenAIFormat(resp []byte, modelName string, originalPrompt string) *ImageGenerationResponse {
=======
func (h *OpenAIImagesAPIHandler) convertToOpenAIFormat(resp []byte, modelName string, originalPrompt string, responseFormat string) *ImageGenerationResponse {
>>>>>>> archive/pr-234-head-20260223
	created := time.Now().Unix()

	// Check if this is a Gemini-style response
	if h.isGeminiImageModel(modelName) {
<<<<<<< HEAD
		return h.convertGeminiToOpenAI(resp, created, originalPrompt)
=======
		return h.convertGeminiToOpenAI(resp, created, originalPrompt, responseFormat)
>>>>>>> archive/pr-234-head-20260223
	}

	// Try to parse as OpenAI-style response directly
	var openAIResp ImageGenerationResponse
	if err := json.Unmarshal(resp, &openAIResp); err == nil && len(openAIResp.Data) > 0 {
		return &openAIResp
	}

	// Fallback: wrap raw response as b64_json
	return &ImageGenerationResponse{
		Created: created,
		Data: []ImageData{
			{
				B64JSON:       string(resp),
				RevisedPrompt: originalPrompt,
			},
		},
	}
}

// convertGeminiToOpenAI converts Gemini image response to OpenAI Images format.
<<<<<<< HEAD
func (h *OpenAIImagesAPIHandler) convertGeminiToOpenAI(resp []byte, created int64, originalPrompt string) *ImageGenerationResponse {
=======
func (h *OpenAIImagesAPIHandler) convertGeminiToOpenAI(resp []byte, created int64, originalPrompt string, responseFormat string) *ImageGenerationResponse {
>>>>>>> archive/pr-234-head-20260223
	response := &ImageGenerationResponse{
		Created: created,
		Data:    []ImageData{},
	}

	// Parse Gemini response - try candidates[].content.parts[] format
	parts := gjson.GetBytes(resp, "candidates.0.content.parts")
	if parts.Exists() && parts.IsArray() {
		for _, part := range parts.Array() {
			// Check for inlineData (base64 image)
			inlineData := part.Get("inlineData")
			if inlineData.Exists() {
				data := inlineData.Get("data").String()
				mimeType := inlineData.Get("mimeType").String()

				if data != "" {
<<<<<<< HEAD
					// Build data URL
					imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
					response.Data = append(response.Data, ImageData{
						URL:           imageURL,
						RevisedPrompt: originalPrompt,
					})
=======
					image := ImageData{
						RevisedPrompt: originalPrompt,
					}
					if responseFormat == "b64_json" {
						image.B64JSON = data
					} else {
						image.URL = fmt.Sprintf("data:%s;base64,%s", mimeType, data)
					}
					response.Data = append(response.Data, image)
>>>>>>> archive/pr-234-head-20260223
				}
			}
		}
	}

	// If no images found, return error placeholder
	if len(response.Data) == 0 {
		response.Data = append(response.Data, ImageData{
			RevisedPrompt: originalPrompt,
		})
	}

	return response
}

// contains checks if s contains substr (case-insensitive helper).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && containsSubstring(s, substr)))
}

// containsSubstring performs case-insensitive substring check.
func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := s[i+j]
			subc := substr[j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 32
			}
			if subc >= 'A' && subc <= 'Z' {
				subc += 32
			}
			if sc != subc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// WriteErrorResponse writes an error message to the response writer.
func (h *OpenAIImagesAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}

	errText := http.StatusText(status)
	if msg != nil && msg.Error != nil {
		if v := msg.Error.Error(); v != "" {
			errText = v
		}
	}

	body := handlers.BuildErrorResponseBody(status, errText)

	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Status(status)
	_, _ = c.Writer.Write(body)
}

// sjson helpers are already imported, using them for potential future extensions
var _ = sjson.Set
