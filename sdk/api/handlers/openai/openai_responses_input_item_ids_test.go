package openai

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestNormalizeResponsesInputItemIDsRewritesObservedLocalIDs(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":[{"type":"function_call","id":"item_call123","call_id":"call_1","name":"wait"},{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"done"},{"type":"message","id":"item_msg456","role":"assistant","content":[]}]}`)

	got := normalizeResponsesInputItemIDs(payload)

	checks := map[string]string{
		"input.0.id":      "fc_call123",
		"input.0.call_id": "call_1",
		"input.1.id":      "fco_1",
		"input.1.call_id": "call_1",
		"input.2.id":      "msg_msg456",
		"input.2.role":    "assistant",
	}
	for path, want := range checks {
		if actual := gjson.GetBytes(got, path).String(); actual != want {
			t.Fatalf("%s = %q, want %q; payload=%s", path, actual, want, got)
		}
	}
}

func TestNormalizeResponsesInputItemIDsPreservesValidAndUnknownItems(t *testing.T) {
	payload := []byte(`{"input":[{"type":"function_call","id":"fc_valid","call_id":"call_1"},{"type":"message","id":"msg_valid","role":"assistant"},{"type":"custom_tool_call","id":"item_custom","call_id":"call_2"}]}`)

	got := normalizeResponsesInputItemIDs(payload)

	if !bytes.Equal(got, payload) {
		t.Fatalf("payload changed unexpectedly: %s", got)
	}
}

func TestResponsesNormalizesLocalInputItemIDsBeforeExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &responsesMultiAgentCaptureExecutor{}
	handler, modelID := newResponsesMultiAgentTestHandler(t, executor)
	router := gin.New()
	router.POST("/v1/responses", handler.Responses)

	payload := fmt.Sprintf(`{"model":%q,"input":[{"type":"function_call","id":"item_call123","call_id":"call_1","name":"wait"},{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"done"},{"type":"message","id":"item_msg456","role":"assistant","content":[]}],"stream":false}`, modelID)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("captured payload count = %d, want 1", len(payloads))
	}
	captured := payloads[0]
	if actual := gjson.GetBytes(captured, "input.0.id").String(); actual != "fc_call123" {
		t.Fatalf("function_call id = %q, want fc_call123; payload=%s", actual, captured)
	}
	if actual := gjson.GetBytes(captured, "input.2.id").String(); actual != "msg_msg456" {
		t.Fatalf("message id = %q, want msg_msg456; payload=%s", actual, captured)
	}
	if actual := gjson.GetBytes(captured, "input.0.call_id").String(); actual != "call_1" {
		t.Fatalf("call_id = %q, want call_1; payload=%s", actual, captured)
	}
}
