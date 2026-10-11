package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"
)

func TestReadRequestBodyRejectsRawBodyAboveLimit(t *testing.T) {
	c, _ := requestBodyTestContext(bytes.Repeat([]byte("x"), requestBodyLimit+1))

	_, err := ReadRequestBody(c)
	if err == nil {
		t.Fatal("ReadRequestBody() error = nil, want request body too large")
	}
	var tooLargeErr *RequestBodyTooLargeError
	if !errors.As(err, &tooLargeErr) {
		t.Fatalf("ReadRequestBody() error = %T %v, want RequestBodyTooLargeError", err, err)
	}
	if tooLargeErr.Limit != requestBodyLimit {
		t.Fatalf("limit = %d, want %d", tooLargeErr.Limit, requestBodyLimit)
	}
}

func TestReadRequestBodyAcceptsRawBodyAtLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), requestBodyLimit)
	c, _ := requestBodyTestContext(body)

	got, err := ReadRequestBody(c)
	if err != nil {
		t.Fatalf("ReadRequestBody() error = %v, want nil", err)
	}
	if len(got) != len(body) {
		t.Fatalf("body length = %d, want %d", len(got), len(body))
	}
}

func TestReadRequestBodyRejectsDecodedBodyAboveLimit(t *testing.T) {
	decodedBody := bytes.Repeat([]byte("x"), requestBodyLimit+1)
	compressedBody := compressRequestBody(t, decodedBody)
	if len(compressedBody) >= requestBodyLimit {
		t.Fatalf("compressed body length = %d, want below %d", len(compressedBody), requestBodyLimit)
	}

	c, _ := requestBodyTestContext(compressedBody)
	c.Request.Header.Set("Content-Encoding", "zstd")

	_, err := ReadRequestBody(c)
	if err == nil {
		t.Fatal("ReadRequestBody() error = nil, want decoded body too large")
	}
	var tooLargeErr *RequestBodyTooLargeError
	if !errors.As(err, &tooLargeErr) {
		t.Fatalf("ReadRequestBody() error = %T %v, want RequestBodyTooLargeError", err, err)
	}
	if tooLargeErr.RawBytes != int64(len(compressedBody)) {
		t.Fatalf("raw bytes = %d, want %d", tooLargeErr.RawBytes, len(compressedBody))
	}
	if tooLargeErr.DecodedBytes != requestBodyLimit+1 {
		t.Fatalf("decoded bytes = %d, want %d", tooLargeErr.DecodedBytes, requestBodyLimit+1)
	}
	if tooLargeErr.Route != "/v1/responses" {
		t.Fatalf("route = %q, want /v1/responses", tooLargeErr.Route)
	}
	if tooLargeErr.ContentEncoding != "zstd" {
		t.Fatalf("content encoding = %q, want zstd", tooLargeErr.ContentEncoding)
	}
}

func TestReadRequestBodyAcceptsDecodedBodyAtLimit(t *testing.T) {
	decodedBody := bytes.Repeat([]byte("x"), requestBodyLimit)
	compressedBody := compressRequestBody(t, decodedBody)
	c, _ := requestBodyTestContext(compressedBody)
	c.Request.Header.Set("Content-Encoding", "zstd")

	got, err := ReadRequestBody(c)
	if err != nil {
		t.Fatalf("ReadRequestBody() error = %v, want nil", err)
	}
	if len(got) != len(decodedBody) {
		t.Fatalf("decoded body length = %d, want %d", len(got), len(decodedBody))
	}
}

func TestReadRequestBodyPreservesOrdinaryDecodeError(t *testing.T) {
	c, _ := requestBodyTestContext([]byte("not-zstd"))
	c.Request.Header.Set("Content-Encoding", "zstd")

	_, err := ReadRequestBody(c)
	if err == nil {
		t.Fatal("ReadRequestBody() error = nil, want decode error")
	}
	var tooLargeErr *RequestBodyTooLargeError
	if errors.As(err, &tooLargeErr) {
		t.Fatalf("ReadRequestBody() error = %v, want ordinary decode error", err)
	}
}

func TestReadRequestBodyDecodedOverflowReportsOriginalRawSize(t *testing.T) {
	decodedBody := bytes.Repeat([]byte("x"), requestBodyLimit+1)
	innerCompressed := compressRequestBody(t, decodedBody)
	outerCompressed := compressRequestBody(t, innerCompressed)
	c, _ := requestBodyTestContext(outerCompressed)
	c.Request.Header.Set("Content-Encoding", "zstd, zstd")

	_, err := ReadRequestBody(c)
	var tooLargeErr *RequestBodyTooLargeError
	if !errors.As(err, &tooLargeErr) {
		t.Fatalf("ReadRequestBody() error = %T %v, want RequestBodyTooLargeError", err, err)
	}
	if tooLargeErr.RawBytes != int64(len(outerCompressed)) {
		t.Fatalf("raw bytes = %d, want original transport size %d", tooLargeErr.RawBytes, len(outerCompressed))
	}
}

func TestWriteRequestBodyErrorReturnsStable413Contract(t *testing.T) {
	c, recorder := requestBodyTestContext(nil)
	err := &RequestBodyTooLargeError{Limit: requestBodyLimit}

	if !WriteRequestBodyError(c, err) {
		t.Fatal("WriteRequestBodyError() = false, want true")
	}
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}

	var response ErrorResponse
	if unmarshalErr := json.Unmarshal(recorder.Body.Bytes(), &response); unmarshalErr != nil {
		t.Fatalf("decode response: %v", unmarshalErr)
	}
	if response.Error.Code != RequestBodyTooLargeCode {
		t.Fatalf("error code = %q, want %q", response.Error.Code, RequestBodyTooLargeCode)
	}
	if response.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", response.Error.Type)
	}
}

func TestWriteRequestBodyErrorLogsOnlyBoundedMetadata(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	defer logger.ReplaceHooks(previousHooks)
	hook := &requestBodyLogHook{}
	logger.AddHook(hook)

	c, _ := requestBodyTestContext(nil)
	err := &RequestBodyTooLargeError{
		Limit:           requestBodyLimit,
		Route:           "/v1/responses",
		ContentEncoding: "zstd",
		RawBytes:        128,
		DecodedBytes:    requestBodyLimit + 1,
	}
	if !WriteRequestBodyError(c, err) {
		t.Fatal("WriteRequestBodyError() = false, want true")
	}
	if hook.entry == nil {
		t.Fatal("request body rejection did not emit a log entry")
	}
	allowed := map[string]bool{
		"route": true, "content_encoding": true, "raw_bytes": true,
		"decoded_bytes": true, "limit_bytes": true,
	}
	for key := range hook.entry.Data {
		if !allowed[key] {
			t.Fatalf("unexpected rejection metadata field %q", key)
		}
	}
}

func requestBodyTestContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	return c, recorder
}

func compressRequestBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err = encoder.Write(body); err != nil {
		t.Fatalf("zstd encoder write: %v", err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatalf("zstd encoder close: %v", err)
	}
	return compressed.Bytes()
}

type requestBodyLogHook struct {
	entry *log.Entry
}

func (h *requestBodyLogHook) Levels() []log.Level {
	return log.AllLevels
}

func (h *requestBodyLogHook) Fire(entry *log.Entry) error {
	h.entry = entry
	return nil
}
