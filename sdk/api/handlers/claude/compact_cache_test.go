package claude

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCompactResponseReturnsRawCiphertextAndResumesWithoutPinning(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	opaque := rawCiphertextForTest(50000)
	raw := mustJSONMarshalForTest(t, map[string]any{
		"id": "compact-test", "object": "response.compaction",
		"output": []any{map[string]any{"type": "compaction", "encrypted_content": opaque}},
	})
	response, marker, err := buildClaudeCompactResponse(raw, responsesBridgeClientModel, responsesBridgeUpstreamModel)
	if err != nil {
		t.Fatal(err)
	}
	if marker != opaque || gjson.GetBytes(response, "content.0.text").String() != opaque {
		t.Fatal("ciphertext was wrapped or changed")
	}
	for _, stream := range []bool{false, true} {
		handler, executor := newResponsesBridgeHandler(t)
		body := mustJSONMarshalForTest(t, map[string]any{"model": responsesBridgeClientModel, "stream": stream, "max_tokens": 128, "messages": []any{
			map[string]any{"role": "user", "content": marker}, map[string]any{"role": "user", "content": "continue"},
		}})
		recorder := serveClaudeMessages(t, handler, "/v1/messages", string(body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("resume: %d %s", recorder.Code, recorder.Body.String())
		}
		if executor.options.Metadata[coreexecutor.PinnedAuthMetadataKey] != nil {
			t.Fatal("resume pinned a credential")
		}
		if strings.Contains(gjson.GetBytes(executor.request.Payload, "messages").Raw, "ref:") {
			t.Fatal("reference leaked into ordinary messages")
		}
		if gjson.GetBytes(executor.request.Payload, constant.ClaudeResponsesCompactionField+".output.0.encrypted_content").String() != opaque {
			t.Fatal("resume lost compact state")
		}
	}
}

func TestLegacyCompactCapsuleIgnoresRetiredCredential(t *testing.T) {
	payload := []byte(`{"version":1,"model":"gpt-5.6-sol","auth_id":"deleted-credential","output":[{"type":"compaction","encrypted_content":"old-state"}]}`)
	marker := claudeCompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(payload) + claudeCompactionCapsuleSuffix
	handler, executor := newResponsesBridgeHandler(t)
	body := mustJSONMarshalForTest(t, map[string]any{"model": responsesBridgeClientModel, "messages": []any{map[string]any{"role": "user", "content": marker}, map[string]any{"role": "user", "content": "continue"}}})
	recorder := serveClaudeMessages(t, handler, "/v1/messages", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy resume: %d %s", recorder.Code, recorder.Body.String())
	}
	if executor.options.Metadata[coreexecutor.PinnedAuthMetadataKey] != nil {
		t.Fatal("legacy auth_id pinned resume")
	}
}

func TestMissingCompactReferenceStopsBeforeUpstream(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	handler, executor := newResponsesBridgeHandler(t)
	marker := claudeCompactionCapsulePrefix + "ref:" + strings.Repeat("0", 64) + claudeCompactionCapsuleSuffix
	encoded, _ := json.Marshal(marker)
	recorder := serveClaudeMessages(t, handler, "/v1/messages", `{"model":"`+responsesBridgeClientModel+`","messages":[{"role":"user","content":`+string(encoded)+`}]}`)
	if recorder.Code != http.StatusBadRequest || executor.executeCalls != 0 || executor.streamCalls != 0 {
		t.Fatalf("missing cache was sent upstream: %d", recorder.Code)
	}
}
