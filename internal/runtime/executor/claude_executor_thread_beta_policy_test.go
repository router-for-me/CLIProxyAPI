package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeThreadCachedProfileRejectsMissingCapabilities(t *testing.T) {
	for _, addition := range []string{`"speed":"fast"`, `"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}`} {
		t.Run(addition, func(t *testing.T) {
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-new-capability"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
			if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},` + addition + `,"messages":[{"role":"user","content":"second"}]}`)
			_, errExecute := executor.Execute(ctx, auth, request, opts)
			var requestErr cliproxyexecutor.RequestScopedError
			if errExecute == nil || !errors.As(errExecute, &requestErr) || !requestErr.IsRequestScoped() || calls != 1 {
				t.Fatalf("incompatible capability: error=%v, sends=%d, want request-scoped conflict without sending", errExecute, calls)
			}
		})
	}
}

func TestClaudeThreadDynamicBetaPolicyCannotBeOverridden(t *testing.T) {
	calls := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-dynamic", "base_url": "https://gateway.example.com", "header:Anthropic-Beta": "$X-Upstream-Betas"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}, "X-Upstream-Betas": []string{"configured-a"}}}
	request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
	if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
		t.Fatal(errExecute)
	}
	opts.Headers.Set("X-Upstream-Betas", "configured-b")
	request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"messages":[{"role":"user","content":"second"}]}`)
	_, errExecute := executor.Execute(ctx, auth, request, opts)
	var requestErr cliproxyexecutor.RequestScopedError
	if errExecute == nil || !errors.As(errExecute, &requestErr) || !requestErr.IsRequestScoped() || calls != 1 {
		t.Fatalf("dynamic configured beta was overwritten: error=%v, sends=%d", errExecute, calls)
	}
}

func TestClaudeThreadUnknownProfileRequiresReplay(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("sent an unverified Thread beta guess")
		return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_unknown")), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-unknown"}}
	request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_unknown"},"messages":[{"role":"user","content":"second"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}}}
	if _, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, request, opts); errExecute == nil {
		t.Fatal("missing actual snapshot was accepted instead of requiring replay")
	}
}

func TestClaudeThreadCallerBodyBetasMayBeOmittedOnContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var sent []string
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				sent = append(sent, strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","))
				if stream {
					return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", claudeThreadTestSSE("msg_thread_1")), nil
				}
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-body-declaration"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}}}
			for _, body := range []string{
				`{"model":"claude-opus-5","thread":{"type":"create"},"betas":["effort-2025-11-24"],"messages":[{"role":"user","content":"first"}]}`,
				`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`,
			} {
				request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(body)}
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			}
			if len(sent) != 2 || sent[0] != sent[1] || !strings.Contains(sent[0], "effort-2025-11-24") {
				t.Fatalf("caller body declaration was not retained: %q", sent)
			}
		})
	}
}

func TestClaudeThreadPayloadFilterDoesNotConstrainIndependentHeaderDeclarations(t *testing.T) {
	var sent []string
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		sent = append(sent, strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","))
		return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "sk-ant-oat-header-declaration"}, Metadata: map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12,effort-2025-11-24"}}}
	request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"betas":["effort-2025-11-24"],"safeguards":{},"messages":[{"role":"user","content":"first"}]}`)}
	if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
		t.Fatal(errExecute)
	}
	cfg := &config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "claude-opus-5"}}, Params: []string{"betas"}}}}}
	request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"betas":["effort-2025-11-24"],"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`)
	if _, errExecute := executor.WithConfig(cfg).Execute(ctx, auth, request, opts); errExecute != nil {
		t.Fatalf("body filter invalidated an independently declared header capability: %v", errExecute)
	}
	if len(sent) != 2 || sent[0] != sent[1] {
		t.Fatalf("header declarations changed while body declarations were filtered: %q", sent)
	}
}

func TestClaudeThreadManagerRoutesToOriginalCredential(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, sameKey := range []bool{false, true} {
			for _, variant := range []string{"normal", "disabled owner", "conflicting pin", "foreign caller"} {
				t.Run(fmt.Sprintf("stream=%v/same-key=%v/%s", stream, sameKey, variant), func(t *testing.T) {
					caller := t.Name() + uuid.NewString()
					manager := cliproxyauth.NewManager(nil, nil, nil)
					manager.SetRetryConfig(0, 0, 0)
					manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
					if _, sticky := manager.Selector().(*cliproxyauth.SessionAffinitySelector); sticky {
						t.Fatal("test requires default round-robin without session affinity")
					}
					auths := make([]*cliproxyauth.Auth, 0, 2)
					for i := range 2 {
						key := "key-thread-routing"
						if !sameKey {
							key += fmt.Sprint(i)
						}
						auth := &cliproxyauth.Auth{ID: fmt.Sprintf("%s-%d", caller, i), Provider: "claude", Attributes: map[string]string{"api_key": key}}
						registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5"}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
						if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
							t.Fatal(errRegister)
						}
						auths = append(auths, auth)
					}
					calls := 0
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						if stream {
							return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", claudeThreadTestSSE("msg_thread_1")), nil
						}
						return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					var selected []string
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}}, Metadata: map[string]any{
						cliproxyexecutor.CallerScopeMetadataKey:          caller,
						cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(id string) { selected = append(selected, id) },
					}}
					execute := func(request cliproxyexecutor.Request) error {
						if stream {
							result, errStream := manager.ExecuteStream(ctx, []string{"claude"}, request, opts)
							if errStream != nil {
								return errStream
							}
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									return chunk.Err
								}
							}
							return nil
						}
						_, errExecute := manager.Execute(ctx, []string{"claude"}, request, opts)
						return errExecute
					}
					request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
					if errExecute := execute(request); errExecute != nil {
						t.Fatal(errExecute)
					}
					if len(selected) != 1 {
						t.Fatalf("initial selections = %v", selected)
					}
					switch variant {
					case "disabled owner":
						owner, _ := manager.GetByID(selected[0])
						owner.Disabled = true
						if _, errUpdate := manager.Update(ctx, owner); errUpdate != nil {
							t.Fatal(errUpdate)
						}
					case "conflicting pin":
						for _, auth := range auths {
							if auth.ID != selected[0] {
								opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = auth.ID
							}
						}
					case "foreign caller":
						opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "foreign-" + caller
					}
					request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"messages":[{"role":"user","content":"second"}]}`)
					errExecute := execute(request)
					if variant == "normal" {
						if errExecute != nil || calls != 2 || len(selected) != 2 || selected[1] != selected[0] {
							t.Fatalf("Thread lost original credential: error=%v, calls=%d, selections=%v", errExecute, calls, selected)
						}
					} else {
						if errExecute == nil || calls != 1 {
							t.Fatalf("unsafe continuation: error=%v, calls=%d, selections=%v", errExecute, calls, selected)
						}
						if variant != "foreign caller" && len(selected) != 1 {
							t.Fatalf("selected a different credential instead of honoring ownership: %v", selected)
						}
					}
				})
			}
		}
	}
}

func claudeThreadTestKVStore(t *testing.T) string {
	t.Helper()
	homekv.ClearCurrent()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	path := filepath.Join(t.TempDir(), "kv.db")
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(func() {
		homekv.ClearCurrent()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})
	return path
}
