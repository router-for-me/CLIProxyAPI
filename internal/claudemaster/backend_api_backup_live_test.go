package claudemaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// This opt-in smoke test makes two small, separately billed API requests. It
// uses a synthetic exhausted subscription, never a real subscription login.
// The environment contains only a key-file path, never the key itself.
func TestLiveClaudeAPIBackup(t *testing.T) {
	keyFile := os.Getenv("CLAUDE_MASTER_LIVE_API_KEY_FILE")
	if keyFile == "" {
		t.Skip("set CLAUDE_MASTER_LIVE_API_KEY_FILE to enable billed API smoke test")
	}
	keyBytes, errRead := os.ReadFile(keyFile)
	if errRead != nil || len(keyBytes) == 0 || len(keyBytes) > 16<<10 {
		t.Fatal("cannot read bounded API-key file")
	}
	key := strings.TrimSpace(string(keyBytes))
	for _, char := range key {
		if char <= ' ' || char >= 127 {
			t.Fatal("API-key file must contain one HTTP credential")
		}
	}
	model := liveClaudeBackupModel(t, key)
	opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "synthetic-live-subscription.json", map[string]any{
		"type": "claude", "account_uuid": integrationClaudeAccount,
		"claude_device_ids": []string{integrationClaudeDevice},
	})
	backend, errBackend := NewBackendSeries(t.Context(), BackendSeriesOptions{
		Credentials:  []BackendCredential{{AuthDir: opts.AuthDir, AuthID: opts.AuthID, Provider: "claude"}},
		BackupAPIKey: key, ModelMap: map[string]string{"claude-master-live-source": model},
		QuotaRequest: func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
			if auth == nil || auth.AuthKind() != coreauth.AuthKindOAuth {
				return nil, errors.New("smoke fixture only measures synthetic OAuth quota")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":100,"resets_at":"2099-01-01T00:00:00Z"}}`))}, nil
		},
	})
	if errBackend != nil {
		t.Fatal("cannot construct API-backup smoke backend")
	}
	t.Cleanup(func() {
		if errClose := backend.Close(); errClose != nil {
			t.Error("cannot close API-backup smoke backend")
		}
	})
	executor := &liveClaudeBackupExecutor{ClaudeExecutor: runtimeexecutor.NewClaudeExecutor(backendConfig(opts.AuthDir)), calls: &atomic.Int32{}}
	backend.manager.RegisterExecutor(executor)
	for _, stream := range []bool{false, true} {
		payload := fmt.Sprintf(`{"model":"claude-master-live-source","max_tokens":16,"stream":%t,"metadata":{"user_id":"master-private-identity-canary"},"messages":[{"role":"user","content":"Reply with exactly OK."}]}`, stream)
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Anthropic-Version", "2023-06-01")
		request.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
		request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
		request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
		writer := httptest.NewRecorder()
		backend.Handler().ServeHTTP(writer, request)
		if writer.Code != http.StatusOK {
			// Show only the bounded public error message, never full provider
			// bodies or the credential that might be reflected by an upstream.
			message := strings.ReplaceAll(gjson.GetBytes(writer.Body.Bytes(), "error.message").String(), key, "[redacted]")
			if len(message) > 256 {
				message = message[:256]
			}
			t.Fatalf("API-backup smoke request failed: stream=%t status=%d message=%q", stream, writer.Code, message)
		}
		if stream {
			if !strings.Contains(writer.Body.String(), "event: message_start") || !strings.Contains(writer.Body.String(), "event: message_stop") {
				t.Fatal("API-backup smoke stream did not complete")
			}
		} else if gjson.GetBytes(writer.Body.Bytes(), "type").String() != "message" || gjson.GetBytes(writer.Body.Bytes(), "model").String() != model {
			t.Fatal("API-backup smoke response/model did not match")
		}
		t.Logf("API backup succeeded: stream=%t status=%d mapped_model=%s", stream, writer.Code, model)
	}
	if executor.calls.Load() != 2 {
		t.Fatal("API-backup smoke unexpectedly replayed or used a subscription")
	}
}

func liveClaudeBackupModel(t *testing.T, key string) string {
	t.Helper()
	request, errRequest := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if errRequest != nil {
		t.Fatal("cannot construct model catalog request")
	}
	request.Header.Set("X-Api-Key", key)
	request.Header.Set("Anthropic-Version", "2023-06-01")
	response, errResponse := http.DefaultClient.Do(request)
	if errResponse != nil {
		t.Fatal("cannot reach authenticated model catalog")
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Error("cannot close model catalog response")
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model catalog rejected API key: status=%d", response.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errDecode := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&catalog); errDecode != nil {
		t.Fatal("cannot decode authenticated model catalog")
	}
	// Use an advertised low-cost model for the bounded smoke requests. No
	// guessed model names or proxy allowlist influence ordinary mapped traffic.
	for _, model := range catalog.Data {
		if strings.Contains(model.ID, "haiku") {
			return model.ID
		}
	}
	t.Fatal("model catalog did not advertise a Haiku model for this smoke test")
	return ""
}

type liveClaudeBackupExecutor struct {
	*runtimeexecutor.ClaudeExecutor
	calls *atomic.Int32
}

// API-key configuration scoping must preserve the smoke test's guards and
// counter rather than promoting the embedded executor's bare scoped clone.
func (e *liveClaudeBackupExecutor) ForAPIKey() coreauth.ProviderExecutor {
	return &liveClaudeBackupExecutor{
		ClaudeExecutor: e.ClaudeExecutor.ForAPIKey().(*runtimeexecutor.ClaudeExecutor),
		calls:          e.calls,
	}
}

func (e *liveClaudeBackupExecutor) Execute(ctx context.Context, auth *coreauth.Auth, request coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	if auth == nil || auth.AuthKind() != coreauth.AuthKindAPIKey {
		return coreexecutor.Response{}, errors.New("live smoke refuses to send subscription traffic")
	}
	e.calls.Add(1)
	return e.ClaudeExecutor.Execute(ctx, auth, request, opts)
}

func (e *liveClaudeBackupExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, request coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if auth == nil || auth.AuthKind() != coreauth.AuthKindAPIKey {
		return nil, errors.New("live smoke refuses to send subscription traffic")
	}
	e.calls.Add(1)
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, request, opts)
}
