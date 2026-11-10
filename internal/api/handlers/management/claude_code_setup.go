package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const (
	claudeCodeSettingsFile      = "settings.json"
	claudeCodeMCPFile           = ".claude.json"
	claudeCodeExaMCPURL         = "https://mcp.exa.ai/mcp"
	claudeCodeFallbackAuthToken = "sk-cli-proxy-api"
)

var (
	claudeCodeUserHomeDir      = os.UserHomeDir
	claudeCodeLookPath         = exec.LookPath
	claudeCodeTrailingCommaRE  = regexp.MustCompile(`,(\s*[}\]])`)
	claudeCodeManagedEnvFields = []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
	}
)

type claudeCodeSettingsPayload struct {
	Env            map[string]string `json:"env"`
	ExaMCPEnabled  *bool             `json:"exaMcpEnabled"`
	CCFilterNaming *bool             `json:"ccFilterNaming"`
}

type claudeCodeModelOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Family      string `json:"family,omitempty"`
	Source      string `json:"source,omitempty"`
}

func (h *Handler) GetClaudeCodeSettings(c *gin.Context) {
	settingsPath, errPath := claudeCodeSettingsPath()
	if errPath != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "home_dir_failed", "message": errPath.Error()})
		return
	}
	claudeJSONPath, errJSONPath := claudeCodeJSONPath()
	if errJSONPath != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "home_dir_failed", "message": errJSONPath.Error()})
		return
	}

	settings, _ := readClaudeCodeJSONMap(settingsPath)
	claudeJSON, _ := readClaudeCodeJSONMap(claudeJSONPath)
	env := claudeCodeEnvMap(settings)
	models, defaults := h.claudeCodeModelOptions()
	if env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "" {
		defaults["opus"] = env["ANTHROPIC_DEFAULT_OPUS_MODEL"]
	}
	if env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "" {
		defaults["sonnet"] = env["ANTHROPIC_DEFAULT_SONNET_MODEL"]
	}
	if env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "" {
		defaults["haiku"] = env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]
	}
	if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "" {
		defaults["maxContextTokens"] = env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]
	}

	c.JSON(http.StatusOK, gin.H{
		"installed":        claudeCodeInstalled(settingsPath),
		"settings":         settings,
		"settingsPath":     settingsPath,
		"claudeJsonPath":   claudeJSONPath,
		"hasProxy":         env["ANTHROPIC_BASE_URL"] != "",
		"exaMcpEnabled":    claudeCodeExaMCPEnabled(claudeJSON),
		"ccFilterNaming":   h != nil && h.cfg != nil && h.cfg.ClaudeCode.FilterNamingRequests,
		"serverLocal":      true,
		"availableModels":  models,
		"defaults":         defaults,
		"suggestedEnv":     h.suggestedClaudeCodeEnv(c, defaults),
		"managedEnvFields": claudeCodeManagedEnvFields,
	})
}

func (h *Handler) PostClaudeCodeSettings(c *gin.Context) {
	var payload claudeCodeSettingsPayload
	if errBind := c.ShouldBindJSON(&payload); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errBind.Error()})
		return
	}
	if payload.Env == nil {
		payload.Env = map[string]string{}
	}

	settingsPath, errPath := claudeCodeSettingsPath()
	if errPath != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "home_dir_failed", "message": errPath.Error()})
		return
	}
	if errWrite := writeClaudeCodeSettings(settingsPath, payload.Env); errWrite != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": errWrite.Error()})
		return
	}
	if payload.ExaMCPEnabled != nil {
		claudeJSONPath, errJSONPath := claudeCodeJSONPath()
		if errJSONPath != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "home_dir_failed", "message": errJSONPath.Error()})
			return
		}
		if errMCP := writeClaudeCodeExaMCP(claudeJSONPath, *payload.ExaMCPEnabled); errMCP != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "mcp_write_failed", "message": errMCP.Error()})
			return
		}
	}
	if payload.CCFilterNaming != nil && h != nil && h.cfg != nil {
		if !h.persistClaudeCodeFilterNaming(c, *payload.CCFilterNaming) {
			return
		}
	}
	if !h.persistClaudeCodeAuthToken(c, payload.Env["ANTHROPIC_AUTH_TOKEN"]) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Claude Code settings updated successfully"})
}

func (h *Handler) DeleteClaudeCodeSettings(c *gin.Context) {
	settingsPath, errPath := claudeCodeSettingsPath()
	if errPath != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "home_dir_failed", "message": errPath.Error()})
		return
	}
	if errReset := resetClaudeCodeSettings(settingsPath); errReset != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reset_failed", "message": errReset.Error()})
		return
	}
	claudeJSONPath, errJSONPath := claudeCodeJSONPath()
	if errJSONPath != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "home_dir_failed", "message": errJSONPath.Error()})
		return
	}
	if errMCP := writeClaudeCodeExaMCP(claudeJSONPath, false); errMCP != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mcp_reset_failed", "message": errMCP.Error()})
		return
	}
	if h != nil && h.cfg != nil && h.cfg.ClaudeCode.FilterNamingRequests {
		if !h.persistClaudeCodeFilterNaming(c, false) {
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Claude Code settings reset successfully"})
}

func claudeCodeSettingsPath() (string, error) {
	home, errHome := claudeCodeUserHomeDir()
	if errHome != nil {
		return "", errHome
	}
	return filepath.Join(home, ".claude", claudeCodeSettingsFile), nil
}

func claudeCodeJSONPath() (string, error) {
	home, errHome := claudeCodeUserHomeDir()
	if errHome != nil {
		return "", errHome
	}
	return filepath.Join(home, claudeCodeMCPFile), nil
}

func readClaudeCodeJSONMap(path string) (map[string]any, error) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil, nil
		}
		return nil, errRead
	}
	data = claudeCodeTrailingCommaRE.ReplaceAll(data, []byte("$1"))
	var out map[string]any
	if errJSON := json.Unmarshal(data, &out); errJSON != nil {
		return nil, errJSON
	}
	return out, nil
}

func writeClaudeCodeJSONMap(path string, data map[string]any) error {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	body, errJSON := json.MarshalIndent(data, "", "  ")
	if errJSON != nil {
		return errJSON
	}
	body = append(body, '\n')
	return os.WriteFile(path, body, 0o600)
}

func claudeCodeEnvMap(settings map[string]any) map[string]string {
	out := map[string]string{}
	env, ok := settings["env"].(map[string]any)
	if !ok {
		return out
	}
	for key, value := range env {
		if text, okText := value.(string); okText {
			out[key] = text
		}
	}
	return out
}

func writeClaudeCodeSettings(path string, env map[string]string) error {
	settings, errRead := readClaudeCodeJSONMap(path)
	if errRead != nil {
		settings = map[string]any{}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	settings["hasCompletedOnboarding"] = true

	currentEnv, _ := settings["env"].(map[string]any)
	if currentEnv == nil {
		currentEnv = map[string]any{}
	}
	for key, value := range env {
		trimmed := strings.TrimSpace(value)
		if key == "ANTHROPIC_BASE_URL" && trimmed != "" {
			trimmed = normalizeClaudeCodeBaseURL(trimmed)
		}
		if key == "CLAUDE_CODE_MAX_CONTEXT_TOKENS" && trimmed == "" {
			delete(currentEnv, key)
			continue
		}
		currentEnv[key] = trimmed
	}
	settings["env"] = currentEnv
	return writeClaudeCodeJSONMap(path, settings)
}

func resetClaudeCodeSettings(path string) error {
	settings, errRead := readClaudeCodeJSONMap(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return errRead
	}
	if settings == nil {
		return nil
	}
	env, ok := settings["env"].(map[string]any)
	if ok {
		for _, key := range claudeCodeManagedEnvFields {
			delete(env, key)
		}
		if len(env) == 0 {
			delete(settings, "env")
		} else {
			settings["env"] = env
		}
	}
	return writeClaudeCodeJSONMap(path, settings)
}

func writeClaudeCodeExaMCP(path string, enabled bool) error {
	data, errRead := readClaudeCodeJSONMap(path)
	if errRead != nil {
		data = map[string]any{}
	}
	if data == nil {
		data = map[string]any{}
	}
	servers, _ := data["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if enabled {
		servers["exa"] = map[string]any{"type": "sse", "url": claudeCodeExaMCPURL}
		data["mcpServers"] = servers
	} else {
		delete(servers, "exa")
		if len(servers) == 0 {
			delete(data, "mcpServers")
		} else {
			data["mcpServers"] = servers
		}
	}
	return writeClaudeCodeJSONMap(path, data)
}

func claudeCodeExaMCPEnabled(data map[string]any) bool {
	servers, ok := data["mcpServers"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = servers["exa"]
	return ok
}

func claudeCodeInstalled(settingsPath string) bool {
	if _, errLook := claudeCodeLookPath("claude"); errLook == nil {
		return true
	}
	_, errAccess := os.Stat(settingsPath)
	return errAccess == nil
}

func normalizeClaudeCodeBaseURL(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

func (h *Handler) suggestedClaudeCodeEnv(c *gin.Context, defaults map[string]string) map[string]string {
	env := map[string]string{"ANTHROPIC_BASE_URL": normalizeClaudeCodeBaseURL(claudeCodeRequestOrigin(c))}
	if token := h.firstProxyAPIKey(); token != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = token
	} else {
		env["ANTHROPIC_AUTH_TOKEN"] = claudeCodeFallbackAuthToken
	}
	if value := strings.TrimSpace(defaults["opus"]); value != "" {
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = value
	}
	if value := strings.TrimSpace(defaults["sonnet"]); value != "" {
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = value
	}
	if value := strings.TrimSpace(defaults["haiku"]); value != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = value
	}
	return env
}

func claudeCodeRequestOrigin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return "http://localhost:8317"
	}
	if override := strings.TrimSpace(c.Query("base-url")); override != "" {
		return override
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	host := strings.TrimSpace(c.Request.Host)
	if host == "" {
		host = "localhost:8317"
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func (h *Handler) firstProxyAPIKey() string {
	if h == nil || h.cfg == nil {
		return ""
	}
	for _, key := range h.cfg.APIKeys {
		if value := strings.TrimSpace(key); value != "" {
			return value
		}
	}
	return ""
}

// claudeCodeModelOptions returns every model exposed by this proxy so Claude Code
// alias mappings can point at any provider (Claude, Codex, Gemini, Antigravity,
// OpenAI-compatible, ...), not just Claude-family models.
func (h *Handler) claudeCodeModelOptions() ([]claudeCodeModelOption, map[string]string) {
	seen := map[string]claudeCodeModelOption{}
	defaults := map[string]string{"opus": "", "sonnet": "", "haiku": "", "maxContextTokens": ""}

	addModel := func(id, displayName, source string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		existing, ok := seen[id]
		if ok && existing.DisplayName != "" {
			return
		}
		seen[id] = claudeCodeModelOption{ID: id, DisplayName: strings.TrimSpace(displayName), Source: strings.TrimSpace(source)}
	}

	// Registered models cover every provider currently backed by an auth file or API key.
	for _, info := range registry.GetGlobalRegistry().GetAvailableModelInfos() {
		if info == nil {
			continue
		}
		id := info.ID
		if id == "" {
			id = info.Name
		}
		addModel(id, info.DisplayName, info.Type)
	}

	if h != nil && h.cfg != nil {
		for _, key := range h.cfg.ClaudeKey {
			for _, model := range key.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "claude")
			}
		}
		for _, key := range h.cfg.CodexKey {
			for _, model := range key.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "codex")
			}
		}
		for _, key := range h.cfg.XAIKey {
			for _, model := range key.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "xai")
			}
		}
		for _, key := range h.cfg.GeminiKey {
			for _, model := range key.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "gemini")
			}
		}
		for _, key := range h.cfg.InteractionsKey {
			for _, model := range key.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "gemini")
			}
		}
		for _, provider := range h.cfg.OpenAICompatibility {
			for _, model := range provider.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "openai-compatibility")
			}
		}
		for _, key := range h.cfg.VertexCompatAPIKey {
			for _, model := range key.Models {
				addModel(firstNonEmpty(model.Alias, model.Name), model.DisplayName, "vertex")
			}
		}
	}

	out := make([]claudeCodeModelOption, 0, len(seen))
	for _, model := range seen {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, defaults
}

func (h *Handler) persistClaudeCodeFilterNaming(c *gin.Context, value bool) bool {
	h.mu.Lock()
	if h.cfg.ClaudeCode.FilterNamingRequests == value {
		h.mu.Unlock()
		return true
	}
	h.cfg.ClaudeCode.FilterNamingRequests = value
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return false
	}
	var reqCtx = c.Request.Context()
	h.reloadConfigAfterManagementSaveAsync(reqCtx, snapshot)
	return true
}

func (h *Handler) persistClaudeCodeAuthToken(c *gin.Context, authToken string) bool {
	trimmed := strings.TrimSpace(authToken)
	if trimmed == "" || trimmed == claudeCodeFallbackAuthToken || h == nil || h.cfg == nil {
		return true
	}

	h.mu.Lock()
	for _, existing := range h.cfg.APIKeys {
		if strings.TrimSpace(existing) == trimmed {
			h.mu.Unlock()
			return true
		}
	}
	h.cfg.APIKeys = append(h.cfg.APIKeys, trimmed)
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return false
	}
	var reqCtx = c.Request.Context()
	h.reloadConfigAfterManagementSaveAsync(reqCtx, snapshot)
	return true
}
