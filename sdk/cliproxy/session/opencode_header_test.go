package session

import (
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestExtractSessionInfoOpenCodeNativeHeader(t *testing.T) {
	t.Parallel()
	// OpenCode Go requires the conversation header used by current Pi clients (#5690).
	for _, tt := range []struct {
		name    string
		headers http.Header
		want    string
	}{
		{name: "native", headers: http.Header{"X-Opencode-Session": {"ses_native"}}, want: "affinity:ses_native"},
		{name: "lowercase", headers: http.Header{"x-opencode-session": {"ses_lower"}}, want: "affinity:ses_lower"},
		{name: "later valid value", headers: http.Header{"X-Opencode-Session": {"bad\nvalue", "ses_valid"}}, want: "affinity:ses_valid"},
		{name: "existing affinity precedence", headers: http.Header{"X-Session-Affinity": {"ses_existing"}, "X-Opencode-Session": {"ses_native"}}, want: "affinity:ses_existing"},
		{name: "empty", headers: http.Header{"X-Opencode-Session": {" "}}},
		{name: "oversized", headers: http.Header{"X-Opencode-Session": {strings.Repeat("x", 257)}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, ok := ExtractSessionInfo(tt.headers, nil, nil)
			if info.SessionID != tt.want || ok != (tt.want != "") {
				t.Fatalf("ExtractSessionInfo() = (%q, %v), want (%q, %v)", info.SessionID, ok, tt.want, tt.want != "")
			}
			if ok && info.ClientType != "opencode" {
				t.Fatalf("client type = %q, want opencode", info.ClientType)
			}
		})
	}
}

func TestEnrichOpenCodeHeaderSeparatesIdenticalPromptsAndSurvivesCompaction(t *testing.T) {
	t.Parallel()
	for _, sessionID := range []string{"session-a", "session-b"} {
		for _, payload := range []string{
			`{"messages":[{"role":"user","content":"Fix the failing test"}]}`,
			`{"messages":[{"role":"user","content":"Compacted conversation summary"}]}`,
		} {
			req, opts := Enrich(
				cliproxyexecutor.Request{Payload: []byte(payload)},
				cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAI,
					Headers:      http.Header{"X-Opencode-Session": {sessionID}},
					Metadata:     map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:stale-root"},
				},
			)
			for _, metadata := range []map[string]any{req.Metadata, opts.Metadata} {
				if got := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "affinity:"+sessionID {
					t.Fatalf("canonical identity = %v, want affinity:%s", got, sessionID)
				}
				if got := DerivedID(metadata); got != "" {
					t.Fatalf("explicit OpenCode session retained derived identity %q", got)
				}
			}
		}
	}
}
