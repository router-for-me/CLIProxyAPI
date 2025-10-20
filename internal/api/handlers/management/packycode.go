package management

import (
    "encoding/json"
    "io"
    "net/http"
    "strings"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// GetPackycode returns the current Packycode configuration block.
func (h *Handler) GetPackycode(c *gin.Context) {
    if h == nil || h.cfg == nil {
        c.JSON(http.StatusOK, gin.H{"packycode": config.PackycodeConfig{}})
        return
    }
    c.JSON(http.StatusOK, gin.H{"packycode": h.cfg.Packycode})
}

// PutPackycode replaces the Packycode configuration block.
func (h *Handler) PutPackycode(c *gin.Context) {
    data, err := io.ReadAll(c.Request.Body)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
        return
    }
    var body config.PackycodeConfig
    if err := json.Unmarshal(data, &body); err != nil {
        // also accept {"packycode": {...}}
        var wrapper struct{ Packycode *config.PackycodeConfig `json:"packycode"` }
        if err2 := json.Unmarshal(data, &wrapper); err2 != nil || wrapper.Packycode == nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
            return
        }
        body = *wrapper.Packycode
    }
    // effective-source is read-only
    body.EffectiveSource = ""
    normalizePackycode(&body)
    // validate using full config clone
    newCfg := *h.cfg
    newCfg.Packycode = body
    if err := config.ValidatePackycode(&newCfg); err != nil {
        c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_packycode", "message": err.Error()})
        return
    }
    body.EffectiveSource = "config.yaml"
    h.cfg.Packycode = body
    h.persist(c)
}

// PatchPackycode updates selected fields in the Packycode configuration block.
func (h *Handler) PatchPackycode(c *gin.Context) {
    data, err := io.ReadAll(c.Request.Body)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
        return
    }
    // detect provided keys
    var raw map[string]any
    _ = json.Unmarshal(data, &raw)
    if v, ok := raw["packycode"]; ok {
        if m, ok2 := v.(map[string]any); ok2 {
            raw = m
        }
    }
    var body config.PackycodeConfig
    _ = json.Unmarshal(data, &body) // best-effort struct mapping

    cur := h.cfg.Packycode
    if _, ok := raw["enabled"]; ok {
        cur.Enabled = body.Enabled
    }
    if v, ok := raw["base-url"]; ok {
        if s, ok2 := v.(string); ok2 && strings.TrimSpace(s) != "" {
            cur.BaseURL = strings.TrimSpace(s)
        } else if ok { // allow clearing if explicitly empty string
            cur.BaseURL = ""
        }
    }
    if _, ok := raw["requires-openai-auth"]; ok {
        cur.RequiresOpenAIAuth = body.RequiresOpenAIAuth
    }
    if _, ok := raw["wire-api"]; ok {
        cur.WireAPI = body.WireAPI
    }
    if v, ok := raw["privacy"]; ok {
        if m, ok2 := v.(map[string]any); ok2 {
            if _, ok3 := m["disable-response-storage"]; ok3 {
                cur.Privacy.DisableResponseStorage = body.Privacy.DisableResponseStorage
            }
        }
    }
    if v, ok := raw["defaults"]; ok {
        if m, ok2 := v.(map[string]any); ok2 {
            if _, ok3 := m["model"]; ok3 { cur.Defaults.Model = body.Defaults.Model }
            if _, ok3 := m["model-reasoning-effort"]; ok3 { cur.Defaults.ModelReasoningEffort = body.Defaults.ModelReasoningEffort }
        }
    }
    if v, ok := raw["credentials"]; ok {
        if m, ok2 := v.(map[string]any); ok2 {
            if _, ok3 := m["openai-api-key"]; ok3 { cur.Credentials.OpenAIAPIKey = body.Credentials.OpenAIAPIKey }
        }
    }
    // Read-only field must not be set by clients
    cur.EffectiveSource = h.cfg.Packycode.EffectiveSource
    normalizePackycode(&cur)
    // validate using full config clone
    newCfg := *h.cfg
    newCfg.Packycode = cur
    if err := config.ValidatePackycode(&newCfg); err != nil {
        c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_packycode", "message": err.Error()})
        return
    }
    cur.EffectiveSource = "config.yaml"
    h.cfg.Packycode = cur
    h.persist(c)
}

func normalizePackycode(pc *config.PackycodeConfig) {
    if pc == nil { return }
    pc.BaseURL = strings.TrimSpace(pc.BaseURL)
    if pc.WireAPI == "" || !strings.EqualFold(pc.WireAPI, "responses") { pc.WireAPI = "responses" }
    if pc.Defaults.Model == "" { pc.Defaults.Model = "gpt-5" }
    if pc.Defaults.ModelReasoningEffort == "" { pc.Defaults.ModelReasoningEffort = "high" }
    if !pc.Privacy.DisableResponseStorage { pc.Privacy.DisableResponseStorage = true }
}
