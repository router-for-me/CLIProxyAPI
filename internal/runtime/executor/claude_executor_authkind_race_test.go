package executor

import (
	"net/http"
	"sync"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Auth.AuthKind falls back to Auth.Metadata, so the Claude request paths that
// classify a credential must read it under the metadata lock like every other
// access. Each case races one locked metadata write against one classification;
// with an api_key attribute none of them takes the lock before classifying, so
// under -race an unlocked read is reported on every run.
func TestClaudeAuthKindCallersReadMetadataUnderLock(t *testing.T) {
	const apiKey = "sk-ant-api03-authkind-probe"
	executor := NewClaudeExecutor(&config.Config{})
	for _, tc := range []struct {
		name     string
		classify func(*cliproxyauth.Auth)
	}{
		{"claudeCredentialUsesOAuth", func(auth *cliproxyauth.Auth) {
			_ = claudeCredentialUsesOAuth(auth, apiKey)
		}},
		{"resolveClaudeWirePolicy", func(auth *cliproxyauth.Auth) {
			_, _ = resolveClaudeWirePolicy(&config.Config{}, auth, apiKey, false)
		}},
		{"claudeThinkingReplayEnabled", func(auth *cliproxyauth.Auth) {
			_ = claudeThinkingReplayEnabled(auth, cliproxyexecutor.Request{Model: "claude-sonnet-4-5"}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		}},
		{"PrepareRequest", func(auth *cliproxyauth.Auth) {
			req, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
			if errRequest != nil {
				t.Errorf("NewRequest() error = %v", errRequest)
				return
			}
			if errPrepare := executor.PrepareRequest(req, auth); errPrepare != nil {
				t.Errorf("PrepareRequest() error = %v", errPrepare)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{
				ID:         "claude-race-authkind-" + tc.name,
				Provider:   "claude",
				Attributes: map[string]string{"api_key": apiKey},
				Metadata:   map[string]any{"email": "user@example.com"},
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
			}()
			go func() {
				defer wg.Done()
				tc.classify(auth)
			}()
			wg.Wait()
		})
	}
}
