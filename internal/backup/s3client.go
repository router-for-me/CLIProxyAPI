package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// SnapshotMeta describes one backup snapshot object stored in S3.
type SnapshotMeta struct {
	Key        string    `json:"key"`
	Size       int64     `json:"size"`
	ExportedAt time.Time `json:"exported_at"`
}

// S3Config holds the target configuration for the backup S3 bucket. It is
// independent from the active ObjectStoreConfig so a deployment can use a
// separate backup account/bucket.
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
	UseSSL    bool
	PathStyle bool
}

// minioClient is the subset of *minio.Client used by S3Client, kept as an
// interface so tests can stub it without a live S3 server.
type minioClient interface {
	PutObject(ctx context.Context, bucket, object string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	ListObjects(ctx context.Context, bucket string, opts minio.ListObjectsOptions) <-chan minio.ObjectInfo
	RemoveObject(ctx context.Context, bucket, object string, opts minio.RemoveObjectOptions) error
}

// objectGetter abstracts object reads down to an io.ReadCloser so Download is
// testable against a stub without constructing a real *minio.Object.
type objectGetter interface {
	Get(ctx context.Context, bucket, object string) (io.ReadCloser, error)
}

// minioObjectGetter adapts *minio.Client.GetObject to objectGetter.
type minioObjectGetter struct {
	client *minio.Client
}

// Get fetches the object and returns it as an io.ReadCloser.
func (g minioObjectGetter) Get(ctx context.Context, bucket, object string) (io.ReadCloser, error) {
	return g.client.GetObject(ctx, bucket, object, minio.GetObjectOptions{})
}

// S3Client uploads, downloads, lists, and deletes backup snapshots on an
// S3-compatible object store.
type S3Client struct {
	client minioClient
	getter objectGetter
	bucket string
	prefix string
}

// NewS3Client builds an S3Client from config, constructing a minio client.
func NewS3Client(cfg S3Config) (*S3Client, error) {
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	if cfg.Endpoint == "" {
		return nil, errors.New("backup s3: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("backup s3: bucket is required")
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}
	if cfg.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	mc, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("backup s3: create client: %w", err)
	}
	return &S3Client{client: mc, getter: minioObjectGetter{client: mc}, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

// prefixedKey namespaces a key under the configured prefix.
func (c *S3Client) prefixedKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if c.prefix == "" {
		return key
	}
	return strings.TrimLeft(c.prefix+"/"+key, "/")
}

// Upload writes data to the bucket under key (prefixed), returning the full key.
func (c *S3Client) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	full := c.prefixedKey(key)
	_, err := c.client.PutObject(ctx, c.bucket, full, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("backup s3: upload %s: %w", full, err)
	}
	return full, nil
}

// Download fetches the object's bytes from the bucket under key (prefixed).
func (c *S3Client) Download(ctx context.Context, key string) ([]byte, error) {
	full := c.prefixedKey(key)
	obj, err := c.getter.Get(ctx, c.bucket, full)
	if err != nil {
		return nil, fmt.Errorf("backup s3: get %s: %w", full, err)
	}
	defer func() {
		if errClose := obj.Close(); errClose != nil {
			// Close errors on object reads are non-fatal; the bytes are what matter.
			_ = errClose
		}
	}()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("backup s3: read %s: %w", full, err)
	}
	return data, nil
}

// List returns metadata for all snapshots under the configured prefix, sorted by
// ExportedAt descending (newest first).
func (c *S3Client) List(ctx context.Context) ([]SnapshotMeta, error) {
	prefix := c.prefixedKey("")
	ch := c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	var out []SnapshotMeta
	for obj := range ch {
		if obj.Err != nil {
			return nil, fmt.Errorf("backup s3: list: %w", obj.Err)
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		out = append(out, SnapshotMeta{Key: obj.Key, Size: obj.Size, ExportedAt: obj.LastModified})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExportedAt.After(out[j].ExportedAt) })
	return out, nil
}

// Delete removes a snapshot object. Accepts either a short key (relative to
// the configured prefix, e.g. "snap.json") or a fully-prefixed key as returned
// by List (e.g. "bk/snap.json"). When the key already carries the prefix it is
// passed through unchanged; otherwise the prefix is applied.
func (c *S3Client) Delete(ctx context.Context, key string) error {
	full := c.prefixedKey(key)
	if c.prefix != "" && strings.HasPrefix(key, c.prefix+"/") {
		full = key // already absolute
	}
	if err := c.client.RemoveObject(ctx, c.bucket, full, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("backup s3: delete %s: %w", full, err)
	}
	return nil
}
