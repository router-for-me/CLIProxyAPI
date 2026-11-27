package engine

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/ignite"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/notify"
)

func (e *Engine) accountLabel(authID string) string {
	r := e.activeRouter()
	if r == nil {
		return authID
	}
	accounts := r.Accounts()
	if l, ok := notify.Labels(accounts)[authID]; ok {
		return l
	}
	return authID
}

func ignitionMessage(ev ignite.Event, label string) notify.Message {
	icon, level := "⚠️", "warning"
	title := ""
	switch ev.Kind {
	case ignite.EventCredential:
		icon, level, title = "🔑", "critical", "credential rejected, ignition stopped"
	case ignite.EventCredentialOK:
		icon, level, title = "✅", "info", "credential works again, ignition resumed"
	case ignite.EventRateLimited:
		icon, title = "⏳", "rate limited, ignition paused"
	case ignite.EventNoWindow:
		title = "ignition did not start a window"
	case ignite.EventNoModel:
		title = "no usable ignition model"
	case ignite.EventPaused:
		title = "ignition paused after repeated failures"
	default:
		title = ev.Kind
	}
	return notify.Message{Title: fmt.Sprintf("%s %s · %s", icon, label, title), Body: ev.Detail, Level: level}
}

// handleExtra serves the prox-only API routes that are not part of the
// ported quota-router management server. It reports whether it handled c.
func (e *Engine) handleExtra(c *gin.Context) bool {
	path := strings.TrimPrefix(c.Request.URL.Path, APIBase)
	e.mu.Lock()
	ign, ntf := e.ign, e.ntf
	e.mu.Unlock()
	switch {
	case path == "/ignition" && c.Request.Method == http.MethodGet:
		if ign == nil {
			c.JSON(http.StatusOK, gin.H{"enabled": false, "plans": []any{}, "events": []any{}})
			return true
		}
		c.JSON(http.StatusOK, gin.H{"now": e.now(), "plans": ign.Plans(), "events": ign.Events(100)})
		return true
	case path == "/ignition/fire" && c.Request.Method == http.MethodPost:
		var body struct {
			AuthID string `json:"auth_id"`
		}
		raw, _ := io.ReadAll(io.LimitReader(c.Request.Body, 4096))
		if json.Unmarshal(raw, &body) != nil || strings.TrimSpace(body.AuthID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id is required"})
			return true
		}
		if !e.IgniteNow(strings.TrimSpace(body.AuthID)) {
			c.JSON(http.StatusConflict, gin.H{"error": "account unknown, not ignitable, or already firing"})
			return true
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
		return true
	case path == "/notify/log" && c.Request.Method == http.MethodGet:
		if ntf == nil {
			c.JSON(http.StatusOK, gin.H{"entries": []any{}})
			return true
		}
		c.JSON(http.StatusOK, gin.H{"entries": ntf.Log()})
		return true
	case path == "/notify/test" && c.Request.Method == http.MethodPost:
		if ntf == nil || !ntf.Configured() {
			c.JSON(http.StatusConflict, gin.H{"error": "no notification channel configured"})
			return true
		}
		ntf.Enqueue(notify.Message{Title: "🔔 prox test notification", Body: "Sent " + e.now().Format(time.RFC1123), Level: "info"})
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
		return true
	}
	return false
}
