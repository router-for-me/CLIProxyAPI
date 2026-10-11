package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	handlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

func TestJSONHandlersRejectOversizedRawBody(t *testing.T) {
	const rawBodyLimit = 16 << 20
	body := bytes.Repeat([]byte("x"), rawBodyLimit+1)

	tests := []struct {
		name string
		call func(*gin.Context)
	}{
		{name: "chat completions", call: (&OpenAIAPIHandler{}).ChatCompletions},
		{name: "completions", call: (&OpenAIAPIHandler{}).Completions},
		{name: "responses", call: (&OpenAIResponsesAPIHandler{}).Responses},
		{name: "compact", call: (&OpenAIResponsesAPIHandler{}).Compact},
		{name: "images generations", call: (&OpenAIAPIHandler{}).ImagesGenerations},
		{name: "images edits", call: (&OpenAIAPIHandler{}).ImagesEdits},
		{name: "videos create", call: (&OpenAIAPIHandler{}).VideosCreate},
		{name: "xai videos generations", call: (&OpenAIAPIHandler{}).XAIVideosGenerations},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			tt.call(c)

			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
			}
			if got := gjson.GetBytes(recorder.Body.Bytes(), "error.code").String(); got != handlers.RequestBodyTooLargeCode {
				t.Fatalf("error.code = %q, want %q; body=%s", got, handlers.RequestBodyTooLargeCode, recorder.Body.String())
			}
		})
	}
}

func TestJSONHandlersRejectOversizedDecodedBody(t *testing.T) {
	const rawBodyLimit = 16 << 20
	decodedBody := bytes.Repeat([]byte("x"), rawBodyLimit+1)
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err = encoder.Write(decodedBody); err != nil {
		t.Fatalf("zstd encoder write: %v", err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatalf("zstd encoder close: %v", err)
	}
	if compressed.Len() >= rawBodyLimit {
		t.Fatalf("compressed body length = %d, want below %d", compressed.Len(), rawBodyLimit)
	}

	tests := []struct {
		name string
		call func(*gin.Context)
	}{
		{name: "chat completions", call: (&OpenAIAPIHandler{}).ChatCompletions},
		{name: "completions", call: (&OpenAIAPIHandler{}).Completions},
		{name: "responses", call: (&OpenAIResponsesAPIHandler{}).Responses},
		{name: "compact", call: (&OpenAIResponsesAPIHandler{}).Compact},
		{name: "images generations", call: (&OpenAIAPIHandler{}).ImagesGenerations},
		{name: "images edits", call: (&OpenAIAPIHandler{}).ImagesEdits},
		{name: "videos create", call: (&OpenAIAPIHandler{}).VideosCreate},
		{name: "xai videos generations", call: (&OpenAIAPIHandler{}).XAIVideosGenerations},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(compressed.Bytes()))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Content-Encoding", "zstd")

			tt.call(c)

			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
			}
			if got := gjson.GetBytes(recorder.Body.Bytes(), "error.code").String(); got != handlers.RequestBodyTooLargeCode {
				t.Fatalf("error.code = %q, want %q; body=%s", got, handlers.RequestBodyTooLargeCode, recorder.Body.String())
			}
		})
	}
}

func TestResponsesPreservesMalformedEncodedBodyAsBadRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("not-zstd")))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Content-Encoding", "zstd")

	(&OpenAIResponsesAPIHandler{}).Responses(c)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; body=%s", got, recorder.Body.String())
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.code").String(); got == handlers.RequestBodyTooLargeCode {
		t.Fatalf("error.code = %q, want ordinary decode error", got)
	}
}
