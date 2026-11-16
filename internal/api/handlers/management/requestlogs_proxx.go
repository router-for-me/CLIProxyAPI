package management

// proxx: request log viewer endpoints (ADR 0006).
// Routes are mounted under /v8/management/proxx/requests by
// internal/api/server_management_proxx_requestlogs.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/requestlogs"
	log "github.com/sirupsen/logrus"
)

const (
	proxxRequestLogsDefaultLimit = 50
	proxxRequestLogsMaxLimit     = 500
	proxxRequestLogsPingInterval = 15 * time.Second
)

func proxxQueryInt64(c *gin.Context, key string) (int64, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": key + " must be a non-negative integer"})
		return 0, false
	}
	return n, true
}

// ListProxxRequestLogs lists request log files newest first.
// Query: limit (default 50, max 500), cursor, status (2xx|4xx|5xx|error), model, provider,
// q (case-insensitive substring over the whole file), since/until (unix ms).
func (h *Handler) ListProxxRequestLogs(c *gin.Context) {
	limit := proxxRequestLogsDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		limit = min(n, proxxRequestLogsMaxLimit)
	}
	status := strings.TrimSpace(c.Query("status"))
	if !requestlogs.ValidStatusFilter(status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be one of 2xx, 4xx, 5xx, error"})
		return
	}
	since, ok := proxxQueryInt64(c, "since")
	if !ok {
		return
	}
	until, ok := proxxQueryInt64(c, "until")
	if !ok {
		return
	}
	store := requestlogs.Default()
	page, err := store.List(requestlogs.Query{
		Limit:    limit,
		Cursor:   strings.TrimSpace(c.Query("cursor")),
		Status:   status,
		Model:    c.Query("model"),
		Provider: c.Query("provider"),
		Q:        c.Query("q"),
		Since:    since,
		Until:    until,
	})
	if err != nil {
		if errors.Is(err, requestlogs.ErrBadCursor) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	st := store.Status()
	items := page.Items
	if items == nil {
		items = []requestlogs.Row{}
	}
	c.JSON(http.StatusOK, gin.H{
		"items":                items,
		"next_cursor":          page.NextCursor,
		"request_log_enabled":  st.Enabled,
		"total_size_bytes":     st.TotalBytes,
		"max_total_size_bytes": st.MaxTotalBytes,
		"log_dir":              st.Dir,
	})
}

func proxxRequestLogError(c *gin.Context, err error) {
	if errors.Is(err, requestlogs.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "request log not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// GetProxxRequestLog returns the parsed detail of one request log.
func (h *Handler) GetProxxRequestLog(c *gin.Context) {
	detail, err := requestlogs.Default().Detail(c.Param("id"))
	if err != nil {
		proxxRequestLogError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// GetProxxRequestLogRaw returns the request log file as text/plain.
func (h *Handler) GetProxxRequestLogRaw(c *gin.Context) {
	path, err := requestlogs.Default().RawPath(c.Param("id"))
	if err != nil {
		proxxRequestLogError(c, err)
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.File(path)
}

// DeleteProxxRequestLog removes one request log.
func (h *Handler) DeleteProxxRequestLog(c *gin.Context) {
	if err := requestlogs.Default().Delete(c.Param("id")); err != nil {
		proxxRequestLogError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"removed": 1})
}

// DeleteProxxRequestLogs removes every request log file (app logs are kept).
func (h *Handler) DeleteProxxRequestLogs(c *gin.Context) {
	removed, err := requestlogs.Default().DeleteAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "removed": removed})
		return
	}
	c.JSON(http.StatusOK, gin.H{"removed": removed})
}

// StreamProxxRequestLogs streams a Row ("event: request") for each newly completed request log
// as Server-Sent Events, with a ": ping" comment every 15s.
func (h *Handler) StreamProxxRequestLogs(c *gin.Context) {
	store := requestlogs.Default()
	rows, cancel := store.Subscribe()
	defer cancel()
	// Mark files that already exist as known before streaming starts.
	store.Refresh()

	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	ctx := c.Request.Context()
	flush := func() bool {
		c.Writer.Flush()
		return ctx.Err() == nil
	}
	if _, err := fmt.Fprint(c.Writer, ": connected\n\n"); err != nil || !flush() {
		return
	}
	ping := time.NewTicker(proxxRequestLogsPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil || !flush() {
				return
			}
		case row := <-rows:
			data, err := json.Marshal(row)
			if err != nil {
				log.WithError(err).Warn("proxx: marshal request log row")
				continue
			}
			if _, err = fmt.Fprintf(c.Writer, "event: request\ndata: %s\n\n", data); err != nil || !flush() {
				return
			}
		}
	}
}
