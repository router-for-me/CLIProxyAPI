package management

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// proxyPoolsStore returns the PG-backed proxy pool store, writing a 503
// response when PG is not configured.
func (h *Handler) proxyPoolsStore(c *gin.Context) (store.ProxyPoolStore, bool) {
	if h == nil || h.pgProxyPools == nil {
		if c != nil {
			h.pgNotConfigured(c)
		}
		return nil, false
	}
	return h.pgProxyPools, true
}

// proxyPoolErrorResponse maps a store error to an HTTP response.
func (h *Handler) proxyPoolErrorResponse(c *gin.Context, err error) {
	if errors.Is(err, store.ErrProxyPoolNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"type": "not_found", "message": "proxy pool not found",
		}})
		return
	}
	if errors.Is(err, store.ErrProxyPoolDuplicateName) {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"type": "conflict", "message": "a proxy pool with that name already exists",
		}})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request", "message": err.Error(),
	}})
}

// validateProxyPoolRequest checks a decoded request body before it reaches
// the store: name/url required, scheme within the allowed set, type within
// the closed set, relay types require an https base.
func validateProxyPoolRequest(body *proxyPoolReq) error {
	if strings.TrimSpace(body.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(body.ProxyURL) == "" {
		return errors.New("proxy_url is required")
	}
	poolType := strings.TrimSpace(body.Type)
	if poolType == "" {
		poolType = "http"
	}
	if !store.ValidProxyPoolType(poolType) {
		return errors.New("type must be one of http, vercel, cloudflare, deno")
	}
	parsed, errParse := url.Parse(strings.TrimSpace(body.ProxyURL))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("proxy_url must be an absolute URL with a scheme and host")
	}
	if poolType == "http" && !allowedProxyPoolSchemes[parsed.Scheme] {
		return errors.New("proxy_url scheme must be one of http, https, socks5, socks5h")
	}
	if poolType != "http" && parsed.Scheme != "https" {
		return errors.New("relay pool proxy_url must be an https URL")
	}
	return nil
}

// toProxyPool maps a decoded request into a store row, applying the handler
// defaults: active + strict + type http + untested when the fields are absent.
func toProxyPool(body *proxyPoolReq) store.ProxyPool {
	poolType := strings.TrimSpace(body.Type)
	if poolType == "" {
		poolType = "http"
	}
	isActive := true
	if body.IsActive != nil {
		isActive = *body.IsActive
	}
	strict := true
	if body.StrictProxy != nil {
		strict = *body.StrictProxy
	}
	return store.ProxyPool{
		Name:        strings.TrimSpace(body.Name),
		ProxyURL:    strings.TrimSpace(body.ProxyURL),
		NoProxy:     strings.TrimSpace(body.NoProxy),
		Type:        poolType,
		IsActive:    isActive,
		StrictProxy: strict,
		TestStatus:  "unknown",
	}
}

// ListProxyPools handles GET /v0/management/proxy-pools. With
// ?include_usage=1 every pool carries its bound_entry_count.
func (h *Handler) ListProxyPools(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	pools, err := srcs.List(c.Request.Context())
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	if c.Query("include_usage") != "1" && c.Query("include_usage") != "true" {
		c.JSON(http.StatusOK, gin.H{"pools": pools})
		return
	}
	enriched := make([]proxyPoolResponse, 0, len(pools))
	for _, p := range pools {
		count, errCount := srcs.BoundEntryCount(c.Request.Context(), p.ID)
		if errCount != nil {
			log.WithError(errCount).Warnf("management: bound count for pool %d failed", p.ID)
		}
		enriched = append(enriched, proxyPoolResponse{ProxyPool: p, BoundEntryCount: count})
	}
	c.JSON(http.StatusOK, gin.H{"pools": enriched})
}

// GetProxyPool handles GET /v0/management/proxy-pools/:id.
func (h *Handler) GetProxyPool(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	id, errID := strconv.ParseInt(c.Param("id"), 10, 64)
	if errID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "invalid pool id",
		}})
		return
	}
	pool, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pool": pool})
}

// CreateProxyPool handles POST /v0/management/proxy-pools, then re-renders
// the upstream artifacts so new bindings can resolve against the pool.
func (h *Handler) CreateProxyPool(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	var body proxyPoolReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if errValidate := validateProxyPoolRequest(&body); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": errValidate.Error(),
		}})
		return
	}
	pool, err := srcs.Create(c.Request.Context(), toProxyPool(&body))
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusCreated, gin.H{"pool": pool})
}

// UpdateProxyPool handles PUT /v0/management/proxy-pools/:id. Only the
// fields present in the body change; test-result fields are untouched here
// (the test endpoint owns them).
func (h *Handler) UpdateProxyPool(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	id, errID := strconv.ParseInt(c.Param("id"), 10, 64)
	if errID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "invalid pool id",
		}})
		return
	}
	var body proxyPoolReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if errValidate := validateProxyPoolRequest(&body); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": errValidate.Error(),
		}})
		return
	}
	current, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	merged := *current
	merged.Name = strings.TrimSpace(body.Name)
	merged.ProxyURL = strings.TrimSpace(body.ProxyURL)
	merged.NoProxy = strings.TrimSpace(body.NoProxy)
	if strings.TrimSpace(body.Type) != "" {
		merged.Type = strings.TrimSpace(body.Type)
	}
	if body.IsActive != nil {
		merged.IsActive = *body.IsActive
	}
	if body.StrictProxy != nil {
		merged.StrictProxy = *body.StrictProxy
	}
	updated, err := srcs.Update(c.Request.Context(), merged)
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"pool": updated})
}

// DeleteProxyPool handles DELETE /v0/management/proxy-pools/:id. Pools still
// referenced by upstream rows/entries are rejected with 409 + the count.
func (h *Handler) DeleteProxyPool(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	id, errID := strconv.ParseInt(c.Param("id"), 10, 64)
	if errID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "invalid pool id",
		}})
		return
	}
	bound, errBound := srcs.BoundEntryCount(c.Request.Context(), id)
	if errBound != nil {
		log.WithError(errBound).Warn("management: bound entry count failed; proceeding with delete")
	} else if bound > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{
				"type":    "conflict",
				"message": "proxy pool is still bound by upstream provider rows or entries",
			},
			"bound_entry_count": bound,
		})
		return
	}
	if err := srcs.Delete(c.Request.Context(), id); err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// TestProxyPool handles POST /v0/management/proxy-pools/:id/test. Status-only:
// the probe records test_status/last_tested_at/last_error and never flips
// is_active (the operator decides — design decision 3).
func (h *Handler) TestProxyPool(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	id, errID := strconv.ParseInt(c.Param("id"), 10, 64)
	if errID != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "invalid pool id",
		}})
		return
	}
	pool, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	probeErr := h.runProxyPoolTest(c.Request.Context(), *pool)

	result := probeResult{ok: probeErr == nil}
	if probeErr != nil {
		result.detail = probeErr.Error()
	}
	updated, errUpdate := h.recordProxyPoolTest(c.Request.Context(), srcs, *pool, result.ok, result.detail, nil)
	if errUpdate != nil {
		log.WithError(errUpdate).Warn("management: record proxy pool test result failed")
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          result.ok,
		"test_status": testStatusFor(result.ok),
		"last_error":  result.detail,
		"pool":        updated,
	})
}

// BatchImportProxyPools handles POST /v0/management/proxy-pools/batch-import.
// Lines parse server-side (9router semantics), dedupe against existing pools,
// and per-line errors never abort the batch. One re-render at the end.
func (h *Handler) BatchImportProxyPools(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	var body struct {
		Lines []string `json:"lines"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	existing, err := srcs.List(c.Request.Context())
	if err != nil {
		h.proxyPoolErrorResponse(c, err)
		return
	}
	seen := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		seen[strings.TrimSpace(p.ProxyURL)] = struct{}{}
	}

	var created, skipped, failed int
	var lineErrors []gin.H
	for i, raw := range body.Lines {
		line := i + 1
		proxyURL, errParse := parseProxyLine(raw)
		if errParse != nil {
			failed++
			lineErrors = append(lineErrors, gin.H{"line": line, "error": errParse.Error()})
			continue
		}
		if _, dup := seen[proxyURL]; dup {
			skipped++
			continue
		}
		seen[proxyURL] = struct{}{}
		name := "Imported " + hostPortOf(proxyURL)
		if _, errCreate := srcs.Create(c.Request.Context(), store.ProxyPool{
			Name: name, ProxyURL: proxyURL, Type: "http", IsActive: true, StrictProxy: true, TestStatus: "unknown",
		}); errCreate != nil {
			failed++
			lineErrors = append(lineErrors, gin.H{"line": line, "error": errCreate.Error()})
			continue
		}
		created++
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"created": created, "skipped": skipped, "failed": failed, "errors": lineErrors})
}

// parseProxyLine parses one batch-import line: "scheme://host[:port]" as-is;
// "host:port" or "user:pass@host:port" normalized to http://. Mirrors
// 9router's parseProxyLine with stricter credential checks.
func parseProxyLine(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", errors.New("empty line")
	}
	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", errors.New("invalid proxy URL")
		}
		if !allowedProxyPoolSchemes[parsed.Scheme] {
			return "", errors.New("scheme must be one of http, https, socks5, socks5h")
		}
		return parsed.String(), nil
	}
	// host[:port][:user:pass] shapes without a scheme.
	at := strings.LastIndex(trimmed, "@")
	hostPart, credPart := trimmed, ""
	if at >= 0 {
		hostPart = trimmed[at+1:]
		credPart = trimmed[:at]
	}
	hostPieces := strings.Split(hostPart, ":")
	if len(hostPieces) != 2 || strings.TrimSpace(hostPieces[0]) == "" || strings.TrimSpace(hostPieces[1]) == "" {
		return "", errors.New("expected host:port or user:pass@host:port")
	}
	if credPart != "" {
		credPieces := strings.SplitN(credPart, ":", 2)
		if len(credPieces) != 2 || credPieces[0] == "" || credPieces[1] == "" {
			return "", errors.New("credentials must be user:pass")
		}
		return "http://" + url.UserPassword(credPieces[0], credPieces[1]).String() + "@" + hostPart, nil
	}
	return "http://" + hostPart, nil
}

// hostPortOf extracts host:port for the auto-generated import name.
func hostPortOf(proxyURL string) string {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return proxyURL
	}
	return parsed.Host
}
