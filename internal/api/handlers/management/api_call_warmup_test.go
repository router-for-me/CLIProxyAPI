package management

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestWarmupAllowedWeeklyOnlyWindowFromRateLimit(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"rate_limit":{
			"primary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808},
			"secondary_window":null
		}
	}`)

	snapshot, ok := parseWarmupQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected parse success")
	}
	if snapshot.HasFiveHourWindow {
		t.Fatal("did not expect five-hour window")
	}
	if !snapshot.HasWeeklyWindow {
		t.Fatal("expected weekly window")
	}
	if !warmupAllowed(snapshot) {
		t.Fatal("weekly-only window should allow warmup when weekly threshold is met")
	}
}

func TestWarmupAllowedDualWindowsRequireBothThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "both windows meet threshold",
			body: `{
				"rate_limit":{
					"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
					"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808}
				}
			}`,
			want: true,
		},
		{
			name: "five-hour below threshold",
			body: `{
				"rate_limit":{
					"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":17999,"reset_at":1777149808},
					"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808}
				}
			}`,
			want: false,
		},
		{
			name: "weekly below threshold",
			body: `{
				"rate_limit":{
					"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
					"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604799,"reset_at":1777149808}
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			snapshot, ok := parseWarmupQuotaSnapshot([]byte(tt.body))
			if !ok {
				t.Fatal("expected parse success")
			}
			if got := warmupAllowed(snapshot); got != tt.want {
				t.Fatalf("warmupAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWarmupAllowedRejectsSingleFiveHourWindow(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"rate_limit":{
			"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
			"secondary_window":null
		}
	}`)

	snapshot, ok := parseWarmupQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected parse success")
	}
	if !snapshot.HasFiveHourWindow {
		t.Fatal("expected five-hour window")
	}
	if snapshot.HasWeeklyWindow {
		t.Fatal("did not expect weekly window")
	}
	if warmupAllowed(snapshot) {
		t.Fatal("five-hour-only window should not allow warmup")
	}
}

func TestWarmupAllowedIgnoresCodeReviewRateLimit(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"rate_limit":{
			"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
			"secondary_window":null
		},
		"code_review_rate_limit":{
			"primary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808},
			"secondary_window":null
		}
	}`)

	snapshot, ok := parseWarmupQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected parse success")
	}
	if snapshot.HasWeeklyWindow {
		t.Fatal("did not expect weekly window from code_review_rate_limit")
	}
	if warmupAllowed(snapshot) {
		t.Fatal("code_review_rate_limit should not affect warmup decision")
	}
}

func TestWarmupListenerSkipsAlreadyWarmedSource(t *testing.T) {
	t.Parallel()

	listener := &WarmupListener{
		warmedAuthIndexes: map[string]struct{}{"source-1": {}},
		lastAcceptedAt:    map[string]time.Time{},
	}

	if !listener.shouldSkipSource("source-1") {
		t.Fatal("expected warmed source to be skipped")
	}
}

func TestWarmupListenerCooldownBlocksDuplicateSource(t *testing.T) {
	t.Parallel()

	listener := &WarmupListener{
		warmedAuthIndexes: map[string]struct{}{},
		lastAcceptedAt:    map[string]time.Time{},
	}
	now := time.Now()

	if listener.shouldIgnoreByCooldown("source-1", now) {
		t.Fatal("first event should not be ignored")
	}
	if !listener.shouldIgnoreByCooldown("source-1", now.Add(5*time.Second)) {
		t.Fatal("second event should be ignored during cooldown")
	}
}

func TestWarmupTargetsExcludeNegativeStateAndCurrentAuth(t *testing.T) {
	t.Parallel()

	listener := &WarmupListener{}
	auths := []*coreauth.Auth{
		{ID: "source", Provider: "codex", Metadata: map[string]any{"email": "source@example.com"}},
		{ID: "good", Provider: "codex", Metadata: map[string]any{"email": "good@example.com"}},
		{ID: "disabled", Provider: "codex", Disabled: true, Metadata: map[string]any{"email": "disabled@example.com"}},
		{ID: "quota", Provider: "codex", Metadata: map[string]any{"email": "quota@example.com"}, Quota: coreauth.QuotaState{Exceeded: true}},
		{ID: "model-error", Provider: "codex", Metadata: map[string]any{"email": "model-error@example.com"}, ModelStates: map[string]*coreauth.ModelState{"gpt-5": {Status: coreauth.StatusError}}},
	}

	got := listener.collectWarmupTargets(auths, "source")
	if len(got) != 1 || got[0].ID != "good" {
		t.Fatalf("targets = %#v", got)
	}
}

func TestSelectWarmupModelPrefersMiniPattern(t *testing.T) {
	authID := "warmup-mini-" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}, {ID: "gpt-5.4-mini"}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	model := selectWarmupModel(authID)
	if model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want %q", model, "gpt-5.4-mini")
	}
}

func TestHandlerSetConfigUpdatesWarmupListener(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(&config.Config{Routing: config.RoutingConfig{Strategy: "fill-first", Warmup: true}}, "", manager)
	if h.warmupListener == nil {
		t.Fatal("expected warmup listener")
	}
	if !h.warmupListener.isWarmupEnabled() {
		t.Fatal("expected warmup enabled before config update")
	}

	h.SetConfig(&config.Config{Routing: config.RoutingConfig{Strategy: "round-robin", Warmup: false}})
	if h.warmupListener.isWarmupEnabled() {
		t.Fatal("expected warmup disabled after config update")
	}
}

func TestWarmupListenerExecutesDefaultResponsesForEachTarget(t *testing.T) {
	t.Parallel()

	testID := strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	manager := coreauth.NewManager(nil, nil, nil)
	source := &coreauth.Auth{ID: "source-" + testID, Provider: "codex", Metadata: map[string]any{"email": "source@example.com"}}
	targetA := &coreauth.Auth{ID: "target-a-" + testID, Provider: "codex", Metadata: map[string]any{"email": "target-a@example.com"}}
	targetB := &coreauth.Auth{ID: "target-b-" + testID, Provider: "codex", Metadata: map[string]any{"email": "target-b@example.com"}}
	if _, err := manager.Register(context.Background(), source); err != nil {
		t.Fatalf("register source: %v", err)
	}
	if _, err := manager.Register(context.Background(), targetA); err != nil {
		t.Fatalf("register targetA: %v", err)
	}
	if _, err := manager.Register(context.Background(), targetB); err != nil {
		t.Fatalf("register targetB: %v", err)
	}

	sourceAuth, ok := manager.GetByID(source.ID)
	if !ok {
		t.Fatal("source auth missing")
	}
	sourceIndex := sourceAuth.EnsureIndex()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(targetA.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4-mini"}})
	reg.RegisterClient(targetB.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4-nano"}})
	t.Cleanup(func() {
		reg.UnregisterClient(targetA.ID)
		reg.UnregisterClient(targetB.ID)
	})

	type executeCall struct {
		providers []string
		req       cliproxyexecutor.Request
		opts      cliproxyexecutor.Options
	}
	calls := make([]executeCall, 0, 2)
	listener := &WarmupListener{
		cfg:               &config.Config{Routing: config.RoutingConfig{Strategy: "fill-first", Warmup: true}},
		authManager:       manager,
		lastAcceptedAt:    map[string]time.Time{},
		warmedAuthIndexes: map[string]struct{}{},
		now:               func() time.Time { return time.Unix(1000, 0) },
		execute: func(_ context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls = append(calls, executeCall{providers: append([]string(nil), providers...), req: req, opts: opts})
			return cliproxyexecutor.Response{}, nil
		},
	}

	listener.OnManagementAPICall(context.Background(), ManagementAPICallEvent{
		AuthIndex:  sourceIndex,
		Method:     "GET",
		URL:        "https://api.openai.com/v1/usage",
		StatusCode: 200,
		RespBody: []byte(`{
			"rate_limit":{
				"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
				"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808}
			}
		}`),
	})

	if len(calls) != 2 {
		t.Fatalf("execute calls = %d, want 2", len(calls))
	}

	seenTargetIDs := map[string]bool{}
	for _, call := range calls {
		if len(call.providers) != 1 || call.providers[0] != "codex" {
			t.Fatalf("providers = %#v", call.providers)
		}
		if call.opts.SourceFormat.String() != "openai-response" {
			t.Fatalf("source format = %q", call.opts.SourceFormat.String())
		}
		if call.opts.Stream {
			t.Fatal("stream should be false")
		}
		if call.opts.Alt != "" {
			t.Fatalf("alt = %q, want empty", call.opts.Alt)
		}
		pinnedID, _ := call.opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey].(string)
		if pinnedID == "" {
			t.Fatalf("missing %q metadata", cliproxyexecutor.PinnedAuthMetadataKey)
		}
		seenTargetIDs[pinnedID] = true

		var payload map[string]any
		if err := json.Unmarshal(call.req.Payload, &payload); err != nil {
			t.Fatalf("payload json: %v", err)
		}
		if payload["input"] != "hello" {
			t.Fatalf("payload input = %#v", payload["input"])
		}
		if payload["model"] != call.req.Model {
			t.Fatalf("payload model = %#v, req model = %q", payload["model"], call.req.Model)
		}
	}
	if !seenTargetIDs[targetA.ID] || !seenTargetIDs[targetB.ID] {
		t.Fatalf("seen pinned auths = %#v", seenTargetIDs)
	}
}

func TestWarmupListenerNonQuotaEventDoesNotConsumeCooldown(t *testing.T) {
	t.Parallel()

	testID := strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	manager := coreauth.NewManager(nil, nil, nil)
	source := &coreauth.Auth{ID: "source-" + testID, Provider: "codex", Metadata: map[string]any{"email": "source@example.com"}}
	target := &coreauth.Auth{ID: "target-" + testID, Provider: "codex", Metadata: map[string]any{"email": "target@example.com"}}
	if _, err := manager.Register(context.Background(), source); err != nil {
		t.Fatalf("register source: %v", err)
	}
	if _, err := manager.Register(context.Background(), target); err != nil {
		t.Fatalf("register target: %v", err)
	}

	sourceAuth, ok := manager.GetByID(source.ID)
	if !ok {
		t.Fatal("source auth missing")
	}
	sourceIndex := sourceAuth.EnsureIndex()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(target.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4-mini"}})
	t.Cleanup(func() {
		reg.UnregisterClient(target.ID)
	})

	calls := 0
	now := time.Unix(1000, 0)
	listener := &WarmupListener{
		cfg:               &config.Config{Routing: config.RoutingConfig{Strategy: "fill-first", Warmup: true}},
		authManager:       manager,
		lastAcceptedAt:    map[string]time.Time{},
		warmedAuthIndexes: map[string]struct{}{},
		now:               func() time.Time { return now },
		execute: func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls++
			return cliproxyexecutor.Response{}, nil
		},
	}

	listener.OnManagementAPICall(context.Background(), ManagementAPICallEvent{
		AuthIndex:  sourceIndex,
		Method:     "GET",
		URL:        "https://api.openai.com/v1/usage",
		StatusCode: 200,
		RespBody:   []byte(`{"ok":true}`),
	})

	listener.OnManagementAPICall(context.Background(), ManagementAPICallEvent{
		AuthIndex:  sourceIndex,
		Method:     "GET",
		URL:        "https://api.openai.com/v1/usage",
		StatusCode: 200,
		RespBody: []byte(`{
			"rate_limit":{
				"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
				"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808}
			}
		}`),
	})

	if calls != 1 {
		t.Fatalf("execute calls = %d, want 1", calls)
	}
}

func TestWarmupListenerSkipsNonUsageURLEvenWhenBodyLooksLikeQuota(t *testing.T) {
	t.Parallel()

	testID := strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	manager := coreauth.NewManager(nil, nil, nil)
	source := &coreauth.Auth{ID: "source-" + testID, Provider: "codex", Metadata: map[string]any{"email": "source@example.com"}}
	target := &coreauth.Auth{ID: "target-" + testID, Provider: "codex", Metadata: map[string]any{"email": "target@example.com"}}
	if _, err := manager.Register(context.Background(), source); err != nil {
		t.Fatalf("register source: %v", err)
	}
	if _, err := manager.Register(context.Background(), target); err != nil {
		t.Fatalf("register target: %v", err)
	}

	sourceAuth, ok := manager.GetByID(source.ID)
	if !ok {
		t.Fatal("source auth missing")
	}
	sourceIndex := sourceAuth.EnsureIndex()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(target.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4-mini"}})
	t.Cleanup(func() {
		reg.UnregisterClient(target.ID)
	})

	calls := 0
	listener := &WarmupListener{
		cfg:               &config.Config{Routing: config.RoutingConfig{Strategy: "fill-first", Warmup: true}},
		authManager:       manager,
		lastAcceptedAt:    map[string]time.Time{},
		warmedAuthIndexes: map[string]struct{}{},
		now:               func() time.Time { return time.Unix(1000, 0) },
		execute: func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls++
			return cliproxyexecutor.Response{}, nil
		},
	}

	listener.OnManagementAPICall(context.Background(), ManagementAPICallEvent{
		AuthIndex:  sourceIndex,
		Method:     "GET",
		URL:        "https://api.openai.com/v1/models",
		StatusCode: 200,
		RespBody: []byte(`{
			"rate_limit":{
				"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
				"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808}
			}
		}`),
	})

	if calls != 0 {
		t.Fatalf("execute calls = %d, want 0", calls)
	}
}

func TestWarmupListenerCurrentExecuteTracksManagerHotUpdate(t *testing.T) {
	t.Parallel()

	oldErr := errors.New("old execute")
	listener := &WarmupListener{
		execute: func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, oldErr
		},
	}

	execBefore := listener.currentExecute()
	if execBefore == nil {
		t.Fatal("expected current execute before update")
	}
	if _, err := execBefore(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); !errors.Is(err, oldErr) {
		t.Fatalf("before update error = %v, want old execute error", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	listener.setAuthManager(manager)

	execAfter := listener.currentExecute()
	if execAfter == nil {
		t.Fatal("expected current execute after update")
	}
	if _, err := execAfter(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); errors.Is(err, oldErr) {
		t.Fatalf("after update still used old execute: %v", err)
	}
}

func TestWarmupListenerMarksSourceOnlyAfterSuccess(t *testing.T) {
	t.Parallel()

	testID := strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	manager := coreauth.NewManager(nil, nil, nil)
	source := &coreauth.Auth{ID: "source-" + testID, Provider: "codex", Metadata: map[string]any{"email": "source@example.com"}}
	target := &coreauth.Auth{ID: "target-" + testID, Provider: "codex", Metadata: map[string]any{"email": "target@example.com"}}
	if _, err := manager.Register(context.Background(), source); err != nil {
		t.Fatalf("register source: %v", err)
	}
	if _, err := manager.Register(context.Background(), target); err != nil {
		t.Fatalf("register target: %v", err)
	}

	sourceAuth, ok := manager.GetByID(source.ID)
	if !ok {
		t.Fatal("source auth missing")
	}
	sourceIndex := sourceAuth.EnsureIndex()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(target.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4-mini"}})
	t.Cleanup(func() {
		reg.UnregisterClient(target.ID)
	})

	now := time.Unix(1000, 0)
	listener := &WarmupListener{
		cfg:               &config.Config{Routing: config.RoutingConfig{Strategy: "fill-first", Warmup: true}},
		authManager:       manager,
		lastAcceptedAt:    map[string]time.Time{},
		warmedAuthIndexes: map[string]struct{}{},
		now:               func() time.Time { return now },
		execute: func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, errors.New("warmup failed")
		},
	}

	evt := ManagementAPICallEvent{
		AuthIndex:  sourceIndex,
		Method:     "GET",
		URL:        "https://api.openai.com/v1/usage",
		StatusCode: 200,
		RespBody: []byte(`{
			"rate_limit":{
				"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":1777149808},
				"secondary_window":{"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1777149808}
			}
		}`),
	}

	listener.OnManagementAPICall(context.Background(), evt)
	if listener.shouldSkipSource(sourceIndex) {
		t.Fatal("source should stay eligible when all warmups fail")
	}

	now = now.Add(warmupCooldown + time.Second)
	listener.execute = func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		return cliproxyexecutor.Response{}, nil
	}
	listener.OnManagementAPICall(context.Background(), evt)
	if !listener.shouldSkipSource(sourceIndex) {
		t.Fatal("source should be marked only after warmup success")
	}
}
