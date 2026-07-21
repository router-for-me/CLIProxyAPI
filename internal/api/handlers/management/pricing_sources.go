package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pricingsource"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// pricingSourcesFilename is the filename used when an operator uploads a
// catalog file. The id-prefixed name keeps uploads unique so successive
// uploads replace rather than clobber each other.
func pricingSourcesFilename(id int64, original string) string {
	ext := ".json"
	if dot := strings.LastIndex(original, "."); dot >= 0 {
		ext = original[dot:]
	}
	return fmt.Sprintf("source-%d%s", id, ext)
}

// pricingSourceReq is the JSON body for POST/PUT /pricing-sources. All
// fields except name + source_type are optional; the URL or file_path is
// required depending on source_type.
type pricingSourceReq struct {
	Name       string `json:"name"`
	SourceType string `json:"source_type"` // "url" | "file"
	URL        string `json:"url"`
	Format     string `json:"format"` // default "litellm"
	Enabled    *bool  `json:"enabled"`
}

// ListPricingSources handles GET /v0/management/pricing-sources.
//
// Returns the operator-managed external pricing catalogs stored in PG,
// plus the in-memory refresh state (entry_count, last_fetched_at,
// last_error) merged in. Disabled sources are included so operators can
// re-enable them without re-entering the URL.
func (h *Handler) ListPricingSources(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	rows, err := srcs.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to list pricing sources: " + err.Error(),
		}})
		return
	}
	// Attach in-memory refresh state from the pricingsource registry so
	// the dashboard shows "last fetched N ago · entry_count" per source.
	registryState := map[string]pricingsource.RegistrySnapshot{}
	for _, snap := range pricingsource.ListExternalSources() {
		registryState[snap.Source.Name] = snap
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		row := gin.H{
			"id":              r.ID,
			"name":            r.Name,
			"source_type":     r.SourceType,
			"url":             r.URL,
			"file_path":       r.FilePath,
			"format":          r.Format,
			"enabled":         r.Enabled,
			"entry_count":     r.EntryCount,
			"last_error":      r.LastError,
			"last_fetched_at": nil,
			"created_at":      r.CreatedAt,
			"updated_at":      r.UpdatedAt,
		}
		if r.LastFetchedAt != nil {
			row["last_fetched_at"] = r.LastFetchedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		// Prefer the in-memory registry state (fresher than the DB row)
		// when available.
		if snap, found := registryState[r.Name]; found {
			row["entry_count"] = snap.EntryCount
			row["last_error"] = snap.LastError
			if !snap.LastFetched.IsZero() {
				row["last_fetched_at"] = snap.LastFetched.UTC().Format("2006-01-02T15:04:05Z07:00")
			}
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"sources": out})
}

// CreatePricingSource handles POST /v0/management/pricing-sources.
//
// Body: pricingSourceReq. The response echoes the created row with its id.
// After creation the source is registered with the pricingsource package
// (enabled=true triggers an immediate refresh; enabled=false just
// registers it for later).
func (h *Handler) CreatePricingSource(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	var body pricingSourceReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	src := store.PricingSource{
		Name:       strings.TrimSpace(body.Name),
		SourceType: strings.TrimSpace(body.SourceType),
		URL:        strings.TrimSpace(body.URL),
		Format:     body.Format,
		Enabled:    true,
	}
	if body.Enabled != nil {
		src.Enabled = *body.Enabled
	}
	if src.Format == "" {
		src.Format = "litellm"
	}
	created, err := srcs.Create(c.Request.Context(), src)
	if err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	// Register with the in-memory pricingsource registry and refresh if
	// enabled. Registration is cheap; refresh is the network I/O cost.
	h.syncPricingSourceToRegistry(created)
	if created.Enabled {
		count, refreshErr := pricingsource.RefreshSource(c.Request.Context(), created.Name)
		h.recordPricingSourceRefresh(c.Request.Context(), srcs, created.ID, count, refreshErr)
		created, _ = srcs.Get(c.Request.Context(), created.ID)
	}
	c.JSON(http.StatusCreated, created)
}

// UpdatePricingSource handles PUT /v0/management/pricing-sources/:id.
//
// Body: pricingSourceReq. The name + source_type + url are mutable; the
// file_path is only mutable when source_type == "file".
func (h *Handler) UpdatePricingSource(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	existing, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	var body pricingSourceReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	// Build the update without clobbering file_path for url sources.
	updated := *existing
	if v := strings.TrimSpace(body.Name); v != "" {
		updated.Name = v
	}
	if v := strings.TrimSpace(body.SourceType); v != "" {
		updated.SourceType = v
	}
	if v := strings.TrimSpace(body.URL); v != "" {
		updated.URL = v
	}
	if v := strings.TrimSpace(body.Format); v != "" {
		updated.Format = v
	} else if updated.Format == "" {
		updated.Format = "litellm"
	}
	if body.Enabled != nil {
		updated.Enabled = *body.Enabled
	}
	// Validate: when source_type=file the existing file_path is kept unless
	// a re-upload supplies a new one (handled by UploadPricingSourceFile).
	if updated.SourceType == "file" && updated.FilePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": "source_type=file requires a file_path; upload a catalog file first",
		}})
		return
	}
	out, err := srcs.Update(c.Request.Context(), updated)
	if err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	h.syncPricingSourceToRegistry(out)
	if out.Enabled {
		count, refreshErr := pricingsource.RefreshSource(c.Request.Context(), out.Name)
		h.recordPricingSourceRefresh(c.Request.Context(), srcs, out.ID, count, refreshErr)
		out, _ = srcs.Get(c.Request.Context(), out.ID)
	}
	c.JSON(http.StatusOK, out)
}

// DeletePricingSource handles DELETE /v0/management/pricing-sources/:id.
//
// Removes the PG row, deregisters from the pricingsource registry, and
// deletes the uploaded catalog file (if any). Idempotent on the row.
func (h *Handler) DeletePricingSource(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	existing, _ := srcs.Get(c.Request.Context(), id)
	if err := srcs.Delete(c.Request.Context(), id); err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	// Deregister + drop loaded entries.
	if existing != nil {
		h.removePricingSourceFromRegistry(existing.Name)
		if existing.FilePath != "" {
			_ = os.Remove(existing.FilePath)
		}
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// RefreshPricingSource handles POST /v0/management/pricing-sources/:id/refresh.
//
// Triggers an on-demand refresh of one source: fetch the URL/file, parse
// the LiteLLM JSON, and replace the in-memory entries. The PG row is
// updated with last_fetched_at / last_error / entry_count.
func (h *Handler) RefreshPricingSource(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	src, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	if !src.Enabled {
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": gin.H{
			"type":    "source_disabled",
			"message": "source is disabled; re-enable it before refreshing",
		}})
		return
	}
	h.syncPricingSourceToRegistry(src)
	count, refreshErr := pricingsource.RefreshSource(c.Request.Context(), src.Name)
	h.recordPricingSourceRefresh(c.Request.Context(), srcs, src.ID, count, refreshErr)
	updated, _ := srcs.Get(c.Request.Context(), src.ID)
	if refreshErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"source": updated,
			"error": gin.H{
				"type":    "refresh_failed",
				"message": refreshErr.Error(),
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"source":      updated,
		"entry_count": count,
	})
}

// RefreshAllPricingSources handles POST /v0/management/pricing-sources/refresh-all.
//
// Refreshes every enabled external pricing source in parallel and returns
// per-source results.
func (h *Handler) RefreshAllPricingSources(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	rows, err := srcs.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to list pricing sources: " + err.Error(),
		}})
		return
	}
	// Filter to enabled rows only and serialize registry registration first.
	type task struct {
		row store.PricingSource
		idx int
	}
	var tasks []task
	for i, r := range rows {
		if r.Enabled {
			tasks = append(tasks, task{row: r, idx: i})
			h.syncPricingSourceToRegistry(&r)
		}
	}
	results := make([]gin.H, len(rows))
	for i, r := range rows {
		results[i] = gin.H{
			"id":          r.ID,
			"name":        r.Name,
			"enabled":     r.Enabled,
			"entry_count": r.EntryCount,
			"last_error":  r.LastError,
		}
	}
	if len(tasks) == 0 {
		c.JSON(http.StatusOK, gin.H{"results": results, "refreshed": 0, "failed": 0})
		return
	}
	type outcome struct {
		idx   int
		count int
		err   error
	}
	ch := make(chan outcome, len(tasks))
	ctx := c.Request.Context()
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(tt task) {
			defer wg.Done()
			count, err := pricingsource.RefreshSource(ctx, tt.row.Name)
			ch <- outcome{idx: tt.idx, count: count, err: err}
		}(t)
	}
	wg.Wait()
	close(ch)
	failed := 0
	refreshed := 0
	for o := range ch {
		row := rows[o.idx]
		h.recordPricingSourceRefresh(ctx, srcs, row.ID, o.count, o.err)
		results[o.idx]["entry_count"] = o.count
		if o.err != nil {
			failed++
			results[o.idx]["last_error"] = o.err.Error()
		} else {
			refreshed++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"results":   results,
		"refreshed": refreshed,
		"failed":    failed,
	})
}

// UploadPricingSourceFile handles POST /v0/management/pricing-sources/:id/upload.
//
// Accepts a multipart file upload (field "file") and stores it on disk so
// the source can be parsed with source_type=file. The PG row's file_path
// is updated to point at the stored file, and a refresh is triggered.
func (h *Handler) UploadPricingSourceFile(c *gin.Context) {
	srcs, ok := h.pricingSourceStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	existing, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": "missing multipart 'file' field: " + err.Error(),
		}})
		return
	}
	// Cap upload at 64 MB to bound memory. The canonical LiteLLM file is ~1.5 MB.
	if fileHeader.Size > 64*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{
			"type":    "file_too_large",
			"message": "uploaded catalog exceeds 64 MB",
		}})
		return
	}
	dir := h.pricingSourcesDir(c)
	if dir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "internal_error", "message": "pricing sources directory not configured",
		}})
		return
	}
	filename := pricingSourcesFilename(id, fileHeader.Filename)
	fullPath := filepath.Join(dir, filename)
	if err := c.SaveUploadedFile(fileHeader, fullPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": "failed to store uploaded file: " + err.Error(),
		}})
		return
	}
	// Replace any previous file path for this row.
	if existing.FilePath != "" && existing.FilePath != fullPath {
		_ = os.Remove(existing.FilePath)
	}
	existing.FilePath = fullPath
	existing.SourceType = "file"
	updated, err := srcs.Update(c.Request.Context(), *existing)
	if err != nil {
		h.pricingSourceErrorResponse(c, err)
		return
	}
	h.syncPricingSourceToRegistry(updated)
	count, refreshErr := pricingsource.RefreshSource(c.Request.Context(), updated.Name)
	h.recordPricingSourceRefresh(c.Request.Context(), srcs, updated.ID, count, refreshErr)
	updated, _ = srcs.Get(c.Request.Context(), updated.ID)
	c.JSON(http.StatusOK, gin.H{
		"source":      updated,
		"entry_count": count,
		"error":       refreshErr,
	})
}

// pricingSourceStore returns the PG-backed PricingSourceStore, or writes a
// 503 response + returns false when PG is not configured.
func (h *Handler) pricingSourceStore(c *gin.Context) (store.PricingSourceStore, bool) {
	h.mu.Lock()
	s := h.pgPricingSources
	h.mu.Unlock()
	if s == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return s, true
}

// pricingSourcesDir returns the directory backing uploaded catalog files.
// Empty when PG is not configured.
func (h *Handler) pricingSourcesDir(c *gin.Context) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pgPricingSourcesDir == "" {
		return ""
	}
	return h.pgPricingSourcesDir
}

// syncPricingSourceToRegistry registers one PG row with the in-memory
// pricingsource registry. Called after every Create/Update so the
// registry stays in sync with PG state.
func (h *Handler) syncPricingSourceToRegistry(src *store.PricingSource) {
	if src == nil {
		return
	}
	// Re-read the full source list from the store so SetExternalSources
	// receives the complete authoritative picture rather than just the
	// one row that changed. Skip when the store is not available.
	h.mu.Lock()
	store := h.pgPricingSources
	h.mu.Unlock()
	if store == nil {
		// Still register this single source so refresh works mid-bootstrap.
		pricingsource.SetExternalSources([]pricingsource.ExternalSource{toExternalSource(*src)})
		return
	}
	rows, err := store.List(context.Background())
	if err != nil {
		pricingsource.SetExternalSources([]pricingsource.ExternalSource{toExternalSource(*src)})
		return
	}
	list := make([]pricingsource.ExternalSource, 0, len(rows))
	for _, r := range rows {
		list = append(list, toExternalSource(r))
	}
	pricingsource.SetExternalSources(list)
}

// removePricingSourceFromRegistry drops one name from the in-memory
// registry and re-syncs the rest from the store.
func (h *Handler) removePricingSourceFromRegistry(name string) {
	h.mu.Lock()
	store := h.pgPricingSources
	h.mu.Unlock()
	if store == nil {
		// Best-effort: drop just this source name.
		existing := pricingsource.ListExternalSources()
		keep := make([]pricingsource.ExternalSource, 0, len(existing))
		for _, s := range existing {
			if s.Source.Name != name {
				keep = append(keep, s.Source)
			}
		}
		pricingsource.SetExternalSources(keep)
		return
	}
	rows, err := store.List(context.Background())
	if err != nil {
		return
	}
	list := make([]pricingsource.ExternalSource, 0, len(rows))
	for _, r := range rows {
		if r.Name == name {
			continue
		}
		list = append(list, toExternalSource(r))
	}
	pricingsource.SetExternalSources(list)
}

// recordPricingSourceRefresh writes a refresh outcome back to the PG row.
// Errors are logged, not surfaced — the refresh itself already succeeded or
// failed, this just persists the audit trail.
func (h *Handler) recordPricingSourceRefresh(ctx context.Context, srcs store.PricingSourceStore, id int64, count int, err error) {
	if srcs == nil {
		return
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	if recErr := srcs.RecordRefresh(ctx, id, count, errMsg); recErr != nil {
		// Non-fatal: the in-memory registry already has the new entries.
		_ = recErr
	}
}

// toExternalSource converts a store.PricingSource row into the registry's
// lightweight ExternalSource shape.
func toExternalSource(s store.PricingSource) pricingsource.ExternalSource {
	format := s.Format
	if format == "" {
		format = "litellm"
	}
	return pricingsource.ExternalSource{
		Name:       s.Name,
		SourceType: s.SourceType,
		URL:        s.URL,
		FilePath:   s.FilePath,
		Format:     format,
		Enabled:    s.Enabled,
	}
}

// pricingSourceErrorResponse maps store errors to HTTP responses.
func (h *Handler) pricingSourceErrorResponse(c *gin.Context, err error) {
	if errors.Is(err, store.ErrPricingSourceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"type": "not_found", "message": "pricing source not found",
		}})
		return
	}
	// UNIQUE constraint violation (duplicate name) → 409 conflict.
	msg := err.Error()
	if strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint") {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"type": "conflict", "message": "a pricing source with that name already exists",
		}})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request", "message": msg,
	}})
}
