package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesHandlerRejectsInvalidToolsBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{
		"model":"test-model",
		"input":"test",
		"tools":[
			{"type":"function","name":"example_namespace__lookup_value"},
			{
				"type":"namespace",
				"name":"example_namespace",
				"tools":[{"type":"function","name":"lookup_value"}]
			}
		]
	}`

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
