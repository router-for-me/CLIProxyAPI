package management

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// requireBackupPG returns the Postgres store backing the /export and /import
// routes, writing a 503 when the PG backend is not wired.
func (h *Handler) requireBackupPG(c *gin.Context) (*store.PostgresStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	pg := h.pgBackup
	h.mu.Unlock()
	if pg == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return pg, true
}

// parseBackupResources splits a comma-separated resource list, validating each
// entry against the known set. An empty result means "all resources" (the
// store treats empty as all).
func parseBackupResources(list string) ([]store.BackupResource, string) {
	var out []store.BackupResource
	seen := make(map[store.BackupResource]bool)
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		res := store.BackupResource(part)
		if !store.ValidBackupResource(part) {
			return nil, "unknown resource: " + part
		}
		if seen[res] {
			continue
		}
		seen[res] = true
		out = append(out, res)
	}
	return out, ""
}

// ListBackupResources returns the canonical ordered list of backup resources so
// the dashboard can render the category checklists.
func (h *Handler) ListBackupResources(c *gin.Context) {
	if _, ok := h.requireBackupPG(c); !ok {
		return
	}
	list := make([]string, 0, len(store.AllBackupResources))
	for _, r := range store.AllBackupResources {
		list = append(list, string(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"resources": list,
	})
}

// ExportAllData produces a portable JSON snapshot of the requested PG-backed
// resources (default all). Use ?resources=api_keys,usage to select a subset.
// Passing ?download=1 sets Content-Disposition so callers can save it as a file.
func (h *Handler) ExportAllData(c *gin.Context) {
	pg, ok := h.requireBackupPG(c)
	if !ok {
		return
	}
	resources, errMsg := parseBackupResources(c.Query("resources"))
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_resource", "message": errMsg}})
		return
	}
	bundle, err := pg.ExportData(c.Request.Context(), store.BackupExportOpts{Resources: resources})
	if err != nil {
		log.WithError(err).Error("management: export all data failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "export_failed", "message": err.Error()}})
		return
	}
	if c.Query("download") == "1" {
		filename := strings.ReplaceAll(time.Now().UTC().Format("2006-01-02T150405"), "T", "_") + "_nixllm_export.json"
		c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	}
	c.JSON(http.StatusOK, bundle)
}

// ImportAllData restores the requested resources (default all present in the
// body) from an exported bundle. The request body must be a BackupBundle
// document (exactly as produced by ExportAllData). Selected resources are wiped
// and replaced within a single transaction. This is destructive for the
// selected categories — callers must confirm intent, and the dashboard always
// warns before sending.
func (h *Handler) ImportAllData(c *gin.Context) {
	pg, ok := h.requireBackupPG(c)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 512<<20)) // 512 MiB upper bound
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "read_body", "message": err.Error()}})
		return
	}
	var bundle store.BackupBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_bundle", "message": "body is not a valid backup bundle: " + err.Error()}})
		return
	}
	var opts store.BackupImportOpts
	// ?resources=api_keys,usage optionally restricts which portions of the
	// bundle to restore; empty means all resources present in the bundle.
	if qs := c.Query("resources"); qs != "" {
		resources, errMsg := parseBackupResources(qs)
		if errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_resource", "message": errMsg}})
			return
		}
		opts.Resources = resources
	}
	report, err := pg.ImportData(c.Request.Context(), bundle, opts)
	if err != nil {
		log.WithError(err).Error("management: import all data failed")
		// The store rolls back on hard failure, so existing data is untouched.
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "import_failed", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"import": report})
}
