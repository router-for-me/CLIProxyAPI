package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type codexCLITokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

type codexCLIOAuthFile struct {
	AuthMode     string         `json:"auth_mode,omitempty"`
	OpenAIAPIKey string         `json:"OPENAI_API_KEY,omitempty"`
	Tokens       *codexCLITokens `json:"tokens,omitempty"`
	AuthMethod   string          `json:"auth_method,omitempty"`
}

func sanitizeDownloadName(index int) string {
	if index < 0 {
		return "cliproxyapi-auth.json"
	}
	return fmt.Sprintf("cliproxyapi-api-key-%d-auth.json", index+1)
}

func normalizeAPIKeyAt(keys []string, index int) string {
	if index < 0 || index >= len(keys) {
		return ""
	}
	return strings.TrimSpace(keys[index])
}

// DownloadCodexCLIOAuthFile exports a Codex CLI compatible auth.json file backed by CLIProxyAPI api-keys.
//
// This file is intentionally provider-agnostic and acts as a proxy credential for CLIProxyAPI itself:
//   - access_token carries a configured CLIProxyAPI API key
//   - id_token / refresh_token / account_id are compatibility placeholders
func (h *Handler) DownloadCodexCLIOAuthFile(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}

	if len(h.cfg.APIKeys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api-keys is empty"})
		return
	}

	index := 0
	if raw := strings.TrimSpace(c.Query("index")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "index must be integer"})
			return
		}
		index = parsed
	}

	apiKey := normalizeAPIKeyAt(h.cfg.APIKeys, index)
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api-key not found at index"})
		return
	}

	payload := codexCLIOAuthFile{
		AuthMode:     "apikey",
		OpenAIAPIKey: apiKey,
	}

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal codex cli auth payload"})
		return
	}
	raw = append(raw, '\n')

	fileName := sanitizeDownloadName(index)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
	c.Data(http.StatusOK, "application/json", raw)
}
