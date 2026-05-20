package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

// GetAPIKeyUsage returns the per-(provider, baseURL, apiKey) usage view
// consumed by the management UI's "AI Providers" page. The shape is:
//
//	{
//	  "<provider>": {
//	    "<baseURL>|<apiKey>": {
//	      "success": int,
//	      "failed":  int,
//	      "recent_requests": [{"time": RFC3339, "success": int, "failed": int}, ...]
//	    }
//	  }
//	}
//
// Provider keys are lowercased; baseURL is empty when the auth has no
// per-key upstream override (matches the frontend's sp("", apiKey)).
func (h *Handler) GetAPIKeyUsage(c *gin.Context) {
	plugin := usage.GetAPIKeyUsagePlugin()
	c.JSON(http.StatusOK, plugin.Snapshot())
}
