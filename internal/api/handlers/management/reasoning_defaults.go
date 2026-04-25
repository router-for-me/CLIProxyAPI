package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

type putDefaultReasoningOnIngressByFormatRequest struct {
	Value map[string]config.ReasoningIngressDefault `json:"value"`
}

type reasoningIngressFormatOptions struct {
	Format          string                            `json:"format"`
	AppliesTo       []string                          `json:"applies-to,omitempty"`
	Policies        []string                          `json:"policies"`
	Modes           []config.ReasoningIngressModeSpec `json:"modes"`
	AvailableModels []string                          `json:"available-models,omitempty"`
}

// GetReasoningIngressOptions returns format/policy/mode/value options for frontend dropdowns.
func (h *Handler) GetReasoningIngressOptions(c *gin.Context) {
	catalog := config.ReasoningIngressFormatCatalog()
	formats := make([]string, 0, len(catalog))
	for format := range catalog {
		formats = append(formats, format)
	}
	sort.Strings(formats)

	items := make([]reasoningIngressFormatOptions, 0, len(formats))
	for _, format := range formats {
		spec := catalog[format]
		items = append(items, reasoningIngressFormatOptions{
			Format:          spec.Format,
			AppliesTo:       append([]string(nil), spec.AppliesTo...),
			Policies:        append([]string(nil), spec.Policies...),
			Modes:           append([]config.ReasoningIngressModeSpec(nil), spec.Modes...),
			AvailableModels: reasoningAvailableModelsByFormat(format),
		})
	}

	c.JSON(http.StatusOK, gin.H{"formats": items})
}

// GetDefaultReasoningOnIngressByFormat returns current ingress defaults.
func (h *Handler) GetDefaultReasoningOnIngressByFormat(c *gin.Context) {
	defaults := make(map[string]config.ReasoningIngressDefault)
	if h != nil && h.cfg != nil {
		for format, entry := range h.cfg.DefaultReasoningOnIngressByFormat {
			defaults[format] = entry
		}
	}
	c.JSON(http.StatusOK, gin.H{"default-reasoning-on-ingress-by-format": defaults})
}

// PutDefaultReasoningOnIngressByFormat replaces ingress defaults.
func (h *Handler) PutDefaultReasoningOnIngressByFormat(c *gin.Context) {
	var body putDefaultReasoningOnIngressByFormatRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	normalized, err := config.NormalizeReasoningOnIngressByFormat(body.Value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid value",
			"message": err.Error(),
		})
		return
	}

	h.cfg.DefaultReasoningOnIngressByFormat = normalized
	h.persist(c)
}

func reasoningAvailableModelsByFormat(format string) []string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return nil
	}

	handlerType := ""
	switch format {
	case config.ReasoningIngressFormatOpenAI:
		handlerType = "openai"
	case config.ReasoningIngressFormatClaude:
		handlerType = "claude"
	case config.ReasoningIngressFormatGemini:
		handlerType = "gemini"
	default:
		return nil
	}

	seen := make(map[string]struct{})
	models := make([]string, 0)
	addModel := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}

	globalRegistry := registry.GetGlobalRegistry()
	for _, item := range globalRegistry.GetAvailableModels(handlerType) {
		if item == nil {
			continue
		}
		if id, ok := item["id"].(string); ok {
			addModel(id)
		}
	}

	sort.Strings(models)
	return models
}
