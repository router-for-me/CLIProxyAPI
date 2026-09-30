package claudemaster

import (
	"testing"
)

func TestNativeRequestRequiresAccountAffinity(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "plain conversation", body: `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`},
		{name: "signed thinking", body: `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"opaque","signature":"sig"}]}]}`, want: true},
		{name: "redacted thinking", body: `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"ciphertext"}]}]}`, want: true},
		{name: "client tool result is self contained", body: `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}]}`},
		{name: "client tool input named signature is self contained", body: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"verify","input":{"signature":"user data"}}]}]}`},
		{name: "unfinished server tool", body: `{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"tool-1"}]}]}`, want: true},
		{name: "completed plain server tool is self contained", body: `{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"tool-1","name":"web_search","input":{"query":"example"}},{"type":"web_search_tool_result","tool_use_id":"tool-1","content":[{"type":"web_search_result","title":"Example","url":"https://example.com"}]}]}]}`},
		{name: "server tool input file reference", body: `{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"tool-1","input":{"file_id":"file_123"}},{"type":"code_execution_tool_result","tool_use_id":"tool-1","content":"ok"}]}]}`, want: true},
		{name: "encrypted server tool result", body: `{"messages":[{"role":"assistant","content":[{"type":"web_search_tool_result","content":[{"type":"web_search_result","encrypted_content":"ciphertext"}]}]}]}`, want: true},
		{name: "encrypted compaction", body: `{"messages":[{"role":"assistant","content":[{"type":"compaction","encrypted_content":"ciphertext"}]}]}`, want: true},
		{name: "top level encrypted continuation", body: `{"messages":[{"role":"user","content":"continue"}],"future_state":{"encrypted_content":"ciphertext"}}`, want: true},
		{name: "top level thought signature", body: `{"messages":[{"role":"user","content":"continue"}],"future_state":{"thought_signature":"signed"}}`, want: true},
		{name: "tool schema marker names are self contained", body: `{"tools":[{"name":"inspect","input_schema":{"type":"object","properties":{"encrypted_content":{"type":"string"},"file_id":{"type":"string"}}}}],"messages":[{"role":"user","content":"hello"}]}`},
		{name: "signed compaction", body: `{"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary","signature":"sig"}]}]}`, want: true},
		{name: "container", body: `{"container":"container_123","messages":[{"role":"user","content":"continue"}]}`, want: true},
		{name: "container upload", body: `{"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file_123"}]}]}`, want: true},
		{name: "file reference", body: `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_123"}}]}]}`, want: true},
		{name: "empty opaque fields", body: `{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":""}]}]}`},
		{name: "invalid", body: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nativeRequestRequiresAccountAffinity([]byte(test.body)); got != test.want {
				t.Fatalf("nativeRequestRequiresAccountAffinity() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAnalyzeNativeRequestAffinityRejectsUnclassifiablePayload(t *testing.T) {
	for _, body := range []string{"", `{`, `{} {}`} {
		analysis := analyzeNativeRequestAffinity([]byte(body))
		if analysis.validJSON {
			t.Fatalf("analyzeNativeRequestAffinity(%q).validJSON = true", body)
		}
		if analysis.requiresAccount {
			t.Fatalf("analyzeNativeRequestAffinity(%q).requiresAccount = true", body)
		}
	}
}
