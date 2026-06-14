package backup

import (
	"fmt"
	"strings"
	"time"
)

// StorageType defines the backup storage backend type.
type StorageType string

const (
	StorageTypeLocal  StorageType = "local"
	StorageTypeS3     StorageType = "s3"
	StorageTypeWebDAV StorageType = "webdav"
)

// Config holds backup configuration.
type Config struct {
	Enabled    bool        `yaml:"enabled" json:"enabled"`
	Schedule   string      `yaml:"schedule" json:"schedule"`
	Storage    StorageType `yaml:"storage" json:"storage"`
	LocalDir   string      `yaml:"local-dir" json:"local-dir"`
	MaxBackups int         `yaml:"max-backups" json:"max-backups"`
	S3         S3Config    `yaml:"s3" json:"s3"`
	WebDAV     WebDAVConfig `yaml:"webdav" json:"webdav"`
}

// Validate checks if the backup configuration is valid.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil // No validation needed if disabled
	}

	// Validate storage type
	if c.Storage == "" {
		return fmt.Errorf("storage type is required when backup is enabled")
	}

	// Support multiple storage types separated by comma
	storageTypes := strings.Split(string(c.Storage), ",")
	for _, st := range storageTypes {
		st = strings.TrimSpace(st)
		storageType := StorageType(st)

		switch storageType {
		case StorageTypeLocal:
			// Local storage is always valid (will use default if LocalDir is empty)
		case StorageTypeS3:
			if err := c.S3.Validate(); err != nil {
				return fmt.Errorf("S3 configuration error: %w", err)
			}
		case StorageTypeWebDAV:
			if err := c.WebDAV.Validate(); err != nil {
				return fmt.Errorf("WebDAV configuration error: %w", err)
			}
		default:
			return fmt.Errorf("unsupported storage type: %s", storageType)
		}
	}

	// Validate schedule format if provided
	if c.Schedule != "" {
		if err := validateSchedule(c.Schedule); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}
	}

	return nil
}

// Validate checks if the S3 configuration is valid.
func (s *S3Config) Validate() error {
	if s.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	if s.Bucket == "" {
		return fmt.Errorf("bucket is required")
	}
	if s.AccessKey == "" {
		return fmt.Errorf("access key is required")
	}
	if s.SecretKey == "" {
		return fmt.Errorf("secret key is required")
	}
	return nil
}

// Validate checks if the WebDAV configuration is valid.
func (w *WebDAVConfig) Validate() error {
	if w.URL == "" {
		return fmt.Errorf("URL is required")
	}
	if !strings.HasPrefix(w.URL, "http://") && !strings.HasPrefix(w.URL, "https://") {
		return fmt.Errorf("URL must start with http:// or https://")
	}
	return nil
}

// validateSchedule validates the schedule format.
func validateSchedule(schedule string) error {
	switch schedule {
	case "@hourly", "@daily", "@weekly", "@monthly":
		return nil
	default:
		// Try to parse as duration
		_, err := time.ParseDuration(schedule)
		return err
	}
}

// S3Config holds S3 storage configuration.
type S3Config struct {
	Endpoint  string `yaml:"endpoint" json:"endpoint"`
	Region    string `yaml:"region" json:"region"`
	Bucket    string `yaml:"bucket" json:"bucket"`
	Path      string `yaml:"path" json:"path"`
	AccessKey string `yaml:"access-key" json:"access-key"`
	SecretKey string `yaml:"secret-key" json:"secret-key"`
	UseSSL    bool   `yaml:"use-ssl" json:"use-ssl"`
}

// WebDAVConfig holds WebDAV storage configuration.
type WebDAVConfig struct {
	URL      string `yaml:"url" json:"url"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	Path     string `yaml:"path" json:"path"`
}

// BackupInfo represents metadata about a backup.
type BackupInfo struct {
	Name      string      `json:"name"`
	Timestamp time.Time   `json:"timestamp"`
	Size      int64       `json:"size"`
	Storage   StorageType `json:"storage"`
	Location  string      `json:"location"`
}

// Storage is the interface for backup storage backends.
type Storage interface {
	// Upload uploads a backup file to the storage backend.
	Upload(filename string, data []byte) error
	// List returns all available backups in the storage.
	List() ([]BackupInfo, error)
	// Download downloads a backup file from the storage.
	Download(name string) ([]byte, error)
	// Delete removes a backup file from the storage.
	Delete(name string) error
	// TestConnection tests if the storage is accessible.
	TestConnection() error
}

