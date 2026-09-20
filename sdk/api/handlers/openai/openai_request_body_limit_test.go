package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
