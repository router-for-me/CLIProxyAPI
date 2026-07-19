package management

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ErrorMessageDTO is the wire shape for both responses and request bodies.
// We expose the curated default alongside the configured override so the
// dashboard can show "current vs default" side-by-side and offer a
// "reset to default" button.
type ErrorMessageDTO struct {
	errormessages.Message
	IsDefault bool `json:"is_default"`
}

// ListErrorMessages handles GET /v0/management/error-messages.
//
// Returns one entry per known status code (from errormessages.KnownCodes)
// plus any operator-added custom codes. Each row carries the configured
// title/message/body_template or — when none is set — the curated default
// values flagged is_default=true.
func (h *Handler) ListErrorMessages(c *gin.Context) {
	emStore := h.errorMessageStore()
	if emStore == nil {
		h.pgNotConfigured(c)
		return
	}

	// Build the response keyed by status code so we can merge defaults
	// with overrides without producing duplicates.
	known := errormessages.KnownCodes()
	byCode := make(map[int]ErrorMessageDTO, len(known)+8)
	for _, code := range known {
		byCode[code] = ErrorMessageDTO{
			Message:   errormessages.Default(code),
			IsDefault: true,
		}
	}

	overrides, err := emStore.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	for _, m := range overrides {
		byCode[m.StatusCode] = ErrorMessageDTO{Message: m, IsDefault: false}
	}

	out := make([]ErrorMessageDTO, 0, len(byCode))
	// Emit known codes first (in KnownCodes order), then any operator-added
	// custom codes sorted by status code.
	emitted := make(map[int]bool, len(byCode))
	for _, code := range known {
		if dto, ok := byCode[code]; ok {
			out = append(out, dto)
			emitted[code] = true
		}
	}
	customCodes := make([]int, 0, len(byCode))
	for code := range byCode {
		if !emitted[code] {
			customCodes = append(customCodes, code)
		}
	}
	// Sort custom codes ascending.
	for i := 1; i < len(customCodes); i++ {
		for j := i; j > 0 && customCodes[j] < customCodes[j-1]; j-- {
			customCodes[j], customCodes[j-1] = customCodes[j-1], customCodes[j]
		}
	}
	for _, code := range customCodes {
		out = append(out, byCode[code])
	}

	c.JSON(http.StatusOK, gin.H{"messages": out, "known_codes": known})
}

// GetErrorMessage handles GET /v0/management/error-messages/:code.
func (h *Handler) GetErrorMessage(c *gin.Context) {
	emStore := h.errorMessageStore()
	if emStore == nil {
		h.pgNotConfigured(c)
		return
	}
	code, err := parseStatusCodeParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	m, err := emStore.GetByStatusCode(c.Request.Context(), code)
	if err != nil {
		if errors.Is(err, store.ErrErrorMessageNotFound) {
			// Not overridden — return the default so the caller sees what
			// the proxy would actually emit.
			d := errormessages.Default(code)
			c.JSON(http.StatusOK, ErrorMessageDTO{Message: d, IsDefault: true})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, ErrorMessageDTO{Message: *m, IsDefault: false})
}

// PutErrorMessage handles PUT /v0/management/error-messages/:code.
// Body: { "title": "...", "message": "...", "body_template": "...", "enabled": true }
// On success the registry cache is invalidated so the next response picks
// up the new text without a server restart.
func (h *Handler) PutErrorMessage(c *gin.Context) {
	emStore := h.errorMessageStore()
	if emStore == nil {
		h.pgNotConfigured(c)
		return
	}
	code, err := parseStatusCodeParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	var req struct {
		Title        string `json:"title"`
		Message      string `json:"message"`
		BodyTemplate string `json:"body_template"`
		Enabled      *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	// When the caller omits fields, fall back to the curated default so
	// operators can PUT a partial body (e.g. only changing the title)
	// without losing the canonical message text.
	base := errormessages.Default(code)
	if req.Title == "" {
		req.Title = base.Title
	}
	if req.Message == "" {
		req.Message = base.Message
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	m := errormessages.Message{
		StatusCode:   code,
		Title:        req.Title,
		Message:      req.Message,
		BodyTemplate: req.BodyTemplate,
		Enabled:      enabled,
	}
	if err := emStore.Upsert(c.Request.Context(), m); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	errormessages.Invalidate()
	c.JSON(http.StatusOK, ErrorMessageDTO{Message: m, IsDefault: false})
}

// DeleteErrorMessage handles DELETE /v0/management/error-messages/:code.
// Removes the operator override for a code. The proxy then falls back to
// the curated default for that status code on subsequent responses.
func (h *Handler) DeleteErrorMessage(c *gin.Context) {
	emStore := h.errorMessageStore()
	if emStore == nil {
		h.pgNotConfigured(c)
		return
	}
	code, err := parseStatusCodeParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if err := emStore.Delete(c.Request.Context(), code); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	errormessages.Invalidate()
	d := errormessages.Default(code)
	c.JSON(http.StatusOK, gin.H{
		"deleted":  true,
		"code":     code,
		"fallback": ErrorMessageDTO{Message: d, IsDefault: true},
	})
}

// PreviewErrorMessage handles POST /v0/management/error-messages/preview.
// Body: { "code": 429, "title": "...", "message": "...", "body_template": "...",
//
//	"details": "..." }
//
// Returns the rendered JSON shape (exactly what the proxy would emit)
// without persisting anything. Useful for the dashboard's live preview pane.
// The preview route does not require PG to be configured — operators can
// try out edits before enabling the backend.
func (h *Handler) PreviewErrorMessage(c *gin.Context) {
	var req struct {
		Code         int    `json:"code"`
		Title        string `json:"title"`
		Message      string `json:"message"`
		BodyTemplate string `json:"body_template"`
		Details      string `json:"details"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if req.Code < 100 || req.Code > 599 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "code must be between 100 and 599",
		}})
		return
	}
	base := errormessages.Default(req.Code)
	if req.Title == "" {
		req.Title = base.Title
	}
	if req.Message == "" {
		req.Message = base.Message
	}
	m := errormessages.Message{
		StatusCode:   req.Code,
		Title:        req.Title,
		Message:      req.Message,
		BodyTemplate: req.BodyTemplate,
		Enabled:      true,
	}
	rendered := renderErrorMessagePreview(m, req.Details)
	c.JSON(http.StatusOK, gin.H{
		"status_code": req.Code,
		"input":       m,
		"rendered":    rendered,
	})
}

// errorMessageStore resolves the PG-backed store from handler state.
// Returns nil when PG is not configured — callers should treat that as
// "feature disabled" and respond with 503.
func (h *Handler) errorMessageStore() errormessages.Store {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pgErrorMessages
}

// parseStatusCodeParam parses the :code path parameter and validates the
// 100-599 range enforced by PostgreSQL.
func parseStatusCodeParam(c *gin.Context) (int, error) {
	raw := c.Param("code")
	code, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("status code must be a number")
	}
	if code < 100 || code > 599 {
		return 0, errors.New("status code must be between 100 and 599")
	}
	return code, nil
}

// renderErrorMessagePreview is a thin wrapper around the registry's internal
// renderBody, kept as a helper so the preview handler does not need to be
// wired into the gin context path (it operates on raw input).
func renderErrorMessagePreview(m errormessages.Message, details string) gin.H {
	// Inline copy of errormessages.renderBody since the package keeps it
	// unexported. The two implementations MUST stay in sync — if the
	// registry's render semantics change, this helper needs the same update.
	return gin.H{
		"error": gin.H{
			"type":    slugForPreview(m.StatusCode),
			"code":    m.StatusCode,
			"title":   m.Title,
			"message": substitutePreview(m.Message, details, m),
		},
		// Note: body_template rendering is performed by the registry at
		// response time; the preview pane cannot evaluate JSON templates
		// faithfully without reproducing the substitution+parse logic, so
		// we surface the default body shape only and let the dashboard show
		// the raw template as a code preview.
	}
}

func slugForPreview(code int) string {
	// Mirror errormessages.slugFor at the package boundary.
	switch code {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 402:
		return "quota_exceeded"
	case 403:
		return "permission_denied"
	case 404:
		return "not_found_error"
	case 405:
		return "method_not_allowed"
	case 408:
		return "timeout_error"
	case 409:
		return "conflict_error"
	case 429:
		return "rate_limit_exceeded"
	case 500:
		return "internal_error"
	case 502:
		return "upstream_error"
	case 503:
		return "service_unavailable"
	case 504:
		return "gateway_timeout"
	default:
		return "error"
	}
}

func substitutePreview(s, details string, m errormessages.Message) string {
	if s == "" {
		return s
	}
	out := s
	out = replaceAll(out, "{{details}}", details)
	out = replaceAll(out, "{{code}}", strconv.Itoa(m.StatusCode))
	out = replaceAll(out, "{{title}}", m.Title)
	out = replaceAll(out, "{{message}}", m.Message)
	return out
}

func replaceAll(s, old, new string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
