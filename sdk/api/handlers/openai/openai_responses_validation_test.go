package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesHandlerRejectsMalformedJSONBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{"model":"test-model","input":"test"`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &OpenAIResponsesAPIHandler{}
	h.Responses(c)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if got := gjson.Get(recorder.Body.String(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; body=%s", got, recorder.Body.String())
	}
}

func TestOpenAIChatCompletionsHandlerRejectsMalformedJSONBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","input":"test"`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &OpenAIAPIHandler{}
	h.ChatCompletions(c)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if got := gjson.Get(recorder.Body.String(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; body=%s", got, recorder.Body.String())
	}
}

func TestOpenAIChatCompletionsHandlerRejectsInvalidResponsesToolsBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{
		"model":"test-model",
		"input":"test",
		"tools":[
			{"type":"function","name":"math__math_add"},
			{
				"type":"namespace",
				"name":"math",
				"tools":[{"type":"function","name":"math_add"}]
			}
		]
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &OpenAIAPIHandler{}
	h.ChatCompletions(c)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if got := gjson.Get(recorder.Body.String(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; body=%s", got, recorder.Body.String())
	}
}

func TestOpenAIResponsesHandlerAllowsProviderSpecificTools(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{
		"model":"test-model",
		"input":"test",
		"tools":[{"type":"web_search","name":"lookup_value"}]
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	defer func() {
		if recover() == nil {
			t.Fatalf("expected request to continue past early validation")
		}
	}()
	h := &OpenAIResponsesAPIHandler{}
	h.Responses(c)
}
