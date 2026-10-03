package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestAuthFileModel executes a small generation request using only the selected credential.
// POST /v0/management/auth-files/test-model requires the normal management authentication.
func (h *Handler) TestAuthFileModel(c *gin.Context) {
	var body struct {
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || strings.TrimSpace(body.AuthIndex) == "" || strings.TrimSpace(body.Model) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index and model are required"})
		return
	}
	body.Model = strings.TrimSpace(body.Model)
	auth := h.authByIndex(body.AuthIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		c.JSON(http.StatusConflict, gin.H{"error": "auth is disabled"})
		return
	}
	var model *registry.ModelInfo
	for _, candidate := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if candidate != nil && candidate.ID == body.Model {
			model = candidate
			break
		}
	}
	if model == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is not registered for this auth"})
		return
	}
	kind := authFileModelTestKind(model)
	if kind == "unsupported" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this model does not support text or image tests"})
		return
	}
	payload := map[string]any{
		"model": body.Model, "stream": false, "max_tokens": 64,
		"messages": []map[string]string{{"role": "user", "content": "Reply with OK."}},
	}
	format := translator.FormatOpenAI
	path := "/v1/chat/completions"
	if kind == "image" {
		format = translator.FromString(registry.OpenAIImageModelType)
		path = "/v1/images/generations"
		payload = map[string]any{"model": body.Model, "prompt": "A small blue circle on a plain white background.", "n": 1, "response_format": "b64_json"}
	}
	if kind == "image" && (auth.Provider == "gemini" || auth.Provider == "gemini-cli" || auth.Provider == "antigravity" || auth.Provider == "vertex") {
		format = translator.FormatOpenAI
		path = "/v1/chat/completions"
		payload = map[string]any{
			"model": body.Model, "stream": false,
			"messages":   []map[string]string{{"role": "user", "content": "Generate an image of a small blue circle on a plain white background."}},
			"modalities": []string{"text", "image"},
		}
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare model test"})
		return
	}
	opts := coreexecutor.Options{
		SourceFormat: format, OriginalRequest: raw,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Metadata: map[string]any{
			coreexecutor.PinnedAuthMetadataKey:     auth.ID,
			coreexecutor.RequestedModelMetadataKey: body.Model,
			coreexecutor.RequestPathMetadataKey:    path,
		},
	}
	if kind == "image" {
		opts.Metadata[coreexecutor.DisallowFreeAuthMetadataKey] = true
	}
	started := time.Now()
	response, errExecute := h.authManager.Execute(c.Request.Context(), []string{auth.Provider}, coreexecutor.Request{Model: body.Model, Payload: raw}, opts)
	result := gin.H{"success": false, "model": body.Model, "kind": kind, "latency_ms": time.Since(started).Milliseconds()}
	if errExecute != nil {
		// Keep upstream failures inside a successful management response: an upstream 401 must not log out the operator.
		status := clienterror.HTTPStatusFromErrorOr(errExecute, http.StatusBadGateway)
		message := http.StatusText(status)
		if message == "" {
			message = "Model request failed"
		}
		result["error"] = gin.H{"code": "upstream_error", "status_code": status, "message": message}
		c.JSON(http.StatusOK, result)
		return
	}
	text, images := authFileModelTestOutput(response.Payload, kind)
	if text == "" && images == 0 {
		result["error"] = gin.H{"code": "empty_output", "message": "Upstream returned no generated output"}
	} else {
		result["success"] = true
		if text != "" {
			result["message"] = text
		}
		if images > 0 {
			result["image_count"] = images
		}
	}
	c.JSON(http.StatusOK, result)
}

// authFileModelTestKind follows the image endpoints' supported model families.
func authFileModelTestKind(model *registry.ModelInfo) string {
	if model.Type == registry.OpenAIImageModelType {
		return "image"
	}
	for _, name := range []string{model.ID, model.Name, model.Version} {
		name = strings.ToLower(strings.TrimSpace(name))
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		switch name {
		case "gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-image-quality", "grok-imagine-image-2.0":
			return "image"
		}
		if strings.Contains(name, "video") || strings.Contains(name, "embedding") || strings.Contains(name, "whisper") || strings.Contains(name, "tts") {
			return "unsupported"
		}
	}
	for _, modality := range model.SupportedOutputModalities {
		if strings.EqualFold(modality, "IMAGE") {
			return "image"
		}
		if strings.EqualFold(modality, "VIDEO") || strings.EqualFold(modality, "AUDIO") {
			return "unsupported"
		}
	}
	return "text"
}

func authFileModelTestOutput(payload []byte, kind string) (string, int) {
	if !gjson.ValidBytes(payload) || gjson.GetBytes(payload, "error").Exists() {
		return "", 0
	}
	if kind == "image" {
		count := 0
		for _, item := range gjson.GetBytes(payload, "data").Array() {
			if strings.TrimSpace(item.Get("b64_json").String()) != "" || strings.TrimSpace(item.Get("url").String()) != "" {
				count++
			}
		}
		for _, item := range gjson.GetBytes(payload, "choices.0.message.images").Array() {
			if strings.TrimSpace(item.Get("image_url.url").String()) != "" {
				count++
			}
		}
		return "", count
	}
	content := gjson.GetBytes(payload, "choices.0.message.content")
	text := ""
	if content.Type == gjson.String {
		text = content.String()
	} else if content.IsArray() {
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				text += part.Get("text").String()
			}
		}
	}
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > 512 {
		text = string(runes[:512]) + "…"
	}
	return text, 0
}
