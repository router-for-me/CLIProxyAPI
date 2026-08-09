package loguploader

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
)

var ErrObjectConflict = errors.New("TOS object already exists and overwrite is forbidden")

var errPreparedArchiveIdentityMismatch = errors.New("prepared archive identity mismatch")

const archiveChecksumMetadataKey = "cliproxy-sha256"

type tosObjectClient interface {
	PutObjectV2(context.Context, *tos.PutObjectV2Input) (*tos.PutObjectV2Output, error)
	HeadObjectV2(context.Context, *tos.HeadObjectV2Input) (*tos.HeadObjectV2Output, error)
	CreateMultipartUploadV2(context.Context, *tos.CreateMultipartUploadV2Input) (*tos.CreateMultipartUploadV2Output, error)
	UploadPartV2(context.Context, *tos.UploadPartV2Input) (*tos.UploadPartV2Output, error)
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

func (u *TOSUploader) UploadFile(ctx context.Context, bucket, objectKey, path string, expected objectIdentity) error {
	if errIdentity := validateObjectIdentity(expected); errIdentity != nil {
		return errIdentity
	}
	// Prefer multipart for multi-hundred-MiB archives so flaky links only
	// retry a 64 MiB part instead of re-sending a multi-GB single PUT.
	if shouldUseMultipart(expected.Size) {
		return u.uploadMultipart(ctx, bucket, objectKey, path, expected)
	}
	archive, _, errOpen := openVerifiedArchive(path, expected, 0)
	if errOpen != nil {
		return errOpen
	}
	defer closeUploadedArchive(archive, objectKey)
	_, errUpload := u.client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket:          bucket,
			Key:             objectKey,
			ContentLength:   expected.Size,
			ContentSHA256:   expected.SHA256,
			ContentType:     "application/zstd",
			ForbidOverwrite: true,
			Meta: map[string]string{
				archiveChecksumMetadataKey: expected.SHA256,
			},
		},
		Content: io.NewSectionReader(archive, 0, expected.Size),
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
// Kept moderate so weak uplinks are less likely to stall half-open connections.
const tosMultipartConcurrency = 4

// tosMultipartPartTimeout bounds a single UploadPartV2 call so a hung
// TCP write becomes an error and can be retried instead of blocking forever.
const tosMultipartPartTimeout = 5 * time.Minute

// tosMultipartStallTimeout cancels the whole multipart upload when no part has
// succeeded for this long (progress watchdog).
const tosMultipartStallTimeout = 15 * time.Minute

// tosMultipartPartAttempts is the max tries per part (including the first).
const tosMultipartPartAttempts = 3

// shouldUseMultipart reports whether an archive of the given size should be
// uploaded with multipart (true for size >= 256 MiB).
func shouldUseMultipart(fileSize int64) bool {
	return fileSize >= tosMultipartThreshold
}

func (u *TOSUploader) uploadMultipart(ctx context.Context, bucket, objectKey, path string, expected objectIdentity) error {
	multipartStart := time.Now()
	if errIdentity := validateObjectIdentity(expected); errIdentity != nil {
		return errIdentity
	}
	archive, specs, errOpen := openVerifiedArchive(path, expected, tosMultipartPartSize)
	if errOpen != nil {
		return errOpen
	}
	defer closeUploadedArchive(archive, objectKey)
	createOut, errCreate := u.client.CreateMultipartUploadV2(ctx, &tos.CreateMultipartUploadV2Input{
		Bucket:          bucket,
		Key:             objectKey,
		ContentType:     "application/zstd",
		ForbidOverwrite: true,
		Meta: map[string]string{
			archiveChecksumMetadataKey: expected.SHA256,
		},
	})
	if errCreate != nil {
		if isTOSObjectConflict(errCreate) {
			return fmt.Errorf("%w for %s: %w", ErrObjectConflict, objectKey, errCreate)
		}
		return fmt.Errorf("create multipart upload: %w", errCreate)
	}
	uploadID := createOut.UploadID

	totalParts := len(specs)
	log.WithFields(log.Fields{
		"object_key":    objectKey,
		"total_parts":   totalParts,
		"part_size_mb":  tosMultipartPartSize / (1024 * 1024),
		"concurrency":   tosMultipartConcurrency,
		"part_timeout":  tosMultipartPartTimeout.String(),
		"stall_timeout": tosMultipartStallTimeout.String(),
	}).Info("starting multipart upload")

	// uploadCtx is cancelled on parent cancel, part failure, or stall watchdog.
	uploadCtx, cancelUpload := context.WithCancel(ctx)
	defer cancelUpload()

	var lastProgress atomic.Value // time.Time of last successful part
	lastProgress.Store(time.Now())

	watchDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-uploadCtx.Done():
				return
			case <-ticker.C:
				last, _ := lastProgress.Load().(time.Time)
				if last.IsZero() {
					continue
				}
				stalledFor := time.Since(last)
				if stalledFor < tosMultipartStallTimeout {
					continue
				}
				log.WithFields(log.Fields{
					"object_key":  objectKey,
					"stalled_for": stalledFor.String(),
					"timeout":     tosMultipartStallTimeout.String(),
				}).Error("multipart upload stalled with no successful parts; cancelling")
				cancelUpload()
				return
			}
		}
	}()
	defer close(watchDone)

	// Upload parts with bounded concurrency.
	var (
		mu       sync.Mutex
		parts    = make([]tos.UploadedPartV2, 0, totalParts)
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, tosMultipartConcurrency)

	for _, spec := range specs {
		if uploadCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(s multipartPartSpec) {
			defer wg.Done()
			defer func() { <-sem }()

			var partOut *tos.UploadPartV2Output
			var errPart error
			for attempt := 0; attempt < tosMultipartPartAttempts; attempt++ {
				if uploadCtx.Err() != nil {
					errPart = uploadCtx.Err()
					break
				}
				if attempt > 0 {
					log.WithFields(log.Fields{
						"part_number": s.number,
						"attempt":     attempt + 1,
						"error":       errPart.Error(),
					}).Warn("retrying multipart upload part")
				}
				partStart := time.Now()
				partCtx, cancelPart := context.WithTimeout(uploadCtx, tosMultipartPartTimeout)
				partOut, errPart = u.client.UploadPartV2(partCtx, &tos.UploadPartV2Input{
					UploadPartBasicInput: tos.UploadPartBasicInput{
						Bucket:     bucket,
						Key:        objectKey,
						UploadID:   uploadID,
						PartNumber: s.number,
						ContentMD5: s.contentMD5,
					},
					Content:       io.NewSectionReader(archive, s.offset, s.size),
					ContentLength: s.size,
				})
				cancelPart()
				if errPart == nil {
					lastProgress.Store(time.Now())
					log.WithFields(log.Fields{
						"part_number": s.number,
						"total_parts": totalParts,
						"size_mb":     s.size / (1024 * 1024),
						"duration":    time.Since(partStart).String(),
					}).Info("multipart part uploaded")
					break
				}
				if errors.Is(errPart, context.DeadlineExceeded) {
					errPart = fmt.Errorf("part %d attempt %d timed out after %s: %w", s.number, attempt+1, tosMultipartPartTimeout, errPart)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if errPart != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("upload part %d: %w", s.number, errPart)
					// Stop sibling workers; do not wait forever on hung peers.
					cancelUpload()
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
		u.abortMultipartUpload(bucket, objectKey, uploadID)
		return firstErr
	}
	if errCtx := uploadCtx.Err(); errCtx != nil && len(parts) < totalParts {
		u.abortMultipartUpload(bucket, objectKey, uploadID)
		return fmt.Errorf("multipart upload cancelled: %w", errCtx)
	}
	if len(parts) != totalParts {
		u.abortMultipartUpload(bucket, objectKey, uploadID)
		return fmt.Errorf("multipart incomplete: got %d parts, want %d", len(parts), totalParts)
	}

	// Sort parts by part number (required by TOS).
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})

	completeCtx, cancelComplete := context.WithTimeout(ctx, tosMultipartPartTimeout)
	defer cancelComplete()
	_, errComplete := u.client.CompleteMultipartUploadV2(completeCtx, &tos.CompleteMultipartUploadV2Input{
		Bucket:          bucket,
		Key:             objectKey,
		UploadID:        uploadID,
		ForbidOverwrite: true,
		Parts:           parts,
	})
	if errComplete != nil {
		u.abortMultipartUpload(bucket, objectKey, uploadID)
		if isTOSObjectConflict(errComplete) {
			return fmt.Errorf("%w for %s: %w", ErrObjectConflict, objectKey, errComplete)
		}
		return fmt.Errorf("complete multipart upload: %w", errComplete)
	}
	log.WithFields(log.Fields{
		"object_key":     objectKey,
		"file_size_mb":   expected.Size / (1024 * 1024),
		"total_parts":    totalParts,
		"total_duration": time.Since(multipartStart).String(),
	}).Info("multipart upload completed")
	return nil
}

// abortMultipartUpload best-effort aborts a remote multipart session. Uses a
// short background timeout so cleanup still runs when the upload context was
// already cancelled by stall/part timeout.
func (u *TOSUploader) abortMultipartUpload(bucket, objectKey, uploadID string) {
	abortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, errAbort := u.client.AbortMultipartUpload(abortCtx, &tos.AbortMultipartUploadInput{
		Bucket:   bucket,
		Key:      objectKey,
		UploadID: uploadID,
	})
	if errAbort != nil {
		log.WithError(errAbort).WithFields(log.Fields{
			"object_key": objectKey,
			"upload_id":  uploadID,
		}).Warn("abort multipart upload failed")
	}
}

// MatchObject reports whether a remote object has the expected size and SHA-256 metadata.
func (u *TOSUploader) MatchObject(ctx context.Context, bucket, objectKey string, expected objectIdentity) (bool, error) {
	if errIdentity := validateObjectIdentity(expected); errIdentity != nil {
		return false, errIdentity
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
	if output.ContentLength != expected.Size || output.Meta == nil {
		return false, nil
	}
	remoteChecksum, exists := output.Meta.Get(archiveChecksumMetadataKey)
	if !exists {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(remoteChecksum), expected.SHA256), nil
}

func validateObjectIdentity(expected objectIdentity) error {
	if expected.Size < 0 || !isSHA256(expected.SHA256) || strings.ToLower(expected.SHA256) != expected.SHA256 {
		return fmt.Errorf("invalid expected archive identity")
	}
	return nil
}

type multipartPartSpec struct {
	number     int
	offset     int64
	size       int64
	contentMD5 string
}

func openVerifiedArchive(path string, expected objectIdentity, partSize int64) (*os.File, []multipartPartSpec, error) {
	archive, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, nil, fmt.Errorf("open archive for upload: %w", errOpen)
	}
	specs, errVerify := verifyOpenedArchive(archive, expected, partSize)
	if errVerify != nil {
		if errClose := archive.Close(); errClose != nil {
			return nil, nil, errors.Join(errVerify, fmt.Errorf("close rejected archive: %w", errClose))
		}
		return nil, nil, errVerify
	}
	return archive, specs, nil
}

func verifyOpenedArchive(archive *os.File, expected objectIdentity, partSize int64) ([]multipartPartSpec, error) {
	info, errStat := archive.Stat()
	if errStat != nil {
		return nil, fmt.Errorf("stat prepared archive identity: %w", errStat)
	}
	if info.Size() != expected.Size {
		return nil, errPreparedArchiveIdentityMismatch
	}

	checksum := sha256.New()
	if partSize <= 0 {
		copied, errCopy := io.Copy(checksum, io.NewSectionReader(archive, 0, expected.Size))
		if errCopy != nil {
			return nil, fmt.Errorf("verify prepared archive identity: %w", errCopy)
		}
		if copied != expected.Size {
			return nil, errPreparedArchiveIdentityMismatch
		}
	} else {
		var specs []multipartPartSpec
		for offset, number := int64(0), 1; offset < expected.Size; number++ {
			size := partSize
			if offset+size > expected.Size {
				size = expected.Size - offset
			}
			partChecksum := md5.New()
			copied, errCopy := io.Copy(
				io.MultiWriter(checksum, partChecksum),
				io.NewSectionReader(archive, offset, size),
			)
			if errCopy != nil {
				return nil, fmt.Errorf("verify prepared multipart archive identity: %w", errCopy)
			}
			if copied != size {
				return nil, errPreparedArchiveIdentityMismatch
			}
			specs = append(specs, multipartPartSpec{
				number:     number,
				offset:     offset,
				size:       size,
				contentMD5: base64.StdEncoding.EncodeToString(partChecksum.Sum(nil)),
			})
			offset += size
		}
		if errIdentity := verifyOpenedArchiveResult(archive, expected, checksum); errIdentity != nil {
			return nil, errIdentity
		}
		return specs, nil
	}
	if errIdentity := verifyOpenedArchiveResult(archive, expected, checksum); errIdentity != nil {
		return nil, errIdentity
	}
	return nil, nil
}

func verifyOpenedArchiveResult(archive *os.File, expected objectIdentity, checksum hash.Hash) error {
	info, errStat := archive.Stat()
	if errStat != nil {
		return fmt.Errorf("restat prepared archive identity: %w", errStat)
	}
	if info.Size() != expected.Size || fmt.Sprintf("%x", checksum.Sum(nil)) != expected.SHA256 {
		return errPreparedArchiveIdentityMismatch
	}
	return nil
}

func closeUploadedArchive(archive *os.File, objectKey string) {
	if errClose := archive.Close(); errClose != nil {
		log.WithError(errClose).WithField("object_key", objectKey).Warn("close uploaded archive failed")
	}
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
