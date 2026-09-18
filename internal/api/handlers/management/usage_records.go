package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

// GetUsageRecords returns reverse-chronological per-request usage records.
func (h *Handler) GetUsageRecords(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1).Add(-time.Nanosecond)
	if raw := c.Query("date"); raw != "" {
		date, err := time.Parse(usageStatsDateLayout, strings.TrimSpace(raw))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid date, expected YYYY-MM-DD"})
			return
		}
		from = date.UTC()
		to = from.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}

	var err error
	if raw := c.Query("from"); raw != "" {
		from, err = parseUsageTime(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from: " + err.Error()})
			return
		}
	}
	if raw := c.Query("to"); raw != "" {
		to, err = parseUsageTime(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to: " + err.Error()})
			return
		}
	}
	if from.After(to) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must not be after to"})
		return
	}

	limit, err := parseNonNegativeInt(c.Query("limit"), 50)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
		return
	}
	offset, err := parseNonNegativeInt(c.Query("offset"), 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
		return
	}

	var failed *bool
	if raw := c.Query("failed"); raw != "" {
		value, errParse := strconv.ParseBool(raw)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid failed, expected true or false"})
			return
		}
		failed = &value
	}

	page, err := usagestats.QueryRecords(usagestats.Dir(h.logDirectory()), from, to, usagestats.RecordFilter{
		Provider: c.Query("provider"),
		Model:    c.Query("model"),
		Account:  c.Query("account"),
		Failed:   failed,
	}, offset, limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"from":        from.Format(time.RFC3339Nano),
		"to":          to.Format(time.RFC3339Nano),
		"records":     page.Records,
		"has_more":    page.HasMore,
		"next_offset": page.NextOff,
	})
}

func parseNonNegativeInt(raw string, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("value must be non-negative")
	}
	return value, nil
}
