package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type KimiExecutor struct {
	*authFallbackExecutor
}

func (e *KimiExecutor) Identifier() string { return "kimi" }

func TestManager_Execute_LogsRoutePlanWithFallback(t *testing.T) {
	hook := logtest.NewGlobal()
	hook.Reset()

	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 0, 1)
	executor := &KimiExecutor{authFallbackExecutor: &authFallbackExecutor{
		id: "kimi",
		executeErrors: map[string]error{
			"aa-rate-limited-auth": &Error{
				Code:       "rate_limit_error",
				HTTPStatus: http.StatusTooManyRequests,
				Message:    "upstream rate limited",
				Retryable:  true,
			},
		},
	}}
	m.RegisterExecutor(executor)

	model := "kimi-k2.6"
	blockedAuth := &Auth{ID: "aa-rate-limited-auth", Provider: "kimi", Attributes: map[string]string{"routing_group": "group-a"}}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "kimi", Attributes: map[string]string{"routing_group": "group-b"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "kimi", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "kimi", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	ctx := logging.WithRequestID(context.Background(), "req-route-plan-1")
	_, errExecute := m.Execute(ctx, []string{"kimi"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/chat/completions",
		},
	})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}

	plans := findRoutePlanEntries(hook.AllEntries())
	if len(plans) < 2 {
		t.Fatalf("route_plan entries = %d, want at least 2", len(plans))
	}

	first := plans[0]
	if first.RequestedModel != model {
		t.Fatalf("first requested_model = %q, want %q", first.RequestedModel, model)
	}
	if first.ResolvedModel != model {
		t.Fatalf("first resolved_model = %q, want %q", first.ResolvedModel, model)
	}
	if first.Provider != "kimi" {
		t.Fatalf("first provider = %q, want kimi", first.Provider)
	}
	if first.Protocol != "claude_messages" {
		t.Fatalf("first protocol = %q, want claude_messages", first.Protocol)
	}
	if first.Executor != "KimiExecutor" {
		t.Fatalf("first executor = %q, want KimiExecutor", first.Executor)
	}
	if first.UpstreamPath != "/v1/chat/completions" {
		t.Fatalf("first upstream_path = %q, want /v1/chat/completions", first.UpstreamPath)
	}
	if first.Translator != "ClaudeToKimiOpenAICompat" {
		t.Fatalf("first translator = %q, want ClaudeToKimiOpenAICompat", first.Translator)
	}
	if first.FallbackFrom != "" || first.FallbackReason != "" {
		t.Fatalf("first fallback fields should be empty: %+v", first)
	}

	second := plans[1]
	if second.Executor != "KimiExecutor" {
		t.Fatalf("second executor = %q, want KimiExecutor", second.Executor)
	}
	if second.FallbackReason != "rate_limit_error" {
		t.Fatalf("second fallback_reason = %q, want rate_limit_error", second.FallbackReason)
	}
	if second.FallbackFrom == "" {
		t.Fatalf("second fallback_from should be populated: %+v", second)
	}
	if second.RoutingGroup != "group-b" {
		t.Fatalf("second routing_group = %q, want group-b", second.RoutingGroup)
	}
}

func TestRoutePlanHelperMappings(t *testing.T) {
	cases := []struct {
		name         string
		protocol     string
		requestPath  string
		executorName string
		operation    string
		wantPath     string
		wantTrans    string
	}{
		{
			name:         "codex responses",
			protocol:     "openai_responses",
			requestPath:  "/v1/responses",
			executorName: "CodexAutoExecutor",
			operation:    "execute",
			wantPath:     "/responses",
			wantTrans:    "OpenAIResponsesToCodex",
		},
		{
			name:         "claude count",
			protocol:     "openai_chat",
			requestPath:  "/v1/chat/completions",
			executorName: "ClaudeExecutor",
			operation:    "count",
			wantPath:     "/v1/messages/count_tokens?beta=true",
			wantTrans:    "OpenAIToClaude",
		},
		{
			name:         "openai compat responses",
			protocol:     "openai_responses",
			requestPath:  "/v1/responses",
			executorName: "OpenAICompatExecutor",
			operation:    "execute",
			wantPath:     "/responses/compact",
			wantTrans:    "OpenAIResponsesToOpenAICompat",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routePlanUpstreamPath(tc.protocol, tc.requestPath, tc.executorName, tc.operation); got != tc.wantPath {
				t.Fatalf("routePlanUpstreamPath() = %q, want %q", got, tc.wantPath)
			}
			if got := routePlanTranslator(tc.protocol, tc.requestPath, tc.executorName); got != tc.wantTrans {
				t.Fatalf("routePlanTranslator() = %q, want %q", got, tc.wantTrans)
			}
		})
	}
}

func findRoutePlanEntries(entries []*log.Entry) []routePlanSummary {
	out := make([]routePlanSummary, 0)
	for _, entry := range entries {
		if entry == nil || entry.Data["event"] != "route_plan" {
			continue
		}
		plan, ok := entry.Data["route_plan"].(routePlanSummary)
		if !ok {
			continue
		}
		out = append(out, plan)
	}
	return out
}
