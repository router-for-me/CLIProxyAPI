package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type notificationWebhooksRequest struct {
	Webhooks []config.NotificationWebhookConfig `json:"webhooks"`
	Items    []config.NotificationWebhookConfig `json:"items"`
}

// GetNotificationWebhooks returns the configured outbound notification webhooks.
func (h *Handler) GetNotificationWebhooks(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"webhooks": append([]config.NotificationWebhookConfig(nil), h.cfg.Notifications.Webhooks...)})
}

// PutNotificationWebhooks replaces the outbound notification webhook list.
func (h *Handler) PutNotificationWebhooks(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}

	webhooks, err := decodeNotificationWebhooks(data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body", "message": err.Error()})
		return
	}
	webhooks, err = normalizeAndValidateNotificationWebhooks(webhooks)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webhook", "message": err.Error()})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.Notifications.Webhooks = webhooks
	h.persistLocked(c)
}

// PatchNotificationWebhook replaces a single webhook by index or name.
func (h *Handler) PatchNotificationWebhook(c *gin.Context) {
	var body struct {
		Index *int                              `json:"index"`
		Name  *string                           `json:"name"`
		Value *config.NotificationWebhookConfig `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.Notifications.Webhooks) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		for i := range h.cfg.Notifications.Webhooks {
			if strings.TrimSpace(h.cfg.Notifications.Webhooks[i].Name) == name {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}

	next := append([]config.NotificationWebhookConfig(nil), h.cfg.Notifications.Webhooks...)
	next[targetIndex] = *body.Value
	webhooks, err := normalizeAndValidateNotificationWebhooks(next)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webhook", "message": err.Error()})
		return
	}

	h.cfg.Notifications.Webhooks = webhooks
	h.persistLocked(c)
}

// DeleteNotificationWebhook removes a webhook by index or name.
func (h *Handler) DeleteNotificationWebhook(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if name := strings.TrimSpace(c.Query("name")); name != "" {
		next := make([]config.NotificationWebhookConfig, 0, len(h.cfg.Notifications.Webhooks))
		for _, hook := range h.cfg.Notifications.Webhooks {
			if strings.TrimSpace(hook.Name) != name {
				next = append(next, hook)
			}
		}
		if len(next) == len(h.cfg.Notifications.Webhooks) {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		h.cfg.Notifications.Webhooks = next
		h.persistLocked(c)
		return
	}

	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.Notifications.Webhooks) {
			h.cfg.Notifications.Webhooks = append(h.cfg.Notifications.Webhooks[:idx], h.cfg.Notifications.Webhooks[idx+1:]...)
			h.persistLocked(c)
			return
		}
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "missing name or index"})
}

func decodeNotificationWebhooks(data []byte) ([]config.NotificationWebhookConfig, error) {
	var arr []config.NotificationWebhookConfig
	if err := json.Unmarshal(data, &arr); err == nil {
		if arr == nil {
			return []config.NotificationWebhookConfig{}, nil
		}
		return arr, nil
	}

	var obj notificationWebhooksRequest
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	if obj.Webhooks != nil {
		return obj.Webhooks, nil
	}
	if obj.Items != nil {
		return obj.Items, nil
	}
	return nil, fmt.Errorf("missing webhooks")
}

func normalizeAndValidateNotificationWebhooks(webhooks []config.NotificationWebhookConfig) ([]config.NotificationWebhookConfig, error) {
	cfg := config.Config{
		Notifications: config.NotificationsConfig{
			Webhooks: append([]config.NotificationWebhookConfig(nil), webhooks...),
		},
	}
	cfg.NormalizeNotificationsConfig()

	for i, hook := range cfg.Notifications.Webhooks {
		if hook.Disabled {
			continue
		}
		if strings.TrimSpace(hook.URL) == "" {
			return nil, fmt.Errorf("webhooks[%d].url is required when enabled", i)
		}
		if len(hook.Events) == 0 {
			return nil, fmt.Errorf("webhooks[%d].events is required when enabled", i)
		}
		switch hook.Adapter {
		case "generic", "json", "slack", "feishu", "lark", "telegram":
		default:
			return nil, fmt.Errorf("webhooks[%d].adapter is unsupported: %s", i, hook.Adapter)
		}
		if hook.Adapter == "telegram" && strings.TrimSpace(hook.Target) == "" {
			return nil, fmt.Errorf("webhooks[%d].target is required for telegram", i)
		}
	}

	return cfg.Notifications.Webhooks, nil
}
