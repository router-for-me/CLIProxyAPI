package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCursorRejectsUnsupportedFamilyBeforeNetwork(t *testing.T) {
	for _, stream := range []bool{false, true} {
		e := NewCursorExecutor(&config.Config{})
		e.openStream = func(string) (cursorStream, error) {
			t.Fatal("unexpected upstream connection")
			return nil, errors.New("unexpected connection")
		}
		auth := &cliproxyauth.Auth{ID: "cursor-resolution-test", Provider: "cursor", Metadata: map[string]any{"access_token": "test"}}
		helps.StoreCursorRoutingModels(auth.ID, []*registry.ModelInfo{{ID: "claude-fable-5-1-thinking-high"}})
		req := cliproxyexecutor.Request{Model: "claude-fable-5-1", Payload: []byte(`{"model":"claude-fable-5-1","messages":[]}`)}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: []byte(`{"reasoning":{"effort":"low"}}`)}
		var err error
		if stream {
			_, err = e.ExecuteStream(context.Background(), auth, req, opts)
		} else {
			_, err = e.Execute(context.Background(), auth, req, opts)
		}
		var status *cliproxyauth.Error
		if !errors.As(err, &status) || status.StatusCode() != 400 || status.Code != cliproxyauth.ErrorCodeModelVariantUnavailable {
			t.Fatalf("stream=%t: got %v, want account-specific variant unavailability", stream, err)
		}
	}
}

func TestCursorBuildRequestUsesResolvedFamilyVariant(t *testing.T) {
	payload := []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`)
	resolved, err := helps.ResolveCursorModel("claude-fable-5-1", payload, "openai", []*registry.ModelInfo{{ID: "claude-fable-5-1-thinking-high"}})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseOpenAIRequest(payload)
	params := buildRunRequestParams(parsed, "test", resolved)
	if params.ModelId != "claude-fable-5-1-thinking-high" || parsed.Model != "claude-fable-5-1" {
		t.Fatalf("upstream=%s response=%s", params.ModelId, parsed.Model)
	}
}

func TestCursorFamilyAccountRouting(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name          string
			options       string
			secondEffort  string
			wantErrorCode string
			wantAttempts  int
		}{
			{"alternate account", `{"reasoning_effort":"high"}`, "high", "", 2},
			{"all unavailable", `{"reasoning_effort":"high"}`, "medium", cliproxyauth.ErrorCodeModelVariantUnavailable, 2},
			{"invalid speed", `{"speed":"invalid"}`, "high", cliproxyauth.ErrorCodeRequestScoped, 1},
			{"invalid effort", `{"reasoning_effort":"invalid"}`, "high", cliproxyauth.ErrorCodeRequestScoped, 1},
			{"conflicting options", `{"reasoning_effort":"none","thinking":{"type":"enabled"}}`, "high", cliproxyauth.ErrorCodeRequestScoped, 1},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				ctx := context.Background()
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetConfig(&config.Config{})
				model := "claude-fable-5-1"
				firstID, secondID := t.Name()+"-first", t.Name()+"-second"
				for i, id := range []string{firstID, secondID} {
					effort, priority := "medium", "10"
					if i == 1 {
						effort, priority = tc.secondEffort, "0"
					}
					catalog := []*registry.ModelInfo{{ID: model + "-thinking-" + effort}}
					helps.StoreCursorRoutingModels(id, catalog)
					registry.GetGlobalRegistry().RegisterClient(id, "cursor", helps.AddCursorModelFamilies(catalog))
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					_, errRegister := manager.Register(ctx, &cliproxyauth.Auth{
						ID: id, Provider: "cursor", Status: cliproxyauth.StatusActive,
						Attributes: map[string]string{"priority": priority},
						Metadata:   map[string]any{"access_token": id},
					})
					if errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				exec := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
					onText("answer", false)
					return nil
				})
				var opened []string
				exec.openStream = func(token string) (cursorStream, error) {
					opened = append(opened, token)
					return newFakeCursorStream(), nil
				}
				manager.RegisterExecutor(exec)
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"medium"}`)}
				opts := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(tc.options)}
				execute := func() error {
					if stream {
						result, errExecute := manager.ExecuteStream(ctx, []string{"cursor"}, req, opts)
						if errExecute != nil {
							return errExecute
						}
						for _, chunk := range collectCursorStream(t, result) {
							if chunk.Err != nil {
								return chunk.Err
							}
						}
						return nil
					}
					_, errExecute := manager.Execute(ctx, []string{"cursor"}, req, opts)
					return errExecute
				}
				errExecute := execute()
				if tc.wantErrorCode == "" {
					if errExecute != nil || len(opened) != 1 || opened[0] != secondID {
						t.Fatalf("error=%v opened=%v, want second account success", errExecute, opened)
					}
				} else {
					var authErr *cliproxyauth.Error
					if !errors.As(errExecute, &authErr) || authErr.Code != tc.wantErrorCode || authErr.StatusCode() != 400 {
						t.Fatalf("error=%v, want %s with HTTP 400", errExecute, tc.wantErrorCode)
					}
					if len(opened) != 0 {
						t.Fatalf("unexpected upstream connections: %v", opened)
					}
				}
				attempts := 0
				for _, id := range []string{firstID, secondID} {
					auth, ok := manager.GetByID(id)
					if !ok {
						t.Fatal("registered account disappeared")
					}
					attempts += int(auth.Success + auth.Failed)
					if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
						t.Fatalf("account cooled after option mismatch: %+v", auth)
					}
					for _, state := range auth.ModelStates {
						if state.Unavailable || !state.NextRetryAfter.IsZero() {
							t.Fatalf("family cooled after option mismatch: %+v", state)
						}
					}
				}
				if attempts != tc.wantAttempts {
					t.Fatalf("attempts=%d, want %d", attempts, tc.wantAttempts)
				}
				opts.OriginalRequest = []byte(`{"reasoning_effort":"medium"}`)
				if errRetry := execute(); errRetry != nil {
					t.Fatalf("first account no longer serves its supported options: %v", errRetry)
				}
				if len(opened) == 0 || !strings.HasSuffix(opened[len(opened)-1], "-first") {
					t.Fatalf("supported request did not use first account: %v", opened)
				}
			})
		}
	}
}
