package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type issue7737StaticRTProvider struct {
	rt http.RoundTripper
}

func (p issue7737StaticRTProvider) RoundTripperFor(*cliproxyauth.Auth) http.RoundTripper {
	return p.rt
}

// TestAntigravityVerifyInferenceEndToEnd_Issue7737 verifies that Manager.VerifyInference
// works end-to-end against AntigravityExecutor when the credential starts with an expired
// access token and missing project_id, using Manager's RoundTripperProvider and without
// imposing a 16-token maxOutputTokens cap on an Antigravity Claude thinking model.
func TestAntigravityVerifyInferenceEndToEnd_Issue7737(t *testing.T) {
	var (
		mu              sync.Mutex
		sawOAuthRefresh bool
		sawProjectFetch bool
		sawGenerate     bool
		generateBody    []byte
		generateAuthHdr string
	)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://oauth2.googleapis.com/token":
			mu.Lock()
			sawOAuthRefresh = true
			mu.Unlock()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh-ag-token-7737","expires_in":3600,"token_type":"Bearer"}`)),
			}, nil

		case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
			mu.Lock()
			sawProjectFetch = true
			mu.Unlock()
			if got := req.Header.Get("Authorization"); got != "Bearer fresh-ag-token-7737" {
				t.Errorf("loadCodeAssist Authorization = %q, want Bearer fresh-ag-token-7737", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"ag-proj-live-7737"}`)),
			}, nil

		case strings.Contains(req.URL.Path, "streamGenerateContent"):
			bodyBytes, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Errorf("read generate body: %v", errRead)
			}
			mu.Lock()
			sawGenerate = true
			generateBody = append([]byte(nil), bodyBytes...)
			generateAuthHdr = req.Header.Get("Authorization")
			mu.Unlock()
			sse := "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}],"modelVersion":"claude-opus-5-5-high","responseId":"resp_ag_7737","usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil

		default:
			t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected"}`)),
			}, nil
		}
	})

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRoundTripperProvider(issue7737StaticRTProvider{rt: transport})
	manager.RegisterExecutor(exec)

	ctx := cliproxyauth.WithSkipPersist(context.Background())
	authID := "ag-e2e-verify-7737-" + uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-verify-7737@example.com",
			"access_token":  "expired-ag-token-7737",
			"refresh_token": "ag-refresh-token-7737-" + uuid.NewString(),
			"expired":       time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: "claude-opus-5-5-high", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000, ZeroAllowed: true, Levels: []string{"low", "medium", "high"}}},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	verified, errVerify := manager.VerifyInference(ctx, authID, "claude-opus-5-5-high")
	if errVerify != nil {
		t.Fatalf("VerifyInference error = %v, want nil", errVerify)
	}
	if verified == nil || !verified.InferenceVerified {
		t.Fatalf("expected InferenceVerified=true, got %+v", verified)
	}
	if gotProj, _ := verified.Metadata["project_id"].(string); gotProj != "ag-proj-live-7737" {
		t.Fatalf("persisted project_id = %q, want ag-proj-live-7737", gotProj)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawOAuthRefresh || !sawProjectFetch || !sawGenerate {
		t.Fatalf("expected refresh=%v projectFetch=%v generate=%v to all be true", sawOAuthRefresh, sawProjectFetch, sawGenerate)
	}
	if generateAuthHdr != "Bearer fresh-ag-token-7737" {
		t.Fatalf("generate Authorization = %q, want Bearer fresh-ag-token-7737", generateAuthHdr)
	}
	if got := ojson.GetBytes(generateBody, "project").String(); got != "ag-proj-live-7737" {
		t.Fatalf("generate body project = %q, want ag-proj-live-7737; body=%s", got, string(generateBody))
	}
	if maxOut := ojson.GetBytes(generateBody, "request.generationConfig.maxOutputTokens"); maxOut.Exists() && maxOut.Int() <= 16 {
		t.Fatalf("antigravity Claude thinking verify request must not send <=16 maxOutputTokens; body=%s", string(generateBody))
	}
}

// TestAntigravityVerifyInferencePrepare401RefreshesAndSucceeds_Issue7737 verifies that when
// an Antigravity credential has a locally unexpired but upstream-revoked access token and is
// missing project_id, VerifyInference refreshes once on the 401 from loadCodeAssist, retries
// project discovery with the new token, and completes verification.
func TestAntigravityVerifyInferencePrepare401RefreshesAndSucceeds_Issue7737(t *testing.T) {
	var (
		mu                    sync.Mutex
		loadCodeRejectedCalls int
		loadCodeAcceptedCalls int
		loadCodeBadAuth       string
		unexpectedURL         string
		refreshCalls          int
		generateAuthHdr       string
	)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
			authHdr := req.Header.Get("Authorization")
			if authHdr == "Bearer revoked-unexpired-ag-token" {
				mu.Lock()
				loadCodeRejectedCalls++
				mu.Unlock()
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"code":401,"message":"invalid authentication credentials","status":"UNAUTHENTICATED"}}`)),
				}, nil
			}
			mu.Lock()
			loadCodeAcceptedCalls++
			if authHdr != "Bearer refreshed-ag-token-prepare-401" && loadCodeBadAuth == "" {
				loadCodeBadAuth = authHdr
			}
			mu.Unlock()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"ag-proj-recovered-401"}`)),
			}, nil

		case req.URL.String() == "https://oauth2.googleapis.com/token":
			mu.Lock()
			refreshCalls++
			mu.Unlock()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"refreshed-ag-token-prepare-401","expires_in":3600,"token_type":"Bearer"}`)),
			}, nil

		case strings.Contains(req.URL.Path, "streamGenerateContent"):
			mu.Lock()
			generateAuthHdr = req.Header.Get("Authorization")
			mu.Unlock()
			sse := "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}],"modelVersion":"claude-opus-5-5-high","responseId":"resp_ag_prep401","usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil

		default:
			mu.Lock()
			if unexpectedURL == "" {
				unexpectedURL = req.Method + " " + req.URL.String()
			}
			mu.Unlock()
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected"}`)),
			}, nil
		}
	})

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRoundTripperProvider(issue7737StaticRTProvider{rt: transport})
	manager.RegisterExecutor(exec)

	ctx := cliproxyauth.WithSkipPersist(context.Background())
	authID := "ag-e2e-prepare-401-" + uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID + ".json",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "ag-prep401@example.com",
			"access_token":  "revoked-unexpired-ag-token",
			"refresh_token": "ag-valid-rt-401-" + uuid.NewString(),
			"expired":       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errReg := manager.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: "claude-opus-5-5-high", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000, ZeroAllowed: true, Levels: []string{"low", "medium", "high"}}},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	verified, errVerify := manager.VerifyInference(ctx, authID, "claude-opus-5-5-high")
	if errVerify != nil {
		t.Fatalf("VerifyInference error = %v, want nil", errVerify)
	}
	if verified == nil || !verified.InferenceVerified {
		t.Fatalf("expected InferenceVerified=true, got %+v", verified)
	}
	if gotProj, _ := verified.Metadata["project_id"].(string); gotProj != "ag-proj-recovered-401" {
		t.Fatalf("persisted project_id = %q, want ag-proj-recovered-401", gotProj)
	}

	mu.Lock()
	defer mu.Unlock()
	if unexpectedURL != "" {
		t.Fatalf("unexpected HTTP request: %s", unexpectedURL)
	}
	if loadCodeBadAuth != "" {
		t.Fatalf("retry loadCodeAssist Authorization = %q, want Bearer refreshed-ag-token-prepare-401", loadCodeBadAuth)
	}
	if loadCodeRejectedCalls < 1 || loadCodeAcceptedCalls < 1 || refreshCalls != 1 {
		t.Fatalf("loadCodeAssist rejected=%d (want >=1) accepted=%d (want >=1), refresh=%d (want 1)", loadCodeRejectedCalls, loadCodeAcceptedCalls, refreshCalls)
	}
	if generateAuthHdr != "Bearer refreshed-ag-token-prepare-401" {
		t.Fatalf("generate Authorization = %q, want Bearer refreshed-ag-token-prepare-401", generateAuthHdr)
	}
}
