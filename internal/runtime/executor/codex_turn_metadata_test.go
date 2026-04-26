package executor

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCodexBuildTurnMetadataHeaderKeepsBaseFields(t *testing.T) {
	header := codexBuildTurnMetadataHeader("session-1", codexDefaultThreadSource, "turn-1", codexDefaultSandboxTag)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(header), &parsed); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, _ := parsed["session_id"].(string); got != "session-1" {
		t.Fatalf("session_id = %q, want %q", got, "session-1")
	}
	if got, _ := parsed["thread_source"].(string); got != codexDefaultThreadSource {
		t.Fatalf("thread_source = %q, want %q", got, codexDefaultThreadSource)
	}
	if got, _ := parsed["turn_id"].(string); got != "turn-1" {
		t.Fatalf("turn_id = %q, want %q", got, "turn-1")
	}
	if got, _ := parsed["sandbox"].(string); got != codexDefaultSandboxTag {
		t.Fatalf("sandbox = %q, want %q", got, codexDefaultSandboxTag)
	}
	if got := parsed["workspaces"]; got != nil {
		t.Fatalf("workspaces = %#v, want nil", got)
	}
}

func TestCodexEnsureTurnMetadataHeaderPreservesClientHeader(t *testing.T) {
	headers := http.Header{}
	source := http.Header{}
	source.Set(codexHeaderTurnMetadata, `{"turn_id":"turn-client"}`)

	codexEnsureTurnMetadataHeader(headers, source, codexTurnMetadataDefaults{
		sessionID:    "session-1",
		threadSource: codexDefaultThreadSource,
		turnID:       "turn-generated",
		sandbox:      codexDefaultSandboxTag,
	})

	if got := headers.Get(codexHeaderTurnMetadata); got != `{"turn_id":"turn-client"}` {
		t.Fatalf("turn metadata = %q, want client value", got)
	}
}

func BenchmarkCodexBuildTurnMetadataHeader(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = codexBuildTurnMetadataHeader("session-1", codexDefaultThreadSource, "turn-1", codexDefaultSandboxTag)
	}
}
