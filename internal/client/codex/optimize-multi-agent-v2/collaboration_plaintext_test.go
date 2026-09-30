package multiagentv2

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/tidwall/sjson"
)

func TestPrepareCodexCollaborationPlaintext(t *testing.T) {
	headers := http.Header{"User-Agent": {"codex_cli_rs/0.144.1"}}
	for _, name := range []string{"spawn_agent", "send_message", "followup_task"} {
		for _, namespace := range []string{"", "agents", "collaboration", "collaboration-optimize"} {
			for _, separator := range []string{"namespace", ".", "__"} {
				t.Run(namespace+"/"+separator+"/"+name, func(t *testing.T) {
					toolName := name
					if namespace != "" && separator != "namespace" {
						toolName = namespace + separator + name
					}
					tools := fmt.Sprintf(`[{"type":"function","name":%q,"parameters":{"properties":{"message":{"type":"string","encrypted":true},"other":{"type":"string","encrypted":true}},"required":["message"]}}]`, toolName)
					prefix := "tools.0"
					if namespace != "" && separator == "namespace" {
						tools = fmt.Sprintf(`[{"type":"namespace","name":%q,"tools":%s}]`, namespace, tools)
						prefix = "tools.0.tools.0"
					}
					payload := []byte(`{"tools":` + tools + `,"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]},{"type":"compaction","encrypted_content":"checkpoint"}]}`)
					want, err := sjson.DeleteBytes(payload, prefix+".parameters.properties.message.encrypted")
					if err != nil {
						t.Fatal(err)
					}
					got := PrepareCodexCollaborationPlaintext(context.Background(), headers, payload, true)
					if !bytes.Equal(got, want) {
						t.Fatalf("got %s, want %s", got, want)
					}
					if again := PrepareCodexCollaborationPlaintext(context.Background(), headers, got, true); !bytes.Equal(again, got) {
						t.Fatal("not idempotent")
					}
				})
			}
		}
	}
}

func TestPrepareCodexCollaborationPlaintextPreservesUnknownDeclarations(t *testing.T) {
	headers := http.Header{"User-Agent": {"Codex Desktop/0.159.0"}}
	for _, tool := range []string{
		`{"type":"function","name":"other","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}`,
		`{"type":"function","name":"unknown.spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}`,
		`{"type":"function","name":"agents/spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}`,
		`{"type":"function","name":"agentsXspawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}`,
		`{"type":"function","name":"agents\\spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}`,
		`{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"object","encrypted":true}}}}`,
		`{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"encrypted":true}}}}`,
		`{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":false}}}}`,
		`{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":"true"}}}}`,
		`{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":null}}}}`,
		`{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"string"}}}}`,
		`{"type":"namespace","name":"unknown","tools":[{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]}`,
	} {
		payload := []byte(`{"tools":[` + tool + `]}`)
		if got := PrepareCodexCollaborationPlaintext(context.Background(), headers, payload, true); !bytes.Equal(got, payload) {
			t.Errorf("unknown declaration changed: %s", payload)
		}
	}
	for _, payload := range []string{`{"tools":[`, `{"tools":{},"input":{}}`} {
		if got := PrepareCodexCollaborationPlaintext(context.Background(), headers, []byte(payload), true); string(got) != payload {
			t.Errorf("invalid/unsupported payload changed: %s", payload)
		}
	}
}
