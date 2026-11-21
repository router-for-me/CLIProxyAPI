package openai

import (
	"strings"

	"github.com/gin-gonic/gin"
	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
)

// IsCodexModelsCatalogRequest reports whether /v1/models should return the Codex
// client catalog shape ({"models":[{"slug","display_name",...}]}).
//
// Real Codex clients append ?client_version=... when fetching model_catalog_url.
// They also send an official Codex User-Agent (codex_cli_rs/..., Codex Desktop/,
// etc.). Either signal is enough so a bare catalog URL still works for Codex.
//
// Detection is limited to the models list route. GET /v1/models/{id} must keep
// OpenAI-shaped detail fields even when a Codex User-Agent is present, because
// unifiedModelsHandler forwards the wildcard detail route through OpenAIModels.
func IsCodexModelsCatalogRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if !isModelsListPath(c.Request.URL.Path) {
		return false
	}
	if _, ok := c.Request.URL.Query()["client_version"]; ok {
		return true
	}
	return multiagentv2.IsCodexClientUserAgent(c.GetHeader("User-Agent"))
}

// isModelsListPath reports whether path is the models list endpoint (not a
// model-detail path such as /v1/models/{id}).
func isModelsListPath(path string) bool {
	path = strings.TrimSuffix(path, "/")
	return path == "/v1/models" || path == "/models"
}

// CodexModelsCatalogClientVersion returns the client version used to filter and
// shape the Codex catalog. Prefer the client_version query (what Codex sends),
// then the Version header, then a version parsed from the User-Agent.
func CodexModelsCatalogClientVersion(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if _, ok := c.Request.URL.Query()["client_version"]; ok {
		return c.Query("client_version")
	}
	if version := strings.TrimSpace(c.GetHeader("Version")); version != "" {
		return version
	}
	return codexClientVersionFromUserAgent(c.GetHeader("User-Agent"))
}

func codexClientVersionFromUserAgent(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	for _, prefix := range []string{
		"codex_cli_rs/",
		"codex-tui/",
		"codex_exec/",
		"Codex Desktop/",
	} {
		if !strings.HasPrefix(userAgent, prefix) {
			continue
		}
		rest := userAgent[len(prefix):]
		end := len(rest)
		for i, r := range rest {
			if r == ' ' || r == '(' {
				end = i
				break
			}
		}
		return strings.TrimSpace(rest[:end])
	}
	return ""
}
