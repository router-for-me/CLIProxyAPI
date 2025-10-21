package management

import (
    "net/http"
    "sort"
    "strings"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// GetProviders lists provider identifiers currently supplying any available model.
// Response: {"providers": ["claude","gemini","packycode", ...]}
func (h *Handler) GetProviders(c *gin.Context) {
    reg := registry.GetGlobalRegistry()
    // Use generic handlerType to retrieve a broad model list
    models := reg.GetAvailableModels("")
    seen := make(map[string]struct{})
    for _, m := range models {
        id, _ := m["id"].(string)
        if id == "" {
            continue
        }
        provs := reg.GetModelProviders(id)
        for _, p := range provs {
            p = strings.TrimSpace(p)
            if p == "" {
                continue
            }
            seen[p] = struct{}{}
        }
    }
    out := make([]string, 0, len(seen))
    for p := range seen {
        out = append(out, p)
    }
    sort.Strings(out)
    c.JSON(http.StatusOK, gin.H{"providers": out})
}

// GetModels returns models filtered by provider: /v0/management/models?provider=packycode
// Response: {"object":"list","data":[{... plus "providers": [..]}]}
func (h *Handler) GetModels(c *gin.Context) {
    provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
    if provider == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing provider"})
        return
    }
    reg := registry.GetGlobalRegistry()
    models := reg.GetAvailableModels("")
    filtered := make([]map[string]any, 0, len(models))
    for _, m := range models {
        id, _ := m["id"].(string)
        if id == "" {
            continue
        }
        provs := reg.GetModelProviders(id)
        include := false
        for _, p := range provs {
            if strings.EqualFold(p, provider) {
                include = true
                break
            }
        }
        if include {
            // annotate providers for management visibility
            cp := make(map[string]any, len(m)+1)
            for k, v := range m {
                cp[k] = v
            }
            cp["providers"] = provs
            filtered = append(filtered, cp)
        }
    }
    c.JSON(http.StatusOK, gin.H{"object": "list", "data": filtered})
}
