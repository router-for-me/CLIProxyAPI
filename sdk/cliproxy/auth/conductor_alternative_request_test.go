package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestHasAlternativeTargetForModelUsesRequestCredentialTrack(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		headers  http.Header
		excluded string
		want     bool
	}{
		{name: "expired SIWC", ctx: context.Background()},
		{name: "outer track from context", ctx: WithCodexClientRequest(context.Background(), true), want: true},
		{name: "outer track from headers", ctx: context.Background(), headers: http.Header{"User-Agent": {"codex_cli_rs/0.77.0"}}, want: true},
		{name: "excluded credential", ctx: WithCodexClientRequest(context.Background(), true), excluded: "alternative-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(&recordingSIWCExecutor{id: "codex"})
			auth := &Auth{
				ID:       "alternative-codex",
				Provider: "codex",
				Status:   StatusActive,
				Metadata: map[string]any{
					"type":         "codex",
					"access_token": "outer-valid-token",
					"expired":      time.Now().Add(2 * time.Hour).Format(time.RFC3339),
					"use_siwc":     true,
					"siwc": map[string]any{
						"access_token": "siwc-expired-token",
						"expired":      time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
					},
				},
			}
			registerTestCodexModels(t, auth.ID)
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			got := manager.HasAlternativeTargetForModel(tc.ctx, []string{"codex"}, "gpt-5.4", cliproxyexecutor.Options{Headers: tc.headers}, tc.excluded)
			if got != tc.want {
				t.Fatalf("HasAlternativeTargetForModel = %v, want %v", got, tc.want)
			}
		})
	}
}
