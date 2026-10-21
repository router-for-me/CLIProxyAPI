package openai

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

type responsesHTTPObserveWriter struct{ write func([]byte) }

func (w responsesHTTPObserveWriter) Write(b []byte) (int, error) { w.write(b); return len(b), nil }

func TestResponsesHTTPContinuationVisibleAtTerminalWrite(t *testing.T) {
	h := NewOpenAIResponsesAPIHandler(nil)
	state := &responsesHTTPExecution{handler: h, scope: "caller", input: []byte(`[{"role":"user","content":"first"}]`), store: true, authID: "auth"}
	framer := &responsesSSEFramer{responsePayload: state.streamPayload}
	var visible bool
	writer := responsesHTTPObserveWriter{write: func(b []byte) {
		if bytes.Contains(b, []byte("response.completed")) {
			saved, ok := h.httpContinuations.load("caller", "terminal")
			visible = ok && bytes.Contains(saved.Output, []byte("encrypted_content"))
			if !gjson.GetBytes(mustResponsesHTTPData(t, b), "response.store").Bool() {
				t.Fatalf("terminal store=false: %s", b)
			}
		}
	}}
	framer.WriteChunk(writer, []byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\",\"summary\":[]}}\n\n"))
	framer.WriteChunk(writer, []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"terminal\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	if !visible {
		t.Fatal("repaired state was not saved before terminal became visible")
	}
}

func mustResponsesHTTPData(t *testing.T, b []byte) []byte {
	t.Helper()
	payload, ok := responsesSSEDataPayload(b)
	if !ok {
		t.Fatalf("invalid frame %s", b)
	}
	return payload
}

func TestResponsesHTTPContinuationRejectedSaveIsHonest(t *testing.T) {
	h := NewOpenAIResponsesAPIHandler(nil)
	h.httpContinuations.maxEntryBytes = 1
	state := &responsesHTTPExecution{handler: h, scope: "caller", input: []byte(`[]`), store: true, authID: "auth"}
	raw := state.response([]byte(`{"id":"large","status":"completed","output":[]}`), true)
	if gjson.GetBytes(raw, "store").Bool() {
		t.Fatalf("rejected save advertised storage: %s", raw)
	}
	if _, ok := h.httpContinuations.load("caller", "large"); ok {
		t.Fatal("oversized save retained")
	}
}

func TestResponsesHTTPContinuationTrustedContextAndInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIResponsesAPIHandler(nil)
	for _, tc := range []struct {
		input string
		valid bool
	}{{`"hello"`, true}, {`[{"type":"message","content":[{"type":"input_image","image_url":"data:image/png;base64,opaque"}]}]`, true}, {`42`, false}, {`{}`, false}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", nil)
		ctx := usage.WithAPIKey(context.Background(), "trusted-key")
		ctx = usage.WithAccessProvider(ctx, "trusted-provider")
		c.Request = c.Request.WithContext(ctx)
		c.Request.Header.Set("X-Tenant-ID", "forged")
		_, ok := h.prepareResponsesHTTP(c, []byte(`{"model":"model","input":`+tc.input+`}`))
		if ok != tc.valid {
			t.Fatalf("input %s valid=%v", tc.input, ok)
		}
		if responsesHTTPCallerScope(c) == "anonymous" {
			t.Fatal("trusted request context treated as anonymous")
		}
	}
}
