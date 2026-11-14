package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	latestReleaseURL       = "https://api.github.com/repos/router-for-me/CLIProxyAPIPlus/releases/latest"
	latestReleaseUserAgent = "CLIProxyAPIPlus"
)

func (h *Handler) GetConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{})
		return
	}
	c.JSON(200, new(*h.cfg))
}

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

func setLatestReleaseRequestHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", latestReleaseUserAgent)
	if token := util.ResolveGitHubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// GetLatestVersion returns the latest release version from GitHub without downloading assets.
func (h *Handler) GetLatestVersion(c *gin.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	proxyURL := ""
	if h != nil && h.cfg != nil {
		proxyURL = strings.TrimSpace(h.cfg.ProxyURL)
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
	setLatestReleaseRequestHeaders(req)

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
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) > 0 && config.IsV8ConfigLayout(doc.Content[0]) {
		var err error
		data, _, err = config.NormalizeConfigLayout(data, true)
		if err != nil {
			return err
		}
	}
	data = config.NormalizeCommentIndentation(data)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
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

func restoreConfigFile(path string, previous []byte, existed bool) error {
	if existed {
		return WriteConfig(path, previous)
	}
	if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) {
		return fmt.Errorf("remove newly created config after failure: %w", errRemove)
	}
	directory, errOpen := os.Open(filepath.Dir(path))
	if errOpen != nil {
		return fmt.Errorf("open config directory after rollback: %w", errOpen)
	}
	errSync := directory.Sync()
	errClose := directory.Close()
	if errSync != nil {
		errSync = fmt.Errorf("sync config rollback: %w", errSync)
	}
	if errClose != nil {
		errClose = fmt.Errorf("close config directory after rollback: %w", errClose)
	}
	return errors.Join(errSync, errClose)
}

func (h *Handler) PutConfigYAML(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_yaml", "message": "cannot read request body"})
		return
	}
	h.mu.Lock()
	publish := h.configPublishHook
	h.mu.Unlock()
	if publish != nil {
		ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
		ifNoneMatch := strings.TrimSpace(c.GetHeader("If-None-Match"))
		var expected *modelrouting.ActiveIdentityV2
		bootstrap := false
		switch {
		case ifNoneMatch == "*" && ifMatch == "":
			bootstrap = true
		case ifNoneMatch == "" && ifMatch != "":
			expected, err = modelrouting.ParseActiveETag(ifMatch)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_precondition", "message": err.Error()})
				return
			}
		default:
			c.JSON(http.StatusPreconditionRequired, gin.H{"error": "precondition_required", "message": "use exactly one of If-None-Match: * or If-Match: <active identity>"})
			return
		}
		published, receipt, errPublish := publish(c.Request.Context(), body, expected, bootstrap)
		if errPublish != nil {
			status := http.StatusInternalServerError
			code := "publication_failed"
			switch {
			case errors.Is(errPublish, modelrouting.ErrCASConflict):
				status, code = http.StatusPreconditionFailed, "cas_conflict"
			case errors.Is(errPublish, modelrouting.ErrInvalidPublication):
				status, code = http.StatusUnprocessableEntity, "invalid_publication"
			}
			c.JSON(status, gin.H{"error": code, "message": errPublish.Error()})
			return
		}
		if published == nil || receipt == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "publication_failed", "message": "publisher returned an incomplete result"})
			return
		}
		etag, errETag := modelrouting.ActiveETag(receipt.Active)
		if errETag != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "receipt_failed", "message": errETag.Error()})
			return
		}
		h.mu.Lock()
		h.cfg = published
		h.mu.Unlock()
		c.Header("ETag", etag)
		c.JSON(http.StatusOK, receipt)
		return
	}
	parsed, err := config.ParseConfigBytes(body)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if parsed.ModelRouting != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "publisher_unavailable", "message": "model-routing publication requires the Service-owned CAS publisher"})
		return
	}
	persistedBody := body
	if parsed.RemoteManagement.SecretKey != "" {
		persistedBody, err = config.UpdateNestedScalarBytes(
			body,
			[]string{"remote-management", "secret-key"},
			parsed.RemoteManagement.SecretKey,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "secret_normalization_failed", "message": err.Error()})
			return
		}
	}
	h.mu.Lock()
	previousBody, previousReadErr := os.ReadFile(h.configFilePath)
	if previousReadErr != nil && !os.IsNotExist(previousReadErr) {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed", "message": previousReadErr.Error()})
		return
	}
	previousCfg := h.cfg
	previousExisted := previousReadErr == nil
	if err = WriteConfig(h.configFilePath, persistedBody); err != nil {
		errRollback := restoreConfigFile(h.configFilePath, previousBody, previousExisted)
		h.mu.Unlock()
		if errRollback != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "rollback_failed", "message": errors.Join(err, errRollback).Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	h.cfg = parsed
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	if err = h.reloadConfigAfterManagementSave(c.Request.Context(), snapshot); err != nil {
		errRollback := restoreConfigFile(h.configFilePath, previousBody, previousExisted)
		h.mu.Lock()
		h.cfg = previousCfg
		recoverySnapshot := h.reloadSnapshotConfigLocked()
		h.mu.Unlock()
		errRecoveryReload := h.reloadConfigAfterManagementSave(c.Request.Context(), recoverySnapshot)
		if errRollback != nil || errRecoveryReload != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "rollback_failed",
				"message": errors.Join(err, errRollback, errRecoveryReload).Error(),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reload_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "changed": []string{"config"}})
}

// GetConfigYAML returns the raw config.yaml file bytes without re-encoding.
// It preserves comments and original formatting/styles.
func (h *Handler) GetConfigYAML(c *gin.Context) {
	data, err := os.ReadFile(h.configFilePath)
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
	// Write raw bytes as-is
	_, _ = c.Writer.Write(data)
}

// Debug
func (h *Handler) GetDebug(c *gin.Context) { c.JSON(200, gin.H{"debug": h.cfg.Debug}) }
func (h *Handler) PutDebug(c *gin.Context) { h.updateBoolField(c, func(v bool) { h.cfg.Debug = v }) }

// UsageStatisticsEnabled
func (h *Handler) GetUsageStatisticsEnabled(c *gin.Context) {
	c.JSON(200, gin.H{"usage-statistics-enabled": h.cfg.UsageStatisticsEnabled})
}
func (h *Handler) PutUsageStatisticsEnabled(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.UsageStatisticsEnabled = v })
}

// UsageStatisticsEnabled
func (h *Handler) GetLoggingToFile(c *gin.Context) {
	c.JSON(200, gin.H{"logging-to-file": h.cfg.LoggingToFile})
}
func (h *Handler) PutLoggingToFile(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.LoggingToFile = v })
}

// LogsMaxTotalSizeMB
func (h *Handler) GetLogsMaxTotalSizeMB(c *gin.Context) {
	c.JSON(200, gin.H{"logs-max-total-size-mb": h.cfg.LogsMaxTotalSizeMB})
}
func (h *Handler) PutLogsMaxTotalSizeMB(c *gin.Context) {
	var body struct {
		Value *int `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	value := *body.Value
	if value < 0 {
		value = 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.LogsMaxTotalSizeMB = value
	h.persistLocked(c)
}

// ErrorLogsMaxFiles
func (h *Handler) GetErrorLogsMaxFiles(c *gin.Context) {
	c.JSON(200, gin.H{"error-logs-max-files": h.cfg.ErrorLogsMaxFiles})
}
func (h *Handler) PutErrorLogsMaxFiles(c *gin.Context) {
	var body struct {
		Value *int `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	value := *body.Value
	if value < 0 {
		value = 10
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.ErrorLogsMaxFiles = value
	h.persistLocked(c)
}

// Request log
func (h *Handler) GetRequestLog(c *gin.Context) { c.JSON(200, gin.H{"request-log": h.cfg.RequestLog}) }
func (h *Handler) PutRequestLog(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.RequestLog = v })
}

// Websocket auth
func (h *Handler) GetWebsocketAuth(c *gin.Context) {
	c.JSON(200, gin.H{"ws-auth": h.cfg.WebsocketAuth})
}
func (h *Handler) PutWebsocketAuth(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.WebsocketAuth = v })
}

// Request retry
func (h *Handler) GetRequestRetry(c *gin.Context) {
	c.JSON(200, gin.H{"request-retry": h.cfg.RequestRetry})
}
func (h *Handler) PutRequestRetry(c *gin.Context) {
	h.updateIntField(c, func(v int) { h.cfg.RequestRetry = v })
}

// Max retry credentials
func (h *Handler) GetMaxRetryCredentials(c *gin.Context) {
	c.JSON(200, gin.H{"max-retry-credentials": h.cfg.MaxRetryCredentials})
}
func (h *Handler) PutMaxRetryCredentials(c *gin.Context) {
	h.updateIntField(c, func(v int) { h.cfg.MaxRetryCredentials = v })
}

// Max retry interval
func (h *Handler) GetMaxRetryInterval(c *gin.Context) {
	c.JSON(200, gin.H{"max-retry-interval": h.cfg.MaxRetryInterval})
}
func (h *Handler) PutMaxRetryInterval(c *gin.Context) {
	h.updateIntField(c, func(v int) { h.cfg.MaxRetryInterval = v })
}

// ForceModelPrefix
func (h *Handler) GetForceModelPrefix(c *gin.Context) {
	c.JSON(200, gin.H{"force-model-prefix": h.cfg.ForceModelPrefix})
}
func (h *Handler) PutForceModelPrefix(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.ForceModelPrefix = v })
}

func normalizeRoutingStrategy(strategy string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	switch normalized {
	case "", "round-robin", "roundrobin", "rr":
		return "round-robin", true
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return "weighted-round-robin", true
	case "fill-first", "fillfirst", "ff":
		return "fill-first", true
	default:
		return "", false
	}
}

// RoutingStrategy
func (h *Handler) GetRoutingStrategy(c *gin.Context) {
	strategy, ok := normalizeRoutingStrategy(h.cfg.Routing.Strategy)
	if !ok {
		c.JSON(200, gin.H{"strategy": strings.TrimSpace(h.cfg.Routing.Strategy)})
		return
	}
	c.JSON(200, gin.H{"strategy": strategy})
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
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.Routing.Strategy = normalized
	h.persistLocked(c)
}

// Proxy URL
func (h *Handler) GetProxyURL(c *gin.Context) { c.JSON(200, gin.H{"proxy-url": h.cfg.ProxyURL}) }
func (h *Handler) PutProxyURL(c *gin.Context) {
	h.updateStringField(c, func(v string) { h.cfg.ProxyURL = v })
}
func (h *Handler) DeleteProxyURL(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.ProxyURL = ""
	h.persistLocked(c)
}
