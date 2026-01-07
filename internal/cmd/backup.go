// Package cmd provides command-line interface functionality for backup and restore operations.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/backup"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

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

	// Determine backup directory (CLI allows absolute paths)
	backupDir := backup.ValidateBackupPathCLI(opts.BackupPath, wd)

	// Determine auth directory using centralized helper
	authDir := backup.ResolveAuthDir(cfg.AuthDir, wd)

	// Create backup using shared package
	backupOpts := backup.BackupOptions{
		Name:       opts.Name,
		BackupPath: backupDir,
		Content: backup.BackupContent{
			Env:    opts.IncludeEnv,
			Config: opts.IncludeConfig,
			Auths:  opts.IncludeAuths,
		},
		WorkDir: wd,
		AuthDir: authDir,
	}

	backupPath, err := backup.CreateBackup(backupOpts)
	if err != nil {
		log.Errorf("failed to create backup: %v", err)
		return
	}

	log.Infof("Backup created successfully: %s", backupPath)
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
	backupDir := filepath.Join(wd, "backup")
	if opts.BackupPath != "" {
		backupDir = backup.ValidateBackupPathCLI(opts.BackupPath, wd)
	}

	if !filepath.IsAbs(zipPath) {
		// Check if it's in the backup directory
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

	// Determine auth directory using centralized helper
	authDir := backup.ResolveAuthDir(cfg.AuthDir, wd)

	// Restore using shared package
	restoreOpts := backup.RestoreOptions{
		BackupPath: filepath.Dir(zipPath),
		BackupName: filepath.Base(zipPath),
		AuthsMode:  opts.AuthsMode,
		WorkDir:    wd,
		AuthDir:    authDir,
	}

	if err := backup.RestoreBackup(restoreOpts); err != nil {
		log.Errorf("failed to restore backup: %v", err)
		return
	}

	log.Info("Backup restored successfully!")
}

// ListBackups lists all available backups
func ListBackups(backupPath string) {
	wd, err := os.Getwd()
	if err != nil {
		log.Errorf("failed to get working directory: %v", err)
		return
	}
	backupDir := backup.ValidateBackupPathCLI(backupPath, wd)

	backups, err := backup.ListBackups(backupDir)
	if err != nil {
		log.Errorf("failed to list backups: %v", err)
		return
	}

	fmt.Println("Available backups:")
	fmt.Println("------------------")

	if len(backups) == 0 {
		fmt.Println("  (no backups found)")
		return
	}

	for _, b := range backups {
		contentParts := []string{}
		if b.Content.Env {
			contentParts = append(contentParts, ".env")
		}
		if b.Content.Config {
			contentParts = append(contentParts, "config")
		}
		if b.Content.Auths {
			contentParts = append(contentParts, "auths")
		}
		contentStr := strings.Join(contentParts, ", ")
		if contentStr == "" {
			contentStr = "unknown"
		}

		fmt.Printf("  %s (%.2f KB, %s) [%s]\n",
			b.Name,
			float64(b.Size)/1024,
			b.Date,
			contentStr,
		)
	}
}
