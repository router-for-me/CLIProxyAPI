package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// CodexClientModelsOverrideSupportHeader reports that this build exposes the local
// Codex client model override management API.
const CodexClientModelsOverrideSupportHeader = "X-CPA-SUPPORT-CODEX-CLIENT-MODEL-OVERRIDE"

// CodexClientModelsInheritSupportHeader reports that this build resolves the $inherit
// directive inside Codex client model override entries.
const CodexClientModelsInheritSupportHeader = "X-CPA-SUPPORT-CODEX-CLIENT-MODEL-INHERIT"

// GetCodexClientModels returns the effective Codex client model catalog together
// with the local override layer applied on top of it.
func (h *Handler) GetCodexClientModels(c *gin.Context) {
	c.JSON(http.StatusOK, registry.GetCodexClientModelsState())
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
	c.JSON(http.StatusOK, registry.GetCodexClientModelsState())
}

func readCodexClientModelsBody(c *gin.Context) ([]byte, bool) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body is required"})
		return nil, false
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read request body: %v", err)})
		return nil, false
	}
	if strings.TrimSpace(string(body)) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body is required"})
		return nil, false
	}
	return body, true
}
