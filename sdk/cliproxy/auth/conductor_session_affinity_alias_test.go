package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestManagerSessionAffinityAliasCooldownPreservesSelection(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for strategy, newFallback := range map[string]func() Selector{
		"round-robin":          func() Selector { return &RoundRobinSelector{} },
		"weighted-round-robin": func() Selector { return &WeightedRoundRobinSelector{} },
		"fill-first":           func() Selector { return &FillFirstSelector{} },
	} {
		for _, mode := range []string{"no-session", "explicit-session", "lcp"} {
			for _, path := range []string{"select", "execute", "stream"} {
				t.Run(strategy+"/"+mode+"/"+path, func(t *testing.T) {
					ctx := context.Background()
					const routeModel, targetModel = "affinity-alias", "affinity-healthy-target"
					selector := NewSessionAffinitySelector(newFallback())
					t.Cleanup(selector.Stop)
					manager := NewManager(nil, selector, nil)
					manager.SetRetryConfig(0, 0, 0)
					// Order the lower tier first to catch fallback paths that forget to
					// narrow the manager's across-priority candidates before selecting.
					highID, lowID := "z-high-"+t.Name(), "a-low-"+t.Name()
					retryAfter := time.Hour
					cool := func(id, model string) {
						manager.MarkResult(ctx, Result{
							AuthID: id, Provider: "codex", Model: model, RetryAfter: &retryAfter,
							Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model quota"},
						})
					}
					for _, candidate := range []*Auth{
						{ID: highID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "4", AttributeWeight: "1"}},
						{ID: lowID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "3", AttributeWeight: "1"}},
					} {
						registry.GetGlobalRegistry().RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: routeModel}, {ID: targetModel}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
						if _, errRegister := manager.Register(ctx, candidate); errRegister != nil {
							t.Fatal(errRegister)
						}
						cool(candidate.ID, routeModel)
					}
					// Introduce an alias while both credentials retain cooldowns under
					// its old name. The newly resolved target is healthy on both.
					manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
						"codex": {{Name: targetModel, Alias: routeModel, Fork: true}},
					})
					var attempts []string
					execute := func(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						attempts = append(attempts, auth.ID)
						if mode == "lcp" && opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] == nil {
							t.Error("expected executor to receive the LCP session ID")
						}
						if req.Model != targetModel {
							t.Errorf("upstream model = %s, want %s", req.Model, targetModel)
						}
						return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
					}
					manager.RegisterExecutor(&customStreamMockExecutor{
						identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute},
						streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
							response, errExecute := execute(ctx, auth, req, opts)
							if errExecute != nil {
								return nil, errExecute
							}
							chunks := make(chan cliproxyexecutor.StreamChunk, 1)
							chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
							close(chunks)
							return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
						},
					})
					options := func(session string) cliproxyexecutor.Options {
						opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
						switch mode {
						case "explicit-session":
							opts.Headers = http.Header{"X-Session-Id": []string{session}}
						case "lcp":
							opts.SourceFormat = sdktranslator.FormatOpenAI
							opts.OriginalRequest = []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":%q},{"role":"user","content":"hello"}]}`, session))
							opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "alias-caller"
						}
						return opts
					}
					run := func(opts cliproxyexecutor.Options) (string, error) {
						switch path {
						case "select":
							auth, errSelect := manager.SelectAuth(ctx, "codex", routeModel, opts)
							if errSelect != nil {
								return "", errSelect
							}
							return auth.ID, nil
						case "execute":
							response, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: routeModel}, opts)
							return string(response.Payload), errExecute
						default:
							opts.Stream = true
							result, errStream := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: routeModel}, opts)
							if errStream != nil {
								return "", errStream
							}
							var payload []byte
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Errorf("unexpected stream error: %v", chunk.Err)
								}
								payload = append(payload, chunk.Payload...)
							}
							return string(payload), nil
						}
					}
					assertPick := func(label, session, wantID string) {
						t.Helper()
						opts := options(session)
						gotID, errPick := run(opts)
						if errPick != nil || gotID != wantID {
							t.Fatalf("%s: auth=%q error=%v, want %s; upstream attempts=%v", label, gotID, errPick, wantID, attempts)
						}
						if mode == "lcp" && path == "select" && opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] == nil {
							t.Fatal("expected LCP matching path to publish a session ID")
						}
					}
					assertPick("cold selection ignores old alias cooldown", "stable", highID)
					cool(highID, targetModel)
					assertPick("actual target cooldown triggers failover", "stable", lowID)
					expireSessionAffinityPriorityModelCooldown(t, manager, highID, targetModel)
					wantAfterRecovery := lowID
					if mode == "no-session" {
						wantAfterRecovery = highID
					}
					assertPick("higher-priority recovery preserves binding", "stable", wantAfterRecovery)
					assertPick("fresh session uses highest priority", "fresh", highID)
					cool(highID, targetModel)
					cool(lowID, targetModel)
					before := len(attempts)
					if _, errPick := run(options("stable")); statusCodeFromError(errPick) != http.StatusTooManyRequests {
						t.Fatalf("all actual targets cooling: error=%v, want 429", errPick)
					}
					if len(attempts) != before {
						t.Fatal("attempted an upstream request while all targets were cooling")
					}
				})
			}
		}
	}
}
