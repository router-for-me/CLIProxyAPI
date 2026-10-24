package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// ModelAllowed checks the public model name before provider aliases are resolved.
func (h *BaseAPIHandler) ModelAllowed(ctx context.Context, model string) bool {
	cfg := h.CurrentConfig()
	if cfg == nil {
		return true
	}
	key := usage.APIKeyFromContext(ctx)
	if key == "" {
		return true
	}
	models, restricted := cfg.APIKeyModels[key]
	if !restricted {
		return true
	}
	for _, allowed := range models {
		if strings.TrimSpace(model) == allowed {
			return true
		}
	}
	return false
}

func (h *BaseAPIHandler) filterModelListBody(ctx context.Context, body []byte) []byte {
	cfg := h.CurrentConfig()
	if cfg == nil {
		return body
	}
	if _, restricted := cfg.APIKeyModels[usage.APIKeyFromContext(ctx)]; !restricted {
		return body
	}
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		return body
	}
	for _, field := range []string{"data", "models"} {
		var entries []map[string]any
		if json.Unmarshal(catalog[field], &entries) != nil {
			continue
		}
		filtered := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			id, _ := entry["id"].(string)
			if id == "" {
				id, _ = entry["name"].(string)
				id = strings.TrimPrefix(id, "models/")
			}
			if id == "" {
				id, _ = entry["slug"].(string)
			}
			if h.ModelAllowed(ctx, id) {
				filtered = append(filtered, entry)
			}
		}
		catalog[field], _ = json.Marshal(filtered)
	}
	result, err := json.Marshal(catalog)
	if err != nil {
		return body
	}
	return result
}

func (h *BaseAPIHandler) modelAccessError(ctx context.Context, model string) *interfaces.ErrorMessage {
	if h.ModelAllowed(ctx, model) {
		return nil
	}
	return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New("model is not allowed for this API key")}
}
