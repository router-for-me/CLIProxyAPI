package loguploader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
)

type fakeTOSObjectClient struct {
	calls      int
	input      *tos.PutObjectFromFileInput
	result     *tos.PutObjectFromFileOutput
	err        error
	headCalls  int
	headInput  *tos.HeadObjectV2Input
	headResult *tos.HeadObjectV2Output
	headErr    error
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
	return c.result, c.err
}

func (c *fakeTOSObjectClient) HeadObjectV2(_ context.Context, input *tos.HeadObjectV2Input) (*tos.HeadObjectV2Output, error) {
	c.headCalls++
	copyInput := *input
	c.headInput = &copyInput
	return c.headResult, c.headErr
}

func (c *fakeTOSObjectClient) CreateMultipartUploadV2(_ context.Context, input *tos.CreateMultipartUploadV2Input) (*tos.CreateMultipartUploadV2Output, error) {
	return &tos.CreateMultipartUploadV2Output{UploadID: "test-upload-id"}, nil
}

func (c *fakeTOSObjectClient) UploadPartFromFile(_ context.Context, input *tos.UploadPartFromFileInput) (*tos.UploadPartFromFileOutput, error) {
	return &tos.UploadPartFromFileOutput{UploadPartV2Output: tos.UploadPartV2Output{ETag: "test-etag", PartNumber: input.PartNumber}}, nil
}

func (c *fakeTOSObjectClient) CompleteMultipartUploadV2(_ context.Context, input *tos.CompleteMultipartUploadV2Input) (*tos.CompleteMultipartUploadV2Output, error) {
	return &tos.CompleteMultipartUploadV2Output{}, nil
}

func (c *fakeTOSObjectClient) AbortMultipartUpload(_ context.Context, input *tos.AbortMultipartUploadInput) (*tos.AbortMultipartUploadOutput, error) {
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
	if errUpload := uploader.UploadFile(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path); errUpload != nil {
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
	if errWrite := os.WriteFile(path, []byte("abc"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	client := &fakeTOSObjectClient{result: &tos.PutObjectFromFileOutput{}}
	uploader := &TOSUploader{client: client}
	if errUpload := uploader.UploadFile(context.Background(), "llm-d1", "logs/panda/archive.jsonl.zst", path); errUpload != nil {
		t.Fatalf("upload file: %v", errUpload)
	}
	if client.calls != 1 || client.input == nil {
		t.Fatalf("PutObjectFromFile calls = %d, want 1", client.calls)
	}
	input := client.input
	if input.Bucket != "llm-d1" || input.Key != "logs/panda/archive.jsonl.zst" || input.FilePath != path {
		t.Errorf("unexpected PutObjectFromFile target: bucket=%q key=%q path=%q", input.Bucket, input.Key, input.FilePath)
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
			matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path)
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
	matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path)
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

func TestMatchObjectRejectsMissingHeadOutput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "archive.jsonl.zst")
	if errWrite := os.WriteFile(path, []byte("archive"), 0o600); errWrite != nil {
		t.Fatalf("write archive: %v", errWrite)
	}
	uploader := &TOSUploader{client: &fakeTOSObjectClient{}}
	matched, errMatch := uploader.MatchObject(context.Background(), "llm-d1", "logs/archive.jsonl.zst", path)
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
			errUpload := uploader.UploadFile(context.Background(), "llm-d1", "existing.jsonl.zst", path)
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
	errUpload := uploader.UploadFile(context.Background(), "llm-d1", "archive.jsonl.zst", path)
	if errUpload == nil || !errors.Is(errUpload, serverError) {
		t.Fatalf("upload error = %v, want wrapped server error", errUpload)
	}
	if errors.Is(errUpload, ErrObjectConflict) {
		t.Fatalf("500 error was incorrectly mapped to ErrObjectConflict")
	}
}
