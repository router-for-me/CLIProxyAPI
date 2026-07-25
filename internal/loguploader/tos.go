package loguploader

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
)

var ErrObjectConflict = errors.New("TOS object already exists and overwrite is forbidden")

const archiveChecksumMetadataKey = "cliproxy-sha256"

type tosObjectClient interface {
	PutObjectFromFile(context.Context, *tos.PutObjectFromFileInput) (*tos.PutObjectFromFileOutput, error)
	HeadObjectV2(context.Context, *tos.HeadObjectV2Input) (*tos.HeadObjectV2Output, error)
	CreateMultipartUploadV2(context.Context, *tos.CreateMultipartUploadV2Input) (*tos.CreateMultipartUploadV2Output, error)
	UploadPartFromFile(context.Context, *tos.UploadPartFromFileInput) (*tos.UploadPartFromFileOutput, error)
	CompleteMultipartUploadV2(context.Context, *tos.CompleteMultipartUploadV2Input) (*tos.CompleteMultipartUploadV2Output, error)
	AbortMultipartUpload(context.Context, *tos.AbortMultipartUploadInput) (*tos.AbortMultipartUploadOutput, error)
}

// TOSUploader uploads archives through the native Volcengine TOS endpoint.
type TOSUploader struct {
	client tosObjectClient
}

func NewTOSUploader(cfg UploadConfig) (*TOSUploader, error) {
	endpoint, errEndpoint := parseTOSEndpoint(cfg.Endpoint)
	if errEndpoint != nil {
		return nil, errEndpoint
	}
	credentials, errCredentials := loadTOSCredentials(cfg)
	if errCredentials != nil {
		return nil, errCredentials
	}
	client, errClient := tos.NewClientV2(endpoint,
		tos.WithRegion(cfg.Region),
		tos.WithCredentials(credentials),
	)
	if errClient != nil {
		return nil, fmt.Errorf("create TOS client: %w", errClient)
	}
	return &TOSUploader{client: client}, nil
}

func loadTOSCredentials(cfg UploadConfig) (*tos.StaticCredentials, error) {
	accessKeyID := strings.TrimSpace(os.Getenv(cfg.AccessKeyIDEnv))
	if accessKeyID == "" {
		return nil, fmt.Errorf("environment variable %s is required", cfg.AccessKeyIDEnv)
	}
	secretAccessKey := strings.TrimSpace(os.Getenv(cfg.SecretAccessKeyEnv))
	if secretAccessKey == "" {
		return nil, fmt.Errorf("environment variable %s is required", cfg.SecretAccessKeyEnv)
	}
	credentials := tos.NewStaticCredentials(accessKeyID, secretAccessKey)
	if sessionToken := strings.TrimSpace(os.Getenv(cfg.SessionTokenEnv)); sessionToken != "" {
		credentials.WithSecurityToken(sessionToken)
	}
	return credentials, nil
}

func parseTOSEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil {
		return "", fmt.Errorf("parse upload endpoint: %w", errParse)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("upload endpoint must use https")
	}
	if parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("upload endpoint must contain only a scheme and host")
	}
	if strings.HasPrefix(strings.ToLower(parsed.Hostname()), "tos-s3-") {
		return "", fmt.Errorf("upload endpoint must be a native TOS endpoint, not an S3-compatible endpoint")
	}
	return "https://" + parsed.Host, nil
}

func (u *TOSUploader) UploadFile(ctx context.Context, bucket, objectKey, path string) error {
	fileInfo, errStat := os.Stat(path)
	if errStat != nil {
		return fmt.Errorf("stat archive for upload: %w", errStat)
	}
	// Prefer multipart for multi-hundred-MiB archives so flaky links only
	// retry a 64 MiB part instead of re-sending a multi-GB single PUT.
	if shouldUseMultipart(fileInfo.Size()) {
		return u.uploadMultipart(ctx, bucket, objectKey, path, fileInfo.Size())
	}
	checksum, _, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		return errChecksum
	}
	_, errUpload := u.client.PutObjectFromFile(ctx, &tos.PutObjectFromFileInput{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket:          bucket,
			Key:             objectKey,
			ContentType:     "application/zstd",
			ForbidOverwrite: true,
			Meta: map[string]string{
				archiveChecksumMetadataKey: checksum,
			},
		},
		FilePath: path,
		GenericInput: tos.GenericInput{RequestHeader: map[string]string{
			tos.HeaderIfNoneMatch: "*",
		}},
	})
	if errUpload != nil {
		if isTOSObjectConflict(errUpload) {
			return fmt.Errorf("%w for %s: %w", ErrObjectConflict, objectKey, errUpload)
		}
		return fmt.Errorf("put TOS object: %w", errUpload)
	}
	return nil
}

// tosMultipartThreshold is the minimum archive size that uses multipart upload.
// Kept far below the TOS single-PUT hard limit (5 GiB) so typical multi-GB
// hourly .zst archives get per-part retries under unstable uplinks.
const tosMultipartThreshold = 256 * 1024 * 1024

// tosMultipartPartSize is the size of each part for multipart uploads (64 MiB).
const tosMultipartPartSize = 64 * 1024 * 1024

// tosMultipartConcurrency is the number of parts uploaded in parallel.
const tosMultipartConcurrency = 8

// shouldUseMultipart reports whether an archive of the given size should be
// uploaded with multipart (true for size >= 256 MiB).
func shouldUseMultipart(fileSize int64) bool {
	return fileSize >= tosMultipartThreshold
}

func (u *TOSUploader) uploadMultipart(ctx context.Context, bucket, objectKey, path string, fileSize int64) error {
	multipartStart := time.Now()
	createOut, errCreate := u.client.CreateMultipartUploadV2(ctx, &tos.CreateMultipartUploadV2Input{
		Bucket:          bucket,
		Key:             objectKey,
		ContentType:     "application/zstd",
		ForbidOverwrite: true,
		Meta: map[string]string{
			archiveChecksumMetadataKey: "multipart",
		},
	})
	if errCreate != nil {
		return fmt.Errorf("create multipart upload: %w", errCreate)
	}
	uploadID := createOut.UploadID

	// Pre-calculate all parts.
	type partSpec struct {
		number int
		offset int64
		size   int64
	}
	var specs []partSpec
	var offset int64
	num := 1
	for offset < fileSize {
		var size int64 = tosMultipartPartSize
		if offset+size > fileSize {
			size = fileSize - offset
		}
		specs = append(specs, partSpec{number: num, offset: offset, size: size})
		offset += size
		num++
	}

	totalParts := len(specs)
	log.WithFields(log.Fields{
		"object_key":   objectKey,
		"total_parts":  totalParts,
		"part_size_mb": tosMultipartPartSize / (1024 * 1024),
		"concurrency":  tosMultipartConcurrency,
	}).Info("starting multipart upload")

	// Upload parts with bounded concurrency.
	var (
		mu       sync.Mutex
		parts    = make([]tos.UploadedPartV2, 0, totalParts)
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, tosMultipartConcurrency)

	for _, spec := range specs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(s partSpec) {
			defer wg.Done()
			defer func() { <-sem }()

			var partOut *tos.UploadPartFromFileOutput
			var errPart error
			for attempt := 0; attempt < 3; attempt++ {
				if attempt > 0 {
					log.WithFields(log.Fields{
						"part_number": s.number,
						"attempt":     attempt + 1,
						"error":       errPart.Error(),
					}).Warn("retrying multipart upload part")
				}
				partStart := time.Now()
				partOut, errPart = u.client.UploadPartFromFile(ctx, &tos.UploadPartFromFileInput{
					UploadPartBasicInput: tos.UploadPartBasicInput{
						Bucket:     bucket,
						Key:        objectKey,
						UploadID:   uploadID,
						PartNumber: s.number,
					},
					FilePath: path,
					Offset:   uint64(s.offset),
					PartSize: s.size,
				})
				if errPart == nil {
					log.WithFields(log.Fields{
						"part_number": s.number,
						"total_parts": totalParts,
						"size_mb":     s.size / (1024 * 1024),
						"duration":    time.Since(partStart).String(),
					}).Info("multipart part uploaded")
					break
				}
				if ctx.Err() != nil {
					break
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if errPart != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("upload part %d: %w", s.number, errPart)
				}
				return
			}
			parts = append(parts, tos.UploadedPartV2{
				PartNumber: s.number,
				ETag:       partOut.ETag,
			})
		}(spec)
	}
	wg.Wait()

	if firstErr != nil {
		_, _ = u.client.AbortMultipartUpload(ctx, &tos.AbortMultipartUploadInput{
			Bucket:   bucket,
			Key:      objectKey,
			UploadID: uploadID,
		})
		return firstErr
	}

	// Sort parts by part number (required by TOS).
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})

	_, errComplete := u.client.CompleteMultipartUploadV2(ctx, &tos.CompleteMultipartUploadV2Input{
		Bucket:          bucket,
		Key:             objectKey,
		UploadID:        uploadID,
		ForbidOverwrite: true,
		Parts:           parts,
	})
	if errComplete != nil {
		_, _ = u.client.AbortMultipartUpload(ctx, &tos.AbortMultipartUploadInput{
			Bucket:   bucket,
			Key:      objectKey,
			UploadID: uploadID,
		})
		return fmt.Errorf("complete multipart upload: %w", errComplete)
	}
	log.WithFields(log.Fields{
		"object_key":     objectKey,
		"file_size_mb":   fileSize / (1024 * 1024),
		"total_parts":    totalParts,
		"total_duration": time.Since(multipartStart).String(),
	}).Info("multipart upload completed")
	return nil
}

// MatchObject reports whether a remote object has the same size and SHA-256 metadata as a local archive.
func (u *TOSUploader) MatchObject(ctx context.Context, bucket, objectKey, path string) (bool, error) {
	checksum, size, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		return false, errChecksum
	}
	output, errHead := u.client.HeadObjectV2(ctx, &tos.HeadObjectV2Input{
		Bucket: bucket,
		Key:    objectKey,
	})
	if errHead != nil {
		return false, fmt.Errorf("head TOS object: %w", errHead)
	}
	if output == nil {
		return false, fmt.Errorf("head TOS object returned no output")
	}
	if output.ContentLength != size || output.Meta == nil {
		return false, nil
	}
	remoteChecksum, exists := output.Meta.Get(archiveChecksumMetadataKey)
	if !exists {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(remoteChecksum), checksum), nil
}

func isTOSObjectConflict(err error) bool {
	var serverError *tos.TosServerError
	if !errors.As(err, &serverError) {
		return false
	}
	switch serverError.Code {
	case codes.PreconditionFailed:
		return serverError.StatusCode == 412
	case codes.DuplicateObject:
		return serverError.StatusCode == 409
	default:
		return false
	}
}

func fileSHA256(path string) (string, int64, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return "", 0, fmt.Errorf("open archive for checksum: %w", errOpen)
	}
	hash := sha256.New()
	size, errCopy := io.Copy(hash, file)
	errClose := file.Close()
	if errCombined := errors.Join(errCopy, errClose); errCombined != nil {
		return "", 0, fmt.Errorf("checksum archive: %w", errCombined)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), size, nil
}
