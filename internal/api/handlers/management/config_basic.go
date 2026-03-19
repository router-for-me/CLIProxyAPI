package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	latestReleaseURL       = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"
	latestReleaseUserAgent = "CLIProxyAPI"
)

func (h *Handler) GetConfig(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config snapshot failed", "message": err.Error()})
		return
	}
	if cfg == nil {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

// GetLatestVersion returns the latest release version from GitHub without downloading assets.
func (h *Handler) GetLatestVersion(c *gin.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	cfg, err := h.configSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config snapshot failed", "message": err.Error()})
		return
	}
	proxyURL := ""
	if cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if proxyURL != "" {
		sdkCfg := &sdkconfig.SDKConfig{ProxyURL: proxyURL}
		util.SetProxy(sdkCfg, client)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "request_create_failed", "message": err.Error()})
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", latestReleaseUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "request_failed", "message": err.Error()})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close latest version response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected_status", "message": fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))})
		return
	}

	var info releaseInfo
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "decode_failed", "message": errDecode.Error()})
		return
	}

	version := strings.TrimSpace(info.TagName)
	if version == "" {
		version = strings.TrimSpace(info.Name)
	}
	if version == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid_response", "message": "missing release version"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"latest-version": version})
}

func WriteConfig(path string, data []byte) error {
	data = config.NormalizeCommentIndentation(data)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, errWrite := f.Write(data); errWrite != nil {
		_ = f.Close()
		return errWrite
	}
	if errSync := f.Sync(); errSync != nil {
		_ = f.Close()
		return errSync
	}
	return f.Close()
}

func (h *Handler) PutConfigYAML(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_yaml", "message": "cannot read request body"})
		return
	}
	var parsed config.Config
	if err = yaml.Unmarshal(body, &parsed); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_yaml", "message": err.Error()})
		return
	}
	if parsed.LogsMaxTotalSizeMB > config.MaxLogsMaxTotalSizeMB {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logs-max-total-size-mb exceeds allowed maximum"})
		return
	}

	snapshot, err := h.runtimeSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config snapshot failed", "message": err.Error()})
		return
	}

	tmpDir := filepath.Dir(snapshot.configFilePath)
	tmpFile, err := os.CreateTemp(tmpDir, "config-validate-*.yaml")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	tempFile := tmpFile.Name()
	if _, errWrite := tmpFile.Write(body); errWrite != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tempFile)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": errWrite.Error()})
		return
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tempFile)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": errClose.Error()})
		return
	}
	defer func() {
		_ = os.Remove(tempFile)
	}()

	if _, err = config.LoadConfigOptional(tempFile, false); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if err := WriteConfig(snapshot.configFilePath, body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": "failed to write config"})
		return
	}
	newCfg, err := config.LoadConfig(snapshot.configFilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reload_failed", "message": err.Error()})
		return
	}
	h.stateMu.Lock()
	h.cfg = newCfg
	h.stateMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true, "changed": []string{"config"}})
}

// GetConfigYAML returns the raw config.yaml file bytes without re-encoding.
// It preserves comments and original formatting/styles.
func (h *Handler) GetConfigYAML(c *gin.Context) {
	snapshot, err := h.runtimeSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config snapshot failed", "message": err.Error()})
		return
	}
	data, err := os.ReadFile(snapshot.configFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "config file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed", "message": err.Error()})
		return
	}
	c.Header("Content-Type", "application/yaml; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	_, _ = c.Writer.Write(data)
}

// Debug
func (h *Handler) GetDebug(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"debug": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"debug": cfg.Debug})
}

func (h *Handler) PutDebug(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.Debug = v })
}

// UsageStatisticsEnabled
func (h *Handler) GetUsageStatisticsEnabled(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"usage-statistics-enabled": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"usage-statistics-enabled": cfg.UsageStatisticsEnabled})
}

func (h *Handler) PutUsageStatisticsEnabled(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.UsageStatisticsEnabled = v })
}

// LoggingToFile
func (h *Handler) GetLoggingToFile(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"logging-to-file": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logging-to-file": cfg.LoggingToFile})
}

func (h *Handler) PutLoggingToFile(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.LoggingToFile = v })
}

// LogsMaxTotalSizeMB
func (h *Handler) GetLogsMaxTotalSizeMB(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"logs-max-total-size-mb": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs-max-total-size-mb": cfg.LogsMaxTotalSizeMB})
}

func (h *Handler) PutLogsMaxTotalSizeMB(c *gin.Context) {
	h.updateIntField(c, func(cfg *config.Config, value int) error {
		if value < 0 {
			value = 0
		}
		if value > config.MaxLogsMaxTotalSizeMB {
			return fmt.Errorf("logs-max-total-size-mb exceeds allowed maximum")
		}
		cfg.LogsMaxTotalSizeMB = value
		return nil
	})
}

// ErrorLogsMaxFiles
func (h *Handler) GetErrorLogsMaxFiles(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"error-logs-max-files": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"error-logs-max-files": cfg.ErrorLogsMaxFiles})
}

func (h *Handler) PutErrorLogsMaxFiles(c *gin.Context) {
	h.updateIntField(c, func(cfg *config.Config, value int) error {
		if value < 0 {
			value = 10
		}
		cfg.ErrorLogsMaxFiles = value
		return nil
	})
}

// Request log
func (h *Handler) GetRequestLog(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"request-log": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"request-log": cfg.RequestLog})
}

func (h *Handler) PutRequestLog(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.RequestLog = v })
}

// Websocket auth
func (h *Handler) GetWebsocketAuth(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"ws-auth": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ws-auth": cfg.WebsocketAuth})
}

func (h *Handler) PutWebsocketAuth(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.WebsocketAuth = v })
}

// Request retry
func (h *Handler) GetRequestRetry(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"request-retry": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"request-retry": cfg.RequestRetry})
}

func (h *Handler) PutRequestRetry(c *gin.Context) {
	h.updateIntField(c, func(cfg *config.Config, v int) error {
		cfg.RequestRetry = v
		return nil
	})
}

// Max retry interval
func (h *Handler) GetMaxRetryInterval(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"max-retry-interval": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"max-retry-interval": cfg.MaxRetryInterval})
}

func (h *Handler) PutMaxRetryInterval(c *gin.Context) {
	h.updateIntField(c, func(cfg *config.Config, v int) error {
		cfg.MaxRetryInterval = v
		return nil
	})
}

// ForceModelPrefix
func (h *Handler) GetForceModelPrefix(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"force-model-prefix": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"force-model-prefix": cfg.ForceModelPrefix})
}

func (h *Handler) PutForceModelPrefix(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.ForceModelPrefix = v })
}

func normalizeRoutingStrategy(strategy string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	switch normalized {
	case "", "round-robin", "roundrobin", "rr":
		return "round-robin", true
	case "fill-first", "fillfirst", "ff":
		return "fill-first", true
	default:
		return "", false
	}
}

// RoutingStrategy
func (h *Handler) GetRoutingStrategy(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"strategy": ""})
		return
	}
	strategy, ok := normalizeRoutingStrategy(cfg.Routing.Strategy)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"strategy": strings.TrimSpace(cfg.Routing.Strategy)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"strategy": strategy})
}

func (h *Handler) PutRoutingStrategy(c *gin.Context) {
	var body struct {
		Value *string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	normalized, ok := normalizeRoutingStrategy(*body.Value)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid strategy"})
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.Routing.Strategy = normalized
		return nil
	})
}

// Proxy URL
func (h *Handler) GetProxyURL(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(http.StatusOK, gin.H{"proxy-url": ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"proxy-url": cfg.ProxyURL})
}

func (h *Handler) PutProxyURL(c *gin.Context) {
	h.updateStringField(c, func(cfg *config.Config, v string) { cfg.ProxyURL = v })
}

func (h *Handler) DeleteProxyURL(c *gin.Context) {
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.ProxyURL = ""
		return nil
	})
}
