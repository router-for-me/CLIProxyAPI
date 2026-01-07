// Package management provides the management API handlers and middleware.
// This file implements backup and restore functionality for .env, config.yaml, and auths folder.
package management

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/backup"
)

// BackupCreateRequest represents a request to create a backup
type BackupCreateRequest struct {
	Name       string               `json:"name"`       // Custom backup name (optional)
	Content    backup.BackupContent `json:"content"`    // What to backup
	BackupPath string               `json:"backupPath"` // Custom backup directory (optional)
}

// BackupRestoreRequest represents a request to restore a backup
type BackupRestoreRequest struct {
	Name      string `json:"name"`      // Backup name to restore
	AuthsMode string `json:"authsMode"` // "overwrite" or "incremental"
}

// BackupListResponse represents the response for listing backups
type BackupListResponse struct {
	Backups    []BackupMetadataResponse `json:"backups"`
	BackupPath string                   `json:"backupPath"`
}

// BackupMetadataResponse is the API response format for backup metadata
type BackupMetadataResponse struct {
	Name    string               `json:"name"`
	Date    time.Time            `json:"date"`
	Content backup.BackupContent `json:"content"`
	Size    int64                `json:"size"`
}

// getBackupDir returns the backup directory path
func (h *Handler) getBackupDir() string {
	wd, err := os.Getwd()
	if err != nil {
		wd = filepath.Dir(h.configFilePath)
	}
	return filepath.Join(wd, "backup")
}

// getAuthDir returns the auth directory path using the centralized helper
func (h *Handler) getAuthDir() string {
	wd, err := os.Getwd()
	if err != nil {
		wd = filepath.Dir(h.configFilePath)
	}
	return backup.ResolveAuthDir(h.cfg.AuthDir, wd)
}

// ListBackups returns a list of all available backups
func (h *Handler) ListBackups(c *gin.Context) {
	backupDir := h.getBackupDir()

	// Ensure backup directory exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create backup directory: %v", err)})
		return
	}

	backups, err := backup.ListBackups(backupDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list backups: %v", err)})
		return
	}

	// Convert to API response format
	var response []BackupMetadataResponse
	for _, b := range backups {
		dateTime, err := time.Parse(time.RFC3339, b.Date)
		if err != nil {
			// Use zero time if parsing fails, but still include the backup
			dateTime = time.Time{}
		}
		response = append(response, BackupMetadataResponse{
			Name:    b.Name,
			Date:    dateTime,
			Content: b.Content,
			Size:    b.Size,
		})
	}

	// Sort by date descending (newest first)
	sort.Slice(response, func(i, j int) bool {
		return response[i].Date.After(response[j].Date)
	})

	c.JSON(http.StatusOK, BackupListResponse{
		Backups:    response,
		BackupPath: backupDir,
	})
}

// CreateBackup creates a new backup
func (h *Handler) CreateBackup(c *gin.Context) {
	var req BackupCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}

	// Default content selection: config.yaml and auths
	if !req.Content.Env && !req.Content.Config && !req.Content.Auths {
		req.Content.Config = true
		req.Content.Auths = true
	}

	wd, err := os.Getwd()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get working directory"})
		return
	}

	// Validate backup path (no absolute paths allowed for API)
	backupDir, err := backup.ValidateBackupPath(req.BackupPath, wd)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create backup using shared package
	opts := backup.BackupOptions{
		Name:       req.Name,
		BackupPath: backupDir,
		Content:    req.Content,
		WorkDir:    wd,
		AuthDir:    h.getAuthDir(),
	}

	backupPath, err := backup.CreateBackup(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create backup: %v", err)})
		return
	}

	// Get file info for response
	info, err := os.Stat(backupPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to stat backup file: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"backup": BackupMetadataResponse{
			Name:    filepath.Base(backupPath),
			Date:    time.Now(),
			Content: req.Content,
			Size:    info.Size(),
		},
		"filepath": backupPath,
	})
}

// DeleteBackup deletes a backup
func (h *Handler) DeleteBackup(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup name is required"})
		return
	}

	backupDir := h.getBackupDir()

	// Ensure .zip extension
	if !strings.HasSuffix(name, ".zip") {
		name = name + ".zip"
	}

	if err := backup.DeleteBackup(backupDir, name); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "backup not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to delete backup: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DownloadBackup downloads a backup file
func (h *Handler) DownloadBackup(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup name is required"})
		return
	}

	backupDir := h.getBackupDir()

	// Sanitize name to prevent path traversal
	name = filepath.Base(name)
	if !strings.HasSuffix(name, ".zip") {
		name = name + ".zip"
	}

	zipPath := filepath.Join(backupDir, name)

	// Check if file exists
	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "backup not found"})
		return
	}

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", name))
	c.Header("Content-Type", "application/zip")
	c.File(zipPath)
}

// RestoreBackup restores from a backup
func (h *Handler) RestoreBackup(c *gin.Context) {
	var req BackupRestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}

	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup name is required"})
		return
	}

	// Default auths mode is overwrite
	if req.AuthsMode == "" {
		req.AuthsMode = "overwrite"
	}

	if req.AuthsMode != "overwrite" && req.AuthsMode != "incremental" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authsMode must be 'overwrite' or 'incremental'"})
		return
	}

	backupDir := h.getBackupDir()
	wd, err := os.Getwd()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get working directory"})
		return
	}

	// Sanitize name
	name := filepath.Base(req.Name)
	if !strings.HasSuffix(name, ".zip") {
		name = name + ".zip"
	}

	// Check if backup exists
	zipPath := filepath.Join(backupDir, name)
	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "backup not found"})
		return
	}

	// Restore using shared package
	opts := backup.RestoreOptions{
		BackupPath: backupDir,
		BackupName: name,
		AuthsMode:  req.AuthsMode,
		WorkDir:    wd,
		AuthDir:    h.getAuthDir(),
	}

	if err := backup.RestoreBackup(opts); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to restore backup: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "backup restored successfully"})
}

// UploadAndRestoreBackup handles uploading a backup file and restoring it
func (h *Handler) UploadAndRestoreBackup(c *gin.Context) {
	authsMode := c.DefaultPostForm("authsMode", "overwrite")
	if authsMode != "overwrite" && authsMode != "incremental" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authsMode must be 'overwrite' or 'incremental'"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	// Validate file extension
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only zip files are allowed"})
		return
	}

	// Limit upload size (100MB)
	const maxUploadSize = 100 * 1024 * 1024
	if header.Size > maxUploadSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file size exceeds maximum allowed size of 100MB"})
		return
	}

	// Save to temp file with restrictive permissions
	tempFile, err := os.CreateTemp("", "backup_upload_*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp file"})
		return
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	// Use LimitReader to enforce size limit
	limitedReader := io.LimitReader(file, maxUploadSize+1)
	n, err := io.Copy(tempFile, limitedReader)
	tempFile.Close()

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save uploaded file"})
		return
	}
	if n > maxUploadSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file size exceeds maximum allowed size of 100MB"})
		return
	}

	wd, err := os.Getwd()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get working directory"})
		return
	}

	// Restore using shared package
	opts := backup.RestoreOptions{
		BackupPath: filepath.Dir(tempPath),
		BackupName: filepath.Base(tempPath),
		AuthsMode:  authsMode,
		WorkDir:    wd,
		AuthDir:    h.getAuthDir(),
	}

	if err := backup.RestoreBackup(opts); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to restore backup: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "backup uploaded and restored successfully"})
}
