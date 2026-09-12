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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPrepareBackendRequest(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?api_key=master-secret", strings.NewReader(`{"model":"master-model","metadata":{"user_id":"master-identity","keep":"value"},"system":"original system","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`))
	for _, key := range []string{"Authorization", "X-Api-Key", "Cookie", "Proxy-Authorization", "X-Forwarded-For", "X-Account-Id", "User-Agent", "X-Claude-Code-Session-Id"} {
		request.Header.Set(key, "master-secret")
	}
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Anthropic-Beta", "test-beta")
	clean, err := prepareBackendRequest(request, "selected-model")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clean), "master-") {
		t.Fatalf("master identity survived: %s", clean)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(clean, &body); err != nil {
		t.Fatal(err)
	}
	if string(body["model"]) != `"selected-model"` || string(body["system"]) != `"original system"` || !strings.Contains(string(body["tools"]), `"Bash"`) {
		t.Fatalf("model replacement damaged inference content: %s", clean)
	}
	if !strings.Contains(string(body["metadata"]), `"keep":"value"`) {
		t.Fatal("non-identity metadata was discarded")
	}
	for key, values := range request.Header {
		if strings.Contains(strings.Join(values, ","), "master-secret") {
			t.Fatalf("master header survived: %s", key)
		}
	}
	if request.Header.Get("Anthropic-Version") != "2023-06-01" || request.URL.RawQuery != "" || request.Host != "" {
		t.Fatal("safe protocol header or URL sanitization incorrect")
	}
	bodyAgain, err := io.ReadAll(request.Body)
	if err != nil || string(bodyAgain) != string(clean) {
		t.Fatal("request body must also be replaced with sanitized content")
	}
}

func TestPrepareBackendRequestRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"", "null", "[]", "1", `{"x":`, `{"stream":"yes"}`, `{"stream":null}`, `{"metadata":[]}`, `{"metadata":null}`, `{"metadata":"identity"}`, `{}` + `{}`} {
		t.Run(input, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(input))
			if _, err := prepareBackendRequest(r, "model"); err == nil {
				t.Fatal("malformed input accepted")
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	r.Header.Set("Content-Encoding", "gzip")
	if _, err := prepareBackendRequest(r, "model"); err == nil {
		t.Fatal("encoded request accepted without decoding")
	}
}

func TestPrepareBackendRequestDropsEmptyIdentityMetadata(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"metadata":{"user_id":"private"}}`))
	clean, err := prepareBackendRequest(r, "model")
	if err != nil || strings.Contains(string(clean), "metadata") {
		t.Fatalf("identity-only metadata survived: %s, %v", clean, err)
	}
}

func writeSyntheticBackendCredential(t *testing.T, dir, name string, extra map[string]any) BackendOptions {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"type": "codex", "access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expired": "2099-01-01T00:00:00Z"}
	for key, value := range extra {
		metadata[key] = value
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return BackendOptions{AuthDir: dir, AuthID: name, Provider: "codex", Model: "gpt-5.6"}
}

func TestLoadBackendCredentialAndConstrainedRefreshStore(t *testing.T) {
	opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", nil)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if auth.ID != opts.AuthID || auth.Metadata["cloak_mode"] != "never" || auth.Metadata["request_retry"] != 0 {
		t.Fatal("credential pin and no-cloaking/retry settings missing")
	}
	auth.Metadata["access_token"] = "synthetic-refreshed"
	if path, err := store.Save(t.Context(), auth); err != nil || path != filepath.Join(opts.AuthDir, opts.AuthID) {
		t.Fatalf("refresh did not save into selected profile: %v", err)
	}
	_, reloaded, err := loadBackendCredential(t.Context(), opts)
	if err != nil || reloaded.Metadata["access_token"] != "synthetic-refreshed" {
		t.Fatal("refreshed account token not persisted")
	}
	for _, mutate := range []func(*coreauth.Auth){
		func(a *coreauth.Auth) { a.ID = "other.json" },
		func(a *coreauth.Auth) { a.Provider = "claude" },
		func(a *coreauth.Auth) { a.FileName = "../other.json" },
		func(a *coreauth.Auth) { a.Attributes[coreauth.AttributePath] = "/tmp/unrelated.json" },
	} {
		changed := auth.Clone()
		mutate(changed)
		if _, err := store.Save(t.Context(), changed); err == nil {
			t.Fatal("refresh was allowed to escape the pinned profile")
		}
	}
	if err := store.Delete(t.Context(), opts.AuthID); err == nil {
		t.Fatal("backend allowed credential deletion")
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != opts.AuthID {
		t.Fatal("successful credential refresh left extra authentication files")
	}
	info, err := os.Stat(filepath.Join(opts.AuthDir, opts.AuthID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("refreshed credential is not mode 0600")
	}
}

func TestBackendRefreshFailurePreservesExistingCredential(t *testing.T) {
	opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", nil)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.AuthDir, opts.AuthID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*coreauth.Auth){
		func(a *coreauth.Auth) {
			a.Attributes[coreauth.AttributePath] = filepath.Join(opts.AuthDir, "other.json")
		},
		func(a *coreauth.Auth) { a.Metadata["unsupported"] = make(chan int) },
		func(a *coreauth.Auth) { a.Metadata = nil },
	} {
		invalid := auth.Clone()
		mutate(invalid)
		if _, err := store.Save(t.Context(), invalid); err == nil {
			t.Fatal("invalid refresh save succeeded")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			t.Fatal("failed refresh modified the existing credential")
		}
		entries, err := os.ReadDir(opts.AuthDir)
		if err != nil || len(entries) != 1 {
			t.Fatal("failed refresh left an extra auth file")
		}
	}
}

func TestBackendRefreshPersistsAfterCancellation(t *testing.T) {
	opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", nil)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	auth.Metadata["refresh_token"] = "synthetic-already-rotated"
	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatal(err)
	}
	_, updated, err := loadBackendCredential(t.Context(), opts)
	if err != nil || updated.Metadata["refresh_token"] != "synthetic-already-rotated" {
		t.Fatal("already rotated token was discarded during shutdown")
	}
}

func TestBackendSealedStoreRejectsLateResultWrites(t *testing.T) {
	opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", nil)
	store, auth, err := loadBackendCredential(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.AuthDir, opts.AuthID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store.seal()
	store.seal()
	auth.Metadata["access_token"] = "synthetic-late-old-generation"
	if _, err := store.Save(t.Context(), auth); err == nil {
		t.Fatal("sealed store accepted a late stream result write")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("late stream result changed the persisted credential")
	}
	entries, err := os.ReadDir(opts.AuthDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("late stream result staged a credential file")
	}
}

func TestLoadBackendCredentialRejectsOverrides(t *testing.T) {
	cases := []map[string]any{
		{"type": "claude"}, {"disabled": true}, {"refresh_token": ""}, {"access_token": ""},
		{"proxy_url": "https://untrusted.invalid"}, {"base_url": "https://untrusted.invalid"},
		{"headers": map[string]string{"Authorization": "override"}}, {"custom_headers": map[string]string{"X-Key": "override"}},
		{"prefix": "other"}, {"plugins": []string{"other"}}, {"cloak_mode": "always"},
	}
	for i, extra := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", extra)
			if _, _, err := loadBackendCredential(t.Context(), opts); err == nil {
				t.Fatal("unsafe credential accepted")
			}
		})
	}
}

func TestLoadBackendCredentialRejectsExtraOrUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"extra-json", "nested-dir", "symlink", "public-file", "public-dir", "wrong-id", "traversal"} {
		t.Run(kind, func(t *testing.T) {
			opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", nil)
			path := filepath.Join(opts.AuthDir, opts.AuthID)
			switch kind {
			case "extra-json":
				if err := os.WriteFile(filepath.Join(opts.AuthDir, "other.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "nested-dir":
				if err := os.Mkdir(filepath.Join(opts.AuthDir, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.Rename(path, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "public-dir":
				if err := os.Chmod(opts.AuthDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "wrong-id":
				opts.AuthID = "other.json"
			case "traversal":
				opts.AuthID = "../selected.json"
			}
			if _, _, err := loadBackendCredential(t.Context(), opts); err == nil {
				t.Fatal("unsafe profile layout accepted")
			}
		})
	}
}

type backendCapture struct {
	mu         sync.Mutex
	calls      int
	authID     string
	req        coreexecutor.Request
	opts       coreexecutor.Options
	err        error
	streamWait bool
	started    chan struct{}
}

func (e *backendCapture) Identifier() string { return "codex" }
func (e *backendCapture) record(a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.authID, e.req, e.opts = a.ID, req, opts
}
func (e *backendCapture) Execute(_ context.Context, a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.record(a, req, opts)
	return coreexecutor.Response{Payload: []byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`)}, e.err
}
func (e *backendCapture) CountTokens(_ context.Context, a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.record(a, req, opts)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":9}`)}, e.err
}
func (e *backendCapture) ExecuteStream(ctx context.Context, a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.record(a, req, opts)
	if e.err != nil {
		return nil, e.err
	}
	ch := make(chan coreexecutor.StreamChunk, 1)
	if e.streamWait {
		go func() { close(e.started); <-ctx.Done(); close(ch) }()
	} else {
		ch <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
		close(ch)
	}
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}
func (e *backendCapture) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *backendCapture) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func backendTestHandler(t *testing.T, lifetime context.Context, capture *backendCapture) (http.Handler, BackendOptions) {
	t.Helper()
	opts := BackendOptions{AuthID: t.Name() + "-selected", Provider: "codex", Model: t.Name() + "-model"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(capture)
	// Deliberately register another eligible account. HTTP metadata must still
	// explicitly pin the selected one, independently of the production selector.
	for _, id := range []string{"aaa-other-" + t.Name(), opts.AuthID} {
		registry.GetGlobalRegistry().RegisterClient(id, opts.Provider, []*registry.ModelInfo{{ID: opts.Model}})
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: opts.Provider, Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
	return newBackendHandler(lifetime, opts, base), opts
}

func TestBackendPinsEveryInferencePath(t *testing.T) {
	for _, tc := range []struct {
		path      string
		streaming bool
	}{{"/v1/messages", false}, {"/v1/messages", true}, {"/v1/messages/count_tokens", false}} {
		t.Run(fmt.Sprintf("%s-%t", tc.path, tc.streaming), func(t *testing.T) {
			capture := &backendCapture{}
			handler, opts := backendTestHandler(t, t.Context(), capture)
			input := fmt.Sprintf(`{"model":"master","stream":%t,"metadata":{"user_id":"private"},"messages":[{"role":"user","content":"hello"}]}`, tc.streaming)
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(input))
			r.Header.Set("Authorization", "Bearer master-secret")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if capture.calls != 1 || capture.authID != opts.AuthID || capture.opts.Metadata[coreexecutor.PinnedAuthMetadataKey] != opts.AuthID || capture.req.Model != opts.Model {
				t.Fatalf("execution not pinned: auth=%s model=%s metadata=%v", capture.authID, capture.req.Model, capture.opts.Metadata)
			}
			if strings.Contains(string(capture.req.Payload), "private") || capture.opts.Headers.Get("Authorization") != "" {
				t.Fatal("master identity reached inference")
			}
		})
	}
}

func TestBackendDoesNotRetryOrExposeUpstreamErrors(t *testing.T) {
	capture := &backendCapture{err: &coreauth.Error{Code: "rate_limit_exceeded", Message: "private-token-or-prompt", HTTPStatus: 429, Retryable: true}}
	handler, _ := backendTestHandler(t, t.Context(), capture)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"master","messages":[]}`)))
	if capture.calls != 1 || w.Code != 429 || strings.Contains(w.Body.String(), "private-token-or-prompt") {
		t.Fatalf("unsafe error/retry: calls=%d status=%d body=%s", capture.calls, w.Code, w.Body.String())
	}
}

func TestBackendCancellationStopsStreaming(t *testing.T) {
	capture := &backendCapture{streamWait: true, started: make(chan struct{})}
	lifetime, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler, _ := backendTestHandler(t, lifetime, capture)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"stream":true,"messages":[]}`)))
	}()
	<-capture.started
	cancel()
	<-done
}

func TestBackendRejectsClosedLifetimeAndUnexpectedRoutes(t *testing.T) {
	lifetime, cancel := context.WithCancel(t.Context())
	cancel()
	capture := &backendCapture{}
	handler, _ := backendTestHandler(t, lifetime, capture)
	for _, path := range []string{"/v1/messages", "/v0/management/auth-files", "/v1/responses"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if w.Code != http.StatusServiceUnavailable && w.Code != http.StatusNotFound {
			t.Fatalf("unexpected status: %d", w.Code)
		}
	}
	if capture.calls != 0 {
		t.Fatal("closed backend executed inference")
	}
}

func TestBackendConfigDisablesSideEffects(t *testing.T) {
	cfg := backendConfig("/profile/auth")
	if !cfg.CommercialMode || cfg.RequestLog || cfg.Debug || cfg.RequestRetry != 0 || cfg.MaxRetryCredentials != 1 || cfg.Home.Enabled || cfg.Plugins.Enabled || cfg.UsageStatisticsEnabled || cfg.LoggingToFile || cfg.ProxyURL != "direct" || !cfg.DisableClaudeCloakMode || !cfg.Codex.DisableCodexCloaking || cfg.Streaming.BootstrapRetries != 0 {
		t.Fatal("unsafe embedded backend defaults")
	}
}

func TestBackendThroughNativeLikeTLSProxy(t *testing.T) {
	capture := &backendCapture{}
	handler, opts := backendTestHandler(t, t.Context(), capture)
	_, client, _ := proxyTestStart(t, handler, nil)
	response, body := proxyTestRequest(t, client, http.MethodPost, "/v1/messages?beta=true", `{"model":"claude-native-master","stream":true,"metadata":{"user_id":"MASTER-COMPOSITION-CANARY"},"system":"Keep this original system prompt","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`, http.Header{
		"Authorization":     {"Bearer MASTER-COMPOSITION-CANARY"},
		"X-Api-Key":         {"MASTER-COMPOSITION-CANARY"},
		"Cookie":            {"session=MASTER-COMPOSITION-CANARY"},
		"Content-Type":      {"application/json"},
		"Anthropic-Version": {"2023-06-01"},
	})
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("composed proxy/backend SSE failed: %d %s", response.StatusCode, body)
	}
	if capture.calls != 1 || capture.authID != opts.AuthID || capture.opts.Metadata[coreexecutor.PinnedAuthMetadataKey] != opts.AuthID || capture.req.Model != opts.Model {
		t.Fatalf("composed request was not pinned: calls=%d auth=%s model=%s", capture.calls, capture.authID, capture.req.Model)
	}
	if strings.Contains(string(capture.req.Payload), "MASTER-COMPOSITION-CANARY") || strings.Contains(fmt.Sprint(capture.opts.Headers), "MASTER-COMPOSITION-CANARY") || strings.Contains(string(capture.req.Payload), "claude-native-master") {
		t.Fatal("master credentials or master model reached the inference executor")
	}
	if !strings.Contains(string(capture.req.Payload), "Keep this original system prompt") || !strings.Contains(string(capture.req.Payload), `"Bash"`) {
		t.Fatal("composition damaged system prompt or tools")
	}
}

func TestBackendErrorOnlyPreservesValidRetryAfter(t *testing.T) {
	for _, value := range []string{"60", "Wed, 21 Oct 2015 07:28:00 GMT", "private-account", "-1"} {
		w := httptest.NewRecorder()
		writeBackendUpstreamError(w, &interfaces.ErrorMessage{StatusCode: 429, Error: errors.New("private-error"), Addon: http.Header{"Retry-After": {value}, "X-Account": {"private-account"}}})
		want := ""
		if value == "60" || strings.HasPrefix(value, "Wed,") {
			want = value
		}
		if w.Header().Get("Retry-After") != want || w.Header().Get("X-Account") != "" || strings.Contains(w.Body.String(), "private-error") {
			t.Fatalf("unsafe error forwarding for %q", value)
		}
	}
}
