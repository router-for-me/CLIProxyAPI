package helps

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestPayloadFinalizerDefaultsUseOriginalAndNormalizeBeforeRules(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "alias", Protocol: "openai", FromProtocol: "claude", Headers: map[string]string{"X-Test": "yes"}}}, Params: map[string]any{"missing": "user default", "present": "not applied"}}},
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "alias"}}, Params: map[string]any{"tools.0.function.parameters.properties.count.type": "number"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "alias"}}, Params: []string{"late"}}},
	}}
	req := cliproxyexecutor.Request{Model: "alias"}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Test": {"yes"}, "User-Agent": {"codex-cli/0.1"}}}
	finalize := NewPayloadFinalizer(cfg, "openai", "upstream", "openai", "", []byte(`{"present":"caller"}`), req, opts)
	body := []byte(`{"missing":"built-in","present":"caller","late":"injected","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"count":{"type":"integer"}}}}}]}`)
	out := FinalizePayload(WithPayloadFinalizer(context.Background(), finalize), body)
	if gjson.GetBytes(out, "missing").String() != "user default" || gjson.GetBytes(out, "present").String() != "caller" || gjson.GetBytes(out, "late").Exists() || gjson.GetBytes(out, "tools.0.function.parameters.properties.count.type").String() != "number" {
		t.Fatalf("unexpected final payload: %s", out)
	}
}

func TestPayloadFinalizerDevinBusinessFieldsAndCredentials(t *testing.T) {
	wire := BuildDevinGetChatMessageRequest("secret-token", "device", "model", "system", []DevinPrompt{{MessageID: "id", Source: 1, Content: "hello"}}, []DevinTool{{Name: "f", Parameters: []byte(`{"type":"object"}`)}}, nil, 1024, "session", "cascade", nil)
	unchanged, _, errUnchanged := FinalizeDevinPayload(wire, func(body []byte) []byte { return body })
	if errUnchanged != nil || !bytes.Equal(unchanged, wire) {
		t.Fatalf("no-op changed protobuf: %v", errUnchanged)
	}
	final, view, errFinalize := FinalizeDevinPayload(wire, func(body []byte) []byte {
		body, _ = sjson.DeleteBytes(body, "system_prompt")
		body, _ = sjson.DeleteBytes(body, "tools")
		body, _ = sjson.SetBytes(body, "completion_config.max_tokens", 0)
		body, _ = sjson.SetBytes(body, "prompts.0.content", "configured")
		return body
	})
	if errFinalize != nil {
		t.Fatal(errFinalize)
	}
	if gjson.GetBytes(view, "system_prompt").Exists() || gjson.GetBytes(view, "tools").Exists() {
		t.Fatalf("filter undone: %s", view)
	}
	decoded, errDecode := decodeDevinPayload(final, devinPayloadFields)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if decoded["completion_config"].(map[string]any)["max_tokens"] != uint64(0) {
		t.Fatalf("default was re-injected: %v", decoded)
	}
	fieldOne := func(wire []byte) []byte {
		for len(wire) > 0 {
			num, kind, tagLen := protowire.ConsumeTag(wire)
			n := protowire.ConsumeFieldValue(num, kind, wire[tagLen:])
			if num == 1 {
				return wire[:tagLen+n]
			}
			wire = wire[tagLen+n:]
		}
		return nil
	}
	if !bytes.Equal(fieldOne(wire), fieldOne(final)) {
		t.Fatal("payload rules changed credentials")
	}
}

func TestPayloadFinalizerMultipartPreservesFiles(t *testing.T) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if errWrite := writer.WriteField("model", "original"); errWrite != nil {
		t.Fatal(errWrite)
	}
	file, errFile := writer.CreateFormFile("image", "input.png")
	if errFile != nil {
		t.Fatal(errFile)
	}
	if _, errWrite := file.Write([]byte{0, 255, 13, 10}); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "original"}}, Params: map[string]any{"model": "configured"}}}}}
	req := cliproxyexecutor.Request{Model: "original", Payload: buffer.Bytes()}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Content-Type": {writer.FormDataContentType()}}}
	body, contentType, errFinalize := ApplyMediaPayloadConfig(cfg, "openai", "original", "openai", req.Payload, writer.FormDataContentType(), req, opts)
	if errFinalize != nil {
		t.Fatal(errFinalize)
	}
	view, _, errView := mediaPayloadJSON(body, contentType)
	if errView != nil {
		t.Fatal(errView)
	}
	if gjson.GetBytes(view, "model").String() != "configured" || gjson.GetBytes(view, "image.data").String() != "AP8NCg==" {
		t.Fatalf("unexpected multipart business payload: %s", view)
	}
}

func TestPayloadRulesMatchFinalBodyAndTrackPaths(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 100}}}}, Params: map[string]any{"unexpected": true}},
			{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 300}}}}, Params: map[string]any{"diagnostics.user": true}}},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 300}}}}, Params: []string{"context_management", "messages.0"}}},
	}}
	original := []byte(`{"max_tokens":100,"messages":[{},{}]}`)
	body, touched := ApplyPayloadConfigWithTrackedPaths(cfg, "model", "claude", "claude", "", []byte(`{"max_tokens":300,"context_management":{"builtin":true},"messages":[{},{},{}]}`), original, "model", "", nil, "diagnostics", "context_management")
	if gjson.GetBytes(body, "unexpected").Exists() || !gjson.GetBytes(body, "diagnostics.user").Bool() || gjson.GetBytes(body, "context_management").Exists() || gjson.GetBytes(body, "messages.#").Int() != 2 {
		t.Fatalf("rules did not match final body: %s", body)
	}
	if !touched["diagnostics"] || !touched["context_management"] {
		t.Fatalf("final tracking lost: %v", touched)
	}
}
