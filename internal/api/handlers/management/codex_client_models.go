package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// CodexClientModelsOverrideSupportHeader reports that this build exposes the local
// Codex client model override management API.
const CodexClientModelsOverrideSupportHeader = "X-CPA-SUPPORT-CODEX-CLIENT-MODEL-OVERRIDE"

// CodexClientModelsInheritSupportHeader reports that this build resolves the $inherit
// directive inside Codex client model override entries.
const CodexClientModelsInheritSupportHeader = "X-CPA-SUPPORT-CODEX-CLIENT-MODEL-INHERIT"

// codexClientModelsResponseBody is the management representation of the Codex client
// model catalog: the default entry the server assembles for every model it can serve,
// the local override layer that shapes them, and the models it actually hands out.
type codexClientModelsResponseBody struct {
	registry.CodexClientModelsState
	// Models lists the default entry of every servable model, before overrides.
	Models []map[string]any `json:"models"`
	// Origins maps each slug to where its entry comes from.
	Origins map[string]registry.CodexClientModelsOrigin `json:"origins"`
	// OverrideErrors lists the override entries that could not be applied. Every other
	// entry of the same document still takes effect.
	OverrideErrors []registry.CodexClientModelsOverrideIssue `json:"override_errors,omitempty"`
	// ServedModels lists one summary per model Codex clients can request.
	ServedModels []codexmodels.ServedModelSummary `json:"served_models"`
}

// buildCodexClientModelsResponse builds the response every read and mutation returns, so
// clients render the result without a second request.
func buildCodexClientModelsResponse() codexClientModelsResponseBody {
	modelRegistry := registry.GetGlobalRegistry()
	sets := codexmodels.BuildCodexClientModelSets(
		modelRegistry.GetAvailableModels("openai"),
		modelRegistry.GetModelProviders,
		false,
		"",
	)
	return codexClientModelsResponseBody{
		CodexClientModelsState: registry.GetCodexClientModelsState(),
		Models:                 sets.Defaults,
		Origins:                sets.Origins,
		OverrideErrors:         sets.Issues,
		ServedModels:           sets.Summaries,
	}
}

// GetCodexClientModels returns the default catalog entries, the local override layer and
// the served model list.
func (h *Handler) GetCodexClientModels(c *gin.Context) {
	c.JSON(http.StatusOK, buildCodexClientModelsResponse())
}

// PutCodexClientModelsOverride replaces the whole local override document.
func (h *Handler) PutCodexClientModelsOverride(c *gin.Context) {
	body, ok := readCodexClientModelsBody(c)
	if !ok {
		return
	}
	h.respondCodexClientModelsOverride(c, registry.SetCodexClientModelsOverride(body))
}

// DeleteCodexClientModelsOverride clears the local override document.
func (h *Handler) DeleteCodexClientModelsOverride(c *gin.Context) {
	h.respondCodexClientModelsOverride(c, registry.ClearCodexClientModelsOverride())
}

// PutCodexClientModelsOverrideEntry replaces the override patch of a single model slug.
func (h *Handler) PutCodexClientModelsOverrideEntry(c *gin.Context) {
	body, ok := readCodexClientModelsBody(c)
	if !ok {
		return
	}
	h.respondCodexClientModelsOverride(c, registry.SetCodexClientModelsOverrideEntry(c.Param("slug"), json.RawMessage(body)))
}

// DeleteCodexClientModelsOverrideEntry removes the override patch of a single model slug.
func (h *Handler) DeleteCodexClientModelsOverrideEntry(c *gin.Context) {
	h.respondCodexClientModelsOverride(c, registry.DeleteCodexClientModelsOverrideEntry(c.Param("slug")))
}

// respondCodexClientModelsOverride reports a failed override change or the new state
// so clients can render the result without a second request.
func (h *Handler) respondCodexClientModelsOverride(c *gin.Context, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		if registry.IsCodexClientModelsOverrideRejected(err) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, buildCodexClientModelsResponse())
}

func readCodexClientModelsBody(c *gin.Context) ([]byte, bool) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body is required"})
		return nil, false
	}
	// An override document is persisted in a file of a bounded size, so a body above
	// that bound is refused before it is held in memory.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, registry.CodexClientModelsOverrideMaxFileSize)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var errTooLarge *http.MaxBytesError
		if errors.As(err, &errTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("request body exceeds %d bytes", registry.CodexClientModelsOverrideMaxFileSize),
			})
			return nil, false
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read request body: %v", err)})
		return nil, false
	}
	if strings.TrimSpace(string(body)) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body is required"})
		return nil, false
	}
	return body, true
}
