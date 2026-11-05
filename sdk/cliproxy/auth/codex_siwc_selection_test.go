package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type recordingSIWCExecutor struct {
	id        string
	onExecute func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	onStream  func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *recordingSIWCExecutor) Identifier() string { return e.id }

func (e *recordingSIWCExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.onExecute != nil {
		return e.onExecute(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *recordingSIWCExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.onStream != nil {
		return e.onStream(ctx, auth, req, opts)
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *recordingSIWCExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *recordingSIWCExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *recordingSIWCExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func registerTestCodexModels(t *testing.T, authID string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.4"},
		{ID: "gpt-image-2.6"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})
}

func TestManagerExecuteOpenAIImageSelectsOuterTrackWhenSIWCExpiredOrRejected(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(2 * time.Hour).Format(time.RFC3339)
	past := now.Add(-2 * time.Hour).Format(time.RFC3339)

	manager := NewManager(nil, nil, nil)
	var pickedAuthID string
	var wasSIWCForReq bool
	manager.RegisterExecutor(&recordingSIWCExecutor{
		id: "codex",
		onExecute: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			pickedAuthID = auth.ID
			wasSIWCForReq = IsCodexUseSIWCForRequest(ctx, auth, opts.Headers)
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	})

	auth := &Auth{
		ID:                  "codex-siwc-expired-outer-valid",
		Provider:            "codex",
		Status:              StatusActive,
		RejectedAccessToken: "",
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-valid-token",
			"expired":      future,
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-expired-token",
				"expired":      past,
			},
		},
	}
	auth.RejectedSIWCAccessToken = "siwc-expired-token"

	registerTestCodexModels(t, auth.ID)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 1. Default (non-Codex, non-image) request must see this auth as blocked because SIWC is expired/rejected.
	_, errNonImage := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"Python-urllib/3.14"}},
	})
	if errNonImage == nil {
		t.Fatal("expected non-Codex non-image request to fail when SIWC token is expired/rejected")
	}

	// 2. OpenAI image request with non-Codex UA must succeed because it uses the outer credential track.
	_, errImage := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-image-2.6",
		Payload: []byte(`{"model":"gpt-image-2.6","prompt":"a cat"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Headers:      http.Header{"User-Agent": []string{"Python-urllib/3.14"}},
		Metadata:     map[string]any{"request_path": "/v1/images/generations"},
	})
	if errImage != nil {
		t.Fatalf("expected OpenAI image request to select outer-valid auth despite expired SIWC, got error: %v", errImage)
	}
	if pickedAuthID != auth.ID {
		t.Fatalf("pickedAuthID = %q, want %q", pickedAuthID, auth.ID)
	}
	if wasSIWCForReq {
		t.Fatal("IsCodexUseSIWCForRequest was true during image execution, want false")
	}
}

func TestManagerExecuteOpenAIImageBlocksWhenOuterExpiredEvenIfSIWCValid(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(2 * time.Hour).Format(time.RFC3339)
	past := now.Add(-2 * time.Hour).Format(time.RFC3339)

	manager := NewManager(nil, nil, nil)
	var pickedAuthID string
	manager.RegisterExecutor(&recordingSIWCExecutor{
		id: "codex",
		onExecute: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			pickedAuthID = auth.ID
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	})

	// Auth 1: outer token is expired, SIWC token is valid.
	authOuterExpired := &Auth{
		ID:       "codex-1-outer-expired",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-expired-token",
			"expired":      past,
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-valid-token",
				"expired":      future,
			},
		},
	}
	// Auth 2: outer token is valid, SIWC token is valid.
	authOuterValid := &Auth{
		ID:       "codex-2-outer-valid",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-valid-token-2",
			"expired":      future,
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-valid-token-2",
				"expired":      future,
			},
		},
	}

	registerTestCodexModels(t, authOuterExpired.ID)
	registerTestCodexModels(t, authOuterValid.ID)
	if _, err := manager.Register(context.Background(), authOuterExpired); err != nil {
		t.Fatalf("Register authOuterExpired error: %v", err)
	}
	if _, err := manager.Register(context.Background(), authOuterValid); err != nil {
		t.Fatalf("Register authOuterValid error: %v", err)
	}

	// Image request must skip authOuterExpired and pick authOuterValid.
	_, errImage := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-image-2.6",
		Payload: []byte(`{"model":"gpt-image-2.6","prompt":"a cat"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Headers:      http.Header{"User-Agent": []string{"Python-urllib/3.14"}},
		Metadata:     map[string]any{"request_path": "/v1/images/generations"},
	})
	if errImage != nil {
		t.Fatalf("Execute image error: %v", errImage)
	}
	if pickedAuthID != authOuterValid.ID {
		t.Fatalf("pickedAuthID = %q, want %q (authOuterExpired should be skipped for image request)", pickedAuthID, authOuterValid.ID)
	}
}

func TestManagerExecuteStreamOpenAIImage401RejectsOuterTokenNotSIWC(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(2 * time.Hour).Format(time.RFC3339)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&recordingSIWCExecutor{
		id: "codex",
		onStream: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{
				Err: &Error{
					Code:       "unauthorized",
					Message:    "outer token rejected during stream",
					HTTPStatus: http.StatusUnauthorized,
				},
			}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	})

	auth := &Auth{
		ID:       "codex-stream-401",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-stream-token",
			"expired":      future,
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-stream-token",
				"expired":      future,
			},
		},
	}
	registerTestCodexModels(t, auth.ID)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	sr, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-image-2.6",
		Payload: []byte(`{"model":"gpt-image-2.6","prompt":"a cat","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Headers:      http.Header{"User-Agent": []string{"Python-urllib/3.14"}},
		Metadata:     map[string]any{"request_path": "/v1/images/generations"},
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream error: %v", errStream)
	}
	for range sr.Chunks {
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	if updated.RejectedAccessToken != "outer-stream-token" {
		t.Fatalf("RejectedAccessToken = %q, want %q", updated.RejectedAccessToken, "outer-stream-token")
	}
	if updated.RejectedSIWCAccessToken != "" {
		t.Fatalf("RejectedSIWCAccessToken = %q, want empty (SIWC token must not be marked rejected on image stream 401)", updated.RejectedSIWCAccessToken)
	}
}

func TestHomeDispatchHeadersSynthesizesCodexUserAgentForImageRequests(t *testing.T) {
	ctx := WithCodexClientRequest(context.Background(), true)
	in := http.Header{"User-Agent": []string{"Python-urllib/3.14"}}
	out := homeDispatchHeaders(ctx, in)
	if got := out.Get("User-Agent"); got != "codex_cli_rs" {
		t.Fatalf("homeDispatchHeaders User-Agent = %q, want codex_cli_rs", got)
	}
	if got := in.Get("User-Agent"); got != "Python-urllib/3.14" {
		t.Fatalf("original headers mutated: User-Agent = %q", got)
	}
}
