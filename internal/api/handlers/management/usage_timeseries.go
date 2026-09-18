package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

// GetUsageTimeseries returns zero-filled usage buckets aligned to the requested
// interval and timezone.
func (h *Handler) GetUsageTimeseries(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	step := strings.ToLower(strings.TrimSpace(c.DefaultQuery("step", "hour")))
	if step != "hour" && step != "day" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid step, expected hour or day"})
		return
	}

	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	if step == "day" {
		from = to.AddDate(0, 0, -7)
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

	buckets, err := usagestats.QueryTimeseries(usagestats.Dir(h.logDirectory()), from, to, step, usagestats.Filter{
		Provider: c.Query("provider"),
		Model:    c.Query("model"),
		Account:  c.Query("account"),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"from":    from.Format(time.RFC3339Nano),
		"to":      to.Format(time.RFC3339Nano),
		"step":    step,
		"buckets": buckets,
	})
}
