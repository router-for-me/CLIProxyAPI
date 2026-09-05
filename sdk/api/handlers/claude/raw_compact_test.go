package claude

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/tidwall/gjson"
)

func rawCiphertextForTest(blocks int) string {
	raw := make([]byte, 57+16*blocks)
	raw[0] = 0x80
	return base64.URLEncoding.EncodeToString(raw)
}

func TestRawCompactThreeGenerationsReplaceHistory(t *testing.T) {
	var previous string
	for generation := 1; generation <= 3; generation++ {
		token := rawCiphertextForTest(generation)
		compact := mustJSONMarshalForTest(t, map[string]any{"object": "response.compaction", "output": []any{map[string]string{"type": "compaction", "encrypted_content": token}}})
		_, text, err := buildClaudeCompactResponse(compact, responsesBridgeClientModel, responsesBridgeUpstreamModel)
		if err != nil || text != token {
			t.Fatalf("generation %d: %v", generation, err)
		}
		messages := []any{map[string]any{"role": "user", "content": "old history"}}
		if previous != "" {
			messages = append(messages, map[string]any{"role": "user", "content": previous})
		}
		messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "Summary follows:\n\n" + text + "\n\nContinue."}}}, map[string]any{"role": "user", "content": "new turn"})
		body := mustJSONMarshalForTest(t, map[string]any{"model": responsesBridgeUpstreamModel, "messages": messages})
		prepared, replay, err := prepareClaudeCompactionReplay(body, responsesBridgeUpstreamModel)
		if err != nil {
			t.Fatal(err)
		}
		if len(replay.Output) != 1 || gjson.GetBytes(prepared, constant.ClaudeResponsesCompactionField+".output.0.encrypted_content").String() != token {
			t.Fatal("replacement lost")
		}
		remaining := gjson.GetBytes(prepared, "messages").Raw
		if strings.Contains(remaining, token) || strings.Contains(remaining, "old history") || !strings.Contains(remaining, "new turn") {
			t.Fatal("context boundary not replaced")
		}
		previous = text
	}
}

func TestRawCompactLeavesQuotedAndMalformedText(t *testing.T) {
	token := rawCiphertextForTest(1)
	quoted, _ := json.Marshal(token)
	for _, text := range []string{string(quoted), "example: " + token, "gAAAA-not-valid", "`" + token + "`"} {
		got, _, found, err := stripClaudeCompactionCapsule(text)
		if err != nil || found || got != text {
			t.Fatalf("ordinary text interpreted as state: %q", text)
		}
	}
}
