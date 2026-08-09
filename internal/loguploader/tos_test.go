package loguploader

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
)

type fakeTOSObjectClient struct {
	calls                  int
	input                  *tos.PutObjectFromFileInput
	result                 *tos.PutObjectFromFileOutput
	putV2Calls             int
	putV2Input             *tos.PutObjectV2Input
	putV2Result            *tos.PutObjectV2Output
	uploadedContent        []byte
	beforePut              func() error
	err                    error
	headCalls              int
	headInput              *tos.HeadObjectV2Input
	headResult             *tos.HeadObjectV2Output
	headErr                error
	headMetadataFromCreate bool
	headContentLength      int64
	createCalls            int
	createInput            *tos.CreateMultipartUploadV2Input
	beforeCreate           func() error
	createErr              error
	partFromFileCalls      int
	partV2Calls            int
	partV2Inputs           []*tos.UploadPartV2Input
	rejectPartMD5Mismatch  bool
	completeCalls          int
	completeErr            error
	abortCalls             int
	abortInput             *tos.AbortMultipartUploadInput
}

func (c *fakeTOSObjectClient) PutObjectFromFile(_ context.Context, input *tos.PutObjectFromFileInput) (*tos.PutObjectFromFileOutput, error) {
	c.calls++
	copyInput := *input
	copyInput.Meta = make(map[string]string, len(input.Meta))
	for key, value := range input.Meta {
		copyInput.Meta[key] = value
	}
	copyInput.RequestHeader = make(map[string]string, len(input.RequestHeader))
	for key, value := range input.RequestHeader {
		copyInput.RequestHeader[key] = value
	}
	c.input = &copyInput
	if c.beforePut != nil {
		if errBefore := c.beforePut(); errBefore != nil {
			return nil, errBefore
		}
	}
	content, errRead := os.ReadFile(input.FilePath)
	if errRead != nil {
		return nil, errRead
	}
	c.uploadedContent = content
	return c.result, c.err
}

func (c *fakeTOSObjectClient) PutObjectV2(_ context.Context, input *tos.PutObjectV2Input) (*tos.PutObjectV2Output, error) {
	c.putV2Calls++
	copyInput := *input
	copyInput.Meta = make(map[string]string, len(input.Meta))
	for key, value := range input.Meta {
		copyInput.Meta[key] = value
	}
	copyInput.RequestHeader = make(map[string]string, len(input.RequestHeader))
	for key, value := range input.RequestHeader {
		copyInput.RequestHeader[key] = value
	}
	c.putV2Input = &copyInput
	if c.beforePut != nil {
		if errBefore := c.beforePut(); errBefore != nil {
			return nil, errBefore
		}
	}
	content, errRead := io.ReadAll(input.Content)
	if errRead != nil {
		return nil, errRead
	}
	c.uploadedContent = content
	return c.putV2Result, c.err
}

func (c *fakeTOSObjectClient) HeadObjectV2(_ context.Context, input *tos.HeadObjectV2Input) (*tos.HeadObjectV2Output, error) {
	c.headCalls++
	copyInput := *input
	c.headInput = &copyInput
	if c.headMetadataFromCreate && c.createInput != nil {
		metadata := make(fakeTOSMetadata, len(c.createInput.Meta))
		for key, value := range c.createInput.Meta {
			metadata[strings.ToLower(key)] = value
		}
		return &tos.HeadObjectV2Output{ObjectMetaV2: tos.ObjectMetaV2{
			ContentLength: c.headContentLength,
			Meta:          metadata,
		}}, c.headErr
	}
	return c.headResult, c.headErr
}

func (c *fakeTOSObjectClient) CreateMultipartUploadV2(_ context.Context, input *tos.CreateMultipartUploadV2Input) (*tos.CreateMultipartUploadV2Output, error) {
	c.createCalls++
	copyInput := *input
	copyInput.Meta = make(map[string]string, len(input.Meta))
	for key, value := range input.Meta {
		copyInput.Meta[key] = value
	}
	c.createInput = &copyInput
	if c.beforeCreate != nil {
		if errBefore := c.beforeCreate(); errBefore != nil {
			return nil, errBefore
		}
	}
	if c.createErr != nil {
		return nil, c.createErr
	}
	return &tos.CreateMultipartUploadV2Output{UploadID: "test-upload-id"}, nil
}

func (c *fakeTOSObjectClient) UploadPartFromFile(_ context.Context, input *tos.UploadPartFromFileInput) (*tos.UploadPartFromFileOutput, error) {
	c.partFromFileCalls++
	return &tos.UploadPartFromFileOutput{UploadPartV2Output: tos.UploadPartV2Output{ETag: "test-etag", PartNumber: input.PartNumber}}, nil
}

func (c *fakeTOSObjectClient) UploadPartV2(_ context.Context, input *tos.UploadPartV2Input) (*tos.UploadPartV2Output, error) {
	c.partV2Calls++
	copyInput := *input
	c.partV2Inputs = append(c.partV2Inputs, &copyInput)
	content, errRead := io.ReadAll(input.Content)
	if errRead != nil {
		return nil, errRead
	}
	if c.rejectPartMD5Mismatch {
		digest := md5.Sum(content)
		if got := base64.StdEncoding.EncodeToString(digest[:]); got != input.ContentMD5 {
			return nil, errors.New("fake TOS rejected multipart Content-MD5 mismatch")
		}
	}
	return &tos.UploadPartV2Output{ETag: "test-etag", PartNumber: input.PartNumber}, nil
}

func (c *fakeTOSObjectClient) CompleteMultipartUploadV2(_ context.Context, input *tos.CompleteMultipartUploadV2Input) (*tos.CompleteMultipartUploadV2Output, error) {
	c.completeCalls++
	if c.completeErr != nil {
		return nil, c.completeErr
	}
	return &tos.CompleteMultipartUploadV2Output{}, nil
}

func (c *fakeTOSObjectClient) AbortMultipartUpload(_ context.Context, input *tos.AbortMultipartUploadInput) (*tos.AbortMultipartUploadOutput, error) {
	c.abortCalls++
	copyInput := *input
	c.abortInput = &copyInput
	return &tos.AbortMultipartUploadOutput{}, nil
}

type fakeTOSMetadata map[string]string

func (m fakeTOSMetadata) AllKeys() []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func (m fakeTOSMetadata) Get(key string) (string, bool) {
	value, exists := m[strings.ToLower(key)]
	return value, exists
}

func (m fakeTOSMetadata) Range(yield func(key, value string) bool) {
	for key, value := range m {
		if !yield(key, value) {
			return
		}
	}
}

func mustObjectIdentity(t *testing.T, path string) objectIdentity {
	t.Helper()
	checksum, size, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		t.Fatalf("checksum object identity fixture: %v", errChecksum)
	}
	return objectIdentity{Size: size, SHA256: checksum}
}

func TestFileSHA256(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("abc"), 0o600); errWrite != nil {
		t.Fatalf("write checksum fixture: %v", errWrite)
	}
	checksum, size, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		t.Fatalf("checksum file: %v", errChecksum)
	}
	if size != 3 {
		t.Errorf("size = %d, want 3", size)
	}
	if checksum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("checksum = %q", checksum)
	}
}

func TestUploadFileRejectsInvalidExpectedIdentityBeforeTOS(t *testing.T) {
	t.Parallel()

	const validChecksum = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	tests := []struct {
		name     string
		expected objectIdentity
	}{
		{name: "negative size", expected: objectIdentity{Size: -1, SHA256: validChecksum}},
		{name: "short checksum", expected: objectIdentity{Size: 3, SHA256: validChecksum[:63]}},
		{name: "non-hex checksum", expected: objectIdentity{Size: 3, SHA256: strings.Repeat("z", 64)}},
		{name: "uppercase checksum", expected: objectIdentity{Size: 3, SHA256: strings.ToUpper(validChecksum)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeTOSObjectClient{}
			uploader := &TOSUploader{client: client}
			errUpload := uploader.UploadFile(context.Background(), "llm-d1", "logs/archive.jsonl.zst", filepath.Join(t.TempDir(), "missing"), test.expected)
			if errUpload == nil || errUpload.Error() != "invalid expected archive identity" {
				t.Fatalf("invalid identity error = %v", errUpload)
			}
			if client.putV2Calls != 0 || client.calls != 0 || client.createCalls != 0 {
				t.Fatalf("invalid identity reached TOS: put_v2=%d put_from_file=%d create=%d", client.putV2Calls, client.calls, client.createCalls)
			}
		})
	}
}

func TestUploadFileKeepsPreparedHandleWhenPathIsReplacedAtPUT(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.jsonl.zst")
	backupPath := filepath.Join(dir, "opened-archive.jsonl.zst")
	original := []byte("prepared archive bytes")
	replacement := []byte("replacement data bytes")
	if len(original) != len(replacement) {
		t.Fatalf("replacement fixture size = %d, want %d", len(replacement), len(original))
	}
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("write prepared archive: %v", errWrite)
	}
	expected := mustObjectIdentity(t, path)
	client := &fakeTOSObjectClient{putV2Result: &tos.PutObjectV2Output{}}
	pathReplaced := false
	replacementBlockedByOpenHandle := false
	client.beforePut = func() error {
		if errRename := os.Rename(path, backupPath); errRename != nil {
			if runtime.GOOS == "windows" {
				replacementBlockedByOpenHandle = true
				return nil
			}
			return fmt.Errorf("rename opened prepared archive: %w", errRename)
		}
		if errWrite := os.WriteFile(path, replacement, 0o600); errWrite != nil {
			return fmt.Errorf("write replacement archive path: %w", errWrite)
		}
		pathReplaced = true
		return nil
	}
	uploader := &TOSUploader{client: client}

	if errUpload := uploader.UploadFile(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, expected); errUpload != nil {
		t.Fatalf("UploadFile: %v", errUpload)
	}
	if client.putV2Calls != 1 || client.calls != 0 || client.putV2Input == nil {
		t.Fatalf("single PUT calls = reader:%d path:%d, want 1 and 0", client.putV2Calls, client.calls)
	}
	if !bytes.Equal(client.uploadedContent, original) {
		t.Fatalf("uploaded content = %q, want prepared bytes %q", client.uploadedContent, original)
	}
	if !pathReplaced && !replacementBlockedByOpenHandle {
		t.Fatal("path replacement was neither completed nor blocked by the open archive handle")
	}
	if client.putV2Input.ContentLength != expected.Size || client.putV2Input.ContentSHA256 != expected.SHA256 {
		t.Fatalf("single PUT identity = length:%d sha:%q, want %d and %q", client.putV2Input.ContentLength, client.putV2Input.ContentSHA256, expected.Size, expected.SHA256)
	}
}

func TestShouldUseMultipart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		size int64
		want bool
	}{
		{name: "empty", size: 0, want: false},
		{name: "small archive", size: 16 * 1024 * 1024, want: false},
		{name: "just under threshold", size: tosMultipartThreshold - 1, want: false},
		{name: "at threshold uses multipart", size: tosMultipartThreshold, want: true},
		{name: "multi-GB archive", size: 4 * 1024 * 1024 * 1024, want: true},
		{name: "above former 5GiB single-put limit", size: 6 * 1024 * 1024 * 1024, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseMultipart(test.size); got != test.want {
				t.Fatalf("shouldUseMultipart(%d) = %v, want %v", test.size, got, test.want)
			}
		})
	}
}

func TestMultipartUploadTuningDefaults(t *testing.T) {
	t.Parallel()
	if tosMultipartConcurrency != 4 {
		t.Fatalf("tosMultipartConcurrency = %d, want 4", tosMultipartConcurrency)
	}
	if tosMultipartPartTimeout != 5*time.Minute {
		t.Fatalf("tosMultipartPartTimeout = %s, want 5m", tosMultipartPartTimeout)
	}
	if tosMultipartStallTimeout != 15*time.Minute {
		t.Fatalf("tosMultipartStallTimeout = %s, want 15m", tosMultipartStallTimeout)
	}
	if tosMultipartPartAttempts != 3 {
		t.Fatalf("tosMultipartPartAttempts = %d, want 3", tosMultipartPartAttempts)
	}
}

func TestUploadFileUsesMultipartAtThreshold(t *testing.T) {
	t.Parallel()

	// Sparse/truncating to the threshold size avoids writing 256 MiB of data.
	path := filepath.Join(t.TempDir(), "large.jsonl.zst")
	file, errCreate := os.Create(path)
	if errCreate != nil {
		t.Fatalf("create fixture: %v", errCreate)
	}
	if errTruncate := file.Truncate(tosMultipartThreshold); errTruncate != nil {
		_ = file.Close()
		t.Fatalf("truncate fixture: %v", errTruncate)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("close fixture: %v", errClose)
	}

	client := &countingMultipartTOSClient{}
	uploader := &TOSUploader{client: client}
	if errUpload := uploader.UploadFile(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, mustObjectIdentity(t, path)); errUpload != nil {
		t.Fatalf("UploadFile: %v", errUpload)
	}
	if client.putCalls != 0 {
		t.Fatalf("PutObjectFromFile calls = %d, want 0 (multipart path)", client.putCalls)
	}
	if client.createCalls != 1 {
		t.Fatalf("CreateMultipartUploadV2 calls = %d, want 1", client.createCalls)
	}
	if client.partCalls < 1 {
		t.Fatalf("UploadPartFromFile calls = %d, want >= 1", client.partCalls)
	}
	if client.completeCalls != 1 {
		t.Fatalf("CompleteMultipartUploadV2 calls = %d, want 1", client.completeCalls)
	}
}

func TestMultipartUploadStoresLocalSHA256Metadata(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("trusted multipart archive"), 0o600); errWrite != nil {
		t.Fatalf("write multipart fixture: %v", errWrite)
	}
	wantChecksum, size, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		t.Fatalf("checksum multipart fixture: %v", errChecksum)
	}
	client := &fakeTOSObjectClient{}
	uploader := &TOSUploader{client: client}

	if errUpload := uploader.uploadMultipart(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, objectIdentity{Size: size, SHA256: wantChecksum}); errUpload != nil {
		t.Fatalf("uploadMultipart: %v", errUpload)
	}
	if client.createCalls != 1 || client.createInput == nil {
		t.Fatalf("CreateMultipartUploadV2 calls = %d, want 1", client.createCalls)
	}
	if got := client.createInput.Meta[archiveChecksumMetadataKey]; got != wantChecksum {
		t.Fatalf("multipart checksum metadata = %q, want local SHA-256 %q", got, wantChecksum)
	}
}

func TestMultipartUploadRejectsChangedLocalSizeBeforeCreate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("size-checked multipart archive"), 0o600); errWrite != nil {
		t.Fatalf("write multipart fixture: %v", errWrite)
	}
	expected := mustObjectIdentity(t, path)
	expected.Size++
	client := &fakeTOSObjectClient{}
	uploader := &TOSUploader{client: client}

	errUpload := uploader.uploadMultipart(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, expected)
	if !errors.Is(errUpload, errPreparedArchiveIdentityMismatch) {
		t.Fatalf("changed-size upload error = %v, want prepared identity mismatch", errUpload)
	}
	if client.createCalls != 0 {
		t.Fatalf("CreateMultipartUploadV2 calls = %d, want 0 after size mismatch", client.createCalls)
	}
}

func TestMultipartUploadRejectsSameInodeMutationWithPreflightPartMD5(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	original := []byte("prepared multipart archive bytes")
	mutated := bytes.Clone(original)
	mutated[len(mutated)/2] ^= 0xff
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("write multipart fixture: %v", errWrite)
	}
	expected := mustObjectIdentity(t, path)
	wantMD5 := md5.Sum(original)
	client := &fakeTOSObjectClient{rejectPartMD5Mismatch: true}
	client.beforeCreate = func() error {
		return os.WriteFile(path, mutated, 0o600)
	}
	uploader := &TOSUploader{client: client}

	errUpload := uploader.uploadMultipart(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, expected)
	if errUpload == nil || !strings.Contains(errUpload.Error(), "Content-MD5 mismatch") {
		t.Fatalf("same-inode mutation error = %v, want server MD5 rejection", errUpload)
	}
	if client.partV2Calls == 0 || len(client.partV2Inputs) == 0 {
		t.Fatalf("UploadPartV2 calls = %d, want at least 1", client.partV2Calls)
	}
	if got := client.partV2Inputs[0].ContentMD5; got != base64.StdEncoding.EncodeToString(wantMD5[:]) {
		t.Fatalf("part Content-MD5 = %q, want prepared digest %q", got, base64.StdEncoding.EncodeToString(wantMD5[:]))
	}
	if client.partV2Inputs[0].ContentLength != int64(len(original)) {
		t.Fatalf("part Content-Length = %d, want %d", client.partV2Inputs[0].ContentLength, len(original))
	}
	if client.partFromFileCalls != 0 {
		t.Fatalf("UploadPartFromFile calls = %d, want 0", client.partFromFileCalls)
	}
	if client.completeCalls != 0 || client.abortCalls != 1 {
		t.Fatalf("multipart finalization = complete:%d abort:%d, want 0 and 1", client.completeCalls, client.abortCalls)
	}
}

func TestMultipartCreateConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "409 duplicate object",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusConflict}, Code: codes.DuplicateObject},
		},
		{
			name: "412 precondition failed",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed}, Code: codes.PreconditionFailed},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
			if errWrite := os.WriteFile(path, []byte("multipart conflict"), 0o600); errWrite != nil {
				t.Fatalf("write multipart fixture: %v", errWrite)
			}
			client := &fakeTOSObjectClient{createErr: test.err}
			uploader := &TOSUploader{client: client}
			errUpload := uploader.uploadMultipart(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, mustObjectIdentity(t, path))
			if !errors.Is(errUpload, ErrObjectConflict) {
				t.Fatalf("upload error = %v, want ErrObjectConflict", errUpload)
			}
			if !errors.Is(errUpload, test.err) {
				t.Fatalf("upload error = %v, want preserved create error", errUpload)
			}
			if client.abortCalls != 0 {
				t.Fatalf("AbortMultipartUpload calls = %d, want 0 before an upload ID exists", client.abortCalls)
			}
		})
	}
}

func TestMultipartCompleteConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "409 duplicate object",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusConflict}, Code: codes.DuplicateObject},
		},
		{
			name: "412 precondition failed",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed}, Code: codes.PreconditionFailed},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
			if errWrite := os.WriteFile(path, []byte("multipart complete conflict"), 0o600); errWrite != nil {
				t.Fatalf("write multipart fixture: %v", errWrite)
			}
			client := &fakeTOSObjectClient{completeErr: test.err}
			uploader := &TOSUploader{client: client}
			errUpload := uploader.uploadMultipart(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path, mustObjectIdentity(t, path))
			if !errors.Is(errUpload, ErrObjectConflict) {
				t.Fatalf("upload error = %v, want ErrObjectConflict", errUpload)
			}
			if !errors.Is(errUpload, test.err) {
				t.Fatalf("upload error = %v, want preserved complete error", errUpload)
			}
			if client.abortCalls != 1 || client.abortInput == nil {
				t.Fatalf("AbortMultipartUpload calls = %d, want 1", client.abortCalls)
			}
			if client.abortInput.Bucket != "llm-d1" || client.abortInput.Key != "logs/archive.jsonl.zst" || client.abortInput.UploadID != "test-upload-id" {
				t.Errorf("unexpected abort target: bucket=%q key=%q upload_id=%q", client.abortInput.Bucket, client.abortInput.Key, client.abortInput.UploadID)
			}
		})
	}
}

// countingMultipartTOSClient tracks single-put vs multipart API usage.
type countingMultipartTOSClient struct {
	putCalls      int
	createCalls   int
	partCalls     int
	completeCalls int
	abortCalls    int
}

func (c *countingMultipartTOSClient) PutObjectFromFile(context.Context, *tos.PutObjectFromFileInput) (*tos.PutObjectFromFileOutput, error) {
	c.putCalls++
	return &tos.PutObjectFromFileOutput{}, nil
}

func (c *countingMultipartTOSClient) PutObjectV2(context.Context, *tos.PutObjectV2Input) (*tos.PutObjectV2Output, error) {
	c.putCalls++
	return &tos.PutObjectV2Output{}, nil
}

func (c *countingMultipartTOSClient) HeadObjectV2(context.Context, *tos.HeadObjectV2Input) (*tos.HeadObjectV2Output, error) {
	return &tos.HeadObjectV2Output{}, nil
}

func (c *countingMultipartTOSClient) CreateMultipartUploadV2(context.Context, *tos.CreateMultipartUploadV2Input) (*tos.CreateMultipartUploadV2Output, error) {
	c.createCalls++
	return &tos.CreateMultipartUploadV2Output{UploadID: "test-upload-id"}, nil
}

func (c *countingMultipartTOSClient) UploadPartFromFile(_ context.Context, input *tos.UploadPartFromFileInput) (*tos.UploadPartFromFileOutput, error) {
	c.partCalls++
	return &tos.UploadPartFromFileOutput{UploadPartV2Output: tos.UploadPartV2Output{ETag: "etag", PartNumber: input.PartNumber}}, nil
}

func (c *countingMultipartTOSClient) UploadPartV2(_ context.Context, input *tos.UploadPartV2Input) (*tos.UploadPartV2Output, error) {
	c.partCalls++
	return &tos.UploadPartV2Output{ETag: "etag", PartNumber: input.PartNumber}, nil
}

func (c *countingMultipartTOSClient) CompleteMultipartUploadV2(context.Context, *tos.CompleteMultipartUploadV2Input) (*tos.CompleteMultipartUploadV2Output, error) {
	c.completeCalls++
	return &tos.CompleteMultipartUploadV2Output{}, nil
}

func (c *countingMultipartTOSClient) AbortMultipartUpload(context.Context, *tos.AbortMultipartUploadInput) (*tos.AbortMultipartUploadOutput, error) {
	c.abortCalls++
	return &tos.AbortMultipartUploadOutput{}, nil
}

func TestParseTOSEndpoint(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "native host defaults to HTTPS",
			input: "tos-cn-beijing.volces.com",
			want:  "https://tos-cn-beijing.volces.com",
		},
		{
			name:  "explicit HTTPS and root slash",
			input: " https://tos-cn-beijing.volces.com/ ",
			want:  "https://tos-cn-beijing.volces.com",
		},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			endpoint, errParse := parseTOSEndpoint(test.input)
			if errParse != nil {
				t.Fatalf("parse endpoint: %v", errParse)
			}
			if endpoint != test.want {
				t.Errorf("parseTOSEndpoint(%q) = %q, want %q", test.input, endpoint, test.want)
			}
		})
	}

	invalid := []string{
		"",
		"http://127.0.0.1:9000",
		"ftp://tos-cn-beijing.volces.com",
		"https://user:secret@tos-cn-beijing.volces.com",
		"https://tos-cn-beijing.volces.com/path",
		"https://tos-cn-beijing.volces.com?region=cn-beijing",
		"https://tos-cn-beijing.volces.com#fragment",
		"https://tos-s3-cn-beijing.volces.com",
	}
	for _, input := range invalid {
		t.Run("reject "+strings.ReplaceAll(input, "/", "_"), func(t *testing.T) {
			if _, errParse := parseTOSEndpoint(input); errParse == nil {
				t.Errorf("parseTOSEndpoint(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestLoadTOSCredentialsSupportsOptionalSessionToken(t *testing.T) {
	t.Setenv("TEST_TOS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("TEST_TOS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("TEST_TOS_SESSION_TOKEN", "test-session-token")

	credentials, errLoad := loadTOSCredentials(UploadConfig{
		AccessKeyIDEnv:     "TEST_TOS_ACCESS_KEY_ID",
		SecretAccessKeyEnv: "TEST_TOS_SECRET_ACCESS_KEY",
		SessionTokenEnv:    "TEST_TOS_SESSION_TOKEN",
	})
	if errLoad != nil {
		t.Fatalf("load credentials: %v", errLoad)
	}
	credential := credentials.Credential()
	if credential.AccessKeyID != "test-access-key" || credential.AccessKeySecret != "test-secret-key" || credential.SecurityToken != "test-session-token" {
		t.Errorf("unexpected static credentials: access=%q secret_matches=%t token=%q", credential.AccessKeyID, credential.AccessKeySecret == "test-secret-key", credential.SecurityToken)
	}

	t.Setenv("TEST_TOS_SESSION_TOKEN", "")
	credentials, errLoad = loadTOSCredentials(UploadConfig{
		AccessKeyIDEnv:     "TEST_TOS_ACCESS_KEY_ID",
		SecretAccessKeyEnv: "TEST_TOS_SECRET_ACCESS_KEY",
		SessionTokenEnv:    "TEST_TOS_SESSION_TOKEN",
	})
	if errLoad != nil {
		t.Fatalf("load credentials without session token: %v", errLoad)
	}
	if token := credentials.Credential().SecurityToken; token != "" {
		t.Errorf("optional session token = %q, want empty", token)
	}
}

func TestNewTOSUploaderUsesNativeSDKWithoutNetworkRequest(t *testing.T) {
	t.Setenv("TEST_TOS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("TEST_TOS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("TEST_TOS_SESSION_TOKEN", "test-session-token")

	uploader, errNew := NewTOSUploader(UploadConfig{
		Endpoint:           "https://tos-cn-beijing.volces.com",
		Region:             "cn-beijing",
		AccessKeyIDEnv:     "TEST_TOS_ACCESS_KEY_ID",
		SecretAccessKeyEnv: "TEST_TOS_SECRET_ACCESS_KEY",
		SessionTokenEnv:    "TEST_TOS_SESSION_TOKEN",
	})
	if errNew != nil {
		t.Fatalf("create TOS uploader: %v", errNew)
	}
	if _, ok := uploader.client.(*tos.ClientV2); !ok {
		t.Errorf("uploader client type = %T, want *tos.ClientV2", uploader.client)
	}
}

func TestNewTOSUploaderRequiresCredentialEnvironment(t *testing.T) {
	t.Setenv("TEST_TOS_MISSING_ACCESS_KEY_ID", "")
	t.Setenv("TEST_TOS_MISSING_SECRET_ACCESS_KEY", "")

	_, errNew := NewTOSUploader(UploadConfig{
		Endpoint:           "https://tos-cn-beijing.volces.com",
		Region:             "cn-beijing",
		AccessKeyIDEnv:     "TEST_TOS_MISSING_ACCESS_KEY_ID",
		SecretAccessKeyEnv: "TEST_TOS_MISSING_SECRET_ACCESS_KEY",
	})
	if errNew == nil || !strings.Contains(errNew.Error(), "TEST_TOS_MISSING_ACCESS_KEY_ID") {
		t.Fatalf("missing access key error = %v", errNew)
	}
}

func TestUploadFileUsesForbidOverwriteAndChecksumMetadata(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	content := []byte("abc")
	if errWrite := os.WriteFile(path, content, 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	expected := mustObjectIdentity(t, path)
	client := &fakeTOSObjectClient{putV2Result: &tos.PutObjectV2Output{}}
	uploader := &TOSUploader{client: client}
	if errUpload := uploader.UploadFile(context.Background(), "llm-d1", "logs/panda/archive.jsonl.zst", path, expected); errUpload != nil {
		t.Fatalf("upload file: %v", errUpload)
	}
	if client.putV2Calls != 1 || client.calls != 0 || client.putV2Input == nil {
		t.Fatalf("single PUT calls = reader:%d path:%d, want 1 and 0", client.putV2Calls, client.calls)
	}
	input := client.putV2Input
	if input.Bucket != "llm-d1" || input.Key != "logs/panda/archive.jsonl.zst" {
		t.Errorf("unexpected PutObjectV2 target: bucket=%q key=%q", input.Bucket, input.Key)
	}
	if input.ContentLength != expected.Size || input.ContentSHA256 != expected.SHA256 {
		t.Errorf("PutObjectV2 identity = length:%d sha:%q, want %d and %q", input.ContentLength, input.ContentSHA256, expected.Size, expected.SHA256)
	}
	if !bytes.Equal(client.uploadedContent, content) {
		t.Errorf("PutObjectV2 content = %q, want %q", client.uploadedContent, content)
	}
	if !input.ForbidOverwrite {
		t.Errorf("ForbidOverwrite = false, want true")
	}
	if got := input.RequestHeader[tos.HeaderIfNoneMatch]; got != "*" {
		t.Errorf("If-None-Match header = %q, want *", got)
	}
	if input.ContentType != "application/zstd" {
		t.Errorf("ContentType = %q, want application/zstd", input.ContentType)
	}
	if got, want := input.Meta[archiveChecksumMetadataKey], "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Errorf("checksum metadata = %q, want %q", got, want)
	}
}

func TestMatchObjectComparesChecksumAndSize(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("abc"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	const checksum = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	tests := []struct {
		name      string
		length    int64
		metadata  tos.Metadata
		wantMatch bool
	}{
		{
			name:      "matching checksum and size",
			length:    3,
			metadata:  fakeTOSMetadata{archiveChecksumMetadataKey: strings.ToUpper(checksum)},
			wantMatch: true,
		},
		{
			name:     "checksum mismatch",
			length:   3,
			metadata: fakeTOSMetadata{archiveChecksumMetadataKey: strings.Repeat("0", 64)},
		},
		{
			name:     "size mismatch",
			length:   4,
			metadata: fakeTOSMetadata{archiveChecksumMetadataKey: checksum},
		},
		{
			name:     "checksum metadata missing",
			length:   3,
			metadata: fakeTOSMetadata{"different-key": checksum},
		},
		{
			name:   "metadata absent",
			length: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeTOSObjectClient{headResult: &tos.HeadObjectV2Output{
				ObjectMetaV2: tos.ObjectMetaV2{
					ContentLength: test.length,
					Meta:          test.metadata,
				},
			}}
			uploader := &TOSUploader{client: client}
			matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", objectIdentity{Size: 3, SHA256: checksum})
			if errMatch != nil {
				t.Fatalf("match object: %v", errMatch)
			}
			if matched != test.wantMatch {
				t.Errorf("matched = %t, want %t", matched, test.wantMatch)
			}
			if client.headCalls != 1 || client.headInput == nil {
				t.Fatalf("HeadObjectV2 calls = %d, want 1", client.headCalls)
			}
			if client.headInput.Bucket != "llm-d1" || client.headInput.Key != "logs/archive.jsonl.zst" {
				t.Errorf("unexpected HeadObjectV2 target: bucket=%q key=%q", client.headInput.Bucket, client.headInput.Key)
			}
		})
	}
}

func TestMatchObjectPreservesHeadError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("archive"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	headError := errors.New("head unavailable")
	client := &fakeTOSObjectClient{headErr: headError}
	uploader := &TOSUploader{client: client}
	matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", mustObjectIdentity(t, path))
	if matched {
		t.Errorf("matched = true after HeadObjectV2 failure")
	}
	if errMatch == nil || !errors.Is(errMatch, headError) {
		t.Fatalf("match error = %v, want wrapped head error", errMatch)
	}
	if client.headCalls != 1 {
		t.Errorf("HeadObjectV2 calls = %d, want 1", client.headCalls)
	}
}

func TestMatchObjectDoesNotDependOnLocalPathAfterExpectedIdentityIsKnown(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("prepared archive identity"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	checksum, size, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		t.Fatalf("checksum archive: %v", errChecksum)
	}
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatalf("remove local archive before HEAD recovery: %v", errRemove)
	}
	client := &fakeTOSObjectClient{headResult: &tos.HeadObjectV2Output{
		ObjectMetaV2: tos.ObjectMetaV2{
			ContentLength: size,
			Meta:          fakeTOSMetadata{archiveChecksumMetadataKey: checksum},
		},
	}}
	uploader := &TOSUploader{client: client}

	matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", objectIdentity{Size: size, SHA256: checksum})
	if errMatch != nil || !matched {
		t.Fatalf("expected-identity HEAD match = (%t, %v), want true without reading local path", matched, errMatch)
	}
	if client.headCalls != 1 {
		t.Fatalf("HeadObjectV2 calls = %d, want 1", client.headCalls)
	}
}

func TestMatchObjectRejectsMissingHeadOutput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("archive"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	uploader := &TOSUploader{client: &fakeTOSObjectClient{}}
	matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", mustObjectIdentity(t, path))
	if matched || errMatch == nil {
		t.Fatalf("MatchObject() = (%t, %v), want false and error", matched, errMatch)
	}
}

func TestUploadFileMapsOnlyExactObjectConflicts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("archive"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	expected := mustObjectIdentity(t, path)
	tests := []struct {
		name         string
		err          error
		wantConflict bool
	}{
		{
			name:         "412 precondition failed",
			err:          &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed}, Code: codes.PreconditionFailed},
			wantConflict: true,
		},
		{
			name: "wrapped 412 precondition failed",
			err: fmt.Errorf("SDK wrapper: %w", &tos.TosServerError{
				RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed},
				Code:        codes.PreconditionFailed,
			}),
			wantConflict: true,
		},
		{
			name:         "409 duplicate object",
			err:          &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusConflict}, Code: codes.DuplicateObject},
			wantConflict: true,
		},
		{
			name: "412 without condition code",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed}},
		},
		{
			name: "412 access denied code",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed}, Code: codes.AccessDenied},
		},
		{
			name: "409 precondition failed code",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusConflict}, Code: codes.PreconditionFailed},
		},
		{
			name: "412 duplicate object code",
			err:  &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusPreconditionFailed}, Code: codes.DuplicateObject},
		},
		{
			name: "unexpected status error without TOS code",
			err:  tos.NewUnexpectedStatusCodeError(http.StatusPreconditionFailed, http.StatusOK),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uploader := &TOSUploader{client: &fakeTOSObjectClient{err: test.err}}
			errUpload := uploader.UploadFile(context.Background(), "llm-d1", "existing.jsonl.zst", path, expected)
			if gotConflict := errors.Is(errUpload, ErrObjectConflict); gotConflict != test.wantConflict {
				t.Fatalf("errors.Is(ErrObjectConflict) = %t, want %t: %v", gotConflict, test.wantConflict, errUpload)
			}
			if test.wantConflict {
				var original *tos.TosServerError
				if !errors.As(errUpload, &original) {
					t.Fatalf("conflict error does not preserve original TosServerError: %v", errUpload)
				}
				if original.StatusCode != http.StatusPreconditionFailed && original.StatusCode != http.StatusConflict {
					t.Errorf("preserved status = %d", original.StatusCode)
				}
			} else if !errors.Is(errUpload, test.err) {
				t.Fatalf("non-conflict error does not preserve original error: %v", errUpload)
			}
		})
	}
}

func TestUploadFilePreservesNonConflictError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("archive"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	serverError := &tos.TosServerError{RequestInfo: tos.RequestInfo{StatusCode: http.StatusInternalServerError}}
	uploader := &TOSUploader{client: &fakeTOSObjectClient{err: serverError}}
	errUpload := uploader.UploadFile(context.Background(), "llm-d1", "archive.jsonl.zst", path, mustObjectIdentity(t, path))
	if errUpload == nil || !errors.Is(errUpload, serverError) {
		t.Fatalf("upload error = %v, want wrapped server error", errUpload)
	}
	if errors.Is(errUpload, ErrObjectConflict) {
		t.Fatalf("500 error was incorrectly mapped to ErrObjectConflict")
	}
}
