package auth

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCapacityClaudeResetTimestampFormats(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, raw := range []string{
		"1800000060", "1800000000.5",
		now.Add(time.Minute).Format(time.RFC3339Nano),
		now.Add(time.Minute).UTC().Format(http.TimeFormat),
	} {
		t.Run(raw, func(t *testing.T) {
			auth := &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.75",
				"Anthropic-Ratelimit-Unified-5h-Reset":       raw,
			}}}
			remaining, known := observedCapacity(auth, "claude-sonnet-4-6", now)
			if !known || math.Abs(remaining-0.25) > 1e-9 {
				t.Fatalf("reset %q: capacity = %v, %v", raw, remaining, known)
			}
			if _, known = observedCapacity(auth, "claude-sonnet-4-6", now.Add(2*time.Minute)); known {
				t.Fatal("expired reset remained usable")
			}
		})
	}
	for _, raw := range []string{"NaN", "+Inf", "1e100", "-1", "bad"} {
		if _, valid := capacityResetTime(raw); valid {
			t.Fatalf("accepted invalid reset %q", raw)
		}
	}
}

func TestManagerCapacityWebsocketPreferenceAcrossPriorities(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, mode := range []string{"none", "explicit", "lcp"} {
			for _, higherHTTPPriority := range []bool{false, true} {
				for _, path := range []string{"select", "execute", "stream"} {
					t.Run(fmt.Sprintf("affinity=%t/%s/http-priority=%t/%s", affinity, mode, higherHTTPPriority, path), func(t *testing.T) {
						var selector Selector = &CapacitySelector{}
						if affinity {
							s := NewSessionAffinitySelector(selector)
							t.Cleanup(s.Stop)
							selector = s
						}
						manager := NewManager(nil, selector, nil)
						model := "capacity-ws-" + t.Name()
						httpAuth := capacityTestAuth(t.Name()+"-http", 0, time.Now())
						wsAuth := capacityTestAuth(t.Name()+"-ws", 80, time.Now())
						wsAuth.Attributes = map[string]string{"websockets": "true"}
						if higherHTTPPriority {
							httpAuth.Attributes = map[string]string{"priority": "10"}
						}
						for _, auth := range []*Auth{httpAuth, wsAuth} {
							registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
							t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
							if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
								t.Fatal(errRegister)
							}
						}
						execute := func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
							return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
						}
						manager.RegisterExecutor(&customStreamMockExecutor{
							identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute},
							streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
								response, errExecute := execute(ctx, auth, req, opts)
								chunks := make(chan cliproxyexecutor.StreamChunk, 1)
								chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
								close(chunks)
								return &cliproxyexecutor.StreamResult{Chunks: chunks}, errExecute
							},
						})
						opts := cliproxyexecutor.Options{Stream: path == "stream", Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "capacity-ws-caller"}}
						switch mode {
						case "explicit":
							opts.Headers = http.Header{"Session-Id": []string{"stable"}}
						case "lcp":
							opts.SourceFormat = sdktranslator.FormatOpenAI
							opts.OriginalRequest = []byte(`{"messages":[{"role":"system","content":"capacity websocket session"},{"role":"user","content":"hello"}]}`)
						}
						ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
						req := cliproxyexecutor.Request{Model: model}
						var got string
						if path == "select" {
							selected, errSelect := manager.SelectAuth(ctx, "codex", model, opts)
							if errSelect != nil {
								t.Fatal(errSelect)
							}
							got = selected.ID
							if affinity && mode == "lcp" && opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] == nil {
								t.Fatal("expected inferred session selection to exercise LCP affinity")
							}
						} else if path == "stream" {
							result, errStream := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
							if errStream != nil {
								t.Fatal(errStream)
							}
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
								got += string(chunk.Payload)
							}
						} else {
							result, errExecute := manager.Execute(ctx, []string{"codex"}, req, opts)
							if errExecute != nil {
								t.Fatal(errExecute)
							}
							got = string(result.Payload)
						}
						if got != wsAuth.ID {
							t.Fatalf("selected %q, want websocket account %q", got, wsAuth.ID)
						}
					})
				}
			}
		}
	}
}

func TestCapacityWebsocketPreferencePreservesHealthyBinding(t *testing.T) {
	selector := NewSessionAffinitySelector(&CapacitySelector{})
	t.Cleanup(selector.Stop)
	now := time.Now()
	httpAuth := capacityTestAuth("http", 0, now)
	httpAuth.Attributes = map[string]string{"priority": "10"}
	wsAuth := capacityTestAuth("ws", 50, now)
	wsAuth.Attributes = map[string]string{"websockets": "true"}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session-Id": []string{"existing"}}}
	for _, ctx := range []context.Context{context.Background(), cliproxyexecutor.WithDownstreamWebsocket(context.Background())} {
		got, errPick := selector.Pick(ctx, "mixed", "model", opts, []*Auth{httpAuth, wsAuth})
		if errPick != nil || got == nil || got.ID != httpAuth.ID {
			t.Fatalf("healthy session moved: %v, %v", got, errPick)
		}
	}
}
