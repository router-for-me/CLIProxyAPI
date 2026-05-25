package management

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	configVersionHeader = "X-Config-Version"
	configETagHeader    = "ETag"
)

type configSnapshot struct {
	data    []byte
	version string
	info    os.FileInfo
}

func newConfigSnapshot(data []byte, info os.FileInfo) configSnapshot {
	sum := sha256.Sum256(data)
	return configSnapshot{
		data:    data,
		version: fmt.Sprintf("sha256:%x", sum[:]),
		info:    info,
	}
}

func configETag(version string) string {
	if strings.TrimSpace(version) == "" {
		return ""
	}
	return `"` + version + `"`
}

func normalizeConfigVersionToken(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "W/") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "W/"))
	}
	value = strings.Trim(value, `"`)
	return strings.TrimSpace(value)
}

func configVersionMatches(headerValue, currentVersion string) bool {
	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		return strings.TrimSpace(headerValue) == ""
	}
	for _, part := range strings.Split(headerValue, ",") {
		token := normalizeConfigVersionToken(part)
		if token == "*" || token == currentVersion || token == configETag(currentVersion) {
			return true
		}
	}
	return false
}

func (h *Handler) readConfigSnapshot() (configSnapshot, error) {
	data, err := os.ReadFile(h.configFilePath)
	if err != nil {
		return configSnapshot{}, err
	}
	info, err := os.Stat(h.configFilePath)
	if err != nil {
		return configSnapshot{}, err
	}
	return newConfigSnapshot(data, info), nil
}

func setConfigSnapshotHeaders(c *gin.Context, snap configSnapshot) {
	if c == nil || strings.TrimSpace(snap.version) == "" {
		return
	}
	c.Header(configVersionHeader, snap.version)
	c.Header(configETagHeader, configETag(snap.version))
	c.Header("Cache-Control", "no-store")
	if snap.info != nil {
		c.Header("Last-Modified", snap.info.ModTime().UTC().Format(http.TimeFormat))
	}
}

func (h *Handler) setConfigVersionHeaders(c *gin.Context) {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		return
	}
	snap, err := h.readConfigSnapshot()
	if err != nil {
		log.WithError(err).Debug("failed to read config snapshot for version headers")
		return
	}
	setConfigSnapshotHeaders(c, snap)
}

func configWritePrecondition(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if value := strings.TrimSpace(c.GetHeader("If-Match")); value != "" {
		return value
	}
	return strings.TrimSpace(c.GetHeader(configVersionHeader))
}

func (h *Handler) reloadConfigFromDiskLocked() {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		return
	}
	cfg, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		log.WithError(err).Warn("failed to restore management config after write conflict")
		return
	}
	h.cfg = cfg
}

func (h *Handler) checkConfigWritePreconditionLocked(c *gin.Context) (configSnapshot, bool) {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		return configSnapshot{}, true
	}
	snap, err := h.readConfigSnapshot()
	if err != nil {
		if os.IsNotExist(err) {
			return configSnapshot{}, true
		}
		if c != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed", "message": err.Error()})
		}
		return configSnapshot{}, false
	}
	precondition := configWritePrecondition(c)
	if precondition == "" || configVersionMatches(precondition, snap.version) {
		return snap, true
	}

	h.reloadConfigFromDiskLocked()
	setConfigSnapshotHeaders(c, snap)
	submittedVersion := normalizeConfigVersionToken(precondition)
	conflictFields := log.Fields{
		"path":              h.configFilePath,
		"current_version":   snap.version,
		"submitted_version": submittedVersion,
		"last_modified":     configSnapshotLastModified(snap),
	}
	if c != nil && c.Request != nil {
		conflictFields["method"] = c.Request.Method
		conflictFields["route"] = c.FullPath()
		conflictFields["client_ip"] = c.ClientIP()
		conflictFields["user_agent"] = c.Request.UserAgent()
	}
	log.WithFields(conflictFields).Warn("management config write conflict")
	h.auditConfigWrite(c, "conflict", snap.version, snap.version, len(snap.data), len(snap.data), true)
	c.JSON(http.StatusConflict, gin.H{
		"error":             "config_conflict",
		"message":           "config.yaml changed after this page loaded; reload the latest config before saving",
		"current-version":   snap.version,
		"submitted-version": submittedVersion,
		"last-modified":     configSnapshotLastModified(snap),
		"method":            conflictFields["method"],
		"route":             conflictFields["route"],
		"path":              h.configFilePath,
	})
	return snap, false
}

func (h *Handler) auditConfigWrite(c *gin.Context, action, oldVersion, newVersion string, oldBytes, newBytes int, guarded bool) {
	fields := log.Fields{
		"action":       action,
		"path":         h.configFilePath,
		"old_version":  oldVersion,
		"new_version":  newVersion,
		"old_bytes":    oldBytes,
		"new_bytes":    newBytes,
		"precondition": guarded,
	}
	if c != nil && c.Request != nil {
		fields["method"] = c.Request.Method
		fields["route"] = c.FullPath()
		fields["client_ip"] = c.ClientIP()
		fields["user_agent"] = c.Request.UserAgent()
	}
	log.WithFields(fields).Info("management config write audit")
}

func (h *Handler) snapshotAfterConfigWrite(c *gin.Context) configSnapshot {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		return configSnapshot{}
	}
	snap, err := h.readConfigSnapshot()
	if err != nil {
		log.WithError(err).Warn("failed to read config snapshot after write")
		return configSnapshot{}
	}
	setConfigSnapshotHeaders(c, snap)
	return snap
}

func configVersionFromSnapshot(snap configSnapshot) string {
	return strings.TrimSpace(snap.version)
}

func configSnapshotModTime(snap configSnapshot) time.Time {
	if snap.info == nil {
		return time.Time{}
	}
	return snap.info.ModTime()
}

func configSnapshotLastModified(snap configSnapshot) string {
	modTime := configSnapshotModTime(snap)
	if modTime.IsZero() {
		return ""
	}
	return modTime.UTC().Format(http.TimeFormat)
}
