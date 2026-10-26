package management

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// GetRoutingStatus reports the installed strategy, its in-memory reset
// observations and the account-level routing order computed by the selector.
func (h *Handler) GetRoutingStatus(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	c.JSON(http.StatusOK, h.authManager.QuotaRoutingSnapshot())
}

// PutRoutingResetHints merges quota observations into the active selector's
// in-memory state. Earlier observations remain in use until their reset passes
// or a newer observation replaces them, so failed quota reads do not discard a
// known reset. The v8 management middleware authenticates this operation. It
// never writes credential files or changes priorities/availability.
func (h *Handler) PutRoutingResetHints(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var request struct {
		Accounts []struct {
			AuthIndex  string    `json:"auth_index"`
			ObservedAt time.Time `json:"observed_at"`
			ResetAt    time.Time `json:"reset_at"`
		} `json:"accounts"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Accounts == nil || len(request.Accounts) > 1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "accounts must be an array of at most 1024 reset observations"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected one JSON object"})
		return
	}

	now := time.Now()
	hints := make(map[string]coreauth.QuotaResetHint, len(request.Accounts))
	seen := make(map[string]bool, len(request.Accounts))
	for _, account := range request.Accounts {
		index := strings.TrimSpace(account.AuthIndex)
		if index == "" || seen[index] || account.ObservedAt.IsZero() || account.ResetAt.IsZero() ||
			account.ObservedAt.After(now) || !account.ResetAt.After(account.ObservedAt) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or duplicate reset observation"})
			return
		}
		seen[index] = true
		auth := h.authByIndex(index)
		// An account can disappear during a collector poll. Drop that entry;
		// never associate it by file name or expose inventory in the response.
		if auth == nil || !account.ResetAt.After(now) {
			continue
		}
		hints[auth.ID] = coreauth.QuotaResetHint{ObservedAt: account.ObservedAt, ResetAt: account.ResetAt}
	}
	accepted, err := h.authManager.MergeQuotaResetHints(hints)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "soonest-reset-first routing is not active"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "accepted": accepted})
}
