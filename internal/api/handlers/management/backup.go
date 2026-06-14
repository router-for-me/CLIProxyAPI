package management

import (
	"fmt"
	"io"
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
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	c.JSON(http.StatusOK, h.cfg.Backup)
}

// PutBackupConfig updates the backup configuration.
func (h *Handler) PutBackupConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	var newConfig backup.Config
	if err := c.ShouldBindJSON(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误", "message": err.Error()})
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
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	// Check if "download" query parameter is set
	downloadMode := c.Query("download") == "true"

	if downloadMode {
		// Create backup and stream directly to client
		manager, err := h.createBackupManager()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份管理器失败", "message": err.Error()})
			return
		}

		data, filename, errCreate := manager.Create()
		if errCreate != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份失败", "message": errCreate.Error()})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		c.Data(http.StatusOK, "application/zip", data)
	} else {
		// Upload to all configured storage backends
		storageTypes := strings.ToLower(strings.TrimSpace(h.cfg.Backup.Storage))
		if storageTypes == "" {
			storageTypes = "local"
		}

		types := strings.Split(storageTypes, ",")
		successCount := 0
		var lastErr error
		var uploadedInfo backup.BackupInfo

		for _, storageType := range types {
			storageType = strings.TrimSpace(storageType)
			if storageType == "" {
				continue
			}

			// Create manager for this specific storage type
			manager, err := h.createBackupManagerForType(storageType)
			if err != nil {
				log.WithError(err).Warnf("failed to create backup manager for %s", storageType)
				lastErr = err
				continue
			}

			// Upload to this storage
			info, err := manager.Upload()
			if err != nil {
				log.WithError(err).Warnf("failed to upload backup to %s", storageType)
				lastErr = err
				continue
			}

			uploadedInfo = info
			successCount++
			log.Infof("backup uploaded to %s successfully", storageType)

			// Cleanup old backups for this storage
			if h.cfg.Backup.MaxBackups > 0 {
				if err := manager.CleanupOldBackups(h.cfg.Backup.MaxBackups); err != nil {
					log.WithError(err).Warnf("failed to cleanup old backups for %s", storageType)
				}
			}
		}

		if successCount == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "上传备份失败",
				"message": fmt.Sprintf("所有存储后端均失败: %v", lastErr),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("备份创建成功,已上传到 %d 个存储后端", successCount),
			"backup":  uploadedInfo,
		})
	}
}

// ListBackups returns all available backups.
func (h *Handler) ListBackups(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份管理器失败", "message": err.Error()})
		return
	}

	backups, err := manager.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "列出备份失败", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"backups": backups})
}

// DownloadBackup downloads a specific backup.
func (h *Handler) DownloadBackup(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份名称必填"})
		return
	}

	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份管理器失败", "message": err.Error()})
		return
	}

	data, err := manager.Download(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "下载备份失败", "message": err.Error()})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(http.StatusOK, "application/zip", data)
}

// DeleteBackup deletes a specific backup.
func (h *Handler) DeleteBackup(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份名称必填"})
		return
	}

	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份管理器失败", "message": err.Error()})
		return
	}

	if err := manager.Delete(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除备份失败", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "备份删除成功"})
}

// TestBackupConnection tests the backup storage connection.
func (h *Handler) TestBackupConnection(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	storage, err := h.createBackupStorage()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "创建存储失败", "message": err.Error()})
		return
	}

	if err := storage.TestConnection(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "连接测试失败", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "连接成功"})
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

// createBackupManagerForType creates a backup manager for a specific storage type.
func (h *Handler) createBackupManagerForType(storageType string) (*backup.Manager, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	storage, err := h.createBackupStorageByType(storageType)
	if err != nil {
		return nil, err
	}

	// Determine log directory
	logsDir := h.getLogDirectory()

	return backup.NewManager(h.configFilePath, h.cfg.AuthDir, logsDir, storage), nil
}

// createBackupStorage creates a storage backend based on configuration.
// Supports comma-separated storage types (e.g., "local,s3,webdav").
// Returns the first successfully created storage backend.
func (h *Handler) createBackupStorage() (backup.Storage, error) {
	storageTypes := strings.ToLower(strings.TrimSpace(h.cfg.Backup.Storage))
	if storageTypes == "" {
		storageTypes = "local"
	}

	// Split by comma to support multiple storage backends
	types := strings.Split(storageTypes, ",")

	var lastErr error
	for _, storageType := range types {
		storageType = strings.TrimSpace(storageType)
		if storageType == "" {
			continue
		}

		switch backup.StorageType(storageType) {
		case backup.StorageTypeLocal:
			localDir := h.cfg.Backup.LocalDir
			if localDir == "" {
				localDir = "./backups"
			}
			storage, err := backup.NewLocalStorage(localDir)
			if err == nil {
				return storage, nil
			}
			lastErr = err

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
			storage, err := backup.NewS3Storage(s3Config)
			if err == nil {
				return storage, nil
			}
			lastErr = err

		case backup.StorageTypeWebDAV:
			webdavConfig := backup.WebDAVConfig{
				URL:      h.cfg.Backup.WebDAV.URL,
				Username: h.cfg.Backup.WebDAV.Username,
				Password: h.cfg.Backup.WebDAV.Password,
				Path:     h.cfg.Backup.WebDAV.Path,
			}
			storage, err := backup.NewWebDAVStorage(webdavConfig)
			if err == nil {
				return storage, nil
			}
			lastErr = err

		default:
			lastErr = fmt.Errorf("unsupported storage type: %s", storageType)
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no valid storage backend configured")
}

// createBackupStorageByType creates a storage backend for a specific type.
func (h *Handler) createBackupStorageByType(storageType string) (backup.Storage, error) {
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

// RestoreBackup restores configuration from an uploaded backup file.
func (h *Handler) RestoreBackup(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置不可用"})
		return
	}

	// Parse uploaded file
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未上传文件", "message": err.Error()})
		return
	}

	// Validate file extension
	if !strings.HasSuffix(strings.ToLower(file.Filename), ".zip") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件格式无效", "message": "仅支持 .zip 文件"})
		return
	}

	// Open uploaded file
	uploadedFile, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open uploaded file", "message": err.Error()})
		return
	}
	defer uploadedFile.Close()

	// Read file content
	data, err := io.ReadAll(uploadedFile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取上传文件失败", "message": err.Error()})
		return
	}

	// Create backup manager
	manager, err := h.createBackupManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份管理器失败", "message": err.Error()})
		return
	}

	// Restore backup
	if err := manager.Restore(data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "恢复备份失败", "message": err.Error()})
		return
	}

	log.Infof("backup restored from file: %s", file.Filename)
	c.JSON(http.StatusOK, gin.H{
		"message": "备份恢复成功,配置已自动重新加载",
	})
}
