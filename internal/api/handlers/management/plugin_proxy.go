package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// GetPluginProxy returns the dedicated plugin-store proxy/accelerator setting
// and the current global proxy-url (for system-proxy UI display).
func (h *Handler) GetPluginProxy(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, gin.H{
			"plugin-proxy": config.PluginProxyConfig{Status: config.PluginProxyStatusNone},
			"proxy-url":    "",
			"effective":    "",
			"accelerator":  "",
		})
		return
	}

	h.mu.Lock()
	pluginProxy := config.NormalizePluginProxyConfig(h.cfg.PluginProxy)
	proxyURL := strings.TrimSpace(h.cfg.ProxyURL)
	effective := config.EffectivePluginStoreProxyURL(h.cfg)
	accelerator := config.EffectivePluginStoreAcceleratorBase(h.cfg)
	h.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"plugin-proxy": pluginProxy,
		"proxy-url":    proxyURL,
		"effective":    effective,
		"accelerator":  accelerator,
	})
}

// PutPluginProxy updates plugin-proxy url/status/accelerator.
// Status: -1=direct, 0=none/fallback, 1=custom proxy, 2=system, 3=accelerator.
// Custom (status=1) requires a valid traditional proxy URL.
// Accelerator (status=3) requires a valid https accelerator base in accelerator field.
// Switching modes retains the last values of both fields independently.
func (h *Handler) PutPluginProxy(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}

	var body struct {
		Value       *config.PluginProxyConfig `json:"value"`
		URL         *string                   `json:"url"`
		Accelerator *string                   `json:"accelerator"`
		Status      *int                      `json:"status"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.Value == nil && body.URL == nil && body.Accelerator == nil && body.Status == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	current := config.NormalizePluginProxyConfig(h.cfg.PluginProxy)
	next := current

	if body.Value != nil {
		next.URL = body.Value.URL
		next.Accelerator = body.Value.Accelerator
		next.Status = body.Value.Status
	}
	if body.URL != nil {
		next.URL = *body.URL
	}
	if body.Accelerator != nil {
		next.Accelerator = *body.Accelerator
	}
	if body.Status != nil {
		next.Status = *body.Status
	}

	normalized := config.NormalizePluginProxyConfig(next)

	// Retain last values independently so mode switches do not lose input.
	if strings.TrimSpace(normalized.URL) == "" {
		normalized.URL = strings.TrimSpace(current.URL)
	}
	if strings.TrimSpace(normalized.Accelerator) == "" {
		normalized.Accelerator = strings.TrimSpace(current.Accelerator)
	}

	switch normalized.Status {
	case config.PluginProxyStatusCustom:
		url := strings.TrimSpace(normalized.URL)
		if url == "" {
			h.mu.Unlock()
			c.JSON(http.StatusBadRequest, gin.H{"error": "plugin-proxy url is required for custom status"})
			return
		}
		setting, errParse := proxyutil.Parse(url)
		if errParse != nil || setting.Mode != proxyutil.ModeProxy {
			h.mu.Unlock()
			message := "invalid plugin-proxy url"
			if errParse != nil {
				message = errParse.Error()
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": message})
			return
		}
		normalized.URL = url
		normalized.Status = config.PluginProxyStatusCustom
	case config.PluginProxyStatusAccelerator:
		baseRaw := strings.TrimSpace(normalized.Accelerator)
		if baseRaw == "" {
			h.mu.Unlock()
			c.JSON(http.StatusBadRequest, gin.H{"error": "plugin-proxy accelerator is required for accelerator status"})
			return
		}
		base, errNormalize := pluginstore.NormalizeAcceleratorBase(baseRaw)
		if errNormalize != nil {
			h.mu.Unlock()
			c.JSON(http.StatusBadRequest, gin.H{"error": errNormalize.Error()})
			return
		}
		normalized.Accelerator = base
		normalized.Status = config.PluginProxyStatusAccelerator
	default:
		// PluginProxyStatusNone (0) and PluginProxyStatusSystem (2) require no URL validation.
		// Any future unknown status values are clamped to None by NormalizePluginProxyConfig.
	}

	h.cfg.PluginProxy = normalized
	_ = h.persistLocked(c)
	h.mu.Unlock()
}

// ValidatePluginProxyURL checks a candidate plugin-proxy URL without saving.
// Optional status selects validation mode: 3=accelerator base, otherwise traditional proxy.
func (h *Handler) ValidatePluginProxyURL(c *gin.Context) {
	var body struct {
		URL         *string `json:"url"`
		Value       *string `json:"value"`
		Accelerator *string `json:"accelerator"`
		Status      *int    `json:"status"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body", "valid": false})
		return
	}

	raw := ""
	if body.Accelerator != nil {
		raw = *body.Accelerator
	} else if body.URL != nil {
		raw = *body.URL
	} else if body.Value != nil {
		raw = *body.Value
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plugin-proxy url is required", "valid": false})
		return
	}

	status := config.PluginProxyStatusCustom
	if body.Status != nil {
		status = *body.Status
	}

	if status == config.PluginProxyStatusAccelerator {
		base, errNormalize := pluginstore.NormalizeAcceleratorBase(raw)
		if errNormalize != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errNormalize.Error(), "valid": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"valid": true, "accelerator": base, "status": config.PluginProxyStatusAccelerator})
		return
	}

	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode != proxyutil.ModeProxy {
		message := "invalid plugin-proxy url"
		if errParse != nil {
			message = errParse.Error()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": message, "valid": false})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": true, "url": raw, "status": config.PluginProxyStatusCustom})
}
