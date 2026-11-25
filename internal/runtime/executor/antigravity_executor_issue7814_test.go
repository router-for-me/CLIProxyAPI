package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antigravityValidationRequiredBody = `{
	"error": {
		"code": 403,
		"message": "Verify your account to continue.",
		"status": "PERMISSION_DENIED",
		"details": [
			{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "VALIDATION_REQUIRED",
				"domain": "cloudcode-pa.googleapis.com",
				"metadata": {
					"validation_url": "https://accounts.google.com/signin/continue",
					"validation_url_link_text": "Verify your account"
				}
			}
		]
	}
}`

const antigravityValidationRequiredSSEFrame = `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED"}]}}`

func TestNewAntigravityStatusErr_Issue7814_CredentialScoped(t *testing.T) {
	t.Run("403 VALIDATION_REQUIRED is credential scoped and not terminal TOS", func(t *testing.T) {
		errVal := newAntigravityStatusErr(http.StatusForbidden, []byte(antigravityValidationRequiredBody))
		if !errVal.IsCredentialScoped() {
			t.Fatal("newAntigravityStatusErr(403 VALIDATION_REQUIRED).IsCredentialScoped() = false, want true")
		}
		if errVal.IsTerminalTOSViolation() {
			t.Fatal("newAntigravityStatusErr(403 VALIDATION_REQUIRED).IsTerminalTOSViolation() = true, want false")
		}
	})

	t.Run("401 unauthorized is credential scoped", func(t *testing.T) {
		body := []byte(`{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`)
		errVal := newAntigravityStatusErr(http.StatusUnauthorized, body)
		if !errVal.IsCredentialScoped() {
			t.Fatal("newAntigravityStatusErr(401).IsCredentialScoped() = false, want true")
		}
	})

	t.Run("plain 403 without VALIDATION_REQUIRED or TOS_VIOLATION stays non-credential-scoped", func(t *testing.T) {
		body := []byte(`{"error":{"code":403,"message":"Caller does not have permission","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_ACCESS_DENIED"}]}}`)
		errVal := newAntigravityStatusErr(http.StatusForbidden, body)
		if errVal.IsCredentialScoped() {
			t.Fatal("newAntigravityStatusErr(plain 403).IsCredentialScoped() = true, want false")
		}
	})
}

func TestAntigravityIssue7814_ValidationRequiredClosesIdleTransports(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	newAuth := func(id, baseURL string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID:         id,
			Provider:   "antigravity",
			Attributes: map[string]string{"base_url": baseURL},
			Metadata: map[string]any{
				"access_token":  "token-" + id,
				"refresh_token": "refresh-" + id,
				"project_id":    "project-1",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
	}
	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)

	t.Run("Execute non-stream HTTP 403 VALIDATION_REQUIRED", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(antigravityValidationRequiredBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newAuth("issue7814-exec-403", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusForbidden || !sErr.IsCredentialScoped() {
			t.Fatalf("Execute() err = %#v, want credential-scoped 403 statusErr", errExec)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on Execute 403 VALIDATION_REQUIRED")
		}
	})

	t.Run("Execute Claude non-stream HTTP 403 VALIDATION_REQUIRED", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(antigravityValidationRequiredBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newAuth("issue7814-claude-nonstream-403", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusForbidden || !sErr.IsCredentialScoped() {
			t.Fatalf("Execute(claude) err = %#v, want credential-scoped 403 statusErr", errExec)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on Claude non-stream 403 VALIDATION_REQUIRED")
		}
	})

	t.Run("Execute Claude non-stream mid-stream SSE 403 VALIDATION_REQUIRED", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: " + antigravityValidationRequiredSSEFrame + "\n\n"))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newAuth("issue7814-claude-midstream-403", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusForbidden || !sErr.IsCredentialScoped() {
			t.Fatalf("Execute(mid-stream) err = %#v, want credential-scoped 403 statusErr", errExec)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on mid-stream 403 VALIDATION_REQUIRED")
		}
	})

	t.Run("ExecuteStream HTTP 403 and mid-stream SSE 403 VALIDATION_REQUIRED", func(t *testing.T) {
		for _, midStream := range []bool{false, true} {
			name := "http_403"
			if midStream {
				name = "mid_stream_403"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if midStream {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("data: " + antigravityValidationRequiredSSEFrame + "\n\n"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(antigravityValidationRequiredBody))
				}))
				defer server.Close()

				cfg := &config.Config{}
				exec := NewAntigravityExecutor(cfg)
				auth := newAuth("issue7814-stream-"+name, server.URL)
				t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

				res, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-3-flash",
					Payload: reqPayload,
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatAntigravity,
					ResponseFormat: sdktranslator.FormatAntigravity,
				})
				var gotErr error
				if errStream != nil {
					gotErr = errStream
				} else if res != nil {
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							gotErr = chunk.Err
						}
					}
				}
				var sErr statusErr
				if !errors.As(gotErr, &sErr) || sErr.StatusCode() != http.StatusForbidden || !sErr.IsCredentialScoped() {
					t.Fatalf("ExecuteStream(%s) err = %#v, want credential-scoped 403 statusErr", name, gotErr)
				}
				if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
					t.Fatalf("expected idle transports for auth to be evicted on ExecuteStream %s VALIDATION_REQUIRED", name)
				}
			})
		}
	})

	t.Run("CountTokens HTTP 403 VALIDATION_REQUIRED", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(antigravityValidationRequiredBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newAuth("issue7814-tokens-403", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errTokens := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errTokens, &sErr) || sErr.StatusCode() != http.StatusForbidden || !sErr.IsCredentialScoped() {
			t.Fatalf("CountTokens() err = %#v, want credential-scoped 403 statusErr", errTokens)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on CountTokens 403 VALIDATION_REQUIRED")
		}
	})
}

func TestAntigravityIssue7814_ValidationRequiredBlocksSiblingModelInManager(t *testing.T) {
	ctx := context.Background()
	mgr := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	cred := &cliproxyauth.Auth{
		ID:       "ag-issue7814-sibling",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token":  "tok-valid",
			"refresh_token": "ref-valid",
			"project_id":    "proj-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "antigravity", []*registry.ModelInfo{
		{ID: "gemini-3-flash"},
		{ID: "claude-sonnet-4-6"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
	})
	if _, errReg := mgr.Register(cliproxyauth.WithSkipPersist(ctx), cred); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	errVal := newAntigravityStatusErr(http.StatusForbidden, []byte(antigravityValidationRequiredBody))
	mgr.MarkResult(ctx, cliproxyauth.Result{
		AuthID:          cred.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: errVal.IsCredentialScoped(),
		Error: &cliproxyauth.Error{
			HTTPStatus: errVal.StatusCode(),
			Message:    errVal.Error(),
		},
	})

	got, ok := mgr.GetByID(cred.ID)
	if !ok || got == nil {
		t.Fatal("GetByID() missing credential")
	}
	picked, errPick := mgr.Selector().Pick(ctx, "antigravity", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*cliproxyauth.Auth{got})
	if errPick == nil {
		t.Fatalf("Selector.Pick(claude-sonnet-4-6) selected %q after 403 VALIDATION_REQUIRED on gemini-3-flash, want cooldown error", picked.ID)
	}
}
