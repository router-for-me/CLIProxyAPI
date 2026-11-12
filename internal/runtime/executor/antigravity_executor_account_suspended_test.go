package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antigravitySuspendedBody = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com","metadata":{"uiMessage":"true","appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9","appeal_url_link_text":"Submit Appeal"}}]}}`

const antigravityOKStream = "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"

func isAccountSuspendedError(err error) bool {
	var coded interface{ ErrorCode() string }
	return errors.As(err, &coded) && coded.ErrorCode() == cliproxyauth.ErrorCodeAccountSuspended
}

func TestNewAntigravityStatusErr_AccountSuspended(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "suspended", status: http.StatusForbidden, body: antigravitySuspendedBody, want: true},
		{name: "plain 403", status: http.StatusForbidden, body: `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`},
		{name: "not a 403", status: http.StatusBadRequest, body: antigravitySuspendedBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newAntigravityStatusErr(tc.status, []byte(tc.body))
			if got := isAccountSuspendedError(err); got != tc.want {
				t.Fatalf("suspended = %v, want %v", got, tc.want)
			}
			if status := err.(interface{ StatusCode() int }).StatusCode(); status != tc.status || err.Error() != tc.body {
				t.Fatalf("status %d, message %q changed", status, err.Error())
			}
		})
	}
}

func newSuspendedAntigravityAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "antigravity-suspended-auth", Provider: "antigravity", Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{"access_token": "suspended", "project_id": "p", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)},
	}
}

func TestAntigravityExecutor_ExecuteStreamTagsAccountSuspended(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(antigravitySuspendedBody))
	}))
	defer server.Close()

	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	_, err := NewAntigravityExecutor(&config.Config{}).ExecuteStream(context.Background(), newSuspendedAntigravityAuth(server.URL),
		cliproxyexecutor.Request{Model: "gemini-3-flash", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true, OriginalRequest: payload})
	if !isAccountSuspendedError(err) {
		t.Fatalf("ExecuteStream() error = %v, want an account_suspended error", err)
	}
}

func TestAntigravityExecutor_CountTokensTagsAccountSuspended(t *testing.T) {
	var countTokensCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":countTokens") {
			countTokensCalls.Add(1)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(antigravitySuspendedBody))
	}))
	defer server.Close()

	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	_, err := NewAntigravityExecutor(&config.Config{}).CountTokens(context.Background(), newSuspendedAntigravityAuth(server.URL),
		cliproxyexecutor.Request{Model: "gemini-3-flash", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, OriginalRequest: payload})
	if !isAccountSuspendedError(err) {
		t.Fatalf("CountTokens() error = %v, want an account_suspended error", err)
	}
	if countTokensCalls.Load() == 0 {
		t.Fatal("no countTokens call reached the upstream")
	}
}

// A suspended account refuses every model, so after one 403 the manager must
// not send it another request for any model, streaming or not.
func TestAntigravityManager_AccountSuspendedSkipsCredentialForEveryModel(t *testing.T) {
	models := []string{"gemini-3-flash", "claude-sonnet-4-6"}
	var suspendedHits, healthyHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer suspended" {
			suspendedHits.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(antigravitySuspendedBody))
			return
		}
		healthyHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(antigravityOKStream))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(NewAntigravityExecutor(&config.Config{}))
	suspendedID := fmt.Sprintf("ag-suspended-%d", time.Now().UnixNano())
	for id, token := range map[string]string{suspendedID: "suspended", suspendedID + "-healthy": "healthy"} {
		infos := make([]*registry.ModelInfo, 0, len(models))
		for _, model := range models {
			infos = append(infos, &registry.ModelInfo{ID: model})
		}
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", infos)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
			ID: id, Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL},
			Metadata: map[string]any{"access_token": token, "project_id": "p", "expired": time.Now().Add(48 * time.Hour).Format(time.RFC3339)},
		}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	for i := 0; i < 3; i++ {
		req := cliproxyexecutor.Request{Model: models[0], Payload: payload}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity, ResponseFormat: sdktranslator.FormatAntigravity}
		if _, errExec := manager.Execute(context.Background(), []string{"antigravity"}, req, opts); errExec != nil {
			t.Fatalf("Execute() #%d error = %v", i, errExec)
		}
	}
	for i := 0; i < 3; i++ {
		req := cliproxyexecutor.Request{Model: models[1], Payload: payload}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity, ResponseFormat: sdktranslator.FormatAntigravity, Stream: true}
		result, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() #%d error = %v", i, errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("ExecuteStream() #%d chunk error = %v", i, chunk.Err)
			}
		}
	}

	if suspendedHits.Load() != 1 || healthyHits.Load() != 6 {
		t.Fatalf("upstream hits suspended=%d healthy=%d, want 1 and 6", suspendedHits.Load(), healthyHits.Load())
	}
	suspended, _ := manager.GetByID(suspendedID)
	for _, model := range append(models, "a-model-it-never-served") {
		if cliproxyauth.IsAuthAvailableForModel(suspended, model, time.Now().Add(6*time.Hour)) {
			t.Fatalf("suspended credential selectable for %s after 6h", model)
		}
	}
}
