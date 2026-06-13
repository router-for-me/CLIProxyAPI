package backup

import "time"

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
