// Package azureopenai provides HTTP handlers for Azure OpenAI client-facing endpoints.
package azureopenai

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type AzureOpenAIAPIHandler struct {
	openAIHandler *openai.OpenAIAPIHandler
}

func NewAzureOpenAIAPIHandler(apiHandlers *handlers.BaseAPIHandler) *AzureOpenAIAPIHandler {
	return &AzureOpenAIAPIHandler{openAIHandler: openai.NewOpenAIAPIHandler(apiHandlers)}
}

func (h *AzureOpenAIAPIHandler) ChatCompletions(c *gin.Context) {
	h.openAIHandler.ChatCompletions(c)
}

func (h *AzureOpenAIAPIHandler) DeploymentChatCompletions(c *gin.Context) {
	deployment := strings.TrimSpace(c.Param("deployment"))
	if deployment == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "Missing Azure OpenAI deployment", Type: "invalid_request_error"}})
		return
	}

	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: fmt.Sprintf("Invalid request: %v", err), Type: "invalid_request_error"}})
		return
	}
	if !gjson.ValidBytes(rawJSON) || !gjson.ParseBytes(rawJSON).IsObject() {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "Azure OpenAI chat completions request body must be a JSON object", Type: "invalid_request_error"}})
		return
	}

	updatedJSON, err := sjson.SetBytes(rawJSON, "model", deployment)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: fmt.Sprintf("Invalid request JSON: %v", err), Type: "invalid_request_error"}})
		return
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(updatedJSON))
	c.Request.ContentLength = int64(len(updatedJSON))
	c.Request.Header.Del("Content-Encoding")
	h.openAIHandler.ChatCompletions(c)
}
