package api

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// authorizePrefixRequest checks direct protocol routes as well as shared model execution.
// Requests without a model cannot establish a namespace and fail closed for restricted keys.
func authorizePrefixRequest(c *gin.Context) bool {
	if len(sdkaccess.AllowedPrefixes(c.Request.Context())) == 0 {
		return true
	}
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if strings.Contains(c.FullPath(), ":") {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "resource-only routes are unavailable for prefix-restricted API keys"})
		return false
	}
	if c.Request.Method == http.MethodGet {
		if path == "/v1/models" || path == "/v1beta/models" {
			return true
		}
		// Responses WebSockets authorize each response.create after resolving inherited models.
		if path == "/v1/responses" || path == "/backend-api/codex/responses" {
			return true
		}
	}
	model := ""
	if strings.HasPrefix(path, "/v1beta/models/") {
		model = strings.TrimPrefix(path, "/v1beta/models/")
		model, _, _ = strings.Cut(model, ":")
	} else if c.Request.Method == http.MethodGet && path == "/v1/realtime" {
		model = c.Query("model")
	} else if c.Request.Body != nil {
		body, errRead := handlers.ReadRequestBody(c)
		if errRead != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return false
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header.Del("Content-Encoding")
		c.Request.Header.Del("Content-Length")
		mediaType, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if mediaType == "multipart/form-data" {
			reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				part, errPart := reader.NextPart()
				if errPart != nil {
					break
				}
				if part.FormName() == "model" {
					value, errValue := io.ReadAll(io.LimitReader(part, 4096))
					if errValue == nil {
						model = string(value)
					}
				}
				_ = part.Close()
			}
		} else {
			model = gjson.GetBytes(body, "model").String()
			if model == "" {
				model = gjson.GetBytes(body, "session.model").String()
			}
		}
	}
	if sdkaccess.AllowsModel(c.Request.Context(), model) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
		"message": "model is not allowed for this API key; use an authorized prefix/model ID",
		"type":    "permission_error", "code": "model_not_allowed",
	}})
	return false
}
