package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func enableIssue7888LocalKV(t *testing.T) {
	t.Helper()
	homekv.ClearCurrent()
	internalcache.ClearAntigravityReasoningReplayCache()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatalf("kvstore.ShutdownLocal() setup error = %v", errShutdown)
	}
	dbPath := filepath.Join(t.TempDir(), "issue7888-kv.db")
	if errCfg := kvstore.ConfigureLocal(true, dbPath); errCfg != nil {
		t.Fatalf("kvstore.ConfigureLocal(true, %q) error = %v", dbPath, errCfg)
	}
	t.Cleanup(func() {
		internalcache.ClearAntigravityReasoningReplayCache()
		homekv.ClearCurrent()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Errorf("kvstore.ShutdownLocal() cleanup error = %v", errShutdown)
		}
	})
}

func newIssue7888TestAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token":  "token-" + id,
			"refresh_token": "refresh-" + id,
			"project_id":    "project-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func issue7888ReplayItems(t *testing.T, model string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([][]byte, bool) {
	t.Helper()
	scope := antigravityReasoningReplayScopeFromRequestDoc(context.Background(), model, req, opts, ojson.NewDocString("{}"))
	if !scope.valid() {
		t.Fatalf("expected valid replay scope for model=%q format=%s", model, opts.SourceFormat)
	}
	items, found, errGet := internalcache.GetAntigravityReasoningReplayItemsRequired(context.Background(), scope.modelName, scope.sessionKey)
	if errGet != nil {
		t.Fatalf("GetAntigravityReasoningReplayItemsRequired(%q, %q) error = %v", scope.modelName, scope.sessionKey, errGet)
	}
	return items, found
}

func issue7888ItemsContain(items [][]byte, needle string) bool {
	for _, item := range items {
		if strings.Contains(string(item), needle) {
			return true
		}
	}
	return false
}

func TestAntigravityIssue7888_ExecuteClaudeNonStreamDoesNotCommitReplayOnApplyPatchError(t *testing.T) {
	for _, model := range []string{"gemini-3-pro", "gemini-3.1-flash-image"} {
		t.Run(model+"/fresh_session", func(t *testing.T) {
			enableIssue7888LocalKV(t)

			rejectedSig := strings.Repeat("r", 128)
			sessionID := "issue7888-patch-fresh-" + model
			exec := NewAntigravityExecutor(&config.Config{})
			auth := newIssue7888TestAuth("auth-fresh-" + model)

			invalidPatchSSE := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking\",\"thought\":true,\"thoughtSignature\":\"" + rejectedSig + "\"},{\"functionCall\":{\"id\":\"call_bad_patch\",\"name\":\"functions__apply_patch\",\"args\":{\"input\":7}},\"thoughtSignature\":\"" + rejectedSig + "\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15},\"modelVersion\":\"" + model + "\",\"responseId\":\"resp-issue7888-bad\"}}\n\n"

			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
					t.Errorf("request path = %q, want :streamGenerateContent via executeClaudeNonStream", r.URL.Path)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(invalidPatchSSE)),
				}, nil
			}))

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(executorPatchRequest),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: req.Payload,
				Headers:         http.Header{"Session-Id": []string{sessionID}},
			}

			_, errExec := exec.Execute(ctx, auth, req, opts)
			if errExec == nil {
				t.Fatal("Execute() error = nil, want 502 apply_patch translation error")
			}
			var status cliproxyexecutor.StatusError
			if !errors.As(errExec, &status) || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("Execute() error = %T (%v), want HTTP 502", errExec, errExec)
			}
			if !strings.Contains(errExec.Error(), helps.ApplyPatchUpstreamErrorMessage) {
				t.Fatalf("Execute() error = %q, want %q", errExec.Error(), helps.ApplyPatchUpstreamErrorMessage)
			}

			items, found := issue7888ReplayItems(t, model, req, opts)
			if found || len(items) > 0 {
				t.Fatalf("failed apply_patch turn committed reasoning replay to KV: found=%v items=%d", found, len(items))
			}
		})

		t.Run(model+"/preserves_prior_turn_state", func(t *testing.T) {
			enableIssue7888LocalKV(t)

			validSig := strings.Repeat("v", 128)
			rejectedSig := strings.Repeat("x", 128)
			sessionID := "issue7888-patch-preserve-" + model
			exec := NewAntigravityExecutor(&config.Config{})
			auth := newIssue7888TestAuth("auth-preserve-" + model)

			validPatchSSE := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking\",\"thought\":true,\"thoughtSignature\":\"" + validSig + "\"},{\"functionCall\":{\"id\":\"call_good_patch\",\"name\":\"functions__apply_patch\",\"args\":{\"input\":\"  *** Begin Patch\\n*** End Patch\\n \"}},\"thoughtSignature\":\"" + validSig + "\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15},\"modelVersion\":\"" + model + "\",\"responseId\":\"resp-issue7888-good\"}}\n\n"
			invalidPatchSSE := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking2\",\"thought\":true,\"thoughtSignature\":\"" + rejectedSig + "\"},{\"functionCall\":{\"id\":\"call_bad_patch\",\"name\":\"functions__apply_patch\",\"args\":{\"input\":7}},\"thoughtSignature\":\"" + rejectedSig + "\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":6,\"totalTokenCount\":18},\"modelVersion\":\"" + model + "\",\"responseId\":\"resp-issue7888-bad2\"}}\n\n"

			turn := 0
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
				turn++
				body := validPatchSSE
				if turn > 1 {
					body = invalidPatchSSE
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}))

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(executorPatchRequest),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: req.Payload,
				Headers:         http.Header{"Session-Id": []string{sessionID}},
			}

			if _, errTurn1 := exec.Execute(ctx, auth, req, opts); errTurn1 != nil {
				t.Fatalf("Turn 1 Execute() error = %v", errTurn1)
			}
			itemsBefore, foundBefore := issue7888ReplayItems(t, model, req, opts)
			if !foundBefore || !issue7888ItemsContain(itemsBefore, validSig) {
				t.Fatalf("Turn 1 did not persist expected thoughtSignature: found=%v items=%q", foundBefore, itemsBefore)
			}

			_, errTurn2 := exec.Execute(ctx, auth, req, opts)
			if errTurn2 == nil {
				t.Fatal("Turn 2 Execute() error = nil, want 502 apply_patch translation error")
			}

			itemsAfter, foundAfter := issue7888ReplayItems(t, model, req, opts)
			if !foundAfter || !issue7888ItemsContain(itemsAfter, validSig) {
				t.Fatalf("Turn 2 failure corrupted prior Turn 1 KV replay state: found=%v items=%q", foundAfter, itemsAfter)
			}
			if issue7888ItemsContain(itemsAfter, rejectedSig) {
				t.Fatalf("Turn 2 failure leaked rejected thoughtSignature into KV replay cache: %q", itemsAfter)
			}
		})
	}
}

func TestAntigravityIssue7888_ExecuteClaudeNonStreamDoesNotCommitReplayOnTranslatedClaudeError(t *testing.T) {
	enableIssue7888LocalKV(t)

	const model = "gemini-3-pro"
	rejectedSig := strings.Repeat("c", 128)
	sessionID := "issue7888-claude-err-" + model
	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7888TestAuth("auth-claude-err")

	// Grounding chunks bypass CheckGeminiZeroOutputNonStream while
	// ConvertAntigravityResponseToClaudeNonStream sees no visible content parts
	// with finishReason=MALFORMED_FUNCTION_CALL and emits a Claude error object.
	malformedClaudeSSE := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking\",\"thought\":true,\"thoughtSignature\":\"" + rejectedSig + "\"}]},\"finishReason\":\"MALFORMED_FUNCTION_CALL\",\"groundingMetadata\":{\"groundingChunks\":[{\"web\":{\"uri\":\"https://example.com\",\"title\":\"Example\"}}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":0,\"thoughtsTokenCount\":5,\"totalTokenCount\":15},\"modelVersion\":\"" + model + "\",\"responseId\":\"resp-issue7888-claude-err\"}}\n\n"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("request path = %q, want :streamGenerateContent via executeClaudeNonStream", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(malformedClaudeSSE)),
		}, nil
	}))

	reqPayload := []byte(`{"model":"` + model + `","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: reqPayload,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: reqPayload,
		Headers:         http.Header{"Session-Id": []string{sessionID}},
	}

	_, errExec := exec.Execute(ctx, auth, req, opts)
	if errExec == nil {
		t.Fatal("Execute() error = nil, want 502 translated Claude error")
	}
	var status cliproxyexecutor.StatusError
	if !errors.As(errExec, &status) || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("Execute() error = %T (%v), want HTTP 502", errExec, errExec)
	}
	if gotType := ojson.Get(errExec.Error(), "error.type").String(); gotType != "api_error" {
		t.Fatalf("Execute() error.type = %q, want api_error; err=%s", gotType, errExec.Error())
	}

	items, found := issue7888ReplayItems(t, model, req, opts)
	if found || len(items) > 0 {
		t.Fatalf("translated Claude error turn committed reasoning replay to KV: found=%v items=%d", found, len(items))
	}
}

func TestAntigravityIssue7888_ExecuteNonStreamDoesNotCommitReplayOnTranslationError(t *testing.T) {
	const model = "gemini-3.8-flash-high"

	t.Run("apply_patch_error", func(t *testing.T) {
		enableIssue7888LocalKV(t)

		rejectedSig := strings.Repeat("e", 128)
		sessionID := "issue7888-exec-patch"
		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7888TestAuth("auth-exec-patch")

		invalidPatchJSON := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true,"thoughtSignature":"` + rejectedSig + `"},{"functionCall":{"id":"call_bad_exec","name":"functions__apply_patch","args":{"input":7}},"thoughtSignature":"` + rejectedSig + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"` + model + `","responseId":"resp-issue7888-exec-bad"}}`

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.Path, ":generateContent") {
				t.Errorf("request path = %q, want :generateContent via Execute", r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(invalidPatchJSON)),
			}, nil
		}))

		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(executorPatchRequest),
		}
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			OriginalRequest: req.Payload,
			Headers:         http.Header{"Session-Id": []string{sessionID}},
		}

		_, errExec := exec.Execute(ctx, auth, req, opts)
		if errExec == nil {
			t.Fatal("Execute() error = nil, want 502 apply_patch translation error")
		}
		var status cliproxyexecutor.StatusError
		if !errors.As(errExec, &status) || status.StatusCode() != http.StatusBadGateway {
			t.Fatalf("Execute() error = %T (%v), want HTTP 502", errExec, errExec)
		}

		items, found := issue7888ReplayItems(t, model, req, opts)
		if found || len(items) > 0 {
			t.Fatalf("Execute() apply_patch failure committed reasoning replay to KV: found=%v items=%d", found, len(items))
		}
	})

	t.Run("translated_claude_error", func(t *testing.T) {
		enableIssue7888LocalKV(t)

		rejectedSig := strings.Repeat("f", 128)
		sessionID := "issue7888-exec-claude-err"
		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7888TestAuth("auth-exec-claude-err")

		malformedClaudeJSON := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true,"thoughtSignature":"` + rejectedSig + `"}]},"finishReason":"MALFORMED_FUNCTION_CALL","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":5,"totalTokenCount":15},"modelVersion":"` + model + `","responseId":"resp-issue7888-exec-claude-err"}}`

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.Path, ":generateContent") {
				t.Errorf("request path = %q, want :generateContent via Execute", r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(malformedClaudeJSON)),
			}, nil
		}))

		reqPayload := []byte(`{"model":"` + model + `","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: reqPayload,
		}
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatClaude,
			ResponseFormat:  sdktranslator.FormatClaude,
			OriginalRequest: reqPayload,
			Headers:         http.Header{"Session-Id": []string{sessionID}},
		}

		_, errExec := exec.Execute(ctx, auth, req, opts)
		if errExec == nil {
			t.Fatal("Execute() error = nil, want 502 translated Claude error")
		}
		var status cliproxyexecutor.StatusError
		if !errors.As(errExec, &status) || status.StatusCode() != http.StatusBadGateway {
			t.Fatalf("Execute() error = %T (%v), want HTTP 502", errExec, errExec)
		}

		items, found := issue7888ReplayItems(t, model, req, opts)
		if found || len(items) > 0 {
			t.Fatalf("Execute() translated Claude error committed reasoning replay to KV: found=%v items=%d", found, len(items))
		}
	})
}

func TestAntigravityIssue7888_NonStreamCommitsReplayAfterSuccessfulTranslation(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		contentType string
		body        func(sig string) string
	}{
		{
			name:        "executeClaudeNonStream_gemini3Pro",
			model:       "gemini-3-pro",
			contentType: "text/event-stream",
			body: func(sig string) string {
				return "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking\",\"thought\":true,\"thoughtSignature\":\"" + sig + "\"},{\"functionCall\":{\"id\":\"call_ok_claude\",\"name\":\"functions__apply_patch\",\"args\":{\"input\":\"  *** Begin Patch\\n*** End Patch\\n \"}},\"thoughtSignature\":\"" + sig + "\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15},\"modelVersion\":\"gemini-3-pro\",\"responseId\":\"resp-issue7888-ok-claude\"}}\n\n"
			},
		},
		{
			name:        "Execute_gemini38Flash",
			model:       "gemini-3.8-flash-high",
			contentType: "application/json",
			body: func(sig string) string {
				return `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true,"thoughtSignature":"` + sig + `"},{"functionCall":{"id":"call_ok_exec","name":"functions__apply_patch","args":{"input":"  *** Begin Patch\n*** End Patch\n "}},"thoughtSignature":"` + sig + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.8-flash-high","responseId":"resp-issue7888-ok-exec"}}`
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enableIssue7888LocalKV(t)

			validSig := strings.Repeat("p", 128)
			sessionID := "issue7888-ok-" + tc.name
			exec := NewAntigravityExecutor(&config.Config{})
			auth := newIssue7888TestAuth("auth-ok-" + tc.name)

			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{tc.contentType}},
					Body:       io.NopCloser(strings.NewReader(tc.body(validSig))),
				}, nil
			}))

			req := cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: []byte(executorPatchRequest),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: req.Payload,
				Headers:         http.Header{"Session-Id": []string{sessionID}},
			}

			resp, errExec := exec.Execute(ctx, auth, req, opts)
			if errExec != nil {
				t.Fatalf("Execute() error = %v", errExec)
			}
			assertExecutorPatchOutput(t, resp.Payload)

			items, found := issue7888ReplayItems(t, tc.model, req, opts)
			if !found || !issue7888ItemsContain(items, validSig) {
				t.Fatalf("successful turn did not persist thoughtSignature to KV: found=%v items=%q", found, items)
			}
		})
	}
}
