package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	log "github.com/sirupsen/logrus"
)

// S3Storage implements Storage interface for S3-compatible storage.
type S3Storage struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewS3Storage creates a new S3 storage backend.
func NewS3Storage(cfg S3Config) (*S3Storage, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("S3 endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket is required")
	}

	// Clean endpoint: remove protocol prefix and trailing slashes
	endpoint := cfg.Endpoint
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimRight(endpoint, "/")

	// Remove any path components (minio doesn't accept paths in endpoint)
	if idx := strings.Index(endpoint, "/"); idx != -1 {
		endpoint = endpoint[:idx]
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}

	return &S3Storage{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.TrimPrefix(cfg.Path, "/"),
	}, nil
}

// Upload uploads a backup file to S3.
func (s *S3Storage) Upload(filename string, data []byte) error {
	ctx := context.Background()

	objectName := s.objectName(filename)
	reader := bytes.NewReader(data)

	_, err := s.client.PutObject(ctx, s.bucket, objectName, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/zip",
	})
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	log.Infof("backup uploaded to S3: %s/%s", s.bucket, objectName)
	return nil
}

// List returns all available backups in S3.
func (s *S3Storage) List() ([]BackupInfo, error) {
	ctx := context.Background()

	objectCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    s.prefix,
		Recursive: true,
	})

	var backups []BackupInfo
	var lastErr error

	for object := range objectCh {
		if object.Err != nil {
			log.WithError(object.Err).Warn("error listing S3 objects")
			lastErr = object.Err
			continue
		}

		// Extract filename from object key
		name := path.Base(object.Key)
		if !strings.HasSuffix(name, ".zip") {
			continue
		}

		backups = append(backups, BackupInfo{
			Name:      name,
			Timestamp: object.LastModified,
			Size:      object.Size,
			Storage:   StorageTypeS3,
			Location:  object.Key,
		})
	}

	// Return error if listing had errors
	if lastErr != nil {
		return backups, fmt.Errorf("S3 listing completed with errors: %w", lastErr)
	}

	// Sort by name (which includes timestamp)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name < backups[j].Name
	})

	return backups, nil
}

// Download retrieves a backup file from S3.
func (s *S3Storage) Download(name string) ([]byte, error) {
	ctx := context.Background()

	objectName := s.objectName(name)
	object, err := s.client.GetObject(ctx, s.bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 object: %w", err)
	}
	defer object.Close()

	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("failed to read S3 object: %w", err)
	}

	return data, nil
}

// Delete removes a backup file from S3.
func (s *S3Storage) Delete(name string) error {
	ctx := context.Background()

	objectName := s.objectName(name)
	if err := s.client.RemoveObject(ctx, s.bucket, objectName, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("failed to delete S3 object: %w", err)
	}

	log.Infof("backup deleted from S3: %s/%s", s.bucket, objectName)
	return nil
}

// TestConnection tests if the S3 storage is accessible.
func (s *S3Storage) TestConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if bucket exists and is accessible
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("failed to check S3 bucket: %w", err)
	}
	if !exists {
		return fmt.Errorf("S3 bucket does not exist: %s", s.bucket)
	}

	return nil
}

// objectName constructs the full S3 object key with prefix.
func (s *S3Storage) objectName(filename string) string {
	// Security: validate filename to prevent path traversal
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		log.Warnf("rejecting filename with path traversal attempt: %s", filename)
		// Return a safe default - caller should handle invalid filenames
		return "invalid-filename.zip"
	}

	if s.prefix == "" {
		return filename
	}
	return path.Join(s.prefix, filename)
}
