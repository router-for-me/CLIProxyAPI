package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestDevinExecutorUsesClaudeEffortDirective(t *testing.T) {
	const model = "devin/claude-opus-5-5"
	const topLevelOnly = `{"model":"` + model + `","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	const withDirectives = `{"model":"` + model + `","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},` +
		`"messages":[{"role":"system","content":[],"output_config":{"effort":"medium"}},{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"ok"},{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hi"}]}`
	const disabledWithDirective = `{"model":"` + model + `","max_tokens":1024,"thinking":{"type":"disabled"},` +
		`"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hi"}]}`

	wantLow := helps.ResolveDevinChatModelUID(model, "low", 0)
	wantMedium := helps.ResolveDevinChatModelUID(model, "medium", 0)
	wantNone := helps.ResolveDevinChatModelUID(model, "none", 0)
	wantHigh := helps.ResolveDevinChatModelUID(model, "high", 0)
	if wantLow == wantHigh || wantNone == wantHigh || wantMedium == wantHigh {
		t.Fatalf("fixture model does not separate low %q, medium %q, none %q and high %q", wantLow, wantMedium, wantNone, wantHigh)
	}

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "devin-effort-directive", Provider: "devin", Attributes: map[string]string{"api_key": "test-key"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	for _, tc := range []struct {
		name    string
		model   string
		payload string
		want    string
	}{
		{name: "top-level effort only", model: model, payload: topLevelOnly, want: wantLow},
		{name: "last directive wins over the top-level placeholder", model: model, payload: withDirectives, want: wantHigh},
		{name: "model suffix wins over directive", model: model + "(medium)", payload: withDirectives, want: wantMedium},
		{name: "disabled thinking keeps a historical directive from selecting a variant", model: model, payload: disabledWithDirective, want: wantNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload)}
			_, chatModelUID, _, err := exec.prepareDevinHTTPRequest(context.Background(), auth, req, opts)
			if err != nil {
				t.Fatalf("prepareDevinHTTPRequest: %v", err)
			}
			if chatModelUID != tc.want {
				t.Fatalf("chat model uid = %q, want %q", chatModelUID, tc.want)
			}
		})
	}
}
