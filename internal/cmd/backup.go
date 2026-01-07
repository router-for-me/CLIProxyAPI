// Package cmd provides command-line interface functionality for backup and restore operations.
package cmd

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// BackupContent represents the contents that can be backed up
type BackupContent struct {
	Env    bool `json:"env"`
	Config bool `json:"config"`
	Auths  bool `json:"auths"`
}

// BackupMetadata represents metadata about a backup
type BackupMetadata struct {
	Name    string        `json:"name"`
	Date    time.Time     `json:"date"`
	Content BackupContent `json:"content"`
}

// BackupOptions contains options for backup operations
type BackupOptions struct {
	Name          string
	BackupPath    string
	IncludeEnv    bool
	IncludeConfig bool
	IncludeAuths  bool
}

// RestoreOptions contains options for restore operations
type RestoreOptions struct {
	BackupPath string
	AuthsMode  string // "overwrite" or "incremental"
}

// DoBackup performs a backup operation
func DoBackup(cfg *config.Config, configPath string, opts *BackupOptions) {
	wd, err := os.Getwd()
	if err != nil {
		log.Errorf("failed to get working directory: %v", err)
		return
	}

	// Set defaults
	if opts == nil {
		opts = &BackupOptions{
			IncludeConfig: true,
			IncludeAuths:  true,
		}
	}

	// Default content selection
	if !opts.IncludeEnv && !opts.IncludeConfig && !opts.IncludeAuths {
		opts.IncludeConfig = true
		opts.IncludeAuths = true
	}

	// Determine backup directory
	backupDir := opts.BackupPath
	if backupDir == "" {
		backupDir = filepath.Join(wd, "backup")
	}
	if !filepath.IsAbs(backupDir) {
		backupDir = filepath.Join(wd, backupDir)
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		log.Errorf("failed to create backup directory: %v", err)
		return
	}

	// Generate backup name
	backupName := opts.Name
	if backupName == "" {
		backupName = fmt.Sprintf("cliProxyApi_backup_%s", time.Now().Format("20060102_150405"))
	}

	// Ensure unique filename
	zipPath := filepath.Join(backupDir, backupName+".zip")
	if _, err := os.Stat(zipPath); err == nil {
		backupName = fmt.Sprintf("%s_%s", backupName, time.Now().Format("150405"))
		zipPath = filepath.Join(backupDir, backupName+".zip")
	}

	// Create zip file
	zipFile, err := os.Create(zipPath)
	if err != nil {
		log.Errorf("failed to create backup file: %v", err)
		return
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	content := BackupContent{
		Env:    opts.IncludeEnv,
		Config: opts.IncludeConfig,
		Auths:  opts.IncludeAuths,
	}

	// Add .env if selected
	if opts.IncludeEnv {
		envPath := filepath.Join(wd, ".env")
		if _, err := os.Stat(envPath); err == nil {
			if err := addFileToZip(zipWriter, envPath, ".env"); err != nil {
				log.Errorf("failed to add .env to backup: %v", err)
				return
			}
			log.Info("Added .env to backup")
		}
	}

	// Add config.yaml if selected
	if opts.IncludeConfig {
		cfgPath := configPath
		if cfgPath == "" {
			cfgPath = filepath.Join(wd, "config.yaml")
		}
		if _, err := os.Stat(cfgPath); err == nil {
			if err := addFileToZip(zipWriter, cfgPath, "config.yaml"); err != nil {
				log.Errorf("failed to add config.yaml to backup: %v", err)
				return
			}
			log.Info("Added config.yaml to backup")
		}
	}

	// Add auths folder if selected
	if opts.IncludeAuths {
		authDir := cfg.AuthDir
		if authDir == "" {
			authDir = filepath.Join(wd, "auths")
		}
		if _, err := os.Stat(authDir); err == nil {
			if err := addDirToZip(zipWriter, authDir, "auths"); err != nil {
				log.Errorf("failed to add auths to backup: %v", err)
				return
			}
			log.Info("Added auths folder to backup")
		}
	}

	// Write metadata
	metadata := BackupMetadata{
		Name:    backupName,
		Date:    time.Now(),
		Content: content,
	}
	metadataBytes, _ := json.Marshal(metadata)
	metaWriter, err := zipWriter.Create("backup_metadata.json")
	if err == nil {
		metaWriter.Write(metadataBytes)
	}

	log.Infof("Backup created successfully: %s", zipPath)
}

// DoRestore performs a restore operation
func DoRestore(cfg *config.Config, configPath string, backupFile string, opts *RestoreOptions) {
	if opts == nil {
		opts = &RestoreOptions{
			AuthsMode: "overwrite",
		}
	}

	if opts.AuthsMode == "" {
		opts.AuthsMode = "overwrite"
	}

	wd, err := os.Getwd()
	if err != nil {
		log.Errorf("failed to get working directory: %v", err)
		return
	}

	// Determine backup file path
	zipPath := backupFile
	if !filepath.IsAbs(zipPath) {
		// Check if it's in the backup directory
		backupDir := filepath.Join(wd, "backup")
		if opts.BackupPath != "" {
			backupDir = opts.BackupPath
		}

		possiblePath := filepath.Join(backupDir, zipPath)
		if _, err := os.Stat(possiblePath); err == nil {
			zipPath = possiblePath
		} else if !strings.HasSuffix(zipPath, ".zip") {
			possiblePath = filepath.Join(backupDir, zipPath+".zip")
			if _, err := os.Stat(possiblePath); err == nil {
				zipPath = possiblePath
			}
		}
	}

	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		log.Errorf("backup file not found: %s", zipPath)
		return
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		log.Errorf("failed to open backup file: %v", err)
		return
	}
	defer reader.Close()

	authDir := cfg.AuthDir
	if authDir == "" {
		authDir = filepath.Join(wd, "auths")
	}

	cfgPath := configPath
	if cfgPath == "" {
		cfgPath = filepath.Join(wd, "config.yaml")
	}

	// If overwrite mode for auths, clear the directory first
	if opts.AuthsMode == "overwrite" {
		authsCleared := false
		for _, f := range reader.File {
			if strings.HasPrefix(f.Name, "auths/") && !authsCleared {
				log.Info("Clearing auths directory for overwrite...")
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
			log.Info("Restoring .env (overwrite)")
		case f.Name == "config.yaml":
			destPath = cfgPath
			log.Info("Restoring config.yaml (overwrite)")
		case strings.HasPrefix(f.Name, "auths/"):
			relativePath := strings.TrimPrefix(f.Name, "auths/")
			if relativePath == "" {
				continue
			}
			destPath = filepath.Join(authDir, relativePath)
			if opts.AuthsMode == "incremental" {
				log.Infof("Restoring auths/%s (incremental)", relativePath)
			}
		default:
			continue
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			log.Errorf("failed to create directory for %s: %v", f.Name, err)
			return
		}

		// Handle directory entries
		if f.FileInfo().IsDir() {
			os.MkdirAll(destPath, 0755)
			continue
		}

		// Extract file
		srcFile, err := f.Open()
		if err != nil {
			log.Errorf("failed to open file in zip %s: %v", f.Name, err)
			return
		}

		destFile, err := os.Create(destPath)
		if err != nil {
			srcFile.Close()
			log.Errorf("failed to create destination file %s: %v", destPath, err)
			return
		}

		_, err = io.Copy(destFile, srcFile)
		srcFile.Close()
		destFile.Close()

		if err != nil {
			log.Errorf("failed to extract file %s: %v", f.Name, err)
			return
		}
	}

	log.Info("Backup restored successfully!")
}

// ListBackups lists all available backups
func ListBackups(backupPath string) {
	wd, _ := os.Getwd()
	backupDir := backupPath
	if backupDir == "" {
		backupDir = filepath.Join(wd, "backup")
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		log.Errorf("failed to read backup directory: %v", err)
		return
	}

	fmt.Println("Available backups:")
	fmt.Println("------------------")

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".zip")
		fmt.Printf("  %s (%.2f KB, %s)\n", name, float64(info.Size())/1024, info.ModTime().Format("2006-01-02 15:04:05"))
	}
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

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		zipPath := filepath.Join(zipBasePath, relPath)
		zipPath = strings.ReplaceAll(zipPath, "\\", "/")

		if info.IsDir() {
			if relPath != "." {
				_, err := zipWriter.Create(zipPath + "/")
				return err
			}
			return nil
		}

		return addFileToZip(zipWriter, path, zipPath)
	})
}
