package executor

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestDevinPrepareHTTPRequest_NormalizesCUARootOneOf(t *testing.T) {
	// Codex Responses sends the browser JS tool inside a namespace. The
	// oneOf constraint must be folded only on Devin's upstream wire payload.
	const payload = `{
		"model":"devin/claude-opus-5-5",
		"input":"Reply only OK",
		"tools":[
			{"type":"namespace","name":"mcp__cua_repl","tools":[
				{"type":"function","name":"js","parameters":{
					"type":"object",
					"properties":{"code":{"type":"string"},"completion_summary":{"type":"string"}},
					"oneOf":[{"required":["code"]},{"required":["completion_summary"]}]
				}},
				{"type":"function","name":"js_reset","parameters":{"type":"object","properties":{}}}
			]}
		]
	}`
	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "devin-root-union", Provider: "devin", Attributes: map[string]string{"api_key": "test-key"}}
	req := cliproxyexecutor.Request{Model: "devin/claude-opus-5-5", Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}

	httpReq, _, _, err := exec.prepareDevinHTTPRequest(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("prepareDevinHTTPRequest: %v", err)
	}
	framed, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	_, wire, err := helps.ReadConnectFrame(bytes.NewReader(framed))
	if err != nil {
		t.Fatalf("read connect frame: %v", err)
	}
	if !bytes.Contains(wire, []byte("mcp__cua_repl__js")) || !bytes.Contains(wire, []byte("mcp__cua_repl__js_reset")) {
		t.Fatalf("namespace tools were not preserved in wire request")
	}
	if bytes.Contains(wire, []byte(`"oneOf"`)) {
		t.Fatalf("root oneOf reached Devin upstream")
	}
	for _, key := range []string{`"code"`, `"completion_summary"`} {
		if !bytes.Contains(wire, []byte(key)) {
			t.Fatalf("expected property %s missing from Devin wire", key)
		}
	}
}
