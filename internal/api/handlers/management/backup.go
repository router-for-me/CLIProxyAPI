// Package management provides the management API handlers and middleware.
// This file implements backup and restore functionality for .env, config.yaml, and auths folder.
package management

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// BackupContent represents the contents that can be backed up
type BackupContent struct {
	Env    bool `json:"env"`
	Config bool `json:"config"`
	Auths  bool `json:"auths"`
}

// BackupMetadata represents metadata about a backup
type BackupMetadata struct {
	Name     string        `json:"name"`
	Date     time.Time     `json:"date"`
	Content  BackupContent `json:"content"`
	Size     int64         `json:"size"`
	FilePath string        `json:"-"` // Internal use only
}

// BackupCreateRequest represents a request to create a backup
type BackupCreateRequest struct {
	Name       string        `json:"name"`       // Custom backup name (optional)
	Content    BackupContent `json:"content"`    // What to backup
	BackupPath string        `json:"backupPath"` // Custom backup directory (optional)
}

// BackupRestoreRequest represents a request to restore a backup
type BackupRestoreRequest struct {
	Name      string `json:"name"`      // Backup name to restore
	AuthsMode string `json:"authsMode"` // "overwrite" or "incremental"
}

// BackupListResponse represents the response for listing backups
type BackupListResponse struct {
	Backups    []BackupMetadata `json:"backups"`
	BackupPath string           `json:"backupPath"`
}

// getBackupDir returns the backup directory path
func (h *Handler) getBackupDir() string {
	// Default to ./backup relative to working directory
	wd, err := os.Getwd()
	if err != nil {
		wd = filepath.Dir(h.configFilePath)
	}
	return filepath.Join(wd, "backup")
}

// getBackupDirFromConfig returns the backup directory, allowing custom path
func (h *Handler) getBackupDirFromConfig(customPath string) string {
	if customPath != "" {
		if filepath.IsAbs(customPath) {
			return customPath
		}
		wd, _ := os.Getwd()
		return filepath.Join(wd, customPath)
	}
	return h.getBackupDir()
}

// ListBackups returns a list of all available backups
func (h *Handler) ListBackups(c *gin.Context) {
	backupDir := h.getBackupDir()

	// Ensure backup directory exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create backup directory: %v", err)})
		return
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read backup directory: %v", err)})
		return
	}

	var backups []BackupMetadata
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}

		filePath := filepath.Join(backupDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Try to read metadata from zip
		metadata, err := readBackupMetadata(filePath)
		if err != nil {
			// Fallback: parse name for date
			name := strings.TrimSuffix(entry.Name(), ".zip")
			metadata = &BackupMetadata{
				Name:     name,
				Date:     info.ModTime(),
				Size:     info.Size(),
				FilePath: filePath,
				Content:  BackupContent{Config: true, Auths: true}, // Default assumption
			}
		} else {
			metadata.Size = info.Size()
			metadata.FilePath = filePath
		}
		backups = append(backups, *metadata)
	}

	// Sort by date descending (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Date.After(backups[j].Date)
	})

	c.JSON(http.StatusOK, BackupListResponse{
		Backups:    backups,
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

	backupDir := h.getBackupDirFromConfig(req.BackupPath)

	// Ensure backup directory exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create backup directory: %v", err)})
		return
	}

	// Generate backup name if not provided
	backupName := req.Name
	if backupName == "" {
		backupName = fmt.Sprintf("cliProxyApi_backup_%s", time.Now().Format("20060102_150405"))
	}

	// Ensure unique filename
	zipPath := filepath.Join(backupDir, backupName+".zip")
	if _, err := os.Stat(zipPath); err == nil {
		// File exists, append timestamp
		backupName = fmt.Sprintf("%s_%s", backupName, time.Now().Format("150405"))
		zipPath = filepath.Join(backupDir, backupName+".zip")
	}

	// Create zip file
	zipFile, err := os.Create(zipPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create backup file: %v", err)})
		return
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	wd, _ := os.Getwd()

	// Add .env if selected
	if req.Content.Env {
		envPath := filepath.Join(wd, ".env")
		if _, err := os.Stat(envPath); err == nil {
			if err := addFileToZip(zipWriter, envPath, ".env"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to add .env to backup: %v", err)})
				return
			}
		}
	}

	// Add config.yaml if selected
	if req.Content.Config {
		configPath := h.configFilePath
		if configPath == "" {
			configPath = filepath.Join(wd, "config.yaml")
		}
		if _, err := os.Stat(configPath); err == nil {
			if err := addFileToZip(zipWriter, configPath, "config.yaml"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to add config.yaml to backup: %v", err)})
				return
			}
		}
	}

	// Add auths folder if selected
	if req.Content.Auths {
		authDir := h.cfg.AuthDir
		if authDir == "" {
			authDir = filepath.Join(wd, "auths")
		}
		if _, err := os.Stat(authDir); err == nil {
			if err := addDirToZip(zipWriter, authDir, "auths"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to add auths to backup: %v", err)})
				return
			}
		}
	}

	// Write metadata
	metadata := BackupMetadata{
		Name:    backupName,
		Date:    time.Now(),
		Content: req.Content,
	}
	metadataBytes, _ := json.Marshal(metadata)
	metaWriter, err := zipWriter.Create("backup_metadata.json")
	if err == nil {
		metaWriter.Write(metadataBytes)
	}

	// Close zip to flush
	zipWriter.Close()
	zipFile.Close()

	// Get file size
	info, _ := os.Stat(zipPath)
	metadata.Size = info.Size()

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"backup":   metadata,
		"filepath": zipPath,
	})
}

// DeleteBackup deletes a backup
func (h *Handler) DeleteBackup(c *gin.Context) {
	name := c.Query("name")
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

	if err := os.Remove(zipPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to delete backup: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DownloadBackup downloads a backup file
func (h *Handler) DownloadBackup(c *gin.Context) {
	name := c.Query("name")
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

	// Sanitize name
	name := filepath.Base(req.Name)
	if !strings.HasSuffix(name, ".zip") {
		name = name + ".zip"
	}

	zipPath := filepath.Join(backupDir, name)

	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "backup not found"})
		return
	}

	// Extract and restore
	if err := h.restoreFromZip(zipPath, req.AuthsMode); err != nil {
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

	// Save to temp file
	tempFile, err := os.CreateTemp("", "backup_upload_*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp file"})
		return
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := io.Copy(tempFile, file); err != nil {
		tempFile.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save uploaded file"})
		return
	}
	tempFile.Close()

	// Restore from uploaded file
	if err := h.restoreFromZip(tempPath, authsMode); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to restore backup: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "backup uploaded and restored successfully"})
}

// restoreFromZip extracts and restores files from a zip backup
func (h *Handler) restoreFromZip(zipPath, authsMode string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer reader.Close()

	wd, _ := os.Getwd()
	authDir := h.cfg.AuthDir
	if authDir == "" {
		authDir = filepath.Join(wd, "auths")
	}

	// If overwrite mode for auths, clear the directory first
	if authsMode == "overwrite" {
		// We'll handle this when we encounter auths files
		authsCleared := false
		for _, f := range reader.File {
			if strings.HasPrefix(f.Name, "auths/") && !authsCleared {
				// Clear auths directory
				os.RemoveAll(authDir)
				os.MkdirAll(authDir, 0755)
				authsCleared = true
				break
			}
		}
	}

	for _, f := range reader.File {
		// Skip metadata file
		if f.Name == "backup_metadata.json" {
			continue
		}

		// Determine destination path
		var destPath string
		switch {
		case f.Name == ".env":
			destPath = filepath.Join(wd, ".env")
		case f.Name == "config.yaml":
			destPath = h.configFilePath
			if destPath == "" {
				destPath = filepath.Join(wd, "config.yaml")
			}
		case strings.HasPrefix(f.Name, "auths/"):
			relativePath := strings.TrimPrefix(f.Name, "auths/")
			if relativePath == "" {
				continue // Skip directory entry
			}
			destPath = filepath.Join(authDir, relativePath)
		default:
			continue // Skip unknown files
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", f.Name, err)
		}

		// Handle directory entries
		if f.FileInfo().IsDir() {
			os.MkdirAll(destPath, 0755)
			continue
		}

		// Extract file
		srcFile, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in zip %s: %w", f.Name, err)
		}

		destFile, err := os.Create(destPath)
		if err != nil {
			srcFile.Close()
			return fmt.Errorf("failed to create destination file %s: %w", destPath, err)
		}

		_, err = io.Copy(destFile, srcFile)
		srcFile.Close()
		destFile.Close()

		if err != nil {
			return fmt.Errorf("failed to extract file %s: %w", f.Name, err)
		}
	}

	return nil
}

// Helper functions

func addFileToZip(zipWriter *zip.Writer, filePath, zipPath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = zipPath
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	return err
}

func addDirToZip(zipWriter *zip.Writer, dirPath, zipBasePath string) error {
	return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path
		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		zipPath := filepath.Join(zipBasePath, relPath)
		// Normalize path separators for zip
		zipPath = strings.ReplaceAll(zipPath, "\\", "/")

		if info.IsDir() {
			// Add directory entry
			if relPath != "." {
				_, err := zipWriter.Create(zipPath + "/")
				return err
			}
			return nil
		}

		return addFileToZip(zipWriter, path, zipPath)
	})
}

func readBackupMetadata(zipPath string) (*BackupMetadata, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	for _, f := range reader.File {
		if f.Name == "backup_metadata.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			var metadata BackupMetadata
			if err := json.NewDecoder(rc).Decode(&metadata); err != nil {
				return nil, err
			}
			return &metadata, nil
		}
	}

	return nil, fmt.Errorf("metadata not found")
}
