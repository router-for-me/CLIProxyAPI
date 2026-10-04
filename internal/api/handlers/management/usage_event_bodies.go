package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pii"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// bodySection is one captured payload rendered for the dashboard.
type bodySection struct {
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

// eventBodiesResponse is the payload returned by
// GET /usage-stats/events/:id/bodies.
type eventBodiesResponse struct {
	Available          bool         `json:"available"`
	Reason             string       `json:"reason,omitempty"`
	Provider           string       `json:"provider,omitempty"`
	UpstreamProviderID int64        `json:"upstream_provider_id,omitempty"`
	CapturedAt         *time.Time   `json:"captured_at,omitempty"`
	Truncated          bool         `json:"truncated"`
	ClientRequest      *bodySection `json:"client_request,omitempty"`
	ClientResponse     *bodySection `json:"client_response,omitempty"`
	UpstreamRequest    string       `json:"upstream_request,omitempty"`
	UpstreamResponse   string       `json:"upstream_response,omitempty"`
}

// decodeHeaderJSON decodes a captured header JSON-text column into an object.
// An empty or malformed value yields nil so the section renders without headers
// rather than failing the whole payload.
func decodeHeaderJSON(raw string) map[string][]string {
	if raw == "" {
		return nil
	}
	var out map[string][]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// GetUsageEventBodies handles GET /v0/management/usage-stats/events/:id/bodies.
// Returns the captured request/response payloads for the event's request_id, or
// available:false with a reason when capture was off / not found.
func (h *Handler) GetUsageEventBodies(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "id must be a positive integer"}})
		return
	}
	event, err := usage.GetEvent(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrUsageEventNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "usage event not found"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	if event.RequestID == "" {
		c.JSON(http.StatusOK, eventBodiesResponse{Available: false, Reason: "no_request_id"})
		return
	}
	rb, err := usage.GetRequestBodyByRequestID(c.Request.Context(), event.RequestID)
	if err != nil {
		if errors.Is(err, store.ErrRequestBodyNotFound) {
			c.JSON(http.StatusOK, eventBodiesResponse{Available: false, Reason: "not_captured"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Default to redacting PII unless the caller explicitly passes redact=false.
	doRedact := c.DefaultQuery("redact", "true") != "false"

	capturedAt := rb.CreatedAt
	resp := eventBodiesResponse{
		Available:          true,
		Provider:           rb.Provider,
		UpstreamProviderID: rb.UpstreamProviderID,
		CapturedAt:         &capturedAt,
		Truncated:          rb.Truncated,
	}

	if doRedact {
		resp.ClientRequest = redactSection(rb.ClientRequestHeaders, rb.ClientRequestBody)
		resp.ClientResponse = redactSection(rb.ClientResponseHeaders, rb.ClientResponseBody)
		resp.UpstreamRequest = pii.RedactPII(rb.UpstreamRequest)
		resp.UpstreamResponse = pii.RedactPII(rb.UpstreamResponse)
	} else {
		resp.UpstreamRequest = rb.UpstreamRequest
		resp.UpstreamResponse = rb.UpstreamResponse
		if rb.ClientRequestHeaders != "" || rb.ClientRequestBody != "" {
			resp.ClientRequest = &bodySection{Headers: decodeHeaderJSON(rb.ClientRequestHeaders), Body: rb.ClientRequestBody}
		}
		if rb.ClientResponseHeaders != "" || rb.ClientResponseBody != "" {
			resp.ClientResponse = &bodySection{Headers: decodeHeaderJSON(rb.ClientResponseHeaders), Body: rb.ClientResponseBody}
		}
	}
	c.JSON(http.StatusOK, resp)
}

func redactSection(headersRaw, body string) *bodySection {
	out := &bodySection{}
	if headersRaw != "" {
		out.Headers = pii.RedactHeaders(decodeHeaderJSON(headersRaw))
	}
	if body != "" {
		out.Body = pii.RedactPII(body)
	}
	if out.Headers == nil && out.Body == "" {
		return nil
	}
	return out
}
