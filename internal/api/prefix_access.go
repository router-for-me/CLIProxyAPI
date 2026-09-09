package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
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
	liveSession := path == "/v1/live" || path == "/v1/realtime" || path == "/v1/realtime/calls"
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
			modelSeen := false
			for {
				part, errPart := reader.NextPart()
				if errPart == io.EOF {
					break
				}
				if errPart != nil {
					c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid multipart body"})
					return false
				}
				if (liveSession && part.FormName() == "session") || (!liveSession && part.FormName() == "model" && part.FileName() == "" && !modelSeen) {
					value, errValue := io.ReadAll(part)
					if errValue != nil {
						_ = part.Close()
						c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "failed to read multipart field"})
						return false
					}
					model = strings.TrimSpace(string(value))
					if liveSession {
						model = prefixLiveModel(value)
					}
					modelSeen = true
				}
				_ = part.Close()
			}
		} else if mediaType == "application/x-www-form-urlencoded" {
			form, errForm := url.ParseQuery(string(body))
			if errForm != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid form body"})
				return false
			}
			model = strings.TrimSpace(form.Get("model"))
		} else {
			model = gjson.GetBytes(body, "model").String()
			if model == "" {
				model = gjson.GetBytes(body, "session.model").String()
			}
			if liveSession {
				model = prefixLiveModel(body)
			}
		}
	}
	if path == "/v1beta/interactions" {
		model = strings.TrimSpace(model)
		if strings.HasPrefix(model, "models/") && len(model) > len("models/") {
			model = strings.TrimPrefix(model, "models/")
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

// Match Codex Live's nested-session precedence and JSON string validation.
func prefixLiveModel(body []byte) string {
	var payload struct {
		Model   string `json:"model"`
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if model := strings.TrimSpace(payload.Session.Model); model != "" {
		return model
	}
	return strings.TrimSpace(payload.Model)
}
