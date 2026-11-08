package openai

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestSanitizeOrphanResponsesToolOutputsDoc(t *testing.T) {
	t.Parallel()

	doc := ojson.NewDoc([]byte(`{
		"model":"step-5-preview",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call_output","id":"fco_1","name":"codex_apps_open_page","output":"{\"page_id\":null}"},
			{"type":"custom_tool_call_output","id":"fco_2","name":"custom_ctx","output":[{"type":"input_text","text":"part one"},{"type":"input_text","text":"part two"}]},
			{"type":"function_call","call_id":"call_ok","name":"shell","arguments":""},
			{"type":"function_call_output","call_id":"call_ok","output":"ok"},
			{"type":"function_call_output","call_id":" ","output":"blank id"}
		]
	}`))

	if !sanitizeOrphanResponsesToolOutputsDoc(doc) {
		t.Fatal("sanitizeOrphanResponsesToolOutputsDoc returned false, want true")
	}

	output := doc.Bytes()
	for _, index := range []int{1, 2, 5} {
		itemType := ojson.GetBytesNoCopy(output, "input."+strconv.Itoa(index)+".type").String()
		if itemType != "message" {
			t.Fatalf("input.%d.type = %q, want message; output: %s", index, itemType, string(output))
		}
		role := ojson.GetBytesNoCopy(output, "input."+strconv.Itoa(index)+".role").String()
		if role != "user" {
			t.Fatalf("input.%d.role = %q, want user", index, role)
		}
	}

	if text := ojson.GetBytesNoCopy(output, "input.1.content.0.text").String(); !strings.Contains(text, "codex_apps_open_page") || !strings.Contains(text, `{"page_id":null}`) {
		t.Fatalf("orphan output text was not preserved: %q", text)
	}
	if text := ojson.GetBytesNoCopy(output, "input.2.content.0.text").String(); !strings.Contains(text, "part one") || !strings.Contains(text, "part two") {
		t.Fatalf("orphan output array text was not preserved: %q", text)
	}

	if itemType := ojson.GetBytesNoCopy(output, "input.4.type").String(); itemType != "function_call_output" {
		t.Fatalf("paired tool output was rewritten to %q; output: %s", itemType, string(output))
	}
	if callID := ojson.GetBytesNoCopy(output, "input.4.call_id").String(); callID != "call_ok" {
		t.Fatalf("paired tool output call_id = %q, want call_ok", callID)
	}
}

func TestSanitizeOrphanResponsesToolOutputsDocNoChange(t *testing.T) {
	t.Parallel()

	doc := ojson.NewDoc([]byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	if sanitizeOrphanResponsesToolOutputsDoc(doc) {
		t.Fatalf("sanitizeOrphanResponsesToolOutputsDoc rewrote a valid request: %s", string(doc.Bytes()))
	}
}

func TestPrepareCodexRequestPayloadSanitizesOrphanToolOutputs(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request

	payload := []byte(`{
		"model":"step-5-preview",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call_output","id":"fco_test","name":"codex_apps_open_page","output":"{\"page_id\":null}"}
		]
	}`)
	got := handler.prepareCodexRequestPayload(ginContext, payload)

	if itemType := ojson.GetBytesNoCopy(got, "input.1.type").String(); itemType != "message" {
		t.Fatalf("input.1.type = %q, want message; output: %s", itemType, string(got))
	}
	if text := ojson.GetBytesNoCopy(got, "input.1.content.0.text").String(); !strings.Contains(text, "codex_apps_open_page") {
		t.Fatalf("orphan output context was lost: %q", text)
	}
}
