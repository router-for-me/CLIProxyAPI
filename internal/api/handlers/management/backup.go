package management

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/backup"
	log "github.com/sirupsen/logrus"
)

// GetBackupConfig returns the current backup configuration.
func (h *Handler) GetBackupConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	c.JSON(http.StatusOK, h.cfg.Backup)
}

// PutBackupConfig updates the backup configuration.
func (h *Handler) PutBackupConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	var newConfig backup.Config
	if err := c.ShouldBindJSON(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "message": err.Error()})
		return
	}

	h.mu.Lock()
	h.cfg.Backup.Enabled = newConfig.Enabled
	h.cfg.Backup.Schedule = strings.TrimSpace(newConfig.Schedule)
	h.cfg.Backup.Storage = strings.TrimSpace(string(newConfig.Storage))
	h.cfg.Backup.LocalDir = strings.TrimSpace(newConfig.LocalDir)
	h.cfg.Backup.MaxBackups = newConfig.MaxBackups

	// Update S3 config
	h.cfg.Backup.S3.Endpoint = strings.TrimSpace(newConfig.S3.Endpoint)
	h.cfg.Backup.S3.Region = strings.TrimSpace(newConfig.S3.Region)
	h.cfg.Backup.S3.Bucket = strings.TrimSpace(newConfig.S3.Bucket)
	h.cfg.Backup.S3.Path = strings.TrimSpace(newConfig.S3.Path)
	h.cfg.Backup.S3.AccessKey = strings.TrimSpace(newConfig.S3.AccessKey)
	h.cfg.Backup.S3.SecretKey = strings.TrimSpace(newConfig.S3.SecretKey)
	h.cfg.Backup.S3.UseSSL = newConfig.S3.UseSSL

	// Update WebDAV config
	h.cfg.Backup.WebDAV.URL = strings.TrimSpace(newConfig.WebDAV.URL)
	h.cfg.Backup.WebDAV.Username = strings.TrimSpace(newConfig.WebDAV.Username)
	h.cfg.Backup.WebDAV.Password = strings.TrimSpace(newConfig.WebDAV.Password)
	h.cfg.Backup.WebDAV.Path = strings.TrimSpace(newConfig.WebDAV.Path)
	h.mu.Unlock()

	if !h.persist(c) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "backup configuration updated"})
}

// CreateBackup creates a new backup immediately.
func (h *Handler) CreateBackup(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	// Create backup manager
	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backup manager", "message": err.Error()})
		return
	}

	// Check if "download" query parameter is set
	downloadMode := c.Query("download") == "true"

	if downloadMode {
		// Create backup and stream directly to client
		data, filename, errCreate := manager.Create()
		if errCreate != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backup", "message": errCreate.Error()})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		c.Data(http.StatusOK, "application/zip", data)
	} else {
		// Upload to configured storage
		info, err := manager.Upload()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload backup", "message": err.Error()})
			return
		}

		// Cleanup old backups if configured
		if h.cfg.Backup.MaxBackups > 0 {
			if err := manager.CleanupOldBackups(h.cfg.Backup.MaxBackups); err != nil {
				log.WithError(err).Warn("failed to cleanup old backups")
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "backup created successfully",
			"backup":  info,
		})
	}
}

// ListBackups returns all available backups.
func (h *Handler) ListBackups(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backup manager", "message": err.Error()})
		return
	}

	backups, err := manager.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list backups", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"backups": backups})
}

// DownloadBackup downloads a specific backup.
func (h *Handler) DownloadBackup(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup name is required"})
		return
	}

	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backup manager", "message": err.Error()})
		return
	}

	data, err := manager.Download(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to download backup", "message": err.Error()})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(http.StatusOK, "application/zip", data)
}

// DeleteBackup deletes a specific backup.
func (h *Handler) DeleteBackup(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup name is required"})
		return
	}

	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backup manager", "message": err.Error()})
		return
	}

	if err := manager.Delete(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete backup", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "backup deleted successfully"})
}

// TestBackupConnection tests the backup storage connection.
func (h *Handler) TestBackupConnection(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	storage, err := h.createBackupStorage()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to create storage", "message": err.Error()})
		return
	}

	if err := storage.TestConnection(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "connection test failed", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "connection successful"})
}

// createBackupManager creates a backup manager based on current configuration.
func (h *Handler) createBackupManager() (*backup.Manager, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	storage, err := h.createBackupStorage()
	if err != nil {
		return nil, err
	}

	// Determine log directory
	logsDir := h.getLogDirectory()

	return backup.NewManager(h.configFilePath, h.cfg.AuthDir, logsDir, storage), nil
}

// createBackupStorage creates a storage backend based on configuration.
func (h *Handler) createBackupStorage() (backup.Storage, error) {
	storageType := strings.ToLower(strings.TrimSpace(h.cfg.Backup.Storage))
	if storageType == "" {
		storageType = "local"
	}

	switch backup.StorageType(storageType) {
	case backup.StorageTypeLocal:
		localDir := h.cfg.Backup.LocalDir
		if localDir == "" {
			localDir = "./backups"
		}
		return backup.NewLocalStorage(localDir)

	case backup.StorageTypeS3:
		s3Config := backup.S3Config{
			Endpoint:  h.cfg.Backup.S3.Endpoint,
			Region:    h.cfg.Backup.S3.Region,
			Bucket:    h.cfg.Backup.S3.Bucket,
			Path:      h.cfg.Backup.S3.Path,
			AccessKey: h.cfg.Backup.S3.AccessKey,
			SecretKey: h.cfg.Backup.S3.SecretKey,
			UseSSL:    h.cfg.Backup.S3.UseSSL,
		}
		return backup.NewS3Storage(s3Config)

	case backup.StorageTypeWebDAV:
		webdavConfig := backup.WebDAVConfig{
			URL:      h.cfg.Backup.WebDAV.URL,
			Username: h.cfg.Backup.WebDAV.Username,
			Password: h.cfg.Backup.WebDAV.Password,
			Path:     h.cfg.Backup.WebDAV.Path,
		}
		return backup.NewWebDAVStorage(webdavConfig)

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}
}

// getLogDirectory returns the log directory path.
func (h *Handler) getLogDirectory() string {
	if h.logDir != "" {
		return h.logDir
	}
	// Fallback to default log directory
	if h.configFilePath != "" {
		return filepath.Join(filepath.Dir(h.configFilePath), "logs")
	}
	return "logs"
}
