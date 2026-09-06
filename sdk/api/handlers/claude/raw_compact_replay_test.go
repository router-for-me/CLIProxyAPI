package claude

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCompactResponseReturnsRawCiphertextAndResumesWithoutPinning(t *testing.T) {
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
		if strings.Contains(gjson.GetBytes(executor.request.Payload, "messages").Raw, opaque) {
			t.Fatal("reference leaked into ordinary messages")
		}
		if gjson.GetBytes(executor.request.Payload, constant.ClaudeResponsesCompactionField+".output.0.encrypted_content").String() != opaque {
			t.Fatal("resume lost compact state")
		}
	}
}
