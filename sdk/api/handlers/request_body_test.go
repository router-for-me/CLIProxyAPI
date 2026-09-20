package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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

func requestBodyTestContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	return c, recorder
}
