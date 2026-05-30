package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_RecordContentSafetyRequest_WritesSanitizedPromptLog(t *testing.T) {
	logDir := t.TempDir()
	t.Setenv(contentSafetyLogDirEnv, logDir)

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	blockedAuth := &Auth{ID: "aa-blocked-auth", Provider: "claude", Prefix: "stepfun", Label: "blocked"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	payload := mustMarshalContentSafetyTestPayload(t, map[string]any{
		"model":         model,
		"api_key":       "sk-test-secret",
		"authorization": "Bearer hidden-token",
		"messages": []map[string]string{
			{"role": "system", "content": "system prompt for file search"},
			{"role": "user", "content": "please inspect these files"},
		},
		"image": "data:image/png;base64," + strings.Repeat("A", contentSafetyLogMaxStringLen+32),
	})
	originalRequest := mustMarshalContentSafetyTestPayload(t, map[string]any{
		"model":        model,
		"access_token": "original-secret-token",
		"messages": []map[string]string{
			{"role": "user", "content": "original client prompt"},
		},
	})

	ctx := logging.WithRequestID(context.Background(), "req-451")
	resp, errExecute := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{
		OriginalRequest: originalRequest,
	})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success after content safety fallback", errExecute)
	}
	if string(resp.Payload) != goodAuth.ID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), goodAuth.ID)
	}

	files, errGlob := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	if errGlob != nil {
		t.Fatalf("glob content safety logs: %v", errGlob)
	}
	if len(files) != 1 {
		t.Fatalf("content safety log files = %v, want one file", files)
	}
	rawLog, errRead := os.ReadFile(files[0])
	if errRead != nil {
		t.Fatalf("read content safety log: %v", errRead)
	}
	for _, forbidden := range []string{"sk-test-secret", "Bearer hidden-token", "original-secret-token", "data:image/png;base64"} {
		if bytes.Contains(rawLog, []byte(forbidden)) {
			t.Fatalf("content safety log leaked %q: %s", forbidden, string(rawLog))
		}
	}
	for _, required := range []string{"system prompt for file search", "please inspect these files", "original client prompt", "[redacted]"} {
		if !bytes.Contains(rawLog, []byte(required)) {
			t.Fatalf("content safety log missing %q: %s", required, string(rawLog))
		}
	}

	var record contentSafetyLogRecord
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(rawLog), &record); errUnmarshal != nil {
		t.Fatalf("unmarshal content safety log: %v", errUnmarshal)
	}
	if record.RequestID != "req-451" {
		t.Fatalf("request_id = %q, want req-451", record.RequestID)
	}
	if record.StatusCode != http.StatusUnavailableForLegalReasons {
		t.Fatalf("status_code = %d, want %d", record.StatusCode, http.StatusUnavailableForLegalReasons)
	}
	if record.AuthIndex == "" {
		t.Fatal("auth_index should be recorded")
	}
	if record.UpstreamModel != model {
		t.Fatalf("upstream_model = %q, want %q", record.UpstreamModel, model)
	}
	if !record.OriginalRequestPresent {
		t.Fatal("original request should be recorded when it differs from submitted payload")
	}
}

func mustMarshalContentSafetyTestPayload(t *testing.T, value any) []byte {
	t.Helper()
	payload, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal test payload: %v", errMarshal)
	}
	return payload
}
