package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// RemoteProbeModels handles POST /v0/management/remote-probe/models.
//
// Used by the "Manage CPA → AI Providers → Discover models" inline
// section to fetch the model list of an OpenAI-Compat upstream from the
// server side, so the browser is not blocked by CORS. The dashboard
// sends the upstream's base_url + an optional bearer token; the server
// performs GET <base_url>/v1/models with a short timeout, normalizes
// the response, and returns a list of {id, display_name, type,
// owned_by, context_length, max_completion_tokens} records.
//
// We deliberately do NOT persist anything here — this is a read-only
// preview. The Apply step uses the existing per-provider PATCH/PUT
// endpoints so the server's sanitize + persist + reload pipeline stays
// the single source of truth.
//
// Body:
//
//	{
//	  "base_url": "https://...",    // required when name is not set
//	  "api_key":  "sk-...",         // optional; ignored when name resolves
//	  "name":     "Mimo CN"         // optional OpenAI-Compat entry name
//	}
//
// When `name` matches a configured, non-disabled entry under
// cfg.OpenAICompatibility, the server resolves the per-provider
// credential from that entry's first api-key-entries[*].api-key and
// overrides body.api_key — preventing the dashboard from accidentally
// probing the upstream with the global proxy caller key (or any stale
// value). The supplied base_url also falls back to the entry's
// configured base URL when omitted. When `name` is empty or does not
// resolve, the server behaves exactly as before (body.api_key and
// body.base_url are used verbatim).
//
// Response: { "models": [ ... ] }
func (h *Handler) RemoteProbeModels(c *gin.Context) {
	var body struct {
		BaseURL string  `json:"base_url"`
		APIKey  string  `json:"api_key"`
		Name    *string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request",
				"message": err.Error(),
			},
		})
		return
	}

	// Optional resolve-by-name: when the dashboard identified a specific
	// OpenAI-Compat entry, pull the per-provider credential from
	// cfg.OpenAICompatibility instead of trusting the body's api_key.
	// The body's api_key may be empty, stale, or belong to a different
	// entry; the configured entry is the source of truth.
	if body.Name != nil {
		match := strings.TrimSpace(*body.Name)
		if match != "" {
			h.mu.Lock()
			var resolved *config.OpenAICompatibility
			for i := range h.cfg.OpenAICompatibility {
				entry := &h.cfg.OpenAICompatibility[i]
				if entry.Disabled {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(entry.Name), match) {
					resolved = entry
					break
				}
			}
			var resolvedKey string
			var resolvedBase string
			if resolved != nil {
				if len(resolved.APIKeyEntries) > 0 {
					resolvedKey = strings.TrimSpace(resolved.APIKeyEntries[0].APIKey)
				}
				resolvedBase = strings.TrimSpace(resolved.BaseURL)
			}
			h.mu.Unlock()

			if resolved == nil {
				c.JSON(http.StatusNotFound, gin.H{
					"error": gin.H{
						"type":    "entry_not_found",
						"message": fmt.Sprintf("OpenAI-Compat entry %q not found in config", match),
					},
				})
				return
			}
			log.WithFields(log.Fields{
				"name":     strings.TrimSpace(resolved.Name),
				"base_url": resolvedBase,
				"has_key":  resolvedKey != "",
			}).Debug("remote-probe resolved entry from config")
			body.APIKey = resolvedKey
			if strings.TrimSpace(body.BaseURL) == "" && resolvedBase != "" {
				body.BaseURL = resolvedBase
			}
		}
	}

	baseURL := strings.TrimSpace(body.BaseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request",
				"message": "base_url is required",
			},
		})
		return
	}
	// Defensive: only allow http/https schemes to avoid the server being
	// abused to probe arbitrary file:// or other handlers.
	if !strings.HasPrefix(strings.ToLower(baseURL), "http://") &&
		!strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request",
				"message": "base_url must start with http:// or https://",
			},
		})
		return
	}

	probeURL := baseURL + "/v1/models"
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, probeURL, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"type":    "request_create_failed",
				"message": err.Error(),
			},
		})
		return
	}
	req.Header.Set("Accept", "application/json")
	if k := strings.TrimSpace(body.APIKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}

	// 8s timeout keeps the modal responsive even if the upstream is slow.
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_unreachable",
				"message": err.Error(),
			},
		})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close remote-probe response body")
		}
	}()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_read_failed",
				"message": err.Error(),
			},
		})
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to extract a friendly error message from the upstream body
		// so the dashboard can show the operator something more useful
		// than "Upstream returned 401".
		message := fmt.Sprintf("Upstream returned %d", resp.StatusCode)
		var parsed map[string]any
		if err := json.Unmarshal(respBody, &parsed); err == nil {
			if errObj, ok := parsed["error"]; ok {
				switch e := errObj.(type) {
				case map[string]any:
					if m, ok := e["message"].(string); ok && m != "" {
						message = m
					}
				case string:
					if e != "" {
						message = e
					}
				}
			}
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_error",
				"message": message,
				"status":  resp.StatusCode,
			},
		})
		return
	}

	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_decode_failed",
				"message": err.Error(),
			},
		})
		return
	}

	models := pickRemoteModelsList(parsed)
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// pickRemoteModelsList accepts the three array shapes we have seen
// across OpenAI-Compat providers (data[], models[], result[]) and falls
// back to an empty list when the upstream returns something else.
func pickRemoteModelsList(body map[string]any) []remoteModel {
	raw := any(nil)
	if v, ok := body["data"]; ok {
		raw = v
	} else if v, ok := body["models"]; ok {
		raw = v
	} else if v, ok := body["result"]; ok {
		raw = v
	} else {
		return []remoteModel{}
	}
	arr, ok := raw.([]any)
	if !ok {
		return []remoteModel{}
	}
	out := make([]remoteModel, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := stringFromMap(obj, "id", "name", "model")
		if id == "" {
			continue
		}
		out = append(out, remoteModel{
			ID:                  id,
			DisplayName:         stringFromMap(obj, "display_name", "displayName", "name"),
			Type:                stringFromMap(obj, "type", "object"),
			OwnedBy:             stringFromMap(obj, "owned_by", "ownedBy"),
			ContextLength:       intFromMap(obj, "context_length", "contextLength"),
			MaxCompletionTokens: intFromMap(obj, "max_completion_tokens", "maxCompletionTokens"),
		})
	}
	return out
}

// stringFromMap reads the first non-empty string from a map[string]any
// under any of the given keys. Returns "" when none of the keys are
// present or every value is empty after trimming.
func stringFromMap(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// intFromMap reads the first positive integer from a map[string]any
// under any of the given keys. JSON numbers decode as float64.
func intFromMap(obj map[string]any, keys ...string) int {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			if n > 0 {
				return int(n)
			}
		case int:
			if n > 0 {
				return n
			}
		case int64:
			if n > 0 {
				return int(n)
			}
		}
	}
	return 0
}

// remoteModel is the normalized shape returned to the dashboard.
type remoteModel struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"display_name,omitempty"`
	Type                string `json:"type,omitempty"`
	OwnedBy             string `json:"owned_by,omitempty"`
	ContextLength       int    `json:"context_length,omitempty"`
	MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
}
